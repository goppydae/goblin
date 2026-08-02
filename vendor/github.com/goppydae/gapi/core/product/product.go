// Package product carries the identity of the product whose process the
// kernel is running inside.
//
// The kernel is a LIBRARY. gapid links it and is the gapi product;
// goblind links the same code and is the goblin product, and an operator
// of goblind should never have to learn that gapi exists (GAPI-DIV-061).
// Every host-namespaced resource the kernel creates, reads or advertises
// therefore derives from this one value rather than from a literal:
//
//   - the environment prefix, and so every GAPI_/GOBLIN_ variable
//   - the config search directory, /etc/<product>
//   - the agent search paths, /usr/lib/<product>/agents and friends
//   - the default log path, /var/log/<product>/<product>.log
//   - cgroup names, <product>d-infra and <product>d-<id>
//   - the kmsg tag an operator reads in dmesg under --pid1
//
// Three classes of gapi-spelling string exist in this repo and only the
// list above is one of them. PROSE - stderr prefixes, help text,
// Prometheus help - names the role instead ("supervisor"), because
// naming the vendor inside its own output is redundant even for gapid's
// own operator. WIRE - the gapi-quic ALPN, the gapi.v1 protobuf package
// names, the gapi_* metric names - MUST NOT CHANGE: renaming a protocol
// constant or a scraped metric is a compatibility break, and no operator
// reads an ALPN string off a terminal. That exclusion is written down so
// a later sweep does not "finish the job".
//
// # No usable default
//
// There is no fallback identity. Name() panics when nothing has been
// set, so a binary that forgets to declare itself dies at startup rather
// than quietly adopting gapi's namespace - which would mean ignoring the
// operator's configuration and booting on defaults. core/version
// tolerates the same set-then-read shape only because a late read there
// is cosmetic; a late read here is a misconfigured daemon.
//
// The compile-time half of that guarantee lives in pkg/cli: the root
// constructors take the product as a parameter, so an embedder cannot
// build a command tree without naming itself. The panic covers the
// remainder - an embedder that uses the kernel without pkg/cli.
package product

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// nameRe is what a product name may be. The validation is not decoration:
// the constructors that call Set take four adjacent strings (product,
// binary, version, short), and a transposition is otherwise silent. A
// version ("0.1.0-proto2d") and a help line ("GAPI Supervisor Daemon")
// both fail this pattern; "gapid" would not, which is why the caller-side
// tests assert the value and not only that one was set.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// directEnv is every environment variable the kernel reads that is NOT a
// config key, mapped to the code that reads it.
//
// It exists as a registry rather than as eight scattered literals for
// one reason: cli-contract.md requires that a documented variable have a
// reader, and core/config/envnames_test.go enforces that by looking for
// the name. Once the names are composed at runtime no literal spells
// them, so the gate would go blind exactly the way GAPI-DIV-059's exit
// predicted. Composing the list from here keeps the gate honest, and
// EnvKey's panic on an undeclared suffix keeps the list complete: a
// reader cannot obtain a name that is not in it.
//
// Config-key overrides are NOT here. Those are composed from the Config
// struct's mapstructure tags by core/config, which is a stronger source
// than any list - a field added later is bound because it exists.
var directEnv = map[string]string{
	"CONFIG":             "core/config.Load - path to config.yaml",
	"AGENT_PATH":         "core/config.AgentSearchPaths - replaces the whole search path",
	"DEV_AGENTS":         "core/config.AgentSearchPaths, ClassifyPath - highest-priority agent dir",
	"SKIP_SYSTEM_AGENTS": "core/config.AgentSearchPaths - drop the system dirs",
	"CGROUPS_DISABLE":    "core/cgroups.Setup - refuse cgroup delegation",
	"VERIFY_KEY":         "core/supervisor.New - agent signing public key",
	"PY_RUNNER":          "core/supervisor.resolvePyRunner - override the Python runner path",
	"KMSG_PATH":          "core/supervisor pid1 wiring - override /dev/kmsg",
}

var (
	mu   sync.RWMutex
	name string
)

// Set declares the product this process belongs to. Names are lowercase
// and alphanumeric: "gapi", "goblin".
//
// Last writer wins, deliberately. A single process legitimately builds
// more than one command tree - goblinctl mounts gapictl's verbs under
// `agent`, and the flag-parity tests construct both roots side by side -
// so a set-once panic would fire on correct code. What makes that safe is
// that nothing here is read during package initialization; the ordering
// hazard the panic in Name() guards is a read before ANY set, not a
// second set.
func Set(n string) {
	if !nameRe.MatchString(n) {
		panic(fmt.Sprintf("product.Set(%q): a product name is lowercase alphanumeric, "+
			"like \"gapi\" or \"goblin\" - check the argument order at the call site", n))
	}
	mu.Lock()
	defer mu.Unlock()
	name = n
}

// Name returns the product identity, panicking if none was declared.
//
// The panic is the point. Returning "gapi" here would make a binary that
// forgot to declare itself read GAPI_* variables, search /etc/gapi and
// write dmesg lines an operator cannot attribute - a silently
// misconfigured daemon rather than a failed one.
func Name() string {
	mu.RLock()
	defer mu.RUnlock()
	if name == "" {
		panic("product identity was read before it was set: no binary called " +
			"product.Set. A process embedding the kernel must declare its product " +
			"before anything resolves an environment variable, config path, agent " +
			"path or cgroup name (GAPI-DIV-061).")
	}
	return name
}

// IsSet reports whether an identity has been declared, without panicking.
// For tests and for diagnostics that must not themselves fail.
func IsSet() bool {
	mu.RLock()
	defer mu.RUnlock()
	return name != ""
}

// Daemon is the name of the product's daemon binary: gapi -> gapid,
// goblin -> goblind.
//
// The "d" suffix is the silo's binary-naming convention, written down in
// cli-contract.md - four binaries, <product>d and <product>ctl. Deriving
// it rather than passing a second value is what keeps this ONE identity
// instead of two, and it holds today's strings exactly: gapid still tags
// dmesg with "gapid:" and still owns the "gapid-infra" cgroup. The
// tradeoff is that a product whose daemon is not <product>d would need a
// second value here.
func Daemon() string { return Name() + "d" }

// EnvPrefix is the environment namespace: GAPI, GOBLIN.
func EnvPrefix() string { return strings.ToUpper(Name()) }

// EnvKey renders a declared suffix as a full environment variable name.
// An undeclared suffix panics rather than composing a name no gate knows
// about; add it to directEnv with its reader.
func EnvKey(suffix string) string {
	if _, ok := directEnv[suffix]; !ok {
		panic(fmt.Sprintf("product.EnvKey(%q): undeclared environment name. "+
			"Add it to directEnv with the code that reads it, or the "+
			"documented-names gate cannot see it.", suffix))
	}
	return EnvPrefix() + "_" + suffix
}

// DirectEnvNames returns every declared direct name, fully composed, for
// the gate in core/config that checks documented names have readers.
func DirectEnvNames() []string {
	out := make([]string, 0, len(directEnv))
	prefix := EnvPrefix()
	for suffix := range directEnv {
		out = append(out, prefix+"_"+suffix)
	}
	sort.Strings(out)
	return out
}

// ConfigDir is where a release build looks for config.yaml.
func ConfigDir() string { return filepath.Join("/etc", Name()) }

// DefaultLogPath is the default rotated file sink.
func DefaultLogPath() string {
	return filepath.Join("/var/log", Name(), Name()+".log")
}
