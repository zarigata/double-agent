// Package version carries the build identity of the daemon.
package version

import (
	"fmt"
	"runtime"
)

// Build information, set at link time with -ldflags.
var (
	// Version is the semantic version of the build.
	Version = "0.1.0-dev"
	// Commit is the source revision of the build.
	Commit = "unknown"
	// Date is the build timestamp.
	Date = "unknown"
)

// Info describes the running build.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Current returns the build identity of the running binary.
func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

// String renders a one line description of the build.
func (i Info) String() string {
	return fmt.Sprintf("double-agent %s (commit %s, built %s, %s %s/%s)",
		i.Version, i.Commit, i.Date, i.GoVersion, i.OS, i.Arch)
}
