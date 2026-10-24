// Package proxmox implements the Double Agent virtual machine runtime
// backend on top of the Proxmox VE REST API.
//
// The backend provisions disposable QEMU/KVM guests by cloning a template
// that the operator prepared. It talks to the pveproxy with net/http
// instead of depending on a Proxmox SDK, so the daemon keeps a small and
// auditable dependency surface.
//
// Authentication uses either a ticket (a user name and a password) or an API
// token. A ticket is fetched lazily, cached and replaced before it expires;
// an API token needs no round trip at all.
//
// Every guest the backend creates carries an ownership marker in its name
// and in its description. The backend only ever deletes a guest that carries
// that marker, so a guest an operator created by hand is never removed.
//
// The template catalogue has no field for a Proxmox guest identifier, so the
// image of a recipe carries it: a recipe whose image is a plain number names
// the guest that is cloned, every other recipe uses the template of the
// backend configuration.
package proxmox

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// apiPrefix is the root of the Proxmox VE JSON API.
	apiPrefix = "/api2/json"

	// defaultRequestTimeout bounds a single API call, including the read
	// of its answer.
	defaultRequestTimeout = 30 * time.Second

	// ticketTTL is how long a Proxmox authentication ticket stays valid.
	// The server keeps a ticket for two hours.
	ticketTTL = 2 * time.Hour

	// ticketRefreshMargin is how long before the expiry of a ticket a new
	// one is fetched. A call must not start with a ticket that expires
	// while it is in flight.
	ticketRefreshMargin = 5 * time.Minute

	// maxErrorBody bounds how much of a failed answer is read into an
	// error message.
	maxErrorBody = 8 << 10

	// taskPollInterval is how often the state of a task is polled.
	taskPollInterval = 500 * time.Millisecond

	// taskTimeout bounds the wait for a task.
	taskTimeout = 10 * time.Minute
)

// Client is a minimal Proxmox VE API client.
//
// It implements only the calls the runtime backend needs, but it implements
// them faithfully: the request bodies follow the documented API and the
// answers are decoded into plain structs.
//
// The client is safe for concurrent use.
type Client struct {
	baseURL    string
	httpClient *http.Client
	node       string
	storage    string

	username    string
	password    string
	tokenID     string
	tokenSecret string

	mu      sync.Mutex
	ticket  string
	csrf    string
	expires time.Time
}

// NewClient returns a client for the Proxmox VE installation at the
// endpoint of the configuration.
//
// The client does not contact the server: the first ticket is fetched when
// the first call is made.
func NewClient(cfg Config) (*Client, error) {
	endpoint, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	node := strings.TrimSpace(cfg.Node)
	if node == "" {
		return nil, errors.New("proxmox: node is required")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = newHTTPClient(cfg.Insecure)
	}
	client := &Client{
		baseURL:     endpoint,
		httpClient:  httpClient,
		node:        node,
		storage:     strings.TrimSpace(cfg.Storage),
		username:    strings.TrimSpace(cfg.Username),
		password:    cfg.Password,
		tokenID:     strings.TrimSpace(cfg.TokenID),
		tokenSecret: cfg.TokenSecret,
	}
	if !client.usesToken() && (client.username == "" || client.password == "") {
		return nil, errors.New("proxmox: a password or an api token is required")
	}
	return client, nil
}

// Node returns the node the guests of the client live on.
func (c *Client) Node() string { return c.node }

// Storage returns the storage the disks of the guests are placed on.
func (c *Client) Storage() string { return c.storage }

