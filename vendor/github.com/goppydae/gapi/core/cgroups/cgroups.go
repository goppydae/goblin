// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cgroups

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goppydae/gapi/core/product"
	"github.com/goppydae/gapi/internal/safeio"
)

// ResourceSpec defines limits
type ResourceSpec struct {
	CPU    float64 // 0.5 = 50%
	Memory int64   // Bytes
}

// infraCgroup is the delegation root, named for the DAEMON that owns it
// so an operator reading /sys/fs/cgroup can attribute it: gapid-infra
// under gapid, goblind-infra under goblind (GAPI-DIV-061). A function
// rather than a const because the product is not known at compile time.
func infraCgroup() string { return product.Daemon() + "-infra" }

// AgentCgroup names the per-agent cgroup for id.
//
// Exported and used by all five call sites - create, cleanup in both
// agent types, and the two metrics collectors - because those were five
// independent fmt.Sprintf("gapid-%s") literals. Five spellings of one
// name is a cleanup that misses, or a stats read that finds nothing and
// reports zero; composing them here makes agreement structural rather
// than a thing to remember.
func AgentCgroup(id string) string { return product.Daemon() + "-" + id }

const (
	supervisorName = "supervisor"
	subtreeControl = "cgroup.subtree_control"
	cgroupProcs    = "cgroup.procs"
	cgroupMaxDesc  = "cgroup.max.descendants"
	cpuMax         = "cpu.max"
	memoryMax      = "memory.max"
)

var serviceRoot string

// Setup prepares the root cgroup for delegation.
// It moves the current process (the daemon) into '<daemon>-infra/supervisor'.
// Then it enables controllers (+cpu +memory) in '<daemon>-infra'.
func Setup() error {
	if os.Getenv(product.EnvKey("CGROUPS_DISABLE")) != "" {
		return fmt.Errorf("cgroups disabled by configuration")
	}

	root, err := detectRoot()
	if err != nil {
		return err
	}

	// 1. Create hierarchy
	infraPath := filepath.Join(root, infraCgroup())
	if err := os.MkdirAll(infraPath, 0750); err != nil {
		return fmt.Errorf("failed to create infra cgroup: %w", err)
	}

	supPath := filepath.Join(infraPath, supervisorName)
	if err := os.MkdirAll(supPath, 0750); err != nil {
		return fmt.Errorf("failed to create supervisor cgroup: %w", err)
	}

	// 2. Evacuate processes (Self) to supervisor leaf
	// This ensures 'root' (scope) and 'infra' are free of processes, allowing controller enablement.
	procsPath := filepath.Join(root, cgroupProcs)
	pids, err := readPids(procsPath)
	if err != nil {
		return err
	}

	supProcs := filepath.Join(supPath, cgroupProcs)
	for _, pid := range pids {
		if err := writePid(supProcs, pid); err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "[cgroups] warning: failed to move pid %d: %v\n", pid, err)
			}
		}
	}

	// 3. Enable controllers in ROOT (scope)
	if err := enableControllers(root, "+cpu +memory"); err != nil {
		fmt.Fprintf(os.Stderr, "[cgroups] warning: failed to enable controllers in root scope: %v\n", err)
	}

	// 4. Enable controllers in INFRA
	if err := enableControllers(infraPath, "+cpu +memory"); err != nil {
		return fmt.Errorf("failed to enable controllers in infra: %w", err)
	}

	serviceRoot = infraPath
	return nil
}

// Create creates a new cgroup and sets limits
func Create(name string, spec ResourceSpec) (string, error) {
	root := serviceRoot
	if root == "" {
		// Fallback if Setup not called (testing?)
		// But this is risky if we are in a leaf.
		var err error
		root, err = detectRoot()
		if err != nil {
			return "", err
		}
	}

	cgPath := filepath.Join(root, name)
	if err := os.MkdirAll(cgPath, 0750); err != nil {
		return "", err
	}

	// ... apply limits ...
	// Requires that 'root' has controllers enabled.
	// If Setup called, 'serviceRoot' (infra) has controllers enabled.

	// Apply Memory
	if spec.Memory > 0 {
		if err := os.WriteFile(filepath.Join(cgPath, memoryMax), []byte(fmt.Sprintf("%d", spec.Memory)), 0600); err != nil {
			return "", fmt.Errorf("failed to set memory limit: %w", err)
		}
		// Try to disable swap to ensure hard limit
		_ = os.WriteFile(filepath.Join(cgPath, "memory.swap.max"), []byte("0"), 0600)
	}

	// Apply CPU
	if spec.CPU > 0 {
		// quota = period * limit. Period usually 100000us (100ms)
		period := 100000
		quota := int(float64(period) * spec.CPU)
		val := fmt.Sprintf("%d %d", quota, period)
		if err := os.WriteFile(filepath.Join(cgPath, cpuMax), []byte(val), 0600); err != nil {
			return "", fmt.Errorf("failed to set cpu limit: %w", err)
		}
	}

	return cgPath, nil
}

