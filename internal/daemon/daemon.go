// Package daemon wires the Double Agent control plane together and runs it.
//
// The daemon owns the configuration, the metadata store, the runtime
// registry, the local certificate authority, the HTTP listener and the
// background maintenance loops. Everything else is a library that this
// package composes.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zarigata/double-agent/internal/api"
	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/config"
	"github.com/zarigata/double-agent/internal/mcpsrv"
	"github.com/zarigata/double-agent/internal/mdns"
	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/pki"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/runtime/docker"
	"github.com/zarigata/double-agent/internal/runtime/local"
	"github.com/zarigata/double-agent/internal/runtime/proxmox"
	"github.com/zarigata/double-agent/internal/store"
	"github.com/zarigata/double-agent/internal/templates"
	"github.com/zarigata/double-agent/internal/version"
)

// Daemon is a running control plane.
type Daemon struct {
	cfg          config.Config
	log          *slog.Logger
	store        *store.Store
	registry     *runtime.Registry
	orchestrator *orchestrator.Orchestrator
	catalogue    *templates.Catalogue
	artifacts    *artifacts.Store
	mcp          *mcpsrv.Server
	advertiser   *mdns.Advertiser

	httpServer *http.Server
	listener   net.Listener

	// caPath is the path of the published certificate authority.
	caPath string

	// wg tracks the background loops.
	wg sync.WaitGroup
	// shutdownOnce guards the shutdown sequence.
	shutdownOnce sync.Once
}

// Options configures a Daemon.
type Options struct {
	// Config is the daemon configuration.
	Config config.Config
	// Logger receives the daemon logs.
	Logger *slog.Logger
}

// New creates a Daemon from a configuration.
func New(opts Options) (*Daemon, error) {
	cfg := opts.Config
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "daemon")

	// The runtime backends resolve symlinks in their workspace root while the
	// orchestrator builds workspace paths from the configured directory. On
	// systems where the data directory is a symlink (macOS turns /var into
	// /private/var) the two would disagree and every instance would be
	// rejected as escaping its workspace. Resolving once, here, keeps the
	// whole process on a single spelling of the path.
	//
	// The directory is created first: a data directory that does not exist
	// yet cannot be resolved, and keeping its unresolved spelling would
	// break the very first start of a node (EvalSymlinks answers NotExist
	// for a missing final component).
	os.MkdirAll(cfg.DataDir, 0o750)
	if resolved, err := filepath.EvalSymlinks(cfg.DataDir); err == nil {
		cfg.DataDir = resolved
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("daemon: resolve the data directory: %w", err)
	}

	for _, dir := range []string{cfg.DataDir, cfg.StatePath(), cfg.CachePath(), cfg.LogPath(), cfg.PKIPath()} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("daemon: create %s: %w", dir, err)
		}
	}

	st, err := store.Open(cfg.DatabasePath())
	if err != nil {
		return nil, fmt.Errorf("daemon: open store: %w", err)
	}

	registry := runtime.NewRegistry()
	dockerRT, err := docker.New(docker.Config{
		Host:           cfg.Runtime.Docker.Host,
		WorkspaceRoot:  cfg.Runtime.Docker.WorkspaceRoot,
		AllowedImages:  cfg.Runtime.Docker.AllowedImages,
		PullImages:     cfg.Runtime.Docker.PullImages,
		DefaultNetwork: cfg.Runtime.Docker.Network,
	})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("daemon: create docker backend: %w", err)
	}
	registry.Register(dockerRT)

	if cfg.Runtime.Local.Enabled {
		localRT, err := local.New(local.Config{WorkspaceRoot: cfg.Runtime.Local.WorkspaceRoot})
		if err != nil {
			st.Close()
			return nil, fmt.Errorf("daemon: create local backend: %w", err)
		}
		registry.Register(localRT)
	}
	if cfg.Runtime.Proxmox.Enabled {
		proxmoxRT, err := proxmox.New(proxmox.Config{
			Endpoint:         cfg.Runtime.Proxmox.Endpoint,
			Username:         cfg.Runtime.Proxmox.Username,
			Password:         cfg.Runtime.Proxmox.Password,
			TokenID:          cfg.Runtime.Proxmox.TokenID,
			TokenSecret:      cfg.Runtime.Proxmox.TokenSecret,
			Insecure:         cfg.Runtime.Proxmox.Insecure,
			Node:             cfg.Runtime.Proxmox.Node,
			Storage:          cfg.Runtime.Proxmox.Storage,
			TemplateVMID:     cfg.Runtime.Proxmox.TemplateVMID,
			AllowedTemplates: cfg.Runtime.Proxmox.AllowedTemplates,
			Bridge:           cfg.Runtime.Proxmox.Bridge,
			WorkspaceRoot:    cfg.Runtime.Proxmox.WorkspaceRoot,
		})
		if err != nil {
			st.Close()
			return nil, fmt.Errorf("daemon: create proxmox backend: %w", err)
		}
		registry.Register(proxmoxRT)
	}
	registry.Default = cfg.Runtime.Default
	if _, ok := registry.Get(registry.Default); !ok {
		st.Close()
		return nil, fmt.Errorf("daemon: the default runtime %q is not registered", registry.Default)
	}

	catalogue := templates.NewCatalogue()
	for _, t := range templates.Builtin() {
		if err := catalogue.Publish(t); err != nil {
			st.Close()
			return nil, fmt.Errorf("daemon: publish builtin template: %w", err)
		}
	}
	if cfg.TemplatesDir != "" {
		loaded, err := templates.LoadDir(cfg.TemplatesDir)
		if err != nil {
			st.Close()
			return nil, fmt.Errorf("daemon: load templates: %w", err)
		}
		for _, t := range loaded {
			if err := catalogue.Publish(t); err != nil {
				st.Close()
				return nil, fmt.Errorf("daemon: publish template: %w", err)
			}
		}
	}

	artifactStore, err := artifacts.New(artifacts.Options{
		Root:    cfg.Artifacts.Root,
		MaxSize: cfg.Artifacts.MaxSizeBytes,
	})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("daemon: create artifact store: %w", err)
	}

	orch, err := orchestrator.New(orchestrator.Options{
		Store:     st,
		Catalogue: catalogue,
		Runtimes:  registry,
		Logger:    log,
		DataDir:   cfg.DataDir,
		Limits: orchestrator.Limits{
			MaxConcurrentJobs:   cfg.Limits.MaxConcurrentJobs,
			MaxInstances:        cfg.Limits.MaxInstances,
			MaxInstancesPerUser: cfg.Limits.MaxInstancesPerUser,
			DefaultTTL:          cfg.Limits.DefaultTTL.Duration(),
			MaxTTL:              cfg.Limits.MaxTTL.Duration(),
			MaxArtifactBytes:    cfg.Artifacts.MaxSizeBytes,
			MaxExportBytes:      cfg.Artifacts.MaxSizeBytes,
		},
		Artifacts:         artifactStore,
		ArtifactRetention: cfg.Artifacts.Retention.Duration(),
	})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("daemon: create orchestrator: %w", err)
	}

	mcpServer, err := mcpsrv.NewServer(orch, mcpsrv.Options{
		Logger:            log,
		Store:             st,
		Registry:          registry,
		Version:           version.Current().Version,
		Artifacts:         artifactStore,
		ArtifactRetention: cfg.Artifacts.Retention.Duration(),
		UploadMaxBytes:    cfg.Artifacts.MaxUploadBytes,
	})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("daemon: create mcp server: %w", err)
	}

	d := &Daemon{
		cfg:          cfg,
		log:          log,
		store:        st,
		registry:     registry,
		orchestrator: orch,
		catalogue:    catalogue,
		artifacts:    artifactStore,
		mcp:          mcpServer,
	}
	return d, nil
}