// parseEndpoint normalises the address of the pveproxy.
func parseEndpoint(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("proxmox: endpoint is required")
	}
	if !strings.Contains(value, "://") {
		// A bare host or host:port pair is assumed to be the HTTPS
		// endpoint, because pveproxy only speaks TLS.
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("proxmox: parse endpoint %q: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("proxmox: endpoint %q must use http or https", raw)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("proxmox: endpoint %q has no address", raw)
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

// newHTTPClient builds the HTTP client that reaches the pveproxy.
//
// The certificate of a Proxmox installation is self signed, so the
// verification of the certificate can be turned off. It stays on by default:
// an installation that is reached over a network an attacker controls must
// not be talked to with an unverified certificate.
func newHTTPClient(insecure bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the operator asked for it
	}
	return &http.Client{Transport: transport, Timeout: defaultRequestTimeout}
}

// usesToken reports whether the client authenticates with an API token.
func (c *Client) usesToken() bool {
	return c.tokenID != "" && c.tokenSecret != ""
}

// authorize adds the credentials of the client to a request.
//
// A ticket is sent as the cookie of the session and every write carries the
// CSRF token of the ticket: Proxmox rejects a write without it even when
// the ticket itself is still valid. An API token is sent as an
// authorization header and needs no CSRF token.
func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	if c.usesToken() {
		req.Header.Set("Authorization", "PVEAPIToken="+c.tokenID+"="+c.tokenSecret)
		return nil
	}
	ticket, csrf, err := c.authenticate(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Cookie", "PVEAuthCookie="+ticket)
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		req.Header.Set("CSRFPreventionToken", csrf)
	}
	return nil
}

// authenticate returns the ticket of the session, fetching one when the
// cached ticket is missing or about to expire.
//
// The ticket is fetched while the lock is held, so a burst of calls results
// in a single request for a ticket.
func (c *Client) authenticate(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ticket != "" && time.Now().Add(ticketRefreshMargin).Before(c.expires) {
		return c.ticket, c.csrf, nil
	}
	ticket, csrf, err := c.requestTicket(ctx)
	if err != nil {
		return "", "", err
	}
	c.ticket = ticket
	c.csrf = csrf
	c.expires = time.Now().Add(ticketTTL)
	return ticket, csrf, nil
}

// invalidateTicket forgets the cached ticket.
//
// It is called when the server rejects a call, because the ticket was then
// either expired or revoked.
func (c *Client) invalidateTicket() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ticket = ""
	c.csrf = ""
	c.expires = time.Time{}
}

// requestTicket asks the server for a new ticket.
func (c *Client) requestTicket(ctx context.Context) (string, string, error) {
	values := url.Values{}
	values.Set("username", c.username)
	values.Set("password", c.password)
	body := values.Encode()
	resp, err := c.send(ctx, http.MethodPost, "/access/ticket", nil, []byte(body), "application/x-www-form-urlencoded")
	if err != nil {
		return "", "", err
	}
	var answer struct {
		Data struct {
			Ticket              string `json:"ticket"`
			CSRFPreventionToken string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := decodeEnvelope(resp, http.MethodPost, "/access/ticket", &answer); err != nil {
		return "", "", err
	}
	if answer.Data.Ticket == "" {
		return "", "", errors.New("proxmox: the server returned no ticket")
	}
	return answer.Data.Ticket, answer.Data.CSRFPreventionToken, nil
}

// call performs an authenticated request.
//
// A rejected ticket is replaced and the call is repeated once, because a
// ticket can expire or be revoked while it is cached.
func (c *Client) call(ctx context.Context, method, path string, query url.Values, body []byte, contentType string) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, cancel, err := c.newRequest(ctx, method, path, query, body, contentType)
		if err != nil {
			return nil, err
		}
		if err := c.authorize(ctx, req); err != nil {
			cancel()
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("proxmox: %s %s: %w", method, path, err)
		}
		resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && !c.usesToken() {
			resp.Body.Close()
			c.invalidateTicket()
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("proxmox: %s %s: the server rejected the credentials", method, path)
}

// newRequest builds a request against the JSON API.
//
// The context of the request is bounded. The cancel function belongs to the
// caller, which ties it to the body of the answer, so the deadline covers
// the read of the answer as well.
func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body []byte, contentType string) (*http.Request, context.CancelFunc, error) {
	target := c.baseURL + apiPrefix + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("proxmox: build request %s %s: %w", method, path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, cancel, nil
}

// send performs a request without adding the credentials of the client.
//
// It is used for the call that fetches the ticket, which must not carry a
// ticket of its own.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, body []byte, contentType string) (*http.Response, error) {
	req, cancel, err := c.newRequest(ctx, method, path, query, body, contentType)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("proxmox: %s %s: %w", method, path, err)
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelBody ties the deadline of a request to the body of its answer.
//
// The body is closed by whoever reads the answer, and closing it releases
// the timer of the deadline.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

// Close implements io.Closer.
func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// doForm performs a call with a form encoded body and decodes the answer.
//
// The Proxmox API takes the parameters of a write either as a form or as a
// JSON document. The form is used for everything but the guest agent, which
// only understands JSON.
func (c *Client) doForm(ctx context.Context, method, path string, query url.Values, values url.Values, out any) error {
	var body []byte
	contentType := ""
	if values != nil {
		body = []byte(values.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	resp, err := c.call(ctx, method, path, query, body, contentType)
	if err != nil {
		return err
	}
	return decodeAnswer(resp, method, path, out)
}

// doJSON performs a call with a JSON body and decodes the answer.
func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, in any, out any) error {
	var body []byte
	contentType := ""
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("proxmox: encode %s %s: %w", method, path, err)
		}
		body = encoded
		contentType = "application/json"
	}
	resp, err := c.call(ctx, method, path, query, body, contentType)
	if err != nil {
		return err
	}
	return decodeAnswer(resp, method, path, out)
}

// decodeAnswer decodes the data member of an answer.
//
// A nil out discards the answer, which is what a write that only reports a
// task needs.
func decodeAnswer(resp *http.Response, method, path string, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newAPIError(resp, method, path)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("proxmox: decode %s %s: %w", method, path, err)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return fmt.Errorf("proxmox: %s %s: the server returned no data", method, path)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("proxmox: decode %s %s: %w", method, path, err)
	}
	return nil
}

