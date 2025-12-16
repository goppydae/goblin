package version

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
)

// Injected at build time via -ldflags
var (
	GAPIVersion      = "dev"
	GoDDKVersion     = "dev"
	PythonDDKVersion = "dev"
	BuildTag         = "dev"
	SchemaHash       = "unknown"
	Commit           = "unknown"
	Date             = "unknown"
	BuiltBy          = "unknown"
)

type Info struct {
	Name      string
	Version   string
	Commit    string
	BuildDate string
	BuiltBy   string
	GoVersion string
	Platform  string
}

var (
	mu     sync.RWMutex
	active Info
)

func init() {
	active = Info{
		Commit:    Commit,
		BuildDate: Date,
		BuiltBy:   BuiltBy,
		GoVersion: runtime.Version(),
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}

// BinaryVersion returns the registered version string for the current binary.
func BinaryVersion() string {
	mu.RLock()
	defer mu.RUnlock()

	if active.Version != "" {
		return active.Version
	}
	return GAPIVersion // fallback from linker flags
}

// SetBinaryNameAndVersion lets a binary like gapictl override the top-level label
func SetBinaryNameAndVersion(name, version string) {
	mu.Lock()
	defer mu.Unlock()
	active.Name = name
	active.Version = version
}

// SetBuildMetadata lets downstream components override specific build details
func SetBuildMetadata(overrides Info) {
	mu.Lock()
	defer mu.Unlock()
	if overrides.Commit != "" {
		active.Commit = overrides.Commit
	}
	if overrides.BuildDate != "" {
		active.BuildDate = overrides.BuildDate
	}
	if overrides.BuiltBy != "" {
		active.BuiltBy = overrides.BuiltBy
	}
	if overrides.GoVersion != "" {
		active.GoVersion = overrides.GoVersion
	}
	if overrides.Platform != "" {
		active.Platform = overrides.Platform
	}
}

// Summary prints a single version block, merging binary and GAPI info
func Summary() string {
	mu.RLock()
	defer mu.RUnlock()

	name := active.Name
	version := active.Version
	if name == "" {
		name = "GAPI Core"
	}
	if version == "" {
		version = GAPIVersion
	}

	var out strings.Builder
	fmt.Fprintf(&out, "%-11s %s\n", name+":", version)

	// Avoid duplicate GAPI Core line if it's already the label
	if name != "GAPI Core" {
		fmt.Fprintf(&out, "GAPI Core:  %s\n", GAPIVersion)
	}

	fmt.Fprintf(&out, "Go DDK:     %s\n", GoDDKVersion)
	fmt.Fprintf(&out, "Python DDK: %s\n", PythonDDKVersion)
	schemaHash := SchemaHash
	if len(schemaHash) > 16 {
		schemaHash = schemaHash[:16]
	}
	fmt.Fprintf(&out, "Protobuf Schema Hash: %s\n", schemaHash)
	fmt.Fprintf(&out, "Go Version: %s\n", active.GoVersion)
	fmt.Fprintf(&out, "Platform:   %s\n", active.Platform)

	commit := active.Commit
	if len(commit) > 16 {
		commit = commit[:16]
	}
	fmt.Fprintf(&out, "Commit:     %s\n", commit)
	fmt.Fprintf(&out, "Build Tag:  %s\n", BuildTag)
	fmt.Fprintf(&out, "Built Date: %s\n", active.BuildDate)
	fmt.Fprintf(&out, "Built By:   %s\n", active.BuiltBy)
	return out.String()
}
