package daemon_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/config"
	"github.com/zarigata/double-agent/internal/daemon"
	"github.com/zarigata/double-agent/internal/store"
)

// freePort returns a port that is free at the moment.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// newDaemon returns a daemon that runs on a temporary directory.
func newDaemon(t *testing.T) (*daemon.Daemon, config.Config) {
	t.Helper()
	dir := t.TempDir()
	// The runtime backends resolve the symbolic links of their workspace
	// root, so the daemon and the backends have to agree on the spelling of
	// the path.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve the data directory: %v", err)
	}

	cfg := config.Default()
	cfg.DataDir = resolved
	cfg.Listen.Address = "127.0.0.1"
	cfg.Listen.Port = freePort(t)
	cfg.Listen.TLS.Enabled = false
	cfg.Runtime.Default = "local"
	cfg.Runtime.Local.Enabled = true
	cfg.Runtime.Local.WorkspaceRoot = filepath.Join(resolved, "instances")
	cfg.Runtime.Docker.WorkspaceRoot = filepath.Join(resolved, "instances")
	cfg.Artifacts.Root = filepath.Join(resolved, "artifacts")
	// The advertisement needs a multicast network, which a test machine
	// does not always have.
	cfg.Discovery.Enabled = false

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := daemon.New(daemon.Options{Config: cfg, Logger: logger})
	if err != nil {
		t.Fatalf("create the daemon: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d, cfg
}

// TestTheDaemonCreatesItsState checks the directories a node needs.
func TestTheDaemonCreatesItsState(t *testing.T) {
	d, cfg := newDaemon(t)

	if _, err := os.Stat(cfg.DatabasePath()); err != nil {
		t.Errorf("the database was not created: %v", err)
	}
	info, err := os.Stat(cfg.Artifacts.Root)
	if err != nil {
		t.Fatalf("the artifact root was not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("the artifact root is not a directory")
	}
	info, err = os.Stat(cfg.PKIPath())
	if err != nil {
		t.Fatalf("the pki directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("the pki path is not a directory")
	}
	if d.DataDir() != cfg.DataDir {
		t.Errorf("the data directory is %q, want %q", d.DataDir(), cfg.DataDir)
	}
	if d.ArtifactRoot() != cfg.Artifacts.Root {
		t.Errorf("the artifact root is %q, want %q", d.ArtifactRoot(), cfg.Artifacts.Root)
	}
}

// TestTheDaemonStartsAndShutsDownCleanly checks the lifecycle of a node.
func TestTheDaemonStartsAndShutsDownCleanly(t *testing.T) {
	d, cfg := newDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errs := make(chan error, 1)
	go func() {
		errs <- d.Run(ctx)
	}()

	// The daemon answers the requests of the browser.
	url := "http://" + net.JoinHostPort(cfg.Listen.Address, strconv.Itoa(cfg.Listen.Port)) + "/ca.crt"
	deadline := time.Now().Add(15 * time.Second)
	started := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			started = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !started {
		cancel()
		t.Fatalf("the daemon did not answer on %s", url)
	}

	// The certificate authority is published once the daemon runs.
	if _, err := os.Stat(filepath.Join(cfg.PKIPath(), "ca.crt")); err != nil {
		t.Errorf("the certificate authority was not created: %v", err)
	}
	if d.CA() == "" {
		t.Error("the daemon does not publish a certificate authority")
	}

	// The shutdown is clean.
	cancel()
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("the daemon returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon did not shut down")
	}
}

// TestBootstrapAdminCreatesTheFirstAdministrator checks the creation of the
// first account of a node.
func TestBootstrapAdminCreatesTheFirstAdministrator(t *testing.T) {
	d, _ := newDaemon(t)
	ctx := context.Background()

	created, err := d.BootstrapAdmin(ctx, "admin", "administrator-pass-1")
	if err != nil {
		t.Fatalf("bootstrap the administrator: %v", err)
	}
	if !created {
		t.Fatal("the administrator was not created")
	}
	user, err := d.Store().UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("read the administrator: %v", err)
	}
	if user.Role != store.RoleAdmin {
		t.Errorf("the account has the role %q, want %q", user.Role, store.RoleAdmin)
	}
	if user.PasswordHash == "" {
		t.Error("the account carries no password hash")
	}
	if user.PasswordHash == "administrator-pass-1" {
		t.Error("the password was stored in clear text")
	}

	// A second call does not create a second administrator.
	created, err = d.BootstrapAdmin(ctx, "admin", "administrator-pass-1")
	if err != nil {
		t.Fatalf("bootstrap the administrator again: %v", err)
	}
	if created {
		t.Error("a second administrator was created")
	}
	count, err := d.Store().CountUsers(ctx)
	if err != nil {
		t.Fatalf("count the users: %v", err)
	}
	if count != 1 {
		t.Errorf("the node holds %d accounts, want 1", count)
	}
}

// TestBootstrapAdminRefusesAWeakPassword checks the password policy of the
// bootstrap.
func TestBootstrapAdminRefusesAWeakPassword(t *testing.T) {
	d, _ := newDaemon(t)
	if _, err := d.BootstrapAdmin(context.Background(), "admin", "short"); err == nil {
		t.Fatal("a weak password was accepted")
	}
}

// TestTheScopesOfTheRoles checks the scopes of the two roles.
func TestTheScopesOfTheRoles(t *testing.T) {
	if len(daemon.ScopesForRole(store.RoleAdmin)) == 0 {
		t.Error("an administrator holds no scope")
	}
	if len(daemon.ScopesForRole(store.RoleUser)) == 0 {
		t.Error("a user holds no scope")
	}
	if len(daemon.ScopesForRole(store.RoleAdmin)) <= len(daemon.ScopesForRole(store.RoleUser)) {
		t.Error("an administrator holds no more scopes than a user")
	}
}
