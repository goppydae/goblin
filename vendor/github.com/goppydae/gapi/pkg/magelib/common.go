package magelib

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/magefile/mage/sh"
)

// CheckHermetic ensures tools are running from Nix store
func CheckHermetic() error {
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

// Lint runs linters
func Lint() error {
	fmt.Println("Running linters...")

	if _, err := exec.LookPath("golangci-lint"); err == nil {
		return sh.RunV("golangci-lint", "run")
	}

	fmt.Println("golangci-lint not found, using go vet...")
	return sh.RunV("go", "vet", "./...")
}

// GenerateProto generates Go code from Protobuf definitions.
// protoPattern is a glob pattern for source files (e.g. "proto/*.proto").
// targetDir is the directory to place generated files (e.g. "pkg/proto").
func GenerateProto(protoPattern, targetDir string) error {
	fmt.Println("Generating protobuf code...")

	protoFiles, err := filepath.Glob(protoPattern)
	if err != nil {
		return err
	}

	if len(protoFiles) == 0 {
		fmt.Println("No proto files found matching " + protoPattern)
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

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create target directory %s: %w", targetDir, err)
	}

	// Calculate source directory key (e.g., "proto" from "proto/*.proto")
	// Since generated files are relative to CWD (root), they appear in the same folder as .proto files usually
	// unless specified otherwise. In our case, --go_out=. means they land next to source.
	// We need to move them to targetDir.

	// Assuming files are "proto/foo.pb.go"
	dir := filepath.Dir(protoFiles[0]) // e.g. "proto"
	pbFiles, err := filepath.Glob(filepath.Join(dir, "*.pb.go"))
	if err != nil {
		return err
	}

	for _, src := range pbFiles {
		dst := filepath.Join(targetDir, filepath.Base(src))
		if err := sh.Copy(dst, src); err != nil {
			return fmt.Errorf("failed to copy %s to %s: %w", src, dst, err)
		}
	}

	fmt.Println("✅ Protobuf generation complete")
	return nil
}
