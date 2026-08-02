package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/goppydae/gapi/core/product"
	"github.com/goppydae/gapi/core/version"
)

// This file is the single definition of the silo's CLI shape
// (cli-contract.md). Both GAPI binaries build their roots here, and the
// orchestrator's two binaries call the same constructors, so a flag's
// name, shorthand, default and help text exist in exactly one place.
//
// Shared definitions alone are not enough - nothing stops a binary from
// adding a flag locally - so roots_test.go compares each root's
// persistent set against the registrar's output. That comparison is why
// these are CONSTRUCTORS rather than package-level vars: a root that
// cannot be built by a test cannot be asserted on (GAPI-DIV-058).

// ErrNoCommand is returned when a root is invoked with no subcommand.
// The contract requires help plus a NON-ZERO exit, and cobra cannot
// deliver that on its own: a root with no RunE returns flag.ErrHelp from
// execute(), and ExecuteC catches it, prints help and returns nil, so
// the process exits 0.
//
// The obvious fix - give the root a RunE that errors - is exactly what
// GAPI-DIV-057 forbids, because any root RunE restores cobra's
// pass-through and a mistyped subcommand becomes a positional argument
// again. So the bare-invocation case is handled OUTSIDE Execute.
var ErrNoCommand = errors.New("no command given")

// RunRoot executes root against args, returning ErrNoCommand when no
// subcommand was named. Callers exit non-zero on any error.
//
// This exists as a function rather than a few lines in main() so the
// behaviour is testable: main() is not importable, and "bare invocation
// exits non-zero" is a contract clause that needs a gate.
func RunRoot(root *cobra.Command, args []string) error {
	if len(args) == 0 {
		if err := root.Help(); err != nil {
			return err
		}
		return ErrNoCommand
	}
	root.SetArgs(args)
	return root.Execute()
}

// DaemonFlags holds the values bound by RegisterDaemonFlags. These are
// the flags that describe the process itself - who it is, how it
// reports, and what identity material it serves - so they are
// persistent and apply to every subcommand.
type DaemonFlags struct {
	ID          string
	LogLevel    string
	LogFormat   string
	LogFile     string
	LogLokiURL  string
	MetricsAddr string
	TLSCA       string
	TLSCert     string
	TLSKey      string
}

// RegisterDaemonFlags binds the daemon root's persistent set to cmd and
// returns the struct receiving the values.
//
// On a daemon --tls-ca is SERVER-side material: the CA against which
// incoming peers are verified, paired with --tls-cert and --tls-key. The
// control binaries reuse the name for the client's trust root, which is
// conventional and intended - a later pass that "unifies the TLS flags"
// across roles would be merging two different things.
func RegisterDaemonFlags(cmd *cobra.Command) *DaemonFlags {
	f := &DaemonFlags{}
	pf := cmd.PersistentFlags()
	pf.StringVar(&f.ID, "id", "", "Unique node identifier (default: hostname)")
	pf.StringVar(&f.LogLevel, "log-level", "", "Log level: debug, info, warn, error")
	pf.StringVar(&f.LogFormat, "log-format", "", "Log format: json or console")
	pf.StringVar(&f.LogFile, "log-file", "", "Additional rotated file sink")
	pf.StringVar(&f.LogLokiURL, "log-loki-url", "", "Forward logs to a Loki endpoint")
	pf.StringVar(&f.MetricsAddr, "metrics-addr", "", "Prometheus listen address (empty: disabled)")
	pf.StringVar(&f.TLSCA, "tls-ca", "", "CA certificate for peer verification")
	pf.StringVar(&f.TLSCert, "tls-cert", "", "This node's certificate")
	pf.StringVar(&f.TLSKey, "tls-key", "", "This node's private key")
	return f
}

// ControlFlags holds the values bound by RegisterControlFlags: which
// daemon to talk to and how to reach it.
type ControlFlags struct {
	APIAddr     string
	TLSCA       string
	TLSInsecure bool
	LogLevel    string
}

// RegisterControlFlags binds the control root's persistent set to cmd.
//
// --api-addr defaults to EMPTY rather than to a literal address: the two
// daemons listen on different ports, so a shared default would have to
// be wrong for one of them. Empty means "resolve from config", which
// keeps config the source of truth and the flag an override.
func RegisterControlFlags(cmd *cobra.Command) *ControlFlags {
	f := &ControlFlags{}
	pf := cmd.PersistentFlags()
	pf.StringVar(&f.APIAddr, "api-addr", "", "Target daemon's listen address (empty: from config)")
	pf.StringVar(&f.TLSCA, "tls-ca", "", "CA certificate used to verify the daemon")
	pf.BoolVar(&f.TLSInsecure, "tls-insecure", false, "Skip verification (INSECURE)")
	pf.StringVar(&f.LogLevel, "log-level", "", "Log level: debug, info, warn, error")
	return f
}

