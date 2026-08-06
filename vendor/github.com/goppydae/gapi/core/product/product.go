// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package product carries the identity of the product whose process the
// kernel is running inside.
//
// The kernel is a LIBRARY. gapid links it and is the gapi product;
// goblind links the same code and is the goblin product, and an operator
// of goblind should never have to learn that gapi exists (GAPI-DIV-061).
// Every host-namespaced resource the kernel creates, reads or advertises
// therefore derives from this one value rather than from a literal:
//
//   - the environment prefix, and so every GAPI_/GOBLIN_ variable
//   - the config search directory, /etc/<product>
//   - the agent search paths, /usr/lib/<product>/agents and friends
//   - the default log path, /var/log/<product>/<product>.log
//   - cgroup names, <product>d-infra and <product>d-<id>
//   - the kmsg tag an operator reads in dmesg under --pid1
//
// Three classes of gapi-spelling string exist in this repo and only the
// list above is one of them. PROSE - stderr prefixes, help text,
// Prometheus help - names the role instead ("supervisor"), because
// naming the vendor inside its own output is redundant even for gapid's
// own operator. WIRE - the gapi-quic ALPN, the gapi.v1 protobuf package
// names, the gapi_* metric names - MUST NOT CHANGE: renaming a protocol
// constant or a scraped metric is a compatibility break, and no operator
// reads an ALPN string off a terminal. That exclusion is written down so
// a later sweep does not "finish the job".
//
// # No usable default
//
// There is no fallback identity. Name() panics when nothing has been
// set, so a binary that forgets to declare itself dies at startup rather
// than quietly adopting gapi's namespace - which would mean ignoring the
// operator's configuration and booting on defaults. core/version
// tolerates the same set-then-read shape only because a late read there
// is cosmetic; a late read here is a misconfigured daemon.
//
// The compile-time half of that guarantee lives in pkg/cli: the root
// constructors take the product as a parameter, so an embedder cannot
// build a command tree without naming itself. The panic covers the
// remainder - an embedder that uses the kernel without pkg/cli.
package product

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// nameRe is what a product name may be. The validation is not decoration:
// the constructors that call Set take four adjacent strings (product,
// binary, version, short), and a transposition is otherwise silent. A
// version ("0.1.0-proto2d") and a help line ("GAPI Supervisor Daemon")
// both fail this pattern; "gapid" would not, which is why the caller-side
// tests assert the value and not only that one was set.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// directEnv is every environment variable the kernel reads that is NOT a
// config key, mapped to the code that reads it.
//
// It exists as a registry rather than as eight scattered literals for
// one reason: cli-contract.md requires that a documented variable have a
// reader, and core/config/envnames_test.go enforces that by looking for
// the name. Once the names are composed at runtime no literal spells
// them, so the gate would go blind exactly the way GAPI-DIV-059's exit
// predicted. Composing the list from here keeps the gate honest, and
// EnvKey's panic on an undeclared suffix keeps the list complete: a
// reader cannot obtain a name that is not in it.
//
// Config-key overrides are NOT here. Those are composed from the Config
// struct's mapstructure tags by core/config, which is a stronger source
// than any list - a field added later is bound because it exists.
var directEnv = map[string]string{
	"CONFIG":               "core/config.Load - path to config.yaml",
	"AGENT_PATH":           "core/config.AgentSearchPaths - directories PREPENDED to the tiers",
	"AGENT_PATH_EXCLUSIVE": "core/config.AgentSearchPaths - search only what AGENT_PATH names",
	"DEV_AGENTS":           "core/config.AgentSearchPaths, ClassifyPath - highest-priority agent dir",
	"SKIP_SYSTEM_AGENTS":   "core/config.AgentSearchPaths - drop the system dirs",
	"CGROUPS_DISABLE":      "core/cgroups.Setup - refuse cgroup delegation",
	"VERIFY_KEY":           "core/supervisor.New - agent signing public key",
	"PY_ADK":               "core/adkpath.ResolvePyADK - override the Python ADK source tree",
	"GO_ADK":               "pkg/cli.resolveGoADK - override the Go ADK source tree used by 'agent build'",
	"KMSG_PATH":            "core/supervisor pid1 wiring - override /dev/kmsg",
}

