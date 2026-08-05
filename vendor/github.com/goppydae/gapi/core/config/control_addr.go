// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/goppydae/gapi/core/product"
)

// The runtime address directory: where each daemon publishes the
// control-plane address it ACTUALLY BOUND, so a control binary can find
// it without sharing an environment (GAPI-DIV-070).
//
// THE ADDRESS MUST BE THE RESOLVED ONE, NOT THE CONFIGURED ONE. That is
// the whole point: a config of ":0" or a hostname that resolves
// differently leaves the daemon as the only party that knows where it
// ended up. Publishing the configured value would reintroduce the
// defect one layer up.
//
// ONE FILE PER DAEMON, KEYED BY PID, AND THAT IS NOT A REFINEMENT.
// The first version wrote a single shared file, and CI proved within
// one run why that cannot work: `go test ./...` starts a daemon in
// test/adk and another in test/e2e concurrently, the second overwrote
// the first's file, and a client dialled a port belonging to a daemon
// it had never heard of. That is not a test artifact - it is the
// multi-worktree case this entry was filed for, where a developer runs
// a daemon per checkout under one XDG_RUNTIME_DIR. A shared file would
// have broken the motivating scenario.
//
// Keying by pid also makes staleness DETECTABLE rather than merely
// reportable: an entry whose process is gone is skipped, so a daemon
// killed with SIGKILL does not strand a file that misdirects every
// later client.
//
// WHY /run AND XDG_RUNTIME_DIR RATHER THAN /var/lib. A listen address
// is VOLATILE state - meaningless once the process is gone - and these
// directories are cleared on boot, so a crash cannot leave a file that
// outlives its daemon across a reboot. This mirrors the tiering already
// in agent_paths.go, where /run/<p>/agents is documented as "transient,
// generated at runtime" and the user tier is XDG_RUNTIME_DIR.

// controlAddrSubdir is the directory, inside each tier, holding one
// entry per live daemon.
const controlAddrSubdir = "control"

// controlAddrExt suffixes each entry; the stem is the daemon's pid.
const controlAddrExt = ".addr"

// ControlAddr is one published address and where it came from.
type ControlAddr struct {
	Addr string
	PID  int
	From string
}

// ErrAmbiguousControlAddr is returned when more than one live daemon
// has published an address. It is an ERROR rather than a choice on
// purpose: with two daemons running, a client that was given no address
// cannot know which one is meant, and picking one would be a coin flip
// that looks like a decision.
type ErrAmbiguousControlAddr struct {
	Candidates []ControlAddr
}

func (e *ErrAmbiguousControlAddr) Error() string {
	parts := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		parts = append(parts, fmt.Sprintf("%s (pid %d, %s)", c.Addr, c.PID, c.From))
	}
	return fmt.Sprintf("%d daemons have published a control address: %s",
		len(e.Candidates), strings.Join(parts, "; "))
}

// ControlAddrDirs returns the candidate directories, highest priority
// first. A system daemon under systemd has no XDG_RUNTIME_DIR and gets
// exactly one entry; that is normal, not degraded.
func ControlAddrDirs() []string {
	p := product.Name()
	var dirs []string
	if run := os.Getenv("XDG_RUNTIME_DIR"); run != "" {
		dirs = append(dirs, filepath.Join(run, p, controlAddrSubdir))
	}
	return append(dirs, filepath.Join("/run", p, controlAddrSubdir))
}

// controlAddrFile is this process's entry within dir.
func controlAddrFile(dir string) string {
	return filepath.Join(dir, strconv.Itoa(os.Getpid())+controlAddrExt)
}

