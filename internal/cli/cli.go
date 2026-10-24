package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Main runs the command line interface.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return fmt.Errorf("a command is required")
	}
	switch args[0] {
	case "-h", "--help", "help":
		printUsage(stdout)
		return nil
	case "-v", "--version", "version":
		fmt.Fprintln(stdout, "double-agent (client)")
		return nil
	}

	opts := &globalOptions{}
	rest, err := parseGlobal(args[1:], opts)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		printUsage(stderr)
		return fmt.Errorf("a command is required")
	}

	command, sub := rest[0], rest[1:]
	switch command {
	case "capabilities":
		return cmdCapabilities(ctx, opts, stdout)
	case "templates":
		return cmdTemplates(ctx, opts, stdout, sub)
	case "instances":
		return cmdInstances(ctx, opts, stdout, sub)
	case "jobs":
		return cmdJobs(ctx, opts, stdout, sub)
	case "artifacts":
		return cmdArtifacts(ctx, opts, stdout, sub)
	case "keys":
		return cmdKeys(ctx, opts, stdout, sub)
	case "mcp-bridge":
		return cmdMCPBridge(ctx, opts, stdin, stdout, stderr)
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", command)
	}
}

// globalOptions are the options accepted by every command.
type globalOptions struct {
	url   string
	token string
	ca    string
	// jsonOutput renders the raw JSON instead of a table.
	jsonOutput bool
}

// parseGlobal extracts the global options and returns the remaining arguments.
func parseGlobal(args []string, opts *globalOptions) ([]string, error) {
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			i++
			return args[i], nil
		}
		var err error
		switch {
		case arg == "--url":
			opts.url, err = next()
		case strings.HasPrefix(arg, "--url="):
			opts.url = strings.TrimPrefix(arg, "--url=")
		case arg == "--token":
			opts.token, err = next()
		case strings.HasPrefix(arg, "--token="):
			opts.token = strings.TrimPrefix(arg, "--token=")
		case arg == "--ca":
			opts.ca, err = next()
		case strings.HasPrefix(arg, "--ca="):
			opts.ca = strings.TrimPrefix(arg, "--ca=")
		case arg == "--json":
			opts.jsonOutput = true
		case arg == "--":
			rest = append(rest, args[i+1:]...)
			return rest, nil
		default:
			rest = append(rest, arg)
		}
		if err != nil {
			return nil, err
		}
	}
	return rest, nil
}

// client builds an API client from the global options.
func (opts *globalOptions) client() (*Client, error) {
	return NewClient(opts.url, opts.token, opts.ca)
}

// printUsage writes the command help.
func printUsage(w io.Writer) {
	fmt.Fprint(w, `usage: double-agent [global options] <command> [arguments]

Global options:
  --url URL      the node address, for example https://192.168.1.10:7443
  --token TOKEN  the client key
  --ca PATH      the pinned certificate authority of the node
  --json         print the raw JSON response

Commands:
  capabilities             show the runtimes, templates and limits of the node
  templates [list|show ID] list the template catalogue
  instances <list|create|exec|stop|destroy|logs>
  jobs <list|get ID|cancel ID>
  artifacts <list|download ID>
  keys <list|create|revoke ID>
  mcp-bridge               forward stdio MCP traffic to the node
`)
}

// cmdCapabilities prints the node capabilities.
func cmdCapabilities(ctx context.Context, opts *globalOptions, out io.Writer) error {
	client, err := opts.client()
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := client.do(ctx, "GET", "/api/v1/capabilities", nil, &payload); err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeJSON(out, payload)
	}
	fmt.Fprintln(out, "Runtimes:")
	runtimes, _ := payload["runtimes"].([]any)
	if len(runtimes) == 0 {
		fmt.Fprintln(out, "  (none)")
	}
	for _, entry := range runtimes {
		m, _ := entry.(map[string]any)
		fmt.Fprintf(out, "  %-10s %-10s healthy=%v\n",
			str(m["name"]), str(m["kind"]), m["healthy"])
	}
	fmt.Fprintln(out, "Templates:")
	templates, _ := payload["templates"].([]any)
	for _, entry := range templates {
		m, _ := entry.(map[string]any)
		fmt.Fprintf(out, "  %-20s %-8s %s\n", str(m["id"]), str(m["version"]), str(m["title"]))
	}
	if limits, ok := payload["limits"].(map[string]any); ok {
		fmt.Fprintln(out, "Limits:")
		keys := make([]string, 0, len(limits))
		for k := range limits {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "  %-24s %v\n", k, limits[k])
		}
	}
	return nil
}

