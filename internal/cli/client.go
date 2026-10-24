// Package cli implements the operator command line interface.
//
// The CLI is a thin client over the REST API. It never talks to the store or
// the runtimes directly, so the same binary works against a remote node.
package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client is a REST client for a Double Agent node.
type Client struct {
	// BaseURL is the root of the API, e.g. "https://192.168.1.10:7443".
	BaseURL string
	// Token is the client key used for authentication.
	Token string
	// HTTP is the transport used for requests.
	HTTP *http.Client
}

// NewClient builds a client from the environment and the command line.
//
// The node address comes from DOUBLE_AGENT_URL, the client key from
// DOUBLE_AGENT_TOKEN and the optional pinned certificate from
// DOUBLE_AGENT_CA.
func NewClient(overrideURL, overrideToken, caPath string) (*Client, error) {
	base := overrideURL
	if base == "" {
		base = os.Getenv("DOUBLE_AGENT_URL")
	}
	if base == "" {
		return nil, fmt.Errorf("the node address is not set: use --url or DOUBLE_AGENT_URL")
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("invalid node address %q: %w", base, err)
	}

	token := overrideToken
	if token == "" {
		token = os.Getenv("DOUBLE_AGENT_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("the client key is not set: use --token or DOUBLE_AGENT_TOKEN")
	}

	if caPath == "" {
		caPath = os.Getenv("DOUBLE_AGENT_CA")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read the certificate authority: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("the file %s does not contain a certificate", caPath)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}

	return &Client{
		BaseURL: base,
		Token:   token,
		HTTP:    &http.Client{Transport: transport, Timeout: 120 * time.Second},
	}, nil
}

// DefaultCAFile returns the conventional location of the node certificate
// authority inside the operator data directory.
func DefaultCAFile(dataDir string) string {
	return filepath.Join(dataDir, "pki", "ca.crt")
}

// do performs a request and decodes the JSON response.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("contact the node: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read the response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp.StatusCode, payload)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode the response: %w", err)
	}
	return nil
}

// apiError renders a structured API failure.
func apiError(status int, payload []byte) error {
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RetryHint string `json:"retry_hint"`
			RequestID string `json:"request_id"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &envelope); err == nil {
		if envelope.Error.Code != "" {
			msg := envelope.Error.Message
			if msg == "" {
				msg = envelope.Error.Code
			}
			if envelope.Error.RetryHint != "" {
				msg += " (" + envelope.Error.RetryHint + ")"
			}
			return fmt.Errorf("%s: %s", envelope.Error.Code, msg)
		}
		if envelope.Message != "" {
			return fmt.Errorf("http %d: %s", status, envelope.Message)
		}
	}
	trimmed := strings.TrimSpace(string(payload))
	if len(trimmed) > 400 {
		trimmed = trimmed[:400] + "..."
	}
	return fmt.Errorf("http %d: %s", status, trimmed)
}
