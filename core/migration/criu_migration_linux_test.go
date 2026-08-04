// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build criu && linux

// Real CRIU migration. Build-tagged because it needs
// CAP_CHECKPOINT_RESTORE, which no unprivileged process can hold: this
// runs inside the NixOS VM check, never in the ordinary suite.
//
// Everything below is the production path. gapi's core/checkpoint does
// the dump and the restore, this package's Server and Client move the
// image over the real goblin-ckpt ALPN, and the assertion is the one
// that matters: the process comes back alive with the PID it was
// dumped under, because restore reclaims it via clone3(set_tid).
package migration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	gapicheckpoint "github.com/goppydae/gapi/core/checkpoint"

	"github.com/goppydae/goblin/core/migration"
	"github.com/goppydae/goblin/core/transport"
)

func requireCheckpointable(t *testing.T) {
	t.Helper()
	if err := gapicheckpoint.Available(); err != nil {
		// Under the criu tag this is a hard failure, not a skip. A test
		// that quietly passes where it cannot run is the GAPI-DIV-028
		// failure mode, and this suite exists precisely to avoid it.
		t.Fatalf("checkpointing unavailable in a build tagged for it: %v", err)
	}
}

// spawnVictim starts a long-lived process in its own session, which is
// what criu needs to dump a tree it does not share a terminal with.
// The command is returned so the caller can REAP it after the dump -
// see reapVictim, which is load bearing.
func spawnVictim(t *testing.T) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawning victim: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})
	// Let it reach a steady state; dumping a process mid-exec is a
	// different and much flakier thing to test.
	time.Sleep(200 * time.Millisecond)
	return cmd, pid
}

// reapVictim waits for the dumped child so its PID is actually
// released.
//
// This is not test hygiene, it is a real constraint the first VM run
// exposed. criu dump kills the process, but a killed child of a live
// parent becomes a ZOMBIE, and a zombie still occupies its PID. Restore
// then fails with "Can't fork for <pid>: File exists" because
// clone3(set_tid) cannot reclaim a PID that is still taken.
//
// In production gapi's subreaper does this reaping. The ordering
// matters most on the ROLLBACK path, where the restore targets the same
// PID namespace that just held the process; a cross-node restore lands
// in a fresh namespace and would not collide.
func reapVictim(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out reaping the dumped process; its PID is still held")
	}
}

// alive reports whether pid is a live process. A zombie is deliberately
// NOT alive: kill(pid, 0) succeeds on an unreaped zombie, so the naive
// check reports a dumped process as still running.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// "pid (comm) STATE ..." - comm may contain spaces, so index from
	// the last ')' rather than splitting the whole line.
	if i := bytes.LastIndexByte(stat, ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] != 'Z'
	}
	return false
}

// TestRealMigrationRoundTrip is the capstone: a live process is dumped
// on one store, its image is transferred over goblin-ckpt, and it is
// restored from the other store under its original PID.
func TestRealMigrationRoundTrip(t *testing.T) {
	requireCheckpointable(t)

	source := migration.NewStore(t.TempDir())
	dest := migration.NewStore(t.TempDir())
	uuid := []byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 0, 1, 2, 3, 4, 5, 6}
	const epoch = 1

	cmd, pid := spawnVictim(t)
	if !alive(pid) {
		t.Fatalf("victim %d died before the dump", pid)
	}

	// --- dump: the source process is STOPPED by this ------------------
	dir, err := source.Create(uuid, epoch)
	if err != nil {
		t.Fatalf("creating source image dir: %v", err)
	}
	if err := gapicheckpoint.Dump(pid, dir, gapicheckpoint.Options{
		ShellJob:       true,
		TCPEstablished: true,
		FileLocks:      true,
	}); err != nil {
		t.Fatalf("dump pid %d: %v", pid, err)
	}
	if alive(pid) {
		t.Error("process survived its own dump; the image is not a sound rollback point")
	}
	// Release the PID before restoring: a zombie still holds it, and
	// clone3(set_tid) cannot reclaim a PID that is occupied.
	reapVictim(t, cmd)

	// --- transfer over the real ALPN ----------------------------------
	transferImage(t, source, dest, uuid, epoch)

	// --- restore from the destination store ---------------------------
	destDir, err := dest.Dir(uuid, epoch)
	if err != nil {
		t.Fatalf("resolving dest image dir: %v", err)
	}
	restored, err := gapicheckpoint.Restore(destDir, gapicheckpoint.Options{
		ShellJob:       true,
		TCPEstablished: true,
		FileLocks:      true,
	})
	if err != nil {
		t.Fatalf("restore from %s: %v", destDir, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(restored, syscall.SIGKILL) })

	// The entire migration semantic: the PID is reclaimed, so the
	// process that comes back is the one that went away.
	if restored != pid {
		t.Errorf("restored pid = %d, want %d (clone3(set_tid) did not reclaim it)", restored, pid)
	}
	if !alive(restored) {
		t.Fatalf("restored pid %d is not alive", restored)
	}

	// And it is genuinely the same program, not a fresh one.
	if cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(restored) + "/cmdline"); err == nil {
		if !strings.Contains(string(bytes.ReplaceAll(cmdline, []byte{0}, []byte{' '})), "sleep") {
			t.Errorf("restored process is not the victim: cmdline %q", cmdline)
		}
	}
}

// transferImage moves one image from source to dest over a real
// SharedListener on the goblin-ckpt ALPN.
func transferImage(t *testing.T, source, dest *migration.Store, uuid []byte, epoch uint64) {
	t.Helper()

	cert, err := transport.GenerateInsecureSelfSignedCert()
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}
	l, err := transport.NewSharedListener("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}, alwaysReady)
	if err != nil {
		t.Fatalf("NewSharedListener: %v", err)
	}
	defer func() { _ = l.Close() }()

	conns, err := l.Register(transport.ALPNGoblinCkpt)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go migration.NewServer(source, func(_, _ []byte) error { return nil }, nil).Serve(ctx, conns)

	certDER := cert.Certificate[0]
	conn, err := quic.DialAddr(ctx, l.Addr().String(), &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 || !bytes.Equal(rawCerts[0], certDER) {
				return errors.New("server certificate does not match the pinned test certificate")
			}
			return nil
		},
		NextProtos: []string{transport.ALPNGoblinCkpt},
	}, &quic.Config{EnableDatagrams: true})
	if err != nil {
		t.Fatalf("dial goblin-ckpt: %v", err)
	}
	defer func() { _ = conn.CloseWithError(0, "done") }()

	if _, err := migration.NewClient(dest).Fetch(ctx, conn, uuid, epoch, []byte("test-token")); err != nil {
		t.Fatalf("fetching image: %v", err)
	}
}
