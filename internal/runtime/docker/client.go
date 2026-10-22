// Package docker implements the Double Agent container runtime backend on top
// of the Docker Engine REST API.
//
// The backend talks to the daemon directly over the unix socket (or a TCP
// endpoint) with net/http instead of depending on the official Docker client,
// so the daemon keeps a small and auditable dependency surface.
//
// Every container is created with all Linux capabilities dropped, without
// privileged mode, without host namespaces and with a single read-write bind
// limited to the instance workspace, as required by
// docs/SECURITY_STORAGE_AND_LIFECYCLE.md.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// APIVersion is the Docker Engine API version spoken by this client.
	// 1.43 is the first release that advertises the multiplexed stream
	// media type for exec sessions.
	APIVersion = "1.43"

	// defaultHost is the daemon endpoint used when the configuration does
	// not name one.
	defaultHost = "unix:///var/run/docker.sock"

	// mediaTypeMultiplexedStream is the media type the daemon reports for
	// an exec session that frames its output.
	mediaTypeMultiplexedStream = "application/vnd.docker.multiplexed-stream"

	// mediaTypeRawStream is the media type the daemon reports for a
	// session attached to a TTY.
	mediaTypeRawStream = "application/vnd.docker.raw-stream"

	// maxErrorBody bounds how much of an error response is read into an
	// error message.
	maxErrorBody = 8 << 10
)

// Config configures the Docker Engine backend.
type Config struct {
	// Host is the Docker daemon endpoint. It accepts "unix:///var/run/docker.sock"
	// (the default), "tcp://host:port", "http://host:port" and
	// "https://host:port". A bare path is treated as a unix socket.
	Host string
	// WorkspaceRoot is the directory that holds instance workspaces. It is
	// the only directory tree whose paths may be bound into a container.
	WorkspaceRoot string
	// AllowedImages restricts the image references that may be used. An
	// empty list allows every image that is already present on the daemon.
	// A trailing "*" matches any reference with the same prefix.
	AllowedImages []string
	// PullImages allows the backend to pull a missing image during Prepare.
	PullImages bool
	// DefaultNetwork is the egress policy applied when a recipe does not
	// request one. It is one of "none", "restricted" or "allowed".
	DefaultNetwork string
	// HTTPClient overrides the HTTP client used to reach the daemon. It is
	// meant for tests and for callers that need custom TLS settings.
	HTTPClient *http.Client
}

// Client is a minimal Docker Engine API client.
//
// It implements only the calls the runtime backend needs, but it implements
// them faithfully: request bodies follow the documented API and responses are
// decoded into plain structs.
type Client struct {
	baseURL    string
	version    string
	httpClient *http.Client
}

// NewClient returns a client for the daemon reachable at host.
//
// A nil httpClient selects a client tuned for the Docker daemon: compression
// is disabled because the API streams tar archives and exec output, and the
// dialer connects to the unix socket directly.
func NewClient(host string, httpClient *http.Client) (*Client, error) {
	ep, err := parseEndpoint(host)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Transport: newTransport(ep)}
	}
	baseURL := ep.scheme + "://" + ep.address
	if ep.network == "unix" {
		// The authority is ignored by the dialer, but net/http still
		// requires a syntactically valid one.
		baseURL = ep.scheme + "://docker"
	}
	return &Client{baseURL: baseURL, version: APIVersion, httpClient: httpClient}, nil
}

// endpoint describes how to reach the daemon.
type endpoint struct {
	// scheme is the URL scheme used for requests.
	scheme string
	// network is the dial network, "unix" or "tcp".
	network string
	// address is the socket path or the host:port pair.
	address string
}

// parseEndpoint normalises a DOCKER_HOST style endpoint.
func parseEndpoint(host string) (endpoint, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = defaultHost
	}
	switch {
	case strings.HasPrefix(host, "unix://"):
		path := strings.TrimPrefix(host, "unix://")
		if path == "" {
			return endpoint{}, errors.New("docker: unix endpoint has no socket path")
		}
		return endpoint{scheme: "http", network: "unix", address: path}, nil
	case strings.HasPrefix(host, "tcp://"), strings.HasPrefix(host, "http://"), strings.HasPrefix(host, "https://"):
		u, err := url.Parse(host)
		if err != nil {
			return endpoint{}, fmt.Errorf("docker: parse host %q: %w", host, err)
		}
		if u.Host == "" {
			return endpoint{}, fmt.Errorf("docker: host %q has no address", host)
		}
		ep := endpoint{scheme: "http", network: "tcp", address: u.Host}
		if u.Scheme == "https" {
			ep.scheme = "https"
		}
		return ep, nil
	default:
		// A bare path follows the DOCKER_HOST convention and names a
		// unix socket.
		return endpoint{scheme: "http", network: "unix", address: host}, nil
	}
}

