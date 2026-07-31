package magelib

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// DoctorConfig carries the per-repo facts the doctor checks against. The
// checks themselves are ecosystem policy and live here; the differences
// between repos are data, not forks.
type DoctorConfig struct {
	// ReplaceTargets are the relative paths every replace directive points
	// at; each must exist on disk as a sibling checkout (workspace shape).
	ReplaceTargets []string
	// ProtoPlugins are the codegen plugins that must be reachable on PATH
	// from the pinned store (codegen reachability).
	ProtoPlugins []string
	// GopyVersion is the pinned FFI codegen tool version (e.g. "v0.4.10").
	// Empty means the repo has no FFI seam and the check passes with a note.
	GopyVersion string
	// RequiredEnv are environment variables the targets assume are set.
	RequiredEnv []string
	// SharedTools extends the hermetic-resolution tool list beyond the base
	// compiler set (linters, buf, docs toolchain, ...).
	SharedTools []string
	// Out receives the per-check report lines. Nil means os.Stdout, which
	// is what every Magefile caller wants; tests set it to capture the
	// output contract.
	Out io.Writer
}

// Doctor runs the six field-guide checks in order. Output contract: one line
// per check -- name, PASS or FAIL, and a one-line remedy on FAIL; non-nil
// error (nonzero exit) if anything failed. It builds nothing, installs
// nothing, mutates nothing (operator field guide section 2).
func Doctor(cfg DoctorConfig) error {
	out := cfg.Out
	if out == nil {
		out = os.Stdout
	}
	failed := 0
	// The report lines are the terminus: a write failure here has nowhere
	// left to be reported, so the count is discarded deliberately.
	report := func(name string, err error, passNote string) {
		if err != nil {
			failed++
			_, _ = fmt.Fprintf(out, "FAIL %-22s %v\n", name, err)
			return
		}
		if passNote != "" {
			_, _ = fmt.Fprintf(out, "PASS %-22s %s\n", name, passNote)
			return
		}
		_, _ = fmt.Fprintf(out, "PASS %s\n", name)
	}

	// 1. Hermetic resolution: every ecosystem tool from the pinned store.
	report("hermetic-resolution", checkHermeticResolution(cfg.SharedTools), "")

	// 2. Pin agreement: shell go version matches the toolchain directive.
	report("pin-agreement", CheckToolchainPin("go.mod"), "")

	// 3. Workspace shape: every replace target exists as a sibling on disk.
	report("workspace-shape", checkWorkspaceShape(cfg.ReplaceTargets), "")

	// 4. Codegen reachability: proto plugins on PATH at pinned store paths.
	report("codegen-reachability", checkCodegen(cfg.ProtoPlugins), "")

	// 5. Binding toolchain: gopy present at its pinned version, not @latest.
	if cfg.GopyVersion == "" {
		report("binding-toolchain", nil, "no FFI codegen tool required")
	} else {
		report("binding-toolchain", checkGopy(cfg.GopyVersion), "")
	}

	// 6. Required environment: variables the targets assume are set.
	report("required-environment", checkEnv(cfg.RequiredEnv), "")

	if failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	return nil
}

func checkHermeticResolution(extra []string) error {
	tools := append(append([]string{}, baseTools...), extra...)
	for _, tool := range tools {
		if err := checkStorePath(tool); err != nil {
			return err
		}
	}
	return nil
}

func checkWorkspaceShape(targets []string) error {
	for _, dir := range targets {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("replace target %s missing -- clone the sibling repo next to this one", dir)
		}
	}
	return nil
}

func checkCodegen(plugins []string) error {
	for _, plugin := range plugins {
		if err := checkStorePath(plugin); err != nil {
			return fmt.Errorf("%w -- run 'nix develop'", err)
		}
	}
	return nil
}

// checkGopy verifies the FFI codegen tool is present and was built from the
// pinned module version (read from the binary's embedded module info, so a
// floating @latest install is caught even when the binary exists).
func checkGopy(pinned string) error {
	path, err := exec.LookPath("gopy")
	if err != nil {
		return fmt.Errorf("gopy not found -- re-enter 'nix develop' (the shell hook installs the pinned version)")
	}
	out, err := exec.Command("go", "version", "-m", path).Output()
	if err != nil {
		return fmt.Errorf("could not read module info from %s: %w", path, err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "mod" && strings.Contains(fields[1], "gopy") {
			if fields[2] != pinned {
				return fmt.Errorf("gopy is %s, pinned is %s -- remove %s and re-enter 'nix develop'", fields[2], pinned, path)
			}
			return nil
		}
	}
	return fmt.Errorf("no gopy module info in %s -- remove it and re-enter 'nix develop'", path)
}

func checkEnv(vars []string) error {
	for _, v := range vars {
		if os.Getenv(v) == "" {
			return fmt.Errorf("%s is not set -- enter the dev shell ('nix develop'), which exports it", v)
		}
	}
	return nil
}
