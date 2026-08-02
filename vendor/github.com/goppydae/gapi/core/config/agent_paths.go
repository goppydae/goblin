package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/goppydae/gapi/core/product"
)

// Scope selects which tier list is searched.
//
// It is an explicit parameter and MUST NOT be inferred from the effective
// uid. A system daemon commonly runs as an unprivileged service user -
// nix/module.nix creates exactly such a user - and deriving scope from
// privilege would silently flip that daemon into user scope, where it
// would discover a different agent set than the operator installed.
// systemd has the same property: 'systemctl --user' run by root manages
// root's USER instance, because the scope was asked for rather than
// deduced.
type Scope int

const (
	// ScopeSystem is the machine-wide manager: the daemon an operator
	// installs and an init system starts.
	ScopeSystem Scope = iota

	// ScopeUser is the per-user manager, selected by --user. The tier
	// list is defined so that implementing --user is wiring rather than
	// a redesign of the search path.
	ScopeUser
)

func (s Scope) String() string {
	if s == ScopeUser {
		return "user"
	}
	return "system"
}

// AgentSearchPaths returns the system-scope search path.
//
// It is the compatibility spelling for AgentSearchPathsFor(ScopeSystem);
// every existing caller is a system manager.
func AgentSearchPaths() []string { return AgentSearchPathsFor(ScopeSystem) }

// AgentSearchPathsFor returns the ordered directories to search, highest
// precedence first. Discovery is first-ID-wins, so an agent found in an
// earlier directory MASKS one of the same ID found later - which is what
// makes these tiers an override mechanism rather than a concatenation.
//
// Every directory is namespaced by the PRODUCT rather than by the kernel
// (GAPI-DIV-061): gapid searches /usr/lib/gapi/agents, goblind searches
// /usr/lib/goblin/agents. This function is reached inside goblind through
// agentmgr's discovery, so it is one of the kernel surfaces an operator
// who has never heard of gapi would otherwise meet.
//
// THE ORDERING RULE, taken from systemd and XDG and applied to both
// scopes: configuration beats runtime beats data beats vendor. An
// operator's edit outranks a package's file, and a transient unit
// outranks the installed one it shadows.
//
// System scope, highest to lowest, with <p> the product name:
//   - <PREFIX>_DEV_AGENTS          explicit development override
//   - /etc/<p>/agents              operator-authored
//   - /run/<p>/agents              transient, generated at runtime
//   - /usr/local/lib/<p>/agents    locally installed
//   - /usr/lib/<p>/agents          package-owned
//
// User scope, highest to lowest:
//   - <PREFIX>_DEV_AGENTS               explicit development override
//   - $XDG_CONFIG_HOME/<p>/agents       the user's own
//   - /etc/<p>/user/agents              operator-provided, for all users
//   - $XDG_RUNTIME_DIR/<p>/agents       transient
//   - $XDG_DATA_HOME/<p>/agents         user-installed
//   - ~/.<p>/agents                     LEGACY, see below
//   - /usr/lib/<p>/user/agents          package-owned user agents
//
// SYSTEM SCOPE CONTAINS NO HOME-DIRECTORY PATH, and that is a security
// boundary rather than tidiness. agentmgr's safeToExecute already refuses
// world-writable or foreign-owned binaries at EXECUTION time; keeping
// user-writable directories out of the system list is the same defence at
// DISCOVERY time, and the two are not substitutes.
//
// There is deliberately no implicit ./agents tier. It made discovery
// depend on the working directory a daemon happened to be started from -
// a daemon launched from the wrong directory silently discovered nothing,
// and 'agent new' run outside a checkout silently wrote a tree into
// whatever directory the operator was standing in. Development now names
// its directory explicitly through <PREFIX>_DEV_AGENTS, which is also
// what every test and script in this repo already did.
//
// Environment overrides:
//   - <PREFIX>_AGENT_PATH: colon-separated directories PREPENDED to the
//     tiers below. It adds precedence; it does not replace the path.
//   - <PREFIX>_AGENT_PATH_EXCLUSIVE: search ONLY what AGENT_PATH names.
//   - <PREFIX>_DEV_AGENTS: highest-priority directory in either scope.
//   - <PREFIX>_SKIP_SYSTEM_AGENTS: drop the package-owned tiers.
//
// AGENT_PATH used to REPLACE the whole search path, and the replacement
// was load-bearing in two places rather than one, which is why the
// exclusive switch exists rather than the additive behaviour simply
// landing on its own (GAPI-DIV-063). A packaged install set AGENT_PATH to
// one directory, so the tiers above were dead code in the only
// configuration that ships; and test/adk's harness set it to fence
// discovery to a fixture directory, without which the checkout's own
// agents starve the fixtures' state transitions (GAPI-DIV-021). Additive
// fixes the first. The switch preserves the second, and a fence is a
// thing you ask for rather than a side effect of naming a directory.
func AgentSearchPathsFor(scope Scope) []string {
	custom := splitPathList(os.Getenv(product.EnvKey("AGENT_PATH")))

	// Exclusive is checked even when AGENT_PATH is empty: asking for an
	// empty search path is a coherent request, and silently falling back
	// to the full tier list would be the opposite of what was asked.
	if isTruthy(os.Getenv(product.EnvKey("AGENT_PATH_EXCLUSIVE"))) {
		return custom
	}

	var paths []string
	if devPath := os.Getenv(product.EnvKey("DEV_AGENTS")); devPath != "" {
		paths = append(paths, splitPathList(devPath)...)
	}
	paths = append(paths, custom...)

	if scope == ScopeUser {
		paths = append(paths, userScopeDirs()...)
	} else {
		paths = append(paths, systemScopeDirs()...)
	}

	if skipSystemAgents() {
		paths = withoutPrefixes(paths, vendorAgentDirs(scope))
	}
	return paths
}