// newTransport builds the HTTP transport used to reach the daemon.
func newTransport(ep endpoint) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, ep.network, ep.address)
		},
		// The API answers with tar archives and raw exec streams. Asking
		// for compressed content would only complicate the framing.
		DisableCompression: true,
		MaxIdleConns:       4,
		IdleConnTimeout:    30 * time.Second,
	}
}

// APIError reports a Docker Engine API call that failed.
type APIError struct {
	// StatusCode is the HTTP status returned by the daemon.
	StatusCode int
	// Message is the error message reported by the daemon.
	Message string
	// Method and Path identify the call that failed.
	Method string
	Path   string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("docker: %s %s: %s (status %d)", e.Method, e.Path, msg, e.StatusCode)
}

// NotFound reports whether the daemon answered with 404.
func (e *APIError) NotFound() bool { return e.StatusCode == http.StatusNotFound }

// IsNotFound reports whether err is a daemon 404 answer.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.NotFound()
	}
	return false
}

// IsConflict reports whether err is a daemon 409 answer, which the daemon
// uses for "name already in use" and "container is running".
func IsConflict(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusConflict
	}
	return false
}

// do performs a request against the daemon and returns the raw response. The
// caller owns the response body.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string, headers http.Header) (*http.Response, error) {
	u := c.baseURL + "/v" + c.version + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("docker: build request %s %s: %w", method, path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker: %s %s: %w", method, path, err)
	}
	return resp, nil
}

// doJSON performs a JSON request and decodes the answer into out.
func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("docker: encode %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(encoded)
	}
	resp, err := c.do(ctx, method, path, query, body, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newAPIError(resp, method, path)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("docker: decode %s %s: %w", method, path, err)
	}
	return nil
}

// newAPIError turns a failed response into an APIError.
func newAPIError(resp *http.Response, method, path string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var payload struct {
		Message string `json:"message"`
	}
	message := strings.TrimSpace(string(raw))
	if err := json.Unmarshal(raw, &payload); err == nil && payload.Message != "" {
		message = payload.Message
	}
	return &APIError{StatusCode: resp.StatusCode, Message: message, Method: method, Path: path}
}

// Ping checks that the daemon answers.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/_ping", nil, nil, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return newAPIError(resp, http.MethodGet, "/_ping")
	}
	return nil
}

// Info returns the daemon description.
func (c *Client) Info(ctx context.Context) (*DaemonInfo, error) {
	var info DaemonInfo
	if err := c.doJSON(ctx, http.MethodGet, "/info", nil, nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ImageExists reports whether the daemon already has the image.
func (c *Client) ImageExists(ctx context.Context, ref string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil, nil, "", nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	default:
		return false, newAPIError(resp, http.MethodGet, "/images/"+ref+"/json")
	}
}

// ImagePull pulls an image from its registry.
//
// The daemon reports progress as a stream of JSON objects and reports
// failures inside that stream, so the stream has to be parsed to notice a
// failed pull.
func (c *Client) ImagePull(ctx context.Context, ref string) error {
	from, tag := splitImageRef(ref)
	query := url.Values{"fromImage": []string{from}}
	if tag != "" {
		query.Set("tag", tag)
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/create", query, nil, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newAPIError(resp, http.MethodPost, "/images/create")
	}
	decoder := json.NewDecoder(resp.Body)
	for {
		var message struct {
			Status      string `json:"status"`
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("docker: decode pull progress for %s: %w", ref, err)
		}
		if message.Error != "" {
			return fmt.Errorf("docker: pull %s: %s", ref, message.Error)
		}
		if message.ErrorDetail != nil && message.ErrorDetail.Message != "" {
			return fmt.Errorf("docker: pull %s: %s", ref, message.ErrorDetail.Message)
		}
	}
}

// splitImageRef splits an image reference into the repository and the tag the
// daemon expects. Digest references are passed through untouched.
func splitImageRef(ref string) (from, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref, ""
	}
	if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i+1:], "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// ContainerCreate creates a container and returns its identifier.