// decodeEnvelope decodes the whole answer of the server.
//
// The answer of the server is a document with a data member, and an answer
// that carries more than the data is decoded with this helper.
func decodeEnvelope(resp *http.Response, method, path string, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newAPIError(resp, method, path)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("proxmox: decode %s %s: %w", method, path, err)
	}
	return nil
}

// APIError reports a Proxmox API call that failed.
type APIError struct {
	// StatusCode is the HTTP status the server answered with.
	StatusCode int
	// Message is the error message of the server.
	Message string
	// Method and Path identify the call that failed.
	Method string
	Path   string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	message := e.Message
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("proxmox: %s %s: %s (status %d)", e.Method, e.Path, message, e.StatusCode)
}

// NotFound reports whether the server answered that the object is missing.
func (e *APIError) NotFound() bool { return e.StatusCode == http.StatusNotFound }

// IsNotFound reports whether err is the answer of a missing object.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.NotFound()
	}
	return false
}

// newAPIError turns a failed answer into an APIError.
//
// The server reports the reason of a failure either as a map of properties
// or as a plain message, and both are read.
func newAPIError(resp *http.Response, method, path string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	message := strings.TrimSpace(string(raw))
	var payload struct {
		Message string            `json:"message"`
		Errors  map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		switch {
		case len(payload.Errors) > 0:
			parts := make([]string, 0, len(payload.Errors))
			for key, value := range payload.Errors {
				parts = append(parts, key+": "+value)
			}
			sort.Strings(parts)
			message = strings.Join(parts, "; ")
		case payload.Message != "":
			message = payload.Message
		}
	}
	return &APIError{StatusCode: resp.StatusCode, Message: message, Method: method, Path: path}
}

// Version returns the version of the Proxmox VE installation.
func (c *Client) Version(ctx context.Context) (*Version, error) {
	var version Version
	if err := c.doForm(ctx, http.MethodGet, "/version", nil, nil, &version); err != nil {
		return nil, err
	}
	return &version, nil
}