// Run starts the daemon and blocks until the context is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	// The certificate authority is created on the first start so that the
	// browser trust install happens once per node.
	caPath, certPath, keyPath, err := pki.Ensure(d.cfg.PKIPath(), d.certificateHosts(), 5*365*24*time.Hour)
	if err != nil {
		return fmt.Errorf("daemon: prepare the certificate authority: %w", err)
	}
	d.caPath = caPath
	d.log.Info("certificate authority ready", "ca", caPath)

	// The state a restart interrupted is recovered before the node serves,
	// so no client observes an orphaned job or a stale instance state.
	report, err := d.orchestrator.ReconcileAfterRestart(ctx)
	if err != nil {
		d.log.Error("reconciliation after restart failed", "error", err)
	} else if !report.Empty() {
		d.log.Info("recovered state after restart",
			"failed_jobs", report.FailedJobs,
			"failed_instances", report.FailedInstances,
			"corrected_instances", report.CorrectedInstances)
	}

	handler := d.buildHandler(certPath, keyPath)
	addr := fmt.Sprintf("%s:%d", d.cfg.Listen.Address, d.cfg.Listen.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("daemon: listen on %s: %w", addr, err)
	}
	d.listener = listener
	d.httpServer = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0, // long running console streams must not be cut off
		IdleTimeout:       120 * time.Second,
	}

	d.log.Info("listening", "address", listener.Addr().String(), "tls", d.cfg.Listen.TLS.Enabled)

	if d.cfg.Discovery.Enabled {
		d.startDiscovery(ctx, listener.Addr().(*net.TCPAddr).Port)
	}

	d.startMaintenance(ctx)

	serveErr := make(chan error, 1)
	go func() {
		if d.cfg.Listen.TLS.Enabled {
			serveErr <- d.httpServer.ServeTLS(listener, certPath, keyPath)
			return
		}
		serveErr <- d.httpServer.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		d.log.Info("shutting down")
	case sig := <-signals:
		d.log.Info("signal received", "signal", sig.String())
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("daemon: serve: %w", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := d.httpServer.Shutdown(shutdownCtx); err != nil {
		d.log.Error("graceful shutdown failed", "error", err)
	}
	d.wg.Wait()
	return nil
}

// buildHandler assembles the HTTP routes of the daemon.
func (d *Daemon) buildHandler(certPath, keyPath string) http.Handler {
	mux := http.NewServeMux()
	mcpHandler := mcpsrv.NewHandler(d.mcp)
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/ca.crt", d.caHandler())
	apiHandler := api.New(api.Options{
		Store:        d.store,
		Orchestrator: d.orchestrator,
		Artifacts:    d.artifacts,
		Config:       d.cfg,
		Logger:       d.log,
		Registry:     d.registry,
		Catalogue:    d.catalogue,
	})
	mux.Handle("/api/", apiHandler)
	mux.Handle("/", apiHandler)
	return mux
}

// caHandler serves the certificate authority certificate.
func (d *Daemon) caHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(d.caPath)
		if err != nil {
			http.Error(w, "the certificate authority is not available", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		w.Header().Set("Content-Disposition", `attachment; filename="double-agent-ca.crt"`)
		w.Write(data)
	})
}