// systemScopeDirs is the system manager's tier list, highest first.
func systemScopeDirs() []string {
	p := product.Name()
	return []string{
		filepath.Join(product.ConfigDir(), "agents"), // /etc/<p>/agents
		filepath.Join("/run", p, "agents"),           //
		filepath.Join("/usr/local/lib", p, "agents"), //
		filepath.Join("/usr/lib", p, "agents"),       //
	}
}

// userScopeDirs is the per-user manager's tier list, highest first.
//
// Absent tiers are simply omitted: under PID 1 at early boot there may be
// no XDG_RUNTIME_DIR and no HOME at all, and a missing directory is not
// an error - discovery skips paths that do not exist.
func userScopeDirs() []string {
	p := product.Name()
	var paths []string

	if cfg := xdgDir("XDG_CONFIG_HOME", ".config"); cfg != "" {
		paths = append(paths, filepath.Join(cfg, p, "agents"))
	}
	paths = append(paths, filepath.Join(product.ConfigDir(), "user", "agents"))

	if run := os.Getenv("XDG_RUNTIME_DIR"); run != "" {
		paths = append(paths, filepath.Join(run, p, "agents"))
	}
	if data := xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share")); data != "" {
		paths = append(paths, filepath.Join(data, p, "agents"))
	}

	// LEGACY, kept only because it exists in the wild: ~/.<p>/agents
	// predates the XDG layout above and is deliberately the lowest
	// user tier, so an XDG directory always wins. It is probed rather
	// than assumed so it disappears once nobody has one.
	if home, err := os.UserHomeDir(); err == nil {
		legacy := filepath.Join(home, "."+p, "agents")
		if _, err := os.Stat(legacy); err == nil {
			paths = append(paths, legacy)
		}
	}

	return append(paths, filepath.Join("/usr/lib", p, "user", "agents"))
}

// vendorAgentDirs are the package-manager-owned tiers, which
// <PREFIX>_SKIP_SYSTEM_AGENTS removes. Operator-authored and transient
// tiers are NOT vendor directories and survive the flag.
func vendorAgentDirs(scope Scope) []string {
	p := product.Name()
	if scope == ScopeUser {
		return []string{filepath.Join("/usr/lib", p, "user", "agents")}
	}
	return []string{
		filepath.Join("/usr/local/lib", p, "agents"),
		filepath.Join("/usr/lib", p, "agents"),
	}
}

func skipSystemAgents() bool {
	return isTruthy(os.Getenv(product.EnvKey("SKIP_SYSTEM_AGENTS")))
}

// isTruthy is the one spelling of "this flag is on" these variables
// share. Two flags each inventing their own accepted values is how one
// of them ends up silently ignoring "yes".
func isTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// xdgDir resolves an XDG base directory, falling back to the documented
// default beneath the home directory. An unset HOME yields "", which the
// caller renders as an omitted tier rather than a path rooted at "/".
func xdgDir(env, homeRelative string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, homeRelative)
}

// splitPathList splits a colon-separated list, dropping empty entries. An
// empty entry would otherwise become "", which filepath.Join turns into a
// relative path and reintroduces the working-directory dependence this
// scheme removed.
func splitPathList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ":") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func withoutPrefixes(paths, drop []string) []string {
	out := paths[:0:0]
	for _, p := range paths {
		keep := true
		for _, d := range drop {
			if p == d {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, p)
		}
	}
	return out
}

// PathType represents the type of agent path
type PathType int

const (
	PathTypeDevelopment PathType = iota
	PathTypeUser
	PathTypeSystem
)

// ClassifyPath reports which tier a path belongs to.
//
// It classifies against the SAME lists that are searched, so the label a
// log line carries cannot drift from the precedence that produced it -
// they were two literal lists that disagreed about /etc before
// GAPI-DIV-061, and /etc's position has now changed.
func ClassifyPath(path string) PathType {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return PathTypeDevelopment
	}

	if devPath := os.Getenv(product.EnvKey("DEV_AGENTS")); devPath != "" {
		for _, d := range splitPathList(devPath) {
			if underPrefix(absPath, d) {
				return PathTypeDevelopment
			}
		}
	}

	for _, prefix := range systemScopeDirs() {
		if underPrefix(absPath, prefix) {
			return PathTypeSystem
		}
	}

	return PathTypeUser
}

// underPrefix reports whether abs is prefix or lies beneath it.
//
// A plain strings.HasPrefix is wrong here and was: "/usr/lib/gapifoo"
// has "/usr/lib/gapi" as a string prefix while being an unrelated
// directory. Comparing on a separator boundary is the difference between
// a path test and a substring test.
func underPrefix(abs, prefix string) bool {
	p, err := filepath.Abs(prefix)
	if err != nil {
		return false
	}
	if abs == p {
		return true
	}
	return strings.HasPrefix(abs, p+string(filepath.Separator))
}

// PathTypeString returns a human-readable string for the path type
func (pt PathType) String() string {
	switch pt {
	case PathTypeDevelopment:
		return "DEV"
	case PathTypeUser:
		return "USER"
	case PathTypeSystem:
		return "SYSTEM"
	default:
		return "UNKNOWN"
	}
}
