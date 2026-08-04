// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	gapicli "github.com/goppydae/gapi/pkg/cli"
	"github.com/goppydae/goblin/internal/version"
)

// These tests close GOBLIN-DIV-052 for goblinctl. Before it, the root
// was a bare cobra.Command with Version set to a raw string, so
// `--version` printed cobra's one-liner "goblinctl version 0.1.0-proto2"
// and `goblinctl version` exited 1 with "unknown command".
//
// This file is in package cli rather than cli_test because the
// resolution helpers it asserts on - controlAddr, controlTLS - are
// unexported, and they are the part a behavioural test cannot reach:
// every RPC verb reads them, but only against a live daemon.

// freshRoot builds a root the same way RootCmd is built. Fresh per
// invocation because cobra keeps parse state on the command - notably
// the persistent `version` flag, which stays true after one --version
// run and would make a later assertion pass for the wrong reason.
func freshRoot() *cobra.Command {
	root, _ := gapicli.NewControlRoot(
		"goblin", "goblinctl", version.Version, "Goblin distributed supervisor control")
	return root
}

// runFresh executes args against a new root and returns what it wrote.
func runFresh(args ...string) (string, error) {
	root := freshRoot()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	err := gapicli.RunRoot(root, args)
	return buf.String(), err
}

// The identity surface: three spellings, one renderer, a first line that
// names the binary, and the embedded kernel reported alongside it.
func TestGoblinctlRoot_VersionSurfaces(t *testing.T) {
	sub, err := runFresh("version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.HasPrefix(sub, "goblinctl:") {
		t.Errorf("version block does not name the binary, first line = %q",
			strings.SplitN(sub, "\n", 2)[0])
	}
	if !strings.Contains(sub, "Runtime Core") {
		t.Errorf("version block omits the embedded kernel:\n%s", sub)
	}

	for _, flag := range []string{"--version", "-v"} {
		t.Run(flag, func(t *testing.T) {
			got, err := runFresh(flag)
			if err != nil {
				t.Fatalf("%s: %v", flag, err)
			}
			if got != sub {
				t.Errorf("%s and `version` render different bytes:\n%s\n--- vs ---\n%s",
					flag, got, sub)
			}
		})
	}
}

// RootCmd must be the shared construction, not a lookalike. If someone
// rebuilds it as a plain cobra.Command the version surface silently
// reverts to cobra's one-liner, which is the defect this entry names.
func TestGoblinctlRoot_UsesTheSharedConstructor(t *testing.T) {
	if RootCmd.Version != freshRoot().Version {
		t.Error("RootCmd's version block differs from NewControlRoot's - " +
			"the root is no longer built by the shared constructor")
	}
	if RootCmd.RunE != nil || RootCmd.Run != nil {
		t.Error("a control root must carry no RunE: cobra would hand " +
			"unmatched arguments to it as positional parameters")
	}
}

// A bare `goblinctl` prints help and exits NON-ZERO. goblind was moved
// to RunRoot by GOBLIN-DIV-053 while goblinctl stayed on Execute and
// exited 0, so the two roles disagreed on a rule the contract puts on
// both.
func TestGoblinctlRoot_BareInvocation(t *testing.T) {
	out, err := runFresh()

	if !errors.Is(err, gapicli.ErrNoCommand) {
		t.Errorf("err = %v, want ErrNoCommand", err)
	}
	if !strings.Contains(out, "Available Commands") {
		t.Errorf("bare invocation printed no help:\n%s", out)
	}
}

// The persistent set comes from the shared registrar and nothing is
// defined locally. A flag added to one control binary and not its peer
// is what the registrar exists to prevent.
func TestGoblinctlRoot_ControlFlagsAreShared(t *testing.T) {
	for _, name := range []string{"api-addr", "tls-ca", "tls-insecure", "log-level"} {
		if RootCmd.PersistentFlags().Lookup(name) == nil {
			t.Errorf("--%s missing from the control root's persistent set", name)
		}
	}
	// --api-addr's shared default is EMPTY, deliberately: the two daemons
	// listen on different ports, so a shared literal would be wrong for
	// one of them. If this ever reads "127.0.0.1:29000" again, the flag
	// has been redefined locally.
	if got := RootCmd.PersistentFlags().Lookup("api-addr").DefValue; got != "" {
		t.Errorf("--api-addr default = %q, want empty (the shared default)", got)
	}
}

// Empty means the local node; a value overrides it. This is the pair
// that keeps the shared empty default from silently retargeting every
// existing goblinctl invocation, and it is unreachable from a
// behavioural test without a live daemon.
func TestControlAddr_ResolvesEmptyToTheLocalNode(t *testing.T) {
	saved := controlFlags.APIAddr
	t.Cleanup(func() { controlFlags.APIAddr = saved })

	controlFlags.APIAddr = ""
	if got := controlAddr(); got != defaultAPIAddr {
		t.Errorf("empty --api-addr resolved to %q, want %q", got, defaultAPIAddr)
	}

	// The control disagrees with the default, so this fails if
	// controlAddr ignores the flag and always returns the literal.
	controlFlags.APIAddr = "10.9.8.7:12345"
	if got := controlAddr(); got != "10.9.8.7:12345" {
		t.Errorf("explicit --api-addr resolved to %q, want the override", got)
	}
}

// controlTLS must carry both fields through. A registrar that defines
// flags nobody reads is the GAPI-DIV-034 shape, and this is the read.
func TestControlTLS_CarriesBothFields(t *testing.T) {
	savedCA, savedInsecure := controlFlags.TLSCA, controlFlags.TLSInsecure
	t.Cleanup(func() { controlFlags.TLSCA, controlFlags.TLSInsecure = savedCA, savedInsecure })

	controlFlags.TLSCA, controlFlags.TLSInsecure = "/tmp/ca.pem", true
	got := controlTLS()
	if got.CAFile != "/tmp/ca.pem" {
		t.Errorf("CAFile = %q, want the --tls-ca value", got.CAFile)
	}
	if !got.InsecureSkipVerify {
		t.Error("InsecureSkipVerify = false, want the --tls-insecure value")
	}
}
