// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"fmt"
	"os"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	"github.com/spf13/cobra"

	"github.com/goppydae/goblin/core/capability"
)

// operator groups the operator-identity commands. Without a way to
// produce a key, --operator-key is a flag nobody can fill in, so keygen
// ships with the registry rather than with the mint RPC (piece 2).
var operatorCmd = &cobra.Command{
	Use:   "operator",
	Short: "Manage operator identities",
}

var operatorKeygenOut string

var operatorKeygenCmd = &cobra.Command{
	Use:   "keygen",
	Short: "Generate an operator Ed25519 keypair",
	Long: "Writes <out>.key (PEM PKCS#8 private) and <out>.pub (hex public).\n" +
		"Pass the .pub path to goblind --operator-key; keep the .key off the cluster.\n\n" +
		"Never retire your last known-good key until the new key has authorized\n" +
		"one real change. The last registered key cannot be removed - that is what\n" +
		"stops a compromised node emptying the registry and re-seeding it with its\n" +
		"own key - but the rule counts keys, it cannot tell whether anyone holds\n" +
		"the private half. A registry holding only unreachable keys can neither\n" +
		"authorize anything nor be re-seeded.\n\n" +
		"Recovery is wiping the data dir on EVERY replica and re-bootstrapping;\n" +
		"wiping one node only makes it catch up from the leader and inherit the\n" +
		"same dead registry. On a cluster that has already run, that wipe also\n" +
		"destroys the instance table and the append-only tombstone set, making\n" +
		"terminated instance UUIDs re-admittable, and leaves the agent processes\n" +
		"themselves running to be reaped by hand.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if operatorKeygenOut == "" {
			return fmt.Errorf("--out is required")
		}
		kp, err := gapicrypto.GenerateKey()
		if err != nil {
			return fmt.Errorf("generate operator key: %w", err)
		}
		privPath := operatorKeygenOut + ".key"
		// Pre-create restricted, then tighten, both BEFORE SavePrivate
		// writes. gapi's SavePrivate goes through os.Create, which would
		// otherwise land a fresh file at 0644. The mode argument to
		// O_CREATE only applies when the file does not already exist, so
		// re-running keygen over a leftover file needs the explicit chmod
		// as well - without it the key bytes land in a world-readable
		// file and are only protected afterwards.
		f, err := os.OpenFile(privPath, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create private key %s: %w", privPath, err)
		}
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("close private key %s: %w", privPath, cerr)
		}
		if err := os.Chmod(privPath, 0o600); err != nil {
			return fmt.Errorf("restrict private key permissions on %s: %w", privPath, err)
		}
		if err := kp.SavePrivate(privPath); err != nil {
			return fmt.Errorf("write private key: %w", err)
		}
		if err := kp.SavePublic(operatorKeygenOut + ".pub"); err != nil {
			return fmt.Errorf("write public key: %w", err)
		}
		cmd.Printf("operator key id: %s\n", capability.OperatorKeyID(kp.Public))
		cmd.Printf("private: %s.key\npublic:  %s.pub\n", operatorKeygenOut, operatorKeygenOut)
		return nil
	},
}

func init() {
	operatorKeygenCmd.Flags().StringVar(&operatorKeygenOut, "out", "",
		"Output path prefix; writes <prefix>.key and <prefix>.pub")
	operatorCmd.AddCommand(operatorKeygenCmd)
}
