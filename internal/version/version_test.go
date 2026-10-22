package version_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/zarigata/double-agent/internal/version"
)

// TestCurrentIsNotEmpty checks that the build identity is complete.
func TestCurrentIsNotEmpty(t *testing.T) {
	info := version.Current()
	if info.Version == "" {
		t.Error("the build carries no version")
	}
	if info.Commit == "" {
		t.Error("the build carries no commit")
	}
	if info.Date == "" {
		t.Error("the build carries no date")
	}
	if info.GoVersion != runtime.Version() {
		t.Errorf("the build reports the go version %q, want %q", info.GoVersion, runtime.Version())
	}
	if info.OS != runtime.GOOS {
		t.Errorf("the build reports the system %q, want %q", info.OS, runtime.GOOS)
	}
	if info.Arch != runtime.GOARCH {
		t.Errorf("the build reports the architecture %q, want %q", info.Arch, runtime.GOARCH)
	}
}

// TestTheDescriptionHoldsTheVersion checks the one line description of a
// build.
func TestTheDescriptionHoldsTheVersion(t *testing.T) {
	info := version.Current()
	description := info.String()
	if !strings.Contains(description, info.Version) {
		t.Errorf("the description %q does not carry the version %q", description, info.Version)
	}
	if !strings.Contains(description, info.GoVersion) {
		t.Errorf("the description %q does not carry the go version", description)
	}
}