// cmdTemplates lists or shows templates.
func cmdTemplates(ctx context.Context, opts *globalOptions, out io.Writer, args []string) error {
	client, err := opts.client()
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "show" {
		if len(args) < 2 {
			return fmt.Errorf("usage: double-agent templates show <id>")
		}
		var payload map[string]any
		if err := client.do(ctx, "GET", "/api/v1/templates/"+args[1], nil, &payload); err != nil {
			return err
		}
		if opts.jsonOutput {
			return writeJSON(out, payload)
		}
		return writeJSON(out, payload)
	}
	var payload struct {
		Templates []map[string]any `json:"templates"`
	}
	if err := client.do(ctx, "GET", "/api/v1/templates", nil, &payload); err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeJSON(out, payload)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tVERSION\tRUNTIME\tIMAGE\tTITLE")
	for _, t := range payload.Templates {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
			t["id"], t["version"], t["runtime"], t["image"], t["title"])
	}
	return w.Flush()
}

// cmdInstances implements the instance subcommands.
func cmdInstances(ctx context.Context, opts *globalOptions, out io.Writer, args []string) error {
	client, err := opts.client()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		var payload struct {
			Instances []map[string]any `json:"instances"`
		}
		if err := client.do(ctx, "GET", "/api/v1/instances", nil, &payload); err != nil {
			return err
		}
		if opts.jsonOutput {
			return writeJSON(out, payload)
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATE\tTEMPLATE\tRUNTIME\tEXPIRES")
		for _, i := range payload.Instances {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
				i["id"], i["state"], i["template_id"], i["runtime"], i["expires_at"])
		}
		return w.Flush()
	case "create":
		return cmdInstanceCreate(ctx, client, opts, out, args[1:])
	case "exec":
		return cmdInstanceExec(ctx, client, opts, out, args[1:])
	case "stop":
		return cmdInstanceStop(ctx, client, opts, out, args[1:])
	case "destroy":
		return cmdInstanceDestroy(ctx, client, opts, out, args[1:])
	default:
		return fmt.Errorf("unknown instances subcommand %q", args[0])
	}
}

// cmdInstanceCreate creates an instance.
func cmdInstanceCreate(ctx context.Context, client *Client, opts *globalOptions, out io.Writer, args []string) error {
	body := map[string]any{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			return fmt.Errorf("unexpected argument %q", arg)
		}
		name := strings.TrimPrefix(arg, "--")
		value := ""
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, value = name[:eq], name[eq+1:]
		} else {
			if i+1 >= len(args) {
				return fmt.Errorf("%s requires a value", arg)
			}
			i++
			value = args[i]
		}
		switch name {
		case "template":
			body["template_id"] = value
		case "name":
			body["name"] = value
		case "ttl":
			seconds, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid ttl %q", value)
			}
			body["ttl_seconds"] = seconds
		case "env":
			pair := strings.SplitN(value, "=", 2)
			if len(pair) != 2 {
				return fmt.Errorf("invalid env %q, expected NAME=VALUE", value)
			}
			env, _ := body["env"].(map[string]string)
			if env == nil {
				env = map[string]string{}
			}
			env[pair[0]] = pair[1]
			body["env"] = env
		case "idempotency-key":
			body["idempotency_key"] = value
		default:
			return fmt.Errorf("unknown option %q", arg)
		}
	}
	if _, ok := body["template_id"]; !ok {
		return fmt.Errorf("--template is required")
	}
	var payload map[string]any
	if err := client.do(ctx, "POST", "/api/v1/instances", body, &payload); err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeJSON(out, payload)
	}
	fmt.Fprintf(out, "job %s created\n", str(payload["id"]))
	if instance, ok := payload["instance"].(map[string]any); ok {
		fmt.Fprintf(out, "instance %s is %s\n", str(instance["id"]), str(instance["state"]))
	}
	return nil
}

// cmdInstanceExec runs a command inside an instance.
func cmdInstanceExec(ctx context.Context, client *Client, opts *globalOptions, out io.Writer, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: double-agent instances exec <id> <command> [args...]")
	}
	id := args[0]
	body := map[string]any{"argv": args[1:]}
	var payload struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if err := client.do(ctx, "POST", "/api/v1/instances/"+id+"/exec", body, &payload); err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeJSON(out, payload)
	}
	if payload.Stdout != "" {
		fmt.Fprint(out, payload.Stdout)
	}
	if payload.Stderr != "" {
		fmt.Fprint(out, payload.Stderr)
	}
	if payload.ExitCode != 0 {
		return fmt.Errorf("the command exited with status %d", payload.ExitCode)
	}
	return nil
}

