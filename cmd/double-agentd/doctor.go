package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
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
// so that an operator never mistakes it for an isolated runtime. A backend
// that offers deeper checks, such as the Proxmox template and guest agent, is
// asked for them as well.
func doctorCommand(args []string, stdout, stderr *os.File) int {
	fs := newFlagSet("doctor", stderr)
	configPath := fs.String("config", "", "path of the configuration file")
	dataDir := fs.String("data-dir", "", "override the data directory")
	proxmoxVMID := fs.Int("proxmox-vmid", 0, "a running disposable guest (da-) used to verify the guest agent and the virtualization mode")
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

	// The certificate authority must exist and be parsable. The file on disk
	// is read, not the copy of a running daemon: doctor runs in its own
	// process, which never served and therefore holds no path in memory.
	caFile := filepath.Join(cfg.PKIPath(), "ca.crt")
	if ca := d.CA(); ca != "" {
		caFile = ca
	}
	if detail, err := readCACertificate(caFile); err != nil {
		if os.IsNotExist(err) {
			report("certificate authority", "FAIL", "the authority was not created yet (run the daemon once)", false)
		} else {
			report("certificate authority", "FAIL", err.Error(), false)
		}
	} else {
		report("certificate authority", "OK", detail, true)
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
		// A backend with a deeper diagnostics surface is asked for it, so a
		// broken template or a missing guest agent surfaces before the first
		// workload runs.
		if checks, ok := backend.(runtime.DoctorChecks); ok {
			probeDoctorChecks(ctx, checks, *proxmoxVMID, report)
		}
	}

	w.Flush()
	if !healthy {
		return exitError
	}
	return exitOK
}

// probeDoctorChecks runs the extended diagnostics of a backend.
//
// The guest agent check is a warning when it could only inspect the stopped
// template: the agent package inside the image needs a running guest to be
// proven, which the operator opts into with --proxmox-vmid.
func probeDoctorChecks(ctx context.Context, backend runtime.DoctorChecks, vmid int, report func(string, string, string, bool)) {
	name := "runtime:" + backend.Name()
	if err := backend.CheckTemplate(ctx); err != nil {
		report(name+" template", "FAIL", err.Error(), false)
	} else {
		report(name+" template", "OK", "the configured template exists, is a template and is allowed", true)
	}
	guest, err := backend.CheckGuestAgent(ctx, vmid)
	switch {
	case err != nil:
		report(name+" guest agent", "FAIL", err.Error(), false)
	case !guest.Answered:
		// A static check cannot prove the agent inside the image; it is a
		// warning, not a failure.
		report(name+" guest agent", "WARN", guest.Detail, true)
	default:
		report(name+" guest agent", "OK", "the agent of the guest answered a command", true)
		if guest.VirtType != "" && !guest.HardwareAccelerated() {
			// Software emulation is a fact of the host, not a broken node:
			// it stays a warning so the limitation is visible.
			report(name+" acceleration", "WARN", guest.Detail, true)
		} else if guest.VirtType != "" {
			report(name+" acceleration", "OK", guest.Detail, true)
		}
	}
}

// readCACertificate parses the certificate authority of the node and returns
// a one line summary of it.
func readCACertificate(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", fmt.Errorf("%s holds no certificate", path)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("%s is not parsable: %w", path, err)
	}
	return fmt.Sprintf("%s (%s), valid until %s", certificate.Subject.String(), path, certificate.NotAfter.Format("2006-01-02")), nil
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
