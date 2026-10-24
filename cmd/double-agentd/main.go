// Command double-agentd runs the Double Agent control plane.
//
// The daemon owns the node state: the metadata database, the local certificate
// authority, the runtime registry and the HTTP listener that serves the Web
// UI, the REST API and the MCP endpoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/zarigata/double-agent/internal/config"
	"github.com/zarigata/double-agent/internal/daemon"
	"github.com/zarigata/double-agent/internal/version"
)

// exit codes used by the daemon.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command line and returns the process exit code.
func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], stdout, stderr)
	case "backup":
		return backupCommand(args[1:], stdout, stderr)
	case "doctor":
		return doctorCommand(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version.Current().String())
		return exitOK
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "double-agentd: unknown command %q\n\n", args[0])
		usage(stderr)
		return exitUsage
	}
}

// serve starts the daemon.
func serve(args []string, stdout, stderr *os.File) int {
	fs := newFlagSet("serve", stderr)
	configPath := fs.String("config", "", "path of the configuration file")
	dataDir := fs.String("data-dir", "", "override the data directory")
	address := fs.String("address", "", "override the bind address")
	port := fs.Int("port", 0, "override the listening port")
	noTLS := fs.Bool("no-tls", false, "serve plaintext HTTP (development only)")
	logLevel := fs.String("log-level", "", "override the log level")
	bootstrapAdmin := fs.String("bootstrap-admin", "", "username of the initial administrator")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "double-agentd: %v\n", err)
		return exitError
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *address != "" {
		cfg.Listen.Address = *address
	}
	if *port != 0 {
		cfg.Listen.Port = *port
	}
	if *noTLS {
		cfg.Listen.TLS.Enabled = false
	}
	if *logLevel != "" {
		cfg.LogLevel = *logLevel
	}
	if *bootstrapAdmin != "" {
		cfg.Auth.BootstrapAdmin = *bootstrapAdmin
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "double-agentd: %v\n", err)
		return exitError
	}

	logger := newLogger(cfg.LogLevel, stdout)
	slog.SetDefault(logger)

	d, err := daemon.New(daemon.Options{Config: cfg, Logger: logger})
	if err != nil {
		fmt.Fprintf(stderr, "double-agentd: %v\n", err)
		return exitError
	}
	defer d.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// The password of the initial administrator comes from the environment so
	// that it never appears in the process arguments.
	password := os.Getenv("DOUBLE_AGENT_ADMIN_PASSWORD")
	if password == "" {
		password = os.Getenv("DOUBLE_AGENT_BOOTSTRAP_PASSWORD")
	}
	if password != "" {
		created, err := d.BootstrapAdmin(ctx, cfg.Auth.BootstrapAdmin, password)
		if err != nil {
			fmt.Fprintf(stderr, "double-agentd: %v\n", err)
			return exitError
		}
		if created {
			logger.Info("administrator account created", "username", cfg.Auth.BootstrapAdmin)
		}
	} else if n, _ := d.Store().CountUsers(ctx); n == 0 {
		logger.Warn("no account exists yet: set DOUBLE_AGENT_ADMIN_PASSWORD to create the first administrator")
	}

	if err := d.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "double-agentd: %v\n", err)
		return exitError
	}
	return exitOK
}

// newLogger builds the structured logger.
func newLogger(level string, out *os.File) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewTextHandler(out, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}

// usage prints the command line help.
func usage(out *os.File) {
	fmt.Fprint(out, `double-agentd - the Double Agent control plane

Usage:
  double-agentd serve [flags]   run the daemon
  double-agentd backup [flags]  create or restore a backup of the node
  double-agentd doctor [flags]  check the health of the node
  double-agentd version         print the build identity
  double-agentd help            print this help

Flags of "serve":
  --config PATH             path of the YAML configuration file
  --data-dir PATH           override the data directory
  --address ADDR            override the bind address
  --port PORT               override the listening port
  --no-tls                  serve plaintext HTTP (development only)
  --log-level LEVEL         debug, info, warn or error
  --bootstrap-admin NAME    username of the initial administrator

Flags of "doctor":
  --config PATH             path of the YAML configuration file
  --data-dir PATH           override the data directory
  --proxmox-vmid VMID       a running disposable guest (da-) used to verify the
                            Proxmox guest agent and whether the guest runs with
                            KVM or under QEMU software emulation

The password of the initial administrator is read from the environment
variable DOUBLE_AGENT_ADMIN_PASSWORD.
`)
}