// controlAddrDefaults is each product's zero-config control-plane
// address, and it is a TABLE because this one value cannot be composed.
//
// Every other surface here falls out of the name by string composition:
// Daemon is Name()+"d", ConfigDir is /etc/<name>, EnvPrefix is the name
// upper-cased. A PORT does not. 31415 does not fall out of "goblin", and
// Daemon's own comment anticipated this case - "a product whose daemon
// is not <product>d would need a second value here". This is the first
// such value (GAPI-DIV-071).
//
// It lives in the kernel rather than being passed at Set() time so the
// identity stays ONE argument: a second parameter would put a value at
// every call site and reopen the two-declarations problem Set exists to
// close. The cost is real and recorded - changing a product's default
// port is a kernel change, a tag and a re-vendor.
//
// The addresses are LOOPBACK deliberately. A control plane that binds
// every interface by default is a decision an operator should have to
// make, and both architecture docs already specified 127.0.0.1.
// THE NUMBERS ARE MNEMONIC, and the scheme is written down because the
// last one was not (operator decision 47). The CONTROL port is what the
// product IS: 29979 is c, the speed of light, for the kernel; 31415 is
// pi, the closed circle of a cluster, for the orchestrator. goblin takes
// the higher number of each pair.
//
// The reasoning for the PREVIOUS numbers was never recorded and could
// not be reconstructed from the ledger, the handoff or the vault. 14242
// is one transposed digit from 14142 = sqrt(2), which may be all it ever
// was. That is the argument for this paragraph existing.
//
// CONSTRAINT ON ANY VALUE HERE: between 1024 and 32767 - above the
// privileged range and BELOW the ephemeral floor, measured at
// 32768-60999 on the development host. A default inside the ephemeral
// range can be taken by an outbound connection before the daemon binds,
// which presents as an intermittent bind failure that looks like a race
// and is not.
var controlAddrDefaults = map[string]string{
	"gapi":   "127.0.0.1:29979",
	"goblin": "127.0.0.1:31415",
}

// metricsAddrDefaults is the same shape for the Prometheus listener, and
// it is an INDEPENDENT table rather than a derivation from the control
// port (control+1 was proposed and rejected, operator decision 47).
//
// GAPI-DIV-111: this was ONE shared literal, 127.0.0.1:19090, set for
// every product by a config loader that was product-aware on the line
// immediately above it. gapid and goblind on one host therefore defaulted
// to the same metrics listener and contended for it. LATENT rather than
// active - metrics.enabled defaults to false, so nothing binds it today -
// which is exactly why it survived: the first operator to enable metrics
// on both daemons gets a bind failure naming neither product.
//
// The METRICS numbers come from spectroscopy, the science of reading a
// system without consuming it, which is what a metrics endpoint is for:
// 10973 is the Rydberg constant, 13703 the inverse fine-structure
// constant. The same 1024-32767 constraint applies and both satisfy it.
var metricsAddrDefaults = map[string]string{
	"gapi":   "127.0.0.1:10973",
	"goblin": "127.0.0.1:13703",
}

var (
	mu   sync.RWMutex
	name string
)

// Set declares the product this process belongs to. Names are lowercase
// and alphanumeric: "gapi", "goblin".
//
// Last writer wins, deliberately. A single process legitimately builds
// more than one command tree - goblinctl mounts gapictl's verbs under
// `agent`, and the flag-parity tests construct both roots side by side -
// so a set-once panic would fire on correct code. What makes that safe is
// that nothing here is read during package initialization; the ordering
// hazard the panic in Name() guards is a read before ANY set, not a
// second set.
func Set(n string) {
	if !nameRe.MatchString(n) {
		panic(fmt.Sprintf("product.Set(%q): a product name is lowercase alphanumeric, "+
			"like \"gapi\" or \"goblin\" - check the argument order at the call site", n))
	}
	mu.Lock()
	defer mu.Unlock()
	name = n
}

