// Package version carries the build-time version information.
//
// The values are injected at build time with -ldflags -X; see the Makefile and
// .goreleaser.yaml.
package version

import (
	"fmt"
	"runtime"
)

var (
	// Version is the semantic version, e.g. v1.2.3; "dev" when not injected.
	Version = "dev"
	// Commit is the short commit hash the binary was built from.
	Commit = "none"
	// BuildDate is the build timestamp (RFC3339, UTC).
	BuildDate = "unknown"
)

// Info is the structured form of the version information.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Get returns the version information of the running binary.
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}

// String renders a single-line human-readable description.
func (i Info) String() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s, %s)",
		i.Version, i.Commit, i.BuildDate, i.GoVersion, i.Platform)
}
