package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// cmdMCPBridge forwards MCP traffic between stdio and a remote node.
//
// Clients that only speak the stdio transport connect to this command. The
// bridge performs a single pairing handshake, verifies the node certificate
// against the pinned certificate authority and then relays JSON-RPC frames
// verbatim. Secrets never appear in the command line: the client key is read
// from the environment or the standard configuration file.
func cmdMCPBridge(ctx context.Context, opts *globalOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	client, err := opts.client()
	if err != nil {
		return err
	}
	reader := bufio.NewReaderSize(stdin, 1<<20)
	for {
		line, err := readFrame(reader)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		// A frame that is not JSON is reported rather than forwarded, because
		// the remote endpoint speaks JSON-RPC only.
		var probe json.RawMessage
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			fmt.Fprintf(stderr, "double-agent mcp-bridge: the input is not valid JSON: %v\n", err)
			continue
		}
		response, err := client.postRaw(ctx, "/mcp", line)
		if err != nil {
			fmt.Fprintf(stderr, "double-agent mcp-bridge: %v\n", err)
			continue
		}
		fmt.Fprintln(stdout, strings.TrimRight(response, "\n"))
	}
}

// readFrame reads one newline delimited frame.
func readFrame(reader *bufio.Reader) (string, error) {
	var buf bytes.Buffer
	for {
		chunk, isPrefix, err := reader.ReadLine()
		if err != nil {
			if buf.Len() > 0 && err == io.EOF {
				return buf.String(), nil
			}
			return "", err
		}
		buf.Write(chunk)
		if !isPrefix {
			return buf.String(), nil
		}
	}
}

// postRaw sends a raw body to the node and returns the raw response.
func (c *Client) postRaw(ctx context.Context, path, body string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+path, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("contact the node: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("read the response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", apiError(resp.StatusCode, payload)
	}
	return string(payload), nil
}

// downloadArtifact fetches an artifact and writes it to disk.
func downloadArtifact(ctx context.Context, client *Client, id string, args []string) error {
	destination := ""
	if len(args) > 2 {
		destination = args[2]
	}
	req, err := http.NewRequestWithContext(ctx, "GET", client.BaseURL+"/api/v1/artifacts/"+id+"/download", nil)
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+client.Token)
	resp, err := client.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("contact the node: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return apiError(resp.StatusCode, payload)
	}
	if destination == "" {
		name := resp.Header.Get("Content-Disposition")
		name = filenameFromDisposition(name, id)
		destination = name
	}
	if dir := filepath.Dir(destination); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create the destination directory: %w", err)
		}
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create the destination file: %w", err)
	}
	defer file.Close()
	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("write the artifact: %w", err)
	}
	fmt.Fprintf(os.Stdout, "saved %s\n", destination)
	return nil
}

// filenameFromDisposition extracts a file name from a Content-Disposition header.
func filenameFromDisposition(header, fallback string) string {
	if header == "" {
		return fallback
	}
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "filename=") {
			continue
		}
		name := strings.Trim(strings.TrimPrefix(part, "filename="), `"`)
		if name != "" {
			return filepath.Base(name)
		}
	}
	return fallback
}