// Name returns the product identity, panicking if none was declared.
//
// The panic is the point. Returning "gapi" here would make a binary that
// forgot to declare itself read GAPI_* variables, search /etc/gapi and
// write dmesg lines an operator cannot attribute - a silently
// misconfigured daemon rather than a failed one.
func Name() string {
	mu.RLock()
	defer mu.RUnlock()
	if name == "" {
		panic("product identity was read before it was set: no binary called " +
			"product.Set. A process embedding the kernel must declare its product " +
			"before anything resolves an environment variable, config path, agent " +
			"path or cgroup name (GAPI-DIV-061).")
	}
	return name
}

// IsSet reports whether an identity has been declared, without panicking.
// For tests and for diagnostics that must not themselves fail.
func IsSet() bool {
	mu.RLock()
	defer mu.RUnlock()
	return name != ""
}

// Daemon is the name of the product's daemon binary: gapi -> gapid,
// goblin -> goblind.
//
// The "d" suffix is the silo's binary-naming convention, written down in
// cli-contract.md - four binaries, <product>d and <product>ctl. Deriving
// it rather than passing a second value is what keeps this ONE identity
// instead of two, and it holds today's strings exactly: gapid still tags
// dmesg with "gapid:" and still owns the "gapid-infra" cgroup. The
// tradeoff is that a product whose daemon is not <product>d would need a
// second value here.
func Daemon() string { return Name() + "d" }

// EnvPrefix is the environment namespace: GAPI, GOBLIN.
func EnvPrefix() string { return strings.ToUpper(Name()) }

// EnvKey renders a declared suffix as a full environment variable name.
// An undeclared suffix panics rather than composing a name no gate knows
// about; add it to directEnv with its reader.
func EnvKey(suffix string) string {
	if _, ok := directEnv[suffix]; !ok {
		panic(fmt.Sprintf("product.EnvKey(%q): undeclared environment name. "+
			"Add it to directEnv with the code that reads it, or the "+
			"documented-names gate cannot see it.", suffix))
	}
	return EnvPrefix() + "_" + suffix
}

// DirectEnvNames returns every declared direct name, fully composed, for
// the gate in core/config that checks documented names have readers.
func DirectEnvNames() []string {
	out := make([]string, 0, len(directEnv))
	prefix := EnvPrefix()
	for suffix := range directEnv {
		out = append(out, prefix+"_"+suffix)
	}
	sort.Strings(out)
	return out
}

// DefaultControlAddr is the product's zero-config control-plane address.
//
// An identity with no declared address PANICS rather than falling back.
// A fallback would hand an unknown embedder gapi's port while every
// other surface said otherwise - which is the defect GAPI-DIV-071 exists
// to remove, one level up - and Name() already establishes that a
// missing identity is fatal rather than defaulted.
func DefaultControlAddr() string {
	n := Name()
	addr, ok := controlAddrDefaults[n]
	if !ok {
		panic(fmt.Sprintf("product %q has no default control-plane address. "+
			"A port cannot be derived from a product name, so add %q to "+
			"controlAddrDefaults in core/product with the address its daemon "+
			"binds; falling back to another product's port would bind the "+
			"wrong one in silence.", n, n))
	}
	return addr
}

// DefaultMetricsAddr is the product's zero-config Prometheus listen
// address.
//
// PANICS on an unknown identity for the same reason DefaultControlAddr
// does, and the reason is sharper here: the defect this replaces was
// every product silently sharing ONE address (GAPI-DIV-111), so a
// fallback would reinstate exactly the collision it removes.
func DefaultMetricsAddr() string {
	n := Name()
	addr, ok := metricsAddrDefaults[n]
	if !ok {
		panic(fmt.Sprintf("product %q has no default metrics address. "+
			"A port cannot be derived from a product name, so add %q to "+
			"metricsAddrDefaults in core/product with the address its "+
			"daemon binds; falling back to another product's port would "+
			"make two daemons on one host contend for one listener.", n, n))
	}
	return addr
}

// ConfigDir is where a release build looks for config.yaml.
func ConfigDir() string { return filepath.Join("/etc", Name()) }

// DefaultLogPath is the default rotated file sink.
func DefaultLogPath() string {
	return filepath.Join("/var/log", Name(), Name()+".log")
}
