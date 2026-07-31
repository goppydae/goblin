package magelib

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// baseTools are the tools every real target depends on. Repos may extend the
// list via DoctorConfig.SharedTools for the doctor's hermetic check.
var baseTools = []string{"go", "gcc", "protoc"}

// CheckHermetic ensures every core tool resolves from the Nix store and that
// the shell's go toolchain matches the go.mod toolchain directive. Any
// violation fails closed (hermetic shell rule; the double pin).
//
// extra names tools beyond the base compiler set that the caller's targets
// actually execute -- golangci-lint, gosec, buf. It is variadic so callers
// that pass nothing keep compiling: gapi and goblin vendor this package and
// call CheckHermetic() with no arguments. Callers should feed it the same
// value they hand DoctorConfig.SharedTools, so the doctor and the gate
// cannot report different tool sets.
func CheckHermetic(extra ...string) error {
	for _, tool := range hermeticTools(extra) {
		if err := checkStorePath(tool); err != nil {
			return err
		}
	}

	return CheckToolchainPin("go.mod")
}

// hermeticTools returns the tools CheckHermetic will resolve: the base
// compiler set, pandoc when the shell has it, then the caller's extras in
// the order given. Split out of CheckHermetic so the tool-set decision is
// testable without a shell; note the pandoc probe still reads PATH, so it
// is not fully pure.
func hermeticTools(extra []string) []string {
	tools := append([]string{}, baseTools...)
	if _, err := exec.LookPath("pandoc"); err == nil {
		tools = append(tools, "pandoc")
	}
	return append(tools, extra...)
}

// checkStorePath fails unless the named tool resolves into /nix/store.
func checkStorePath(tool string) error {
	path, err := exec.LookPath(tool)
	if err != nil {
		return fmt.Errorf("%s not found. Run 'nix develop'", tool)
	}

	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		realPath = path // fallback
	}

	if !strings.HasPrefix(realPath, "/nix/store") {
		return fmt.Errorf("%s resolves outside the Nix store (%s); hermetic build not guaranteed. Run 'nix develop'", tool, realPath)
	}
	return nil
}

var toolchainRe = regexp.MustCompile(`(?m)^toolchain\s+(\S+)\s*$`)

// CheckToolchainPin asserts that the shell's `go version` equals the
// `toolchain` directive in the given go.mod. The flake pins Go out-of-band;
// the directive pins it in-band; this assertion turns silent skew into a red
// build (go manifesto section 5).
func CheckToolchainPin(gomodPath string) error {
	data, err := os.ReadFile(gomodPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", gomodPath, err)
	}

	m := toolchainRe.FindSubmatch(data)
	if m == nil {
		return fmt.Errorf("%s has no toolchain directive; add one matching the flake's go pin", gomodPath)
	}
	want := string(m[1])

	out, err := exec.Command("go", "version").Output()
	if err != nil {
		return fmt.Errorf("running 'go version': %w", err)
	}
	fields := strings.Fields(string(out)) // "go version go1.26.5 linux/amd64"
	if len(fields) < 3 {
		return fmt.Errorf("unexpected 'go version' output: %q", strings.TrimSpace(string(out)))
	}
	have := fields[2]

	if have != want {
		return fmt.Errorf("toolchain pin skew: shell go is %s but %s toolchain directive is %s; bump the flake pin and the directive in the same commit", have, gomodPath, want)
	}
	return nil
}