// NewDaemonRoot builds a daemon root: no RunE, a version surface, the
// persistent daemon flags, and a `start` subcommand carrying the run
// action.
//
// The root carries NO RunE, and that is the whole point rather than a
// stylistic choice. A root RunE makes cobra treat every unmatched
// argument as a positional parameter and hand it to the daemon, so a
// mistyped subcommand BOOTS A SUPERVISOR instead of failing
// (GAPI-DIV-057; goblind reached a 2-minute timeout this way on the word
// "version").
//
// start is passed in rather than defined here so this package never
// imports core/supervisor. That keeps the CLI shape importable by a test
// - and by the orchestrator, which supplies its own start action.
//
// productName is the PRODUCT this daemon belongs to - "gapi", "goblin" -
// not the binary name. It is a required parameter rather than something
// the kernel assumes, because every host-namespaced resource the
// embedded kernel touches derives from it: the environment prefix,
// /etc/<product>, the agent search paths, the log path, the cgroup
// names, and the dmesg tag under --pid1 (GAPI-DIV-061). Taking it here
// is the compile-time half of that guarantee - an embedder cannot build
// a daemon root without naming itself. core/product.Name()'s panic is
// the other half, for an embedder that never calls this.
func NewDaemonRoot(productName, name, version, short string, start func(*cobra.Command, []string) error) (*cobra.Command, *DaemonFlags) {
	product.Set(productName)
	root := newRoot(name, version, short)
	flags := RegisterDaemonFlags(root)

	root.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the " + name + " daemon",
		RunE:  start,
	})
	return root, flags
}

// NewControlRoot builds a control root: no RunE, a version surface, and
// the persistent control flags. A control binary never starts a daemon,
// so it gains no start verb here or anywhere.
//
// productName carries the same meaning and the same requirement as on
// NewDaemonRoot: a control binary resolves the same config file and the
// same environment namespace as its daemon, so it must agree about which
// product it is.
func NewControlRoot(productName, name, version, short string) (*cobra.Command, *ControlFlags) {
	product.Set(productName)
	return newControlTree(name, version, short)
}

// newControlTree builds a control root WITHOUT claiming the process's
// product identity.
//
// It exists for exactly one caller, gapictl's package-level singleton,
// and the reason is an ordering trap rather than taste. That singleton
// is constructed during THIS package's initialization, which Go runs
// inside every binary that imports pkg/cli - including goblind and
// goblinctl. Had it gone through NewControlRoot, every such binary would
// have had "gapi" set before its own main ran, and core/product's
// panic-on-unset - the whole fail-loud half of GAPI-DIV-061 - would
// never fire for anyone.
//
// gapictl declares itself in Execute(), which is the point its process
// actually starts.
func newControlTree(name, version, short string) (*cobra.Command, *ControlFlags) {
	root := newRoot(name, version, short)
	return root, RegisterControlFlags(root)
}

// newRoot builds the shape both roles share: identity, and no RunE.
//
// The `version` subcommand prints the ROOT'S OWN Version string rather
// than calling version.Summary() again, so the subcommand and the flag
// cannot render different bytes. Rendering the same function twice would
// leave them free to drift the moment anything mutates version state
// between the two calls; sharing one string makes agreement structural.
func newRoot(name, binVersion, short string) *cobra.Command {
	// Give the renderer this binary's identity before taking the
	// summary, so the block names the binary that was invoked. Without
	// this, active.Name stays empty and every binary prints the same
	// anonymous block (GAPI-DIV-056).
	version.SetBinaryNameAndVersion(name, binVersion)

	root := &cobra.Command{
		Use:     name,
		Short:   short,
		Version: version.Summary(),
	}
	// Render the summary alone - cobra's default template wraps it in
	// "<name> version <version>", which would prepend a line to a block
	// that already names the binary.
	root.SetVersionTemplate("{{.Version}}")

	// Persistent, per the contract: -v is bound to version at the root
	// and is therefore unavailable as a "verbose" shorthand anywhere.
	// Registering it here rather than letting cobra add it locally is
	// what makes that true for subcommands too.
	root.PersistentFlags().BoolP("version", "v", false, "version for "+name)

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version info",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprint(cmd.OutOrStdout(), cmd.Root().Version)
			return err
		},
	})
	return root
}

// GapidStartFlags are local to `gapid start`: they configure a run
// rather than describing the process, which is what keeps them off the
// persistent set.
type GapidStartFlags struct {
	ListenAddr    string
	Pid1Mode      bool
	NoEarlyMounts bool
}

// NewGapidRoot builds gapid's root here rather than in package main, so
// a test can construct it. That is not incidental tidiness: GAPI-DIV-058
// is closed by comparing each root's persistent set against the shared
// registrar, and a root declared as a package-level var inside main is
// unreachable from any test.
//
// The start action is a parameter, so this package still never imports
// core/supervisor.
func NewGapidRoot(start func(*cobra.Command, []string) error) (*cobra.Command, *DaemonFlags, *GapidStartFlags) {
	root, daemonFlags := NewDaemonRoot("gapi", "gapid", version.BinaryVersion(), "Supervision kernel daemon", start)

	sf := &GapidStartFlags{}
	startCmd, _, err := root.Find([]string{"start"})
	if err != nil {
		// NewDaemonRoot always adds `start`; a miss means the constructor
		// changed shape and every caller is broken, not just this one.
		panic("gapid: start subcommand missing from daemon root: " + err.Error())
	}
	startCmd.Flags().StringVar(&sf.ListenAddr, "listen-addr", "", "Control-plane bind address (default: 127.0.0.1:14242)")
	startCmd.Flags().BoolVar(&sf.Pid1Mode, "pid1", false, "Run as PID 1: Phase 0 pre-userspace boot (subreaper, signals, mounts, reaping)")
	startCmd.Flags().BoolVar(&sf.NoEarlyMounts, "no-early-mounts", false, "Skip the Phase 0 mount table (container environments)")

	return root, daemonFlags, sf
}