// startDiscovery begins the mDNS advertisement.
func (d *Daemon) startDiscovery(ctx context.Context, port int) {
	name := d.cfg.Discovery.InstanceName
	if name == "" {
		name = "double-agent"
	}
	advertiser := mdns.New(name, port)
	advertiser.TXT = []string{
		"ver=" + version.Current().Version,
		"api=" + "v1",
	}
	if err := advertiser.Start(ctx); err != nil {
		// Discovery is a convenience: a node that cannot multicast is still
		// fully usable through its address.
		d.log.Warn("discovery is unavailable", "error", err)
		return
	}
	d.advertiser = advertiser
	d.log.Info("advertising on the local network", "name", name, "port", port)
}

// startMaintenance runs the background housekeeping loops.
func (d *Daemon) startMaintenance(ctx context.Context) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.runMaintenance(ctx)
			}
		}
	}()
}

// runMaintenance performs one round of housekeeping.
func (d *Daemon) runMaintenance(ctx context.Context) {
	reaped, err := d.orchestrator.ReapExpired(ctx)
	if err != nil {
		d.log.Error("failed to reap expired instances", "error", err)
	} else if reaped > 0 {
		d.log.Info("reaped expired instances", "count", reaped)
	}
	if _, err := d.store.DeleteExpiredSessions(ctx, time.Now().UTC()); err != nil {
		d.log.Error("failed to delete expired sessions", "error", err)
	}
}

// certificateHosts returns the host names the server certificate covers.
func (d *Daemon) certificateHosts() []string {
	hosts := []string{"localhost", "double-agent.local"}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return hosts
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		// The loopback address is included: a node bound to 127.0.0.1 is the
		// safest deployment, and its clients must not be told the certificate
		// does not match.
		if ipNet.IP.IsLinkLocalUnicast() || ipNet.IP.IsLinkLocalMulticast() {
			continue
		}
		hosts = append(hosts, ipNet.IP.String())
	}
	hostname, err := os.Hostname()
	if err == nil && hostname != "" {
		hosts = append(hosts, hostname)
		hosts = append(hosts, strings.Split(hostname, ".")[0]+".local")
	}
	return hosts
}

// Close releases the resources of the daemon.
func (d *Daemon) Close() error {
	d.shutdownOnce.Do(func() {
		if d.advertiser != nil {
			_ = d.advertiser.Stop()
		}
		if d.store != nil {
			_ = d.store.Close()
		}
	})
	return nil
}

// BootstrapAdmin creates the first administrator when the node has no users.
//
// The password is read from the environment so that it never appears in the
// process arguments or in the configuration file.
func (d *Daemon) BootstrapAdmin(ctx context.Context, username, password string) (bool, error) {
	count, err := d.store.CountUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("daemon: count users: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	if username == "" {
		username = "admin"
	}
	hash, err := hashPassword(password)
	if err != nil {
		return false, err
	}
	_, err = d.store.CreateUser(ctx, store.User{
		ID:           newUserID(),
		Username:     username,
		PasswordHash: hash,
		Role:         store.RoleAdmin,
	})
	if err != nil {
		return false, fmt.Errorf("daemon: create the administrator: %w", err)
	}
	d.log.Info("created the initial administrator", "username", username)
	return true, nil
}

// ScopesForRole returns the scopes granted to a role.
func ScopesForRole(role store.Role) []policy.Scope {
	if role == store.RoleAdmin {
		return policy.AdminScopes
	}
	return policy.DefaultUserScopes
}

// Config returns the daemon configuration.
func (d *Daemon) Config() config.Config { return d.cfg }

// Store returns the metadata store.
func (d *Daemon) Store() *store.Store { return d.store }

// Runtimes returns the registry of execution backends available on this node.
func (d *Daemon) Runtimes() *runtime.Registry { return d.registry }

// CA returns the path of the published certificate authority.
func (d *Daemon) CA() string { return d.caPath }

// DataDir returns the node data directory.
func (d *Daemon) DataDir() string { return d.cfg.DataDir }

// ArtifactRoot returns the artifact store root.
func (d *Daemon) ArtifactRoot() string { return d.artifacts.Root() }

// StateFile returns the path of a file inside the state directory.
func (d *Daemon) StateFile(parts ...string) string {
	return filepath.Join(append([]string{d.cfg.DataDir, "state"}, parts...)...)
}