func (c *Client) ContainerCreate(ctx context.Context, name string, req ContainerCreateRequest) (string, []string, error) {
	query := url.Values{}
	if name != "" {
		query.Set("name", name)
	}
	var answer struct {
		ID       string   `json:"Id"`
		Warnings []string `json:"Warnings"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/containers/create", query, req, &answer); err != nil {
		return "", nil, err
	}
	if answer.ID == "" {
		return "", nil, fmt.Errorf("docker: daemon returned no container id")
	}
	return answer.ID, answer.Warnings, nil
}

// ContainerStart starts a created container.
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotModified:
		return nil
	default:
		return newAPIError(resp, http.MethodPost, "/containers/"+id+"/start")
	}
}

// ContainerInspect returns the daemon view of a container.
func (c *Client) ContainerInspect(ctx context.Context, id string) (*ContainerInspect, error) {
	var answer ContainerInspect
	if err := c.doJSON(ctx, http.MethodGet, "/containers/"+id+"/json", nil, nil, &answer); err != nil {
		return nil, err
	}
	return &answer, nil
}

// ContainerStop stops a container, giving it the requested grace period.
func (c *Client) ContainerStop(ctx context.Context, id string, grace time.Duration) error {
	seconds := int(grace.Seconds())
	if seconds < 0 {
		seconds = 0
	}
	query := url.Values{"t": []string{strconv.Itoa(seconds)}}
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+id+"/stop", query, nil, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotModified:
		return nil
	default:
		return newAPIError(resp, http.MethodPost, "/containers/"+id+"/stop")
	}
}

// ContainerRemove deletes a container and its anonymous volumes.
func (c *Client) ContainerRemove(ctx context.Context, id string, force bool) error {
	query := url.Values{"v": []string{"1"}, "force": []string{strconv.FormatBool(force)}}
	resp, err := c.do(ctx, http.MethodDelete, "/containers/"+id, query, nil, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	default:
		return newAPIError(resp, http.MethodDelete, "/containers/"+id)
	}
}

// ContainerLogs returns the container output stream.
func (c *Client) ContainerLogs(ctx context.Context, id string) (io.ReadCloser, error) {
	query := url.Values{
		"stdout":     []string{"1"},
		"stderr":     []string{"1"},
		"timestamps": []string{"0"},
		"follow":     []string{"0"},
		"tail":       []string{"all"},
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/logs", query, nil, "", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, newAPIError(resp, http.MethodGet, "/containers/"+id+"/logs")
	}
	return resp.Body, nil
}

// ExecCreate registers an exec session inside a container.
func (c *Client) ExecCreate(ctx context.Context, containerID string, req ExecCreateRequest) (string, error) {
	var answer struct {
		ID string `json:"Id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/containers/"+containerID+"/exec", nil, req, &answer); err != nil {
		return "", err
	}
	if answer.ID == "" {
		return "", fmt.Errorf("docker: daemon returned no exec id")
	}
	return answer.ID, nil
}

// ExecStart starts an exec session and returns its raw output stream.
//
// The request asks for a protocol upgrade, which is how the daemon hands the
// hijacked connection over: it answers 101 and then writes the output
// directly to the connection. Daemons that ignore the upgrade header answer
// 200 and stream the very same bytes as the response body, so both answers
// are read the same way.
func (c *Client) ExecStart(ctx context.Context, execID string, tty bool) (io.ReadCloser, string, error) {
	encoded, err := json.Marshal(ExecStartRequest{Detach: false, Tty: tty})
	if err != nil {
		return nil, "", fmt.Errorf("docker: encode exec start: %w", err)
	}
	headers := http.Header{"Connection": []string{"Upgrade"}, "Upgrade": []string{"tcp"}}
	resp, err := c.do(ctx, http.MethodPost, "/exec/"+execID+"/start", nil, bytes.NewReader(encoded), "application/json", headers)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		return nil, "", newAPIError(resp, http.MethodPost, "/exec/"+execID+"/start")
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// ExecInspect returns the state of an exec session.
func (c *Client) ExecInspect(ctx context.Context, execID string) (*ExecInspect, error) {
	var answer ExecInspect
	if err := c.doJSON(ctx, http.MethodGet, "/exec/"+execID+"/json", nil, nil, &answer); err != nil {
		return nil, err
	}
	return &answer, nil
}

// CopyToContainer uploads a tar archive into a container path.
func (c *Client) CopyToContainer(ctx context.Context, containerID, path string, archive io.Reader) error {
	query := url.Values{"path": []string{path}}
	resp, err := c.do(ctx, http.MethodPut, "/containers/"+containerID+"/archive", query, archive, "application/x-tar", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return newAPIError(resp, http.MethodPut, "/containers/"+containerID+"/archive")
	}
	return nil
}

// CopyFromContainer downloads a tar archive from a container path.
func (c *Client) CopyFromContainer(ctx context.Context, containerID, path string) (io.ReadCloser, error) {
	query := url.Values{"path": []string{path}}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+containerID+"/archive", query, nil, "", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, newAPIError(resp, http.MethodGet, "/containers/"+containerID+"/archive")
	}
	return resp.Body, nil
}

// NetworkExists reports whether the daemon already has the network.
func (c *Client) NetworkExists(ctx context.Context, name string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, nil, "", nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, newAPIError(resp, http.MethodGet, "/networks/"+name)
	}
}

// NetworkCreate creates a bridge network.
func (c *Client) NetworkCreate(ctx context.Context, req NetworkCreateRequest) (string, error) {
	var answer struct {
		ID      string `json:"Id"`
		Warning string `json:"Warning"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/networks/create", nil, req, &answer); err != nil {
		return "", err
	}
	return answer.ID, nil
}
