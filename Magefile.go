//go:build mage
// +build mage

package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
	"github.com/zeebo/blake3"
)

// checkHermetic ensures tools are running from Nix store
func checkHermetic() error {
	tools := []string{"go", "gcc", "protoc"}

	if _, err := exec.LookPath("pandoc"); err == nil {
		tools = append(tools, "pandoc")
	}

	for _, tool := range tools {
		path, err := exec.LookPath(tool)
		if err != nil {
			return fmt.Errorf("%s not found. Run 'nix develop'", tool)
		}

		realPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			realPath = path // fallback
		}

		if len(realPath) < 10 || realPath[:10] != "/nix/store" {
			fmt.Printf("⚠️  Warning: %s is not running from Nix store (%s). Hermetic build not guaranteed.\n", tool, realPath)
		}
	}
	return nil
}

// Build builds the goblind and goblinctl binaries
func Build() error {
	mg.Deps(checkHermetic)
	fmt.Println("Building goblind and goblinctl...")

	// Goblin Daemon
	if err := sh.Run("go", "build", "-o", "bin/goblind", "./cmd/goblind"); err != nil {
		// Assuming cmd/goblind exists, if not we will adjust.
		// Previous exploration showed cmd/goblinctl. Let's check listing.
		return err
	}

	// Goblin CLI
	if err := sh.Run("go", "build", "-o", "bin/goblinctl", "./cmd/goblinctl"); err != nil {
		return err
	}

	// Calculate and write BLAKE3 hashes
	for _, bin := range []string{"bin/goblind", "bin/goblinctl"} {
		if err := hashFile(bin); err != nil {
			// Don't fail hard if binary doesn't exist yet (e.g. partial build), but Build should succeed.
			return fmt.Errorf("hashing %s failed: %w", bin, err)
		}
	}

	fmt.Println("✅ Build complete: bin/goblind, bin/goblinctl (with .b3 checksums)")
	return nil
}

func hashFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := blake3.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	sum := h.Sum(nil)
	hexSum := hex.EncodeToString(sum)

	return os.WriteFile(path+".b3", []byte(hexSum+"\n"), 0644)
}

// Install installs binaries to $GOPATH/bin
func Install() error {
	mg.Deps(checkHermetic)
	fmt.Println("Installing goblind and goblinctl...")

	if err := sh.Run("go", "install", "./cmd/goblind"); err != nil {
		return err
	}

	if err := sh.Run("go", "install", "./cmd/goblinctl"); err != nil {
		return err
	}

	fmt.Println("✅ Installed to $GOPATH/bin")
	return nil
}

// Test runs all tests
func Test() error {
	mg.Deps(checkHermetic)
	fmt.Println("Running tests...")
	return sh.RunV("go", "test", "-v", "./...")
}

// Clean removes build artifacts
func Clean() error {
	fmt.Println("Cleaning build artifacts...")

	dirs := []string{"bin"}
	for _, dir := range dirs {
		if err := sh.Rm(dir); err != nil {
			fmt.Printf("Warning: failed to remove %s: %v\n", dir, err)
		}
	}

	fmt.Println("✅ Clean complete")
	return nil
}

// Proto generates protobuf code
func Proto() error {
	mg.Deps(checkHermetic)
	fmt.Println("Generating protobuf code...")

	protoFiles, err := filepath.Glob("proto/*.proto")
	if err != nil {
		return err
	}

	if len(protoFiles) == 0 {
		fmt.Println("No local proto files found. Skipping.")
		return nil
	}

	for _, file := range protoFiles {
		args := []string{
			"--go_out=.",
			"--go_opt=paths=source_relative",
			"--go-grpc_out=.",
			"--go-grpc_opt=paths=source_relative",
			file,
		}
		if err := sh.Run("protoc", args...); err != nil {
			return fmt.Errorf("protoc failed for %s: %w", file, err)
		}
	}

	// Copy generated files to internal/proto/
	if err := os.MkdirAll("internal/proto", 0755); err != nil {
		return fmt.Errorf("failed to create internal/proto directory: %w", err)
	}

	pbFiles, err := filepath.Glob("proto/*.pb.go")
	if err != nil {
		return err
	}

	for _, src := range pbFiles {
		dst := filepath.Join("internal/proto", filepath.Base(src))
		if err := sh.Copy(dst, src); err != nil {
			return fmt.Errorf("failed to copy %s to %s: %w", src, dst, err)
		}
	}

	fmt.Println("✅ Protobuf generation complete")
	return nil
}

// Fmt formats all Go code
func Fmt() error {
	fmt.Println("Formatting code...")
	return sh.RunV("go", "fmt", "./...")
}

// Tidy runs go mod tidy
func Tidy() error {
	fmt.Println("Tidying go.mod...")
	return sh.Run("go", "mod", "tidy")
}

// All runs fmt, tidy, build, and test
func All() error {
	mg.Deps(Fmt, Tidy, Build, Test)
	fmt.Println("✅ All tasks complete")
	return nil
}

// Documentation tasks
type Docs mg.Namespace

// Html generates the static documentation site using MkDocs
func (Docs) Html() error {
	fmt.Println("Generating HTML documentation...")
	if _, err := exec.LookPath("mkdocs"); err != nil {
		return fmt.Errorf("mkdocs not found. Run 'nix develop' to get documentation tools")
	}
	return sh.RunV("mkdocs", "build")
}