// cmdInstanceStop stops an instance.
func cmdInstanceStop(ctx context.Context, client *Client, opts *globalOptions, out io.Writer, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: double-agent instances stop <id>")
	}
	if err := client.do(ctx, "POST", "/api/v1/instances/"+args[0]+"/stop", map[string]any{}, nil); err != nil {
		return err
	}
	fmt.Fprintf(out, "instance %s is stopping\n", args[0])
	return nil
}

// cmdInstanceDestroy destroys an instance.
func cmdInstanceDestroy(ctx context.Context, client *Client, opts *globalOptions, out io.Writer, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: double-agent instances destroy <id>")
	}
	if err := client.do(ctx, "DELETE", "/api/v1/instances/"+args[0], nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(out, "instance %s was destroyed\n", args[0])
	return nil
}

// cmdJobs implements the job subcommands.
func cmdJobs(ctx context.Context, opts *globalOptions, out io.Writer, args []string) error {
	client, err := opts.client()
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "get" {
		if len(args) < 2 {
			return fmt.Errorf("usage: double-agent jobs get <id>")
		}
		var payload map[string]any
		if err := client.do(ctx, "GET", "/api/v1/jobs/"+args[1], nil, &payload); err != nil {
			return err
		}
		return writeJSON(out, payload)
	}
	if len(args) > 0 && args[0] == "cancel" {
		if len(args) < 2 {
			return fmt.Errorf("usage: double-agent jobs cancel <id>")
		}
		if err := client.do(ctx, "POST", "/api/v1/jobs/"+args[1]+"/cancel", map[string]any{}, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "job %s was canceled\n", args[1])
		return nil
	}
	var payload struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := client.do(ctx, "GET", "/api/v1/jobs", nil, &payload); err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeJSON(out, payload)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tSTATE\tINSTANCE\tCREATED")
	for _, j := range payload.Jobs {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
			j["id"], j["kind"], j["state"], j["instance_id"], j["created_at"])
	}
	return w.Flush()
}

// cmdArtifacts implements the artifact subcommands.
func cmdArtifacts(ctx context.Context, opts *globalOptions, out io.Writer, args []string) error {
	client, err := opts.client()
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "download" {
		if len(args) < 2 {
			return fmt.Errorf("usage: double-agent artifacts download <id> [destination]")
		}
		return downloadArtifact(ctx, client, args[1], args)
	}
	var payload struct {
		Artifacts []map[string]any `json:"artifacts"`
	}
	if err := client.do(ctx, "GET", "/api/v1/artifacts", nil, &payload); err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeJSON(out, payload)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSIZE\tMIME\tCREATED")
	for _, a := range payload.Artifacts {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
			a["id"], a["name"], a["size"], a["mime"], a["created_at"])
	}
	return w.Flush()
}

// cmdKeys implements the client key subcommands.
func cmdKeys(ctx context.Context, opts *globalOptions, out io.Writer, args []string) error {
	client, err := opts.client()
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "create" {
		name := "cli"
		if len(args) > 1 {
			name = args[1]
		}
		var payload struct {
			Secret string `json:"secret"`
			ID     string `json:"id"`
		}
		if err := client.do(ctx, "POST", "/api/v1/admin/keys",
			map[string]any{"name": name}, &payload); err != nil {
			return err
		}
		// The secret is shown exactly once.
		fmt.Fprintf(out, "%s\n", payload.Secret)
		return nil
	}
	if len(args) > 0 && args[0] == "revoke" {
		if len(args) < 2 {
			return fmt.Errorf("usage: double-agent keys revoke <id>")
		}
		if err := client.do(ctx, "DELETE", "/api/v1/admin/keys/"+args[1], nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "key %s was revoked\n", args[1])
		return nil
	}
	var payload struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := client.do(ctx, "GET", "/api/v1/admin/keys", nil, &payload); err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeJSON(out, payload)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tPREFIX\tSCOPES\tEXPIRES")
	for _, k := range payload.Keys {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
			k["id"], k["name"], k["prefix"], k["scopes"], k["expires_at"])
	}
	return w.Flush()
}

// writeJSON prints a value as indented JSON.
func writeJSON(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

// str renders a value as a string.
func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// parseDuration parses a duration that may omit the unit.
func parseDuration(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", raw)
	}
	return time.Duration(seconds) * time.Second, nil
}

// stdout is the default output stream.
var stdout io.Writer = os.Stdout
