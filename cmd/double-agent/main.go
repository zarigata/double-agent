// Command double-agent is the operator CLI for the Double Agent control plane.
//
// It talks to a running node over the REST API using a client key. The
// companion command "double-agent mcp-bridge" forwards stdio MCP traffic to a
// node, for clients that cannot speak Streamable HTTP.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/zarigata/double-agent/internal/cli"
)

func main() {
	if err := cli.Main(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "double-agent: %v\n", err)
		os.Exit(1)
	}
}