// WriteControlAddr publishes addr under this process's pid in the
// highest tier it can write, and returns the file it used.
//
// Falling through to the next tier is deliberate: an unprivileged
// daemon cannot create /run/<p> and must not be fatal for it, while a
// systemd unit with RuntimeDirectory= can. Only exhausting every tier
// is an error, and it names them all - a daemon that could not publish
// is reachable only with an explicit flag, and the operator needs to
// know that before the first control call fails.
func WriteControlAddr(addr string) (string, error) {
	var attempts []string
	for _, dir := range ControlAddrDirs() {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			attempts = append(attempts, fmt.Sprintf("%s (%v)", dir, err))
			continue
		}
		path := controlAddrFile(dir)
		// 0600/0750, owner-only. An earlier draft used 0644 reasoning
		// that "the address is not a secret", which is true and is not
		// the question: nothing here requires a control binary to run
		// as a different user than the daemon, so the wider mode bought
		// a speculative case at a real cost.
		if err := os.WriteFile(path, []byte(addr+"\n"), 0o600); err != nil {
			attempts = append(attempts, fmt.Sprintf("%s (%v)", path, err))
			continue
		}
		return path, nil
	}
	return "", fmt.Errorf("publish control address %s: no writable runtime directory, tried: %s",
		addr, strings.Join(attempts, "; "))
}

// pidAlive reports whether pid names a running process.
//
// Signal 0 performs the permission and existence checks without
// delivering anything. EPERM means the process EXISTS but belongs to
// another user, which still counts as alive - a daemon running as the
// service user, seen from an operator's shell, is exactly that case.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// LiveControlAddrs returns every address published by a process that is
// still running, highest-priority tier first.
//
// Entries whose process is gone are skipped rather than reported: a
// daemon killed abruptly leaves one behind, and treating that as an
// error would make every later client fail on someone else's crash.
func LiveControlAddrs() ([]ControlAddr, error) {
	var out []ControlAddr
	seen := map[int]bool{}

	for _, dir := range ControlAddrDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read control address dir %s: %w", dir, err)
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), controlAddrExt) {
				names = append(names, e.Name())
			}
		}
		// Deterministic order, so an ambiguity message reads the same
		// way twice and a test can assert it.
		sort.Strings(names)

		for _, name := range names {
			pid, perr := strconv.Atoi(strings.TrimSuffix(name, controlAddrExt))
			if perr != nil || seen[pid] || !pidAlive(pid) {
				continue
			}
			path := filepath.Join(dir, name)
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				if errors.Is(rerr, fs.ErrNotExist) {
					continue // it exited between the listing and the read
				}
				return nil, fmt.Errorf("read control address %s: %w", path, rerr)
			}
			addr := strings.TrimSpace(string(raw))
			if addr == "" {
				continue // a truncated write is not an address
			}
			seen[pid] = true
			out = append(out, ControlAddr{Addr: addr, PID: pid, From: path})
		}
	}
	return out, nil
}

// ReadControlAddr returns the single published address and the file it
// came from, or empty strings when no daemon has published one.
//
// The source is returned rather than discarded because the caller
// cannot otherwise report it, and "a bare timeout that names neither
// address" is the failure this entry was filed for.
//
// More than one live daemon yields *ErrAmbiguousControlAddr, listing
// every candidate. A missing directory is NOT an error: no daemon has
// run, which is the ordinary state of a clean host.
func ReadControlAddr() (addr string, from string, err error) {
	live, err := LiveControlAddrs()
	if err != nil {
		return "", "", err
	}
	switch len(live) {
	case 0:
		return "", "", nil
	case 1:
		return live[0].Addr, live[0].From, nil
	default:
		return "", "", &ErrAmbiguousControlAddr{Candidates: live}
	}
}

// RemoveControlAddr deletes THIS PROCESS'S entry on shutdown.
//
// Only its own, which is the second bug the shared-file version had:
// removing every tier's file meant one daemon shutting down unpublished
// another daemon that was still running.
//
// Absence is success: shutdown runs on paths where the write never
// happened (a daemon that failed before binding), and erroring there
// would turn an orderly stop into a noisy one.
func RemoveControlAddr() error {
	for _, dir := range ControlAddrDirs() {
		path := controlAddrFile(dir)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove control address %s: %w", path, err)
		}
	}
	return nil
}
