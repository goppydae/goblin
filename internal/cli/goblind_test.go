package cli_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	gapicli "github.com/goppydae/gapi/pkg/cli"
	"github.com/goppydae/goblin/internal/cli"
)

// These tests close GOBLIN-DIV-053. Before it, goblind's root carried a
// RunE that WAS the daemon, so cobra handed every unmatched word to it
// as a positional argument and `goblind version` booted a supervisor -
// measured at the time by a 2-minute command timeout.
//
// The daemon is never constructed here. start is a spy, so "did not boot
// a supervisor" is asserted by the spy staying uncalled rather than by
// nothing appearing to happen within some bound. That matters beyond
// tidiness: a test that proved this by waiting could only ever fail by
// timing out, and a revert-proof that hangs spends the demonstration
// without producing it.

// runRoot builds a fresh root and runs it against args, reporting what
// the root wrote and whether the start action fired.
//
// Fresh per call because cobra keeps parse state on the command: reusing
// one root across invocations would let the first call's flags decide
// the second's outcome.
func runRoot(args ...string) (out string, started bool, err error) {
	root, _, _ := cli.NewGoblindRoot(func(*cobra.Command, []string) error {
		started = true
		return nil
	})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	err = gapicli.RunRoot(root, args)
	return buf.String(), started, err
}

// A bare `goblind` prints help and exits non-zero (cli-contract.md).
func TestGoblindRoot_BareInvocation(t *testing.T) {
	out, started, err := runRoot()

	if !errors.Is(err, gapicli.ErrNoCommand) {
		t.Errorf("err = %v, want ErrNoCommand", err)
	}
	if started {
		t.Error("bare invocation started the daemon")
	}
	if !strings.Contains(out, "Available Commands") {
		t.Errorf("bare invocation printed no help:\n%s", out)
	}
}

// An unrecognized word is an error, not a positional argument handed to
// the daemon. This is the regression GOBLIN-DIV-053 names.
func TestGoblindRoot_UnknownCommand(t *testing.T) {
	for _, word := range []string{"stat", "version-ish", "start-now"} {
		t.Run(word, func(t *testing.T) {
			_, started, err := runRoot(word)

			if err == nil {
				t.Errorf("%q returned nil error, want unknown command", word)
			}
			if started {
				t.Errorf("%q started the daemon", word)
			}
		})
	}
}

// The root carries no RunE. This is the assertion that actually holds
// GOBLIN-DIV-053 closed, and the two tests above do NOT.
//
// Measured, not assumed: restoring `root.RunE = start` leaves
// BareInvocation and UnknownCommand both passing. Bare invocation never
// reaches Execute - RunRoot returns ErrNoCommand first - and cobra's
// legacyArgs rejects unknown words for any root that HAS subcommands
// whatever its RunE, which this root now does. That is the same
// mechanism GAPI-DIV-057 recorded backwards and was corrected on: one
// subcommand protects every word, not just its own.
//
// So the behavioural protection is real but INCIDENTAL - it survives
// only while `start` and `version` both exist. Deleting the RunE is what
// makes it structural, and only a structural check can notice it coming
// back.
func TestGoblindRoot_HasNoRunE(t *testing.T) {
	root, _, _ := cli.NewGoblindRoot(func(*cobra.Command, []string) error { return nil })

	if root.RunE != nil {
		t.Error("root has a RunE: cobra will hand unmatched arguments to the daemon " +
			"the moment the root's subcommands change")
	}
	if root.Run != nil {
		t.Error("root has a Run, with the same consequence as a RunE")
	}
}

// The daemon lives behind an explicit verb and nowhere else.
func TestGoblindRoot_StartRunsTheDaemon(t *testing.T) {
	_, started, err := runRoot("start")

	if err != nil {
		t.Errorf("start returned %v, want nil", err)
	}
	if !started {
		t.Error("start did not run the daemon")
	}
}

// The identity surface: three spellings, one renderer, and a first line
// that names the binary (GOBLIN-DIV-052).
func TestGoblindRoot_VersionSurfaces(t *testing.T) {
	sub, started, err := runRoot("version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if started {
		t.Fatal("`goblind version` started the daemon - the defect this entry names")
	}
	if !strings.HasPrefix(sub, "goblind:") {
		t.Errorf("version block does not name the binary, first line = %q",
			strings.SplitN(sub, "\n", 2)[0])
	}
	// The kernel's row must be present: a Goblin binary reports both its
	// own version and the version of the kernel it embeds.
	if !strings.Contains(sub, "Runtime Core") {
		t.Errorf("version block omits the embedded kernel:\n%s", sub)
	}

	for _, flag := range []string{"--version", "-v"} {
		t.Run(flag, func(t *testing.T) {
			got, started, err := runRoot(flag)
			if err != nil {
				t.Fatalf("%s: %v", flag, err)
			}
			if started {
				t.Fatalf("%s started the daemon", flag)
			}
			if got != sub {
				t.Errorf("%s and `version` render different bytes:\n%s\n--- vs ---\n%s",
					flag, got, sub)
			}
		})
	}
}

// The persistent set describes the process; the run-configuring flags
// stay local to start. A flag on the wrong side is how the two sets
// silently merge back into one root-level pile.
func TestGoblindRoot_FlagPlacement(t *testing.T) {
	root, _, _ := cli.NewGoblindRoot(func(*cobra.Command, []string) error { return nil })
	startCmd, _, err := root.Find([]string{"start"})
	if err != nil {
		t.Fatalf("find start: %v", err)
	}

	persistent := []string{
		"id", "log-level", "log-format", "log-file", "log-loki-url",
		"metrics-addr", "tls-ca", "tls-cert", "tls-key",
	}
	for _, name := range persistent {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("--%s is not on the daemon root's persistent set", name)
		}
	}

	local := []string{
		"listen-addr", "advertise-addr", "join", "bootstrap-expect", "encrypt",
		"data", "raft-snapshot-interval", "raft-snapshot-threshold", "raft-trailing-logs",
		"agent-verify-key", "production", "operator-key",
		"pid1", "no-early-mounts", "network-gate-timeout", "shutdown-grace",
		"watchdog-device", "watchdog-interval",
	}
	for _, name := range local {
		if startCmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s is not local to `goblind start`", name)
		}
		if root.PersistentFlags().Lookup(name) != nil {
			t.Errorf("--%s leaked onto the root's persistent set", name)
		}
	}
}
