package magelib

import (
	"fmt"
	"os"

	"github.com/magefile/mage/sh"
)

// BufGenerate is the schema gate: buf generate, then buf lint, then buf
// breaking against the given git input (e.g. ".git#ref=HEAD"). Routing every
// repo's Proto target through here keeps proto gating in one place
// (schema governance: lint catches package drift at generation time,
// breaking makes an incompatible edit a red build).
//
// PROTO_BREAKING_SKIP=1 skips the breaking step, loudly. Its one
// legitimate use is an intentional schema reset (an operator-approved
// wire break, pre-1.0): the working tree cannot pass a comparison
// against the pre-reset baseline by design. The first post-commit run
// must be made WITHOUT the variable so the gate re-arms against the
// new baseline.
func BufGenerate(against string) error {
	fmt.Println("Generating protobuf code (buf)...")
	if err := sh.RunV("buf", "generate"); err != nil {
		return fmt.Errorf("buf generate: %w", err)
	}
	fmt.Println("Linting protobuf definitions (buf lint)...")
	if err := sh.RunV("buf", "lint"); err != nil {
		return fmt.Errorf("buf lint: %w", err)
	}
	if os.Getenv("PROTO_BREAKING_SKIP") == "1" {
		fmt.Println("BREAKING: SKIPPED (PROTO_BREAKING_SKIP=1 - intentional schema reset; re-run without it after commit)")
		return nil
	}
	fmt.Printf("Checking for breaking schema changes (buf breaking --against %s)...\n", against)
	if err := sh.RunV("buf", "breaking", "--against", against); err != nil {
		return fmt.Errorf("buf breaking: %w", err)
	}
	return nil
}

// MirrorGenerated and the protoc-based GenerateProto are gone: both
// repos generate directly into their canonical import directory via
// buf.gen.yaml's module opt, so there is no mirror step to run.
