package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/zarigata/double-agent/internal/config"
	"github.com/zarigata/double-agent/internal/daemon"
	"github.com/zarigata/double-agent/internal/runtime"
)

// doctorCommand reports the health of a node.
//
// The command never guesses: a backend that cannot be reached is reported as
// unavailable, and the development-only local backend is reported as unsafe
// so that an operator never mistakes it for an isolated runtime.
func doctorCommand(args []string, stdout, stderr *os.File) int {
	fs := newFlagSet("doctor", stderr)
	configPath := fs.String("config", "", "path of the configuration file")
	dataDir := fs.String("data-dir", "", "override the data directory")
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

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	d, err := daemon.New(daemon.Options{Config: cfg})
	if err != nil {
		fmt.Fprintf(stderr, "double-agentd: %v\n", err)
		return exitError
	}
	defer d.Close()

	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CHECK\tSTATUS\tDETAIL")

	healthy := true
	report := func(name, status, detail string, ok bool) {
		if !ok {
			healthy = false
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, status, detail)
	}

	// The metadata database must be readable and migrated.
	if _, err := d.Store().CountUsers(ctx); err != nil {
		report("database", "FAIL", err.Error(), false)
	} else {
		report("database", "OK", cfg.DatabasePath(), true)
	}

	// The certificate authority must exist and be parsable.
	if d.CA() == "" {
		report("certificate authority", "FAIL", "the authority was not created yet", false)
	} else {
		report("certificate authority", "OK", d.CA(), true)
	}

	// The artifact store must be writable.
	root := d.ArtifactRoot()
	if err := os.MkdirAll(root, 0o750); err != nil {
		report("artifact store", "FAIL", err.Error(), false)
	} else {
		report("artifact store", "OK", root, true)
	}

	// Every runtime backend is probed on its own.
	for _, name := range d.Runtimes().Names() {
		backend, ok := d.Runtimes().Get(name)
		if !ok {
			continue
		}
		probeRuntime(ctx, backend, report)
	}

	w.Flush()
	if !healthy {
		return exitError
	}
	return exitOK
}

// probeRuntime checks that a backend answers and reports its kind.
func probeRuntime(ctx context.Context, backend runtime.Runtime, report func(string, string, string, bool)) {
	name := "runtime:" + backend.Name()
	if backend.Name() == "local" {
		// The local backend runs the workload as a host process. It is not an
		// isolation boundary and must never be presented as one.
		report(name, "WARN", "development only: it runs the workload as a host process", true)
		return
	}
	if err := backend.Health(ctx); err != nil {
		report(name, "FAIL", err.Error(), false)
		return
	}
	report(name, "OK", string(backend.Kind())+" backend", true)
}
