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

// runtimeCoreLabel is the kernel's own row, and the fallback name when
// no binary has registered an identity.
const runtimeCoreLabel = "Runtime Core"

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
		name = runtimeCoreLabel
	}
	if version == "" {
		version = GAPIVersion
	}

	schemaHash := truncate16(SchemaHash)
	commit := truncate16(active.Commit)

	// Rows first, then one column width taken from the longest label
	// (cli-contract.md). The block used to carry four different hardcoded
	// paddings - %-11s for the name, and separate widths for Go DDK,
	// Platform and a 21-character "Protobuf Schema Hash:" that aligned
	// with nothing - so adding a field meant re-guessing the alignment.
	rows := [][2]string{{name, version}}
	// Runtime Core is emitted only when the invoking binary is not the
	// kernel itself, so gapid does not print its own version twice.
	if name != runtimeCoreLabel {
		rows = append(rows, [2]string{runtimeCoreLabel, GAPIVersion})
	}
	rows = append(rows,
		[2]string{"Go DDK", GoDDKVersion},
		[2]string{"Python DDK", PythonDDKVersion},
		[2]string{"Protobuf Schema Hash", schemaHash},
		[2]string{"Go Version", active.GoVersion},
		[2]string{"Platform", active.Platform},
		[2]string{"Commit", commit},
		[2]string{"Build Tag", BuildTag},
		[2]string{"Built Date", active.BuildDate},
		[2]string{"Built By", active.BuiltBy},
	)

	width := 0
	for _, r := range rows {
		if n := len(r[0]) + 1; n > width { // +1 for the colon
			width = n
		}
	}

	var out strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&out, "%-*s %s\n", width, r[0]+":", r[1])
	}
	return out.String()
}

// truncate16 bounds the hash-shaped fields, which are long enough to
// dominate the block and are identifying at 16 characters.
func truncate16(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}