// Nodes returns the nodes of the cluster.
//
// The list carries the state of every node, so it is the answer that tells
// whether the node of the guests is online.
func (c *Client) Nodes(ctx context.Context) ([]NodeStatus, error) {
	var nodes []NodeStatus
	if err := c.doForm(ctx, http.MethodGet, "/nodes", nil, nil, &nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

// NextID returns the next free guest identifier of the cluster.
//
// The server answers with the identifier as a string, so it is parsed here.
func (c *Client) NextID(ctx context.Context) (int, error) {
	var raw string
	if err := c.doForm(ctx, http.MethodGet, "/cluster/nextid", nil, nil, &raw); err != nil {
		return 0, err
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("proxmox: the server answered with the guest id %q: %w", raw, err)
	}
	return vmid, nil
}

// ListQemu returns the guests of a node.
func (c *Client) ListQemu(ctx context.Context, node string) ([]QemuSummary, error) {
	var guests []QemuSummary
	path := fmt.Sprintf("/nodes/%s/qemu", url.PathEscape(node))
	if err := c.doForm(ctx, http.MethodGet, path, nil, nil, &guests); err != nil {
		return nil, err
	}
	return guests, nil
}

// GetConfig returns the configuration of a guest.
func (c *Client) GetConfig(ctx context.Context, vmid int) (*QemuConfig, error) {
	var config QemuConfig
	path := c.qemuPath(vmid, "/config")
	if err := c.doForm(ctx, http.MethodGet, path, nil, nil, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// SetConfig writes the configuration of a guest.
//
// Only the properties that are set are written, so a property the backend
// does not care about keeps the value of the template.
func (c *Client) SetConfig(ctx context.Context, vmid int, values url.Values) error {
	path := c.qemuPath(vmid, "/config")
	return c.doForm(ctx, http.MethodPut, path, nil, values, nil)
}

// Clone copies a guest and returns the identifier of the task that does it.
//
// The source of the copy is the guest the path of the call names. Proxmox
// answers with the identifier of a task, because the copy of the disks takes
// time.
func (c *Client) Clone(ctx context.Context, req CloneRequest) (string, error) {
	values := url.Values{}
	values.Set("newid", strconv.Itoa(req.NewVMID))
	if req.Full {
		// A linked clone would share the disks of its source, so a
		// workload could damage the template of the operator.
		values.Set("full", "1")
	}
	if req.Name != "" {
		values.Set("name", req.Name)
	}
	if req.Description != "" {
		values.Set("description", req.Description)
	}
	if req.Target != "" {
		values.Set("target", req.Target)
	}
	if req.Storage != "" {
		values.Set("storage", req.Storage)
	}
	if req.Format != "" {
		values.Set("format", req.Format)
	}
	var upid string
	path := c.qemuPath(req.SourceVMID, "/clone")
	if err := c.doForm(ctx, http.MethodPost, path, nil, values, &upid); err != nil {
		return "", err
	}
	return upid, nil
}

// Start starts a guest and returns the identifier of the task that does it.
func (c *Client) Start(ctx context.Context, vmid int) (string, error) {
	var upid string
	path := c.qemuPath(vmid, "/status/start")
	if err := c.doForm(ctx, http.MethodPost, path, nil, nil, &upid); err != nil {
		return "", err
	}
	return upid, nil
}

// Shutdown asks a guest to shut down and returns the identifier of the task
// that waits for it.
//
// The guest is asked through the power management of its firmware, so it can
// stop cleanly. A guest that ignores the request is stopped by Stop.
func (c *Client) Shutdown(ctx context.Context, vmid int, timeoutSeconds int) (string, error) {
	values := url.Values{}
	if timeoutSeconds > 0 {
		values.Set("timeout", strconv.Itoa(timeoutSeconds))
	}
	var upid string
	path := c.qemuPath(vmid, "/status/shutdown")
	if err := c.doForm(ctx, http.MethodPost, path, nil, values, &upid); err != nil {
		return "", err
	}
	return upid, nil
}

// Stop cuts the power of a guest and returns the identifier of the task that
// does it.
//
// The guest is stopped immediately, which is what a guest that ignored the
// shutdown request needs.
func (c *Client) Stop(ctx context.Context, vmid int) (string, error) {
	var upid string
	path := c.qemuPath(vmid, "/status/stop")
	if err := c.doForm(ctx, http.MethodPost, path, nil, nil, &upid); err != nil {
		return "", err
	}
	return upid, nil
}

// Reset resets a guest and returns the identifier of the task that does it.
func (c *Client) Reset(ctx context.Context, vmid int) (string, error) {
	var upid string
	path := c.qemuPath(vmid, "/status/reset")
	if err := c.doForm(ctx, http.MethodPost, path, nil, nil, &upid); err != nil {
		return "", err
	}
	return upid, nil
}

// StatusCurrent returns the runtime state of a guest.
func (c *Client) StatusCurrent(ctx context.Context, vmid int) (*QemuStatus, error) {
	var status QemuStatus
	path := c.qemuPath(vmid, "/status/current")
	if err := c.doForm(ctx, http.MethodGet, path, nil, nil, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// Destroy deletes a guest and returns the identifier of the task that does
// it.
//
// The disks of the guest are destroyed as well, because a guest that is
// deleted but keeps its disk would fill the storage of the node.
//
// The parameters of the call travel in the query of the address, because the
// server only reads the body of a call that writes with a post or a put.
func (c *Client) Destroy(ctx context.Context, vmid int) (string, error) {
	query := url.Values{}
	// The disks of the guest are not referenced by its configuration any
	// more once the configuration is gone, so they are only destroyed when
	// the call asks for it.
	query.Set("destroy-unreferenced-disks", "1")
	var upid string
	path := c.qemuPath(vmid, "")
	if err := c.doForm(ctx, http.MethodDelete, path, query, nil, &upid); err != nil {
		return "", err
	}
	return upid, nil
}

// TaskStatus returns the state of a task.
func (c *Client) TaskStatus(ctx context.Context, node, upid string) (*TaskStatus, error) {
	var status TaskStatus
	path := fmt.Sprintf("/nodes/%s/tasks/%s/status", url.PathEscape(node), url.PathEscape(upid))
	if err := c.doForm(ctx, http.MethodGet, path, nil, nil, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// WaitForTask waits until a task finished.
//
// A task that reports a result other than "OK" failed, and the reason of the
// failure is part of the answer.
func (c *Client) WaitForTask(ctx context.Context, upid string) error {
	if upid == "" {
		return nil
	}
	node := taskNode(upid)
	deadline := time.Now().Add(taskTimeout)
	for {
		status, err := c.TaskStatus(ctx, node, upid)
		if err != nil {
			return err
		}
		if status.Status == "stopped" {
			if status.ExitStatus != "" && status.ExitStatus != "OK" {
				return fmt.Errorf("proxmox: task %s failed: %s", upid, status.ExitStatus)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("proxmox: task %s did not finish in time", upid)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(taskPollInterval):
		}
	}
}

// AgentExec starts a command inside a guest and returns the identifier of
// the process.
//
// The guest agent has to run inside the guest, so the command only works in
// a template that carries the agent.
func (c *Client) AgentExec(ctx context.Context, vmid int, req AgentExecRequest) (int, error) {
	var answer struct {
		PID int `json:"pid"`
	}
	path := c.qemuPath(vmid, "/agent/exec")
	if err := c.doJSON(ctx, http.MethodPost, path, nil, req, &answer); err != nil {
		return 0, err
	}
	if answer.PID == 0 {
		return 0, fmt.Errorf("proxmox: the guest agent returned no process id")
	}
	return answer.PID, nil
}

// AgentExecStatus returns the state of a command that runs inside a guest.
func (c *Client) AgentExecStatus(ctx context.Context, vmid, pid int) (*AgentExecStatus, error) {
	var status AgentExecStatus
	query := url.Values{}
	query.Set("pid", strconv.Itoa(pid))
	path := c.qemuPath(vmid, "/agent/exec-status")
	if err := c.doForm(ctx, http.MethodGet, path, query, nil, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// qemuPath builds the path of a call that works on a guest.
func (c *Client) qemuPath(vmid int, suffix string) string {
	return fmt.Sprintf("/nodes/%s/qemu/%d%s", url.PathEscape(c.node), vmid, suffix)
}

// AgentPing asks whether the guest agent of a guest answers.
//
// The ping is the cheapest call of the agent channel: it carries no command.
// The server answers with an error while the guest is still booting, which is
// how a caller waits for the agent to come up.
func (c *Client) AgentPing(ctx context.Context, vmid int) error {
	path := c.qemuPath(vmid, "/agent/ping")
	if err := c.doForm(ctx, http.MethodPost, path, nil, nil, nil); err != nil {
		return guestPingError(vmid, err)
	}
	return nil
}

// guestPingError explains a failed ping of the guest agent.
//
// The server reports a guest that is still booting, a guest without the
// agent and a guest that is off with different messages; all of them mean the
// channel is not usable yet.
func guestPingError(vmid int, err error) error {
	if isGuestAgentError(err) {
		return fmt.Errorf("%w: guest %d: the agent does not answer yet: %w", ErrGuestAgentUnavailable, vmid, err)
	}
	return err
}

// taskNode returns the node a task runs on.
//
// The identifier of a task carries the node, so a task that was moved to
// another node is still polled on the node that runs it.
func taskNode(upid string) string {
	parts := strings.Split(upid, ":")
	if len(parts) > 1 && parts[0] == "UPID" && parts[1] != "" {
		return parts[1]
	}
	return ""
}
