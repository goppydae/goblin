// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/gapi/internal/safeio"
)

var (
	verifyPubkey      string
	verifyCheckSource bool
	verifySource      string
)

var agentVerifyCmd = &cobra.Command{
	Use:   "verify <binary>",
	Short: "Verify agent binary integrity and authenticity",
	Long: `Verify agent binary using hash chain and optional signature.

Verification steps:
  1. Binary hash (compares against .b3 file)
  2. Signature (if .sig file exists and --pubkey provided)
  3. Source hash (if --check-source and source available)

Examples:
  gapictl agent verify agents/my_service.go.service
  gapictl agent verify agents/my_service.go.service --pubkey=key.pub
  gapictl agent verify agents/my_service.go.service --check-source --source=src/agents/my_service.go.service`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentVerify,
}

func init() {
	agentVerifyCmd.Flags().StringVar(&verifyPubkey, "pubkey", "", "Public key for signature verification")
	agentVerifyCmd.Flags().BoolVar(&verifyCheckSource, "check-source", false, "Verify source hash")
	agentVerifyCmd.Flags().StringVar(&verifySource, "source", "", "Source directory path (for source verification)")

	agentCmd.AddCommand(agentVerifyCmd)
}

func runAgentVerify(cmd *cobra.Command, args []string) error {
	binaryPath := args[0]

	fmt.Printf("Verifying: %s\n\n", binaryPath)

	allPassed := true

	// Step 1: Verify binary hash
	fmt.Println("1. Binary Hash Verification")
	hashFile := binaryPath + ".b3"
	if _, err := os.Stat(hashFile); os.IsNotExist(err) {
		fmt.Printf("   [WARN] No .b3 file found\n")
	} else {
		expectedHash, err := safeio.ReadFile(hashFile)
		if err != nil {
			return fmt.Errorf("failed to read hash file: %w", err)
		}
		expectedHashStr := strings.TrimSpace(string(expectedHash))

		actualHash, err := crypto.HashFile(binaryPath)
		if err != nil {
			return fmt.Errorf("failed to compute binary hash: %w", err)
		}

		if actualHash == expectedHashStr {
			fmt.Printf("   [OK] VERIFIED\n")
			fmt.Printf("      Hash: %s\n", actualHash[:16]+"...")
		} else {
			fmt.Printf("   [FAIL] FAILED\n")
			fmt.Printf("      Expected: %s\n", expectedHashStr[:16]+"...")
			fmt.Printf("      Actual:   %s\n", actualHash[:16]+"...")
			allPassed = false
		}
	}

	// Step 2: Verify signature (if available)
	fmt.Println("\n2. Signature Verification")
	sigFile := binaryPath + ".sig"
	if _, err := os.Stat(sigFile); os.IsNotExist(err) {
		fmt.Printf("   [WARN] No .sig file found (not signed)\n")
	} else if verifyPubkey == "" {
		fmt.Printf("   [WARN] Signature file exists but no --pubkey provided\n")
	} else {
		// Read signature
		sigData, err := safeio.ReadFile(sigFile)
		if err != nil {
			return fmt.Errorf("failed to read signature: %w", err)
		}

		// Read hash (what was signed)
		hashData, err := safeio.ReadFile(hashFile)
		if err != nil {
			return fmt.Errorf("failed to read hash file: %w", err)
		}

		// Load public key
		pubKey, err := crypto.LoadPublic(verifyPubkey)
		if err != nil {
			return fmt.Errorf("failed to load public key: %w", err)
		}

		// Parse signature (hex encoded)
		sigBytes := make([]byte, 64)
		_, err = fmt.Sscanf(string(sigData), "%x", &sigBytes)
		if err != nil {
			return fmt.Errorf("failed to parse signature: %w", err)
		}

		// Verify
		if crypto.Verify(pubKey, []byte(strings.TrimSpace(string(hashData))), sigBytes) {
			fmt.Printf("   [OK] VERIFIED\n")
			fmt.Printf("      Signed with key: %x...\n", pubKey[:8])
		} else {
			fmt.Printf("   [FAIL] FAILED\n")
			fmt.Printf("      Invalid signature\n")
			allPassed = false
		}
	}

	// Step 3: Verify source hash (if requested)
	if verifyCheckSource {
		fmt.Println("\n3. Source Hash Verification")

		// Get embedded source hash from binary
		describeCmd := exec.Command(binaryPath, "--describe")
		output, err := describeCmd.Output()
		if err != nil {
			fmt.Printf("   [WARN] Failed to run --describe: %v\n", err)
		} else {
			var metadata map[string]interface{}
			if err := json.Unmarshal(output, &metadata); err != nil {
				fmt.Printf("   [WARN] Failed to parse describe output: %v\n", err)
			} else {
				describe, ok := metadata["describe"].(map[string]interface{})
				if !ok {
					fmt.Printf("   [WARN] No describe metadata found\n")
				} else {
					buildInfo, ok := describe["build_info"].(map[string]interface{})
					if !ok || buildInfo == nil {
						fmt.Printf("   [WARN] No build_info found (binary may be old)\n")
					} else {
						embeddedHash, ok := buildInfo["source_hash"].(string)
						if !ok || embeddedHash == "" {
							fmt.Printf("   [WARN] No source_hash in build_info\n")
						} else {
							// Compute current source hash
							sourceDir := verifySource
							if sourceDir == "" {
								fmt.Printf("   [WARN] No --source directory provided\n")
							} else {
								currentHash, err := crypto.HashDirectory(sourceDir, "*.go")
								if err != nil {
									fmt.Printf("   [WARN] Failed to compute source hash: %v\n", err)
								} else {
									if currentHash == embeddedHash {
										fmt.Printf("   [OK] VERIFIED\n")
										fmt.Printf("      Source hash: %s...\n", currentHash[:16])
										fmt.Printf("      Build time: %v\n", buildInfo["build_time"])
									} else {
										fmt.Printf("   [FAIL] FAILED\n")
										fmt.Printf("      Embedded: %s...\n", embeddedHash[:16])
										fmt.Printf("      Current:  %s...\n", currentHash[:16])
										fmt.Printf("      (source has changed since build)\n")
										allPassed = false
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Final result
	fmt.Println("\n" + strings.Repeat("=", 50))
	if allPassed {
		fmt.Println("Result: VERIFIED")
		return nil
	} else {
		fmt.Println("Result: VERIFICATION FAILED")
		return fmt.Errorf("verification failed")
	}
}