// Add moves a PID into the cgroup
func Add(cgPath string, pid int) error {
	return writePid(filepath.Join(cgPath, cgroupProcs), pid)
}

// Cleanup removes the cgroup
func Cleanup(name string) error {
	root := serviceRoot
	if root == "" {
		var err error
		root, err = detectRoot()
		if err != nil {
			return err
		}
	}
	cgPath := filepath.Join(root, name)
	// We should ideally kill tasks inside first, but supervisor handles the process lifecycle.
	// If process is gone, cgroup should be empty.
	return os.Remove(cgPath)
}

// Helper functions

func enableControllers(path, controllers string) error {
	// Read what is available
	avail, err := readFirstLine(filepath.Join(path, "cgroup.controllers"))
	if err != nil {
		return err // ignore?
	}

	parts := strings.Fields(controllers)
	var toWrite []string
	for _, p := range parts {
		if strings.HasPrefix(p, "+") {
			k := strings.TrimPrefix(p, "+")
			if strings.Contains(avail, k) {
				toWrite = append(toWrite, p)
			}
		}
	}

	if len(toWrite) > 0 {
		scPath := filepath.Join(path, subtreeControl)
		return os.WriteFile(scPath, []byte(strings.Join(toWrite, " ")), 0600)
	}
	return nil
}

// detectRoot ... same ...
func detectRoot() (string, error) {
	// Read /proc/self/cgroup to search for 0::/path
	f, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return "", err
	}

	scanner := bufio.NewScanner(f)
	var suffix string
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[1] == "" { // cgroup2
			suffix = parts[2]
			break
		}
	}
	if cerr := f.Close(); cerr != nil {
		return "", cerr
	}

	if suffix == "" {
		return "", fmt.Errorf("cgroup2 not found")
	}

	return "/sys/fs/cgroup" + suffix, nil
}

// cgroupfsRoot confines every variable cgroup path this package opens.
const cgroupfsRoot = "/sys/fs/cgroup"

func readPids(path string) ([]int, error) {
	f, err := safeio.OpenUnder(cgroupfsRoot, path)
	if err != nil {
		return nil, err
	}

	var pids []int
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if pid, err := strconv.Atoi(scanner.Text()); err == nil {
			pids = append(pids, pid)
		}
	}
	if cerr := f.Close(); cerr != nil {
		return nil, cerr
	}
	return pids, nil
}

func writePid(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0600)
}

func readFirstLine(path string) (string, error) {
	b, err := safeio.ReadFileUnder(cgroupfsRoot, path)
	if err != nil {
		return "", err
	}
	return strings.Split(string(b), "\n")[0], nil
}

// Stats holds resource usage statistics
type Stats struct {
	CPUUsage    float64 // Percentage (0-100)
	MemoryUsage int64   // Bytes
}

// GetStats reads current resource usage from a cgroup
func GetStats(name string) (Stats, error) {
	root := serviceRoot
	if root == "" {
		var err error
		root, err = detectRoot()
		if err != nil {
			return Stats{}, err
		}
	}

	cgPath := filepath.Join(root, name)
	var stats Stats

	// Read memory usage
	memCurrent, err := readFirstLine(filepath.Join(cgPath, "memory.current"))
	if err == nil {
		if val, err := strconv.ParseInt(memCurrent, 10, 64); err == nil {
			stats.MemoryUsage = val
		}
	}

	// Read CPU usage (this is cumulative microseconds)
	// We'd need to track delta over time for accurate percentage
	// For now, just return 0 as a placeholder
	stats.CPUUsage = 0.0

	return stats, nil
}
