// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// The control half of the runtime address (GAPI-DIV-070): deciding WHERE
// to dial, and explaining that choice when the dial fails. The daemon
// publishes into core/config/control_addr.go's tier list; this reads it.

package cli

import (
	"fmt"
	"strings"

	"github.com/goppydae/gapi/core/client"
	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/product"
)

// controlAddrSource records how the target address was chosen, so a
// failure can say so. Empty means the built-in default.
var controlAddrSource string

// resolveControlAddr consults the daemon's published address file when,
// and only when, nothing else has said where to look (GAPI-DIV-070).
//
// THE TEST IS "IS THIS STILL THE BUILT-IN DEFAULT", not "did the user
// pass a flag", and that distinction is what makes this safe. By the
// time config.Load returns, a flag, an environment variable and a config
// file have all collapsed into one string, and viper cannot say which
// supplied it. But any of them producing a value DIFFERENT from the
// compiled-in default is a deliberate instruction, and it wins. Only
// when the address is still the default does the daemon's own report of
// where it bound become the better answer.
//
// The residual case is a config file that explicitly sets the default
// value; that is indistinguishable from no configuration at all, and the
// address file wins. Consulting a running daemon's real address there is
// the more useful behaviour anyway.
func resolveControlAddr(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if cfg.Transport.Address != product.DefaultControlAddr() {
		// Someone SAID where to look - a flag, an environment variable
		// or a config file. Recorded rather than merely returned: an
		// earlier version fell through to the default branch of
		// describeControlTarget and reported an explicitly-passed
		// address as "the configured default", which is the same
		// misattribution this entry exists to remove.
		controlAddrExplicit = true
		controlAddrSource = ""
		return
	}
	controlAddrExplicit = false
	addr, from, err := config.ReadControlAddr()
	if err != nil {
		// AMBIGUITY IS CARRIED, NOT SWALLOWED. More than one daemon is
		// publishing, so there is no defensible automatic choice; the
		// configured default stays in place and the reason is held for
		// the failure message. Picking one would be a coin flip that
		// looks like a decision.
		controlAddrAmbiguity = err
		return
	}
	if addr == "" {
		return
	}
	cfg.Transport.Address = addr
	controlAddrSource = from
}

// controlAddrAmbiguity holds the reason the published address could not
// be used, when several daemons published one.
var controlAddrAmbiguity error

// controlAddrExplicit records that something OTHER than the compiled-in
// default supplied the address - a flag, an environment variable or a
// config file. Which of the three cannot be recovered by the time
// config.Load returns, but that they disagree with the default can, and
// that is the distinction an error message needs.
var controlAddrExplicit bool

// newControlClient dials the daemon and, on failure, says where it
// tried and why it chose there.
//
// Every control command goes through this rather than calling
// client.New directly, for the reason controlConfig's own comment
// gives: a diagnostic wired into one call site out of six is a
// diagnostic that is absent from the command an operator happens to
// run.
func newControlClient(cfg *config.Config) (*client.Client, error) {
	c, err := client.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, describeControlTarget(cfg))
	}
	return c, nil
}

// describeControlTarget explains WHERE the client is dialling and why,
// for an error message.
//
// GAPI-DIV-070 measured the failure this replaces: 'failed to init
// transport: timeout: no recent network activity', naming neither the
// address dialled nor the one configured, and indistinguishable from a
// dead daemon, a wrong host, a firewall, or a crashed process. It is a
// timeout rather than a refusal because QUIC rides UDP and UDP has no
// RST, so nothing will ever tell the client the port is closed - the
// message is the only diagnostic there can be.
func describeControlTarget(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	addr := cfg.Transport.Address
	switch {
	case controlAddrAmbiguity != nil:
		// The useful message names every candidate, because the
		// operator is the only one who can say which daemon they meant.
		return fmt.Sprintf("dialled %s, the configured default, because %v - "+
			"pass --control-addr to say which one you mean",
			addr, controlAddrAmbiguity)
	case controlAddrExplicit:
		return fmt.Sprintf("dialled %s, as configured - nothing is listening there "+
			"(this address came from a flag, the environment or a config file, "+
			"not from a running daemon)", addr)
	case controlAddrSource != "":
		return fmt.Sprintf("dialled %s, from the daemon's published address at %s "+
			"(pass --control-addr to override)", addr, controlAddrSource)
	default:
		return fmt.Sprintf("dialled %s, the configured default - no live daemon has "+
			"published an address in %s (pass --control-addr if it is listening elsewhere)",
			addr, strings.Join(config.ControlAddrDirs(), " or "))
	}
}
