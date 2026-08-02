package cli

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestControlBinaryHasNoStartVerb pins the contract clause that a control
// binary never starts a daemon (cli-contract.md, Command structure).
//
// goblinctl carried a `start` that built supervisor.Config with four of
// its twenty-two fields, so it brought up a node with no TLS material,
// no gossip encryption, no operator keys and not in production mode -
// exit 0, no warning. GOBLIN-DIV-054.
func TestControlBinaryHasNoStartVerb(t *testing.T) {
	for _, name := range []string{"start", "run-supervisor", "daemon"} {
		cmd, _, err := RootCmd.Find([]string{name})
		if err == nil && cmd != nil && cmd.Name() == name {
			t.Errorf("goblinctl exposes a %q verb: a control binary never starts a daemon", name)
		}
	}
}

// TestControlBinaryRejectsStart is the behavioural half: the word must
// reach the user as an error rather than being silently absorbed.
//
// Cobra hands an unmatched argument to the root's RunE when one exists,
// so "no such subcommand" and "the root swallowed it" are different
// outcomes that look identical from a Find() check alone.
//
// BOUNDED ON PURPOSE. Reverting the fix to prove this test bites showed
// why: with a `start` verb present, Execute() booted a real supervisor
// and the package sat at 600.007s until the test binary was killed. A
// regression must FAIL here, not hang - so the run is capped, and the
// cap expiring is itself the failure. The supervisor honours context
// cancellation (GOBLIN-DIV-038), so a resurrected launcher unwinds
// rather than wedging the suite.
func TestControlBinaryRejectsStart(t *testing.T) {
	if RootCmd.RunE != nil || RootCmd.Run != nil {
		t.Fatal("goblinctl root has a Run/RunE: unmatched words would be absorbed rather than rejected")
	}

	var out, errOut bytes.Buffer
	RootCmd.SetOut(&out)
	RootCmd.SetErr(&errOut)
	RootCmd.SetArgs([]string{"start"})
	t.Cleanup(func() {
		RootCmd.SetArgs(nil)
		RootCmd.SetOut(nil)
		RootCmd.SetErr(nil)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- RootCmd.ExecuteContext(ctx) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("goblinctl start returned nil: it must fail as an unknown command")
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("goblinctl start error = %q, want an unknown-command error", err)
		}
	case <-ctx.Done():
		t.Fatal("goblinctl start did not return within 5s: the root is running something " +
			"rather than rejecting the word, which is the defect GOBLIN-DIV-054 removed")
	}
}

// TestSupervisorNewHasOneProductionCaller is the CLASS-level guard, and
// the reason this file is worth more than a single named verb.
//
// GOBLIN-DIV-054's residual says it plainly: nothing structurally
// prevents a future subcommand from calling supervisor.New, and a test
// naming one verb only catches the verb it names. Asserting the caller
// COUNT catches any second launcher regardless of what it is called.
// The daemon's own entrypoint is the one legitimate caller.
//
// Pattern precedent: core/consensus/fsm_operator_keys_caller_test.go.
func TestSupervisorNewHasOneProductionCaller(t *testing.T) {
	const want = "cmd/goblind/main.go"

	var callers []string
	fset := token.NewFileSet()
	roots := []string{"../../cmd", "../../internal"}

	for _, root := range roots {
		pkgs, err := parseTree(fset, root)
		if err != nil {
			t.Fatalf("parse %s: %v", root, err)
		}
		for path, file := range pkgs {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "New" {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "supervisor" {
					return true
				}
				callers = append(callers, filepath.ToSlash(path))
				return true
			})
		}
	}

	if len(callers) != 1 || !strings.HasSuffix(callers[0], want) {
		t.Errorf("supervisor.New production callers = %v, want exactly one in %s. "+
			"A second caller is a second daemon launcher, which is the defect "+
			"GOBLIN-DIV-054 removed regardless of what the verb is named.", callers, want)
	}
}

// parseTree parses every .go file under root, keyed by path.
func parseTree(fset *token.FileSet, root string) (map[string]*ast.File, error) {
	out := make(map[string]*ast.File)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		out[path] = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
