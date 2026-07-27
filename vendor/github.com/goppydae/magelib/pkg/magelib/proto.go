package magelib

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/magefile/mage/sh"
)

// BufGenerate is the schema gate: buf generate, then buf lint, then buf
// breaking against the given git input (e.g. ".git#ref=HEAD"). Routing every
// repo's Proto target through here keeps proto gating in one place
// (schema governance: lint catches package drift at generation time,
// breaking makes an incompatible edit a red build).
func BufGenerate(against string) error {
	fmt.Println("Generating protobuf code (buf)...")
	if err := sh.RunV("buf", "generate"); err != nil {
		return fmt.Errorf("buf generate: %w", err)
	}
	fmt.Println("Linting protobuf definitions (buf lint)...")
	if err := sh.RunV("buf", "lint"); err != nil {
		return fmt.Errorf("buf lint: %w", err)
	}
	fmt.Printf("Checking for breaking schema changes (buf breaking --against %s)...\n", against)
	if err := sh.RunV("buf", "breaking", "--against", against); err != nil {
		return fmt.Errorf("buf breaking: %w", err)
	}
	return nil
}

// MirrorGenerated copies generated *.pb.go files from the generation
// directory to the importable package directory (the Proto target contract:
// generate, then mirror to import path).
func MirrorGenerated(srcGlob, targetDir string) error {
	files, err := filepath.Glob(srcGlob)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		return fmt.Errorf("failed to create target directory %s: %w", targetDir, err)
	}
	for _, src := range files {
		dst := filepath.Join(targetDir, filepath.Base(src))
		if err := sh.Copy(dst, src); err != nil {
			return fmt.Errorf("failed to copy %s to %s: %w", src, dst, err)
		}
	}
	return nil
}

// GenerateProto generates Go code from Protobuf definitions with raw protoc.
// Deprecated: route Proto targets through BufGenerate + MirrorGenerated; this
// remains only until every consumer has a buf configuration.
func GenerateProto(protoPattern, targetDir string) error {
	fmt.Println("Generating protobuf code (protoc)...")

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

	dir := filepath.Dir(protoFiles[0])
	return MirrorGenerated(filepath.Join(dir, "*.pb.go"), targetDir)
}
