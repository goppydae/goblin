// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build pid1

// goblind's PID-1 smoke (GOBLIN-DIV-024): the embedded kernel's Phase 0
// runs before the cluster stack, and shutdown reverses through the
// distributed layers into the kernel teardown. Single-node --pid1 in a
// rootless podman container, asserting Phase 0 boot narration and a
// clean, ordered SIGTERM teardown as container init.
package main_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var rootfs string

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("podman"); err != nil {
		fmt.Println("SKIP: podman not in PATH; run mage testPid1 inside the dev shell")
		os.Exit(0)
	}

	dir, err := os.MkdirTemp("", "goblind-pid1-rootfs-")
	if err != nil {
		fmt.Println("mkdtemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	for _, d := range []string{"tmp", "proc", "dev", "sys", "run", "etc", "data"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			fmt.Println("mkdir:", err)
			os.Exit(1)
		}
	}
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "goblind"), "../../cmd/goblind")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Println("build goblind:", err)
		os.Exit(1)
	}
	rootfs = dir

	probe := exec.Command("podman", "run", "--rm",
		"-v", "/nix/store:/nix/store:ro",
		"--rootfs", dir+":O", "/goblind", "--help")
	if out, err := probe.CombinedOutput(); err != nil {
		fmt.Printf("SKIP: rootless podman cannot run containers here: %v\n%s\n", err, out)
		fmt.Println("Run mage testPid1 on the operator host to execute the goblind PID-1 smoke.")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestGoblindPid1Smoke(t *testing.T) {
	name := fmt.Sprintf("goblind-pid1-%d", os.Getpid())
	tmpVol := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "container.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}

	run := exec.Command("podman", "run", "--rm",
		"--name", name,
		"-v", "/nix/store:/nix/store:ro",
		"-v", tmpVol+":/tmp",
		"--env", "GOBLIN_KMSG_PATH=/tmp/kmsg",
		"--rootfs", rootfs+":O",
		"/goblind", "start", "--pid1", "--no-early-mounts",
		// Deliberately not goblind's default (31415): passing a
		// non-default port keeps this exercising the flag rather than
		// the default it would otherwise coincide with.
		"--id", "node1", "--listen-addr", "127.0.0.1:29000",
		"--data", "/data/raft", "--log-level", "debug",
	)
	run.Stdout, run.Stderr = logFile, logFile
	if err := run.Start(); err != nil {
		t.Fatalf("start container: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("podman", "rm", "-f", name).Run()
		_ = logFile.Close()
	})

	// Phase 0 runs and completes before the cluster stack (the kmsg
	// narration is the proof it ran first).
	deadline := time.Now().Add(30 * time.Second)
	for {
		kmsg, _ := os.ReadFile(filepath.Join(tmpVol, "kmsg"))
		if strings.Contains(string(kmsg), "phase 0 complete (goblind)") {
			break
		}
		if time.Now().After(deadline) {
			data, _ := os.ReadFile(logPath)
			t.Fatalf("goblind Phase 0 did not complete; kmsg=%q; log:\n%s", kmsg, tailStr(string(data), 3000))
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The single node self-elects: the cluster stack came up AFTER
	// Phase 0.
	waitLog(t, logPath, "raft consensus initialized", 30*time.Second)

	// SIGTERM to init: the pid1 handler catches it (kernel suppresses
	// the default for pid 1), the teardown reverses through the layers,
	// and the container exits cleanly as init.
	if out, err := exec.Command("podman", "kill", "--signal", "SIGTERM", name).CombinedOutput(); err != nil {
		t.Fatalf("podman kill: %v: %s", err, out)
	}
	done := make(chan error, 1)
	go func() { done <- run.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			data, _ := os.ReadFile(logPath)
			t.Fatalf("container exited non-zero after SIGTERM: %v; log:\n%s", err, tailStr(string(data), 3000))
		}
	case <-time.After(30 * time.Second):
		t.Fatal("container did not exit within 30s of SIGTERM")
	}

	data, _ := os.ReadFile(logPath)
	logText := string(data)
	for _, needle := range []string{"shutting down", "reboot unavailable, exiting as container init"} {
		if !strings.Contains(logText, needle) {
			t.Fatalf("teardown narration missing %q; log:\n%s", needle, tailStr(logText, 3000))
		}
	}
	t.Logf("goblind pid1 smoke: Phase 0 before cluster, ordered SIGTERM teardown as container init")
}

func waitLog(t *testing.T, path, needle string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), needle) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q not seen within %s; log:\n%s", needle, timeout, tailStr(string(data), 3000))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
