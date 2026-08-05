// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build linux

package checkpoint

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"

	criu "github.com/checkpoint-restore/go-criu/v7"
	"github.com/checkpoint-restore/go-criu/v7/rpc"
	"google.golang.org/protobuf/proto"
)

// Capability bit positions (linux/capability.h). CAP_CHECKPOINT_RESTORE
// was carved out of CAP_SYS_ADMIN in Linux 5.9 precisely so that
// checkpoint/restore need not run fully privileged (DDR-11).
const (
	capSysAdmin          = 21
	capCheckpointRestore = 40
)

// Available reports whether this process can checkpoint at all,
// returning a typed error naming the specific reason if not.
//
// Callers should assert this once, early - the orchestrator does it at
// admission - rather than discovering at migration time that a host was
// never able to participate.
func Available() error {
	if _, err := exec.LookPath("criu"); err != nil {
		return ErrNoCriu
	}
	capable, err := hasCheckpointCapability()
	if err != nil {
		return err
	}
	if !capable {
		return ErrNotCapable
	}
	return nil
}

// hasCheckpointCapability reads the effective capability set from
// /proc/self/status. Resolving criu on PATH is not enough: the binary
// being present says nothing about whether we may use it, and the
// failure mode without this check is a confusing mid-dump error.
func hasCheckpointCapability() (bool, error) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return false, fmt.Errorf("checkpoint: reading capabilities: %w", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		val, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		eff, err := strconv.ParseUint(strings.TrimSpace(val), 16, 64)
		if err != nil {
			return false, fmt.Errorf("checkpoint: parsing CapEff %q: %w", val, err)
		}
		return eff&(1<<capCheckpointRestore) != 0 || eff&(1<<capSysAdmin) != 0, nil
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("checkpoint: reading capabilities: %w", err)
	}
	return false, fmt.Errorf("checkpoint: no CapEff line in /proc/self/status")
}

// newClient builds a criu client bound to the binary resolved from
// PATH. It is resolved rather than hardcoded because the dev shell and
// the deployed system place criu at different store paths, and an
// absolute path baked in here would work in exactly one of them.
func newClient() (*criu.Criu, error) {
	path, err := exec.LookPath("criu")
	if err != nil {
		return nil, ErrNoCriu
	}
	c := criu.MakeCriu()
	c.SetCriuPath(path)
	return c, nil
}

// openImagesDir opens dir for use as criu's images directory. criu takes
// a directory FILE DESCRIPTOR, not a path, so the directory must stay
// open across the call.
func openImagesDir(dir string) (*os.File, error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrImagesDir, dir, err)
	}
	info, err := d.Stat()
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("%w: %s: %w", ErrImagesDir, dir, err)
	}
	if !info.IsDir() {
		_ = d.Close()
		return nil, fmt.Errorf("%w: %s is not a directory", ErrImagesDir, dir)
	}
	return d, nil
}

// imagesDirFd narrows the directory's descriptor to the int32 the CRIU
// RPC carries. Descriptors are small in practice, but a silently
// truncated one would aim criu at an unrelated open file, so the
// narrowing is checked rather than assumed.
func imagesDirFd(d *os.File) (int32, error) {
	fd := d.Fd()
	if fd > math.MaxInt32 {
		return 0, fmt.Errorf("%w: descriptor %d exceeds int32", ErrImagesDir, fd)
	}
	return int32(fd), nil
}

// pidToInt32 narrows a pid for the same RPC. kernel.pid_max can be
// raised to 2^22 (the module sets exactly that), which fits, but a
// negative or absurd pid is a caller bug worth naming here rather than
// letting criu fail obscurely.
func pidToInt32(pid int) (int32, error) {
	if pid <= 0 || pid > math.MaxInt32 {
		return 0, fmt.Errorf("checkpoint: pid %d out of range", pid)
	}
	return int32(pid), nil
}

func baseOpts(dirFd int32, opt Options) *rpc.CriuOpts {
	o := &rpc.CriuOpts{
		ImagesDirFd:    proto.Int32(dirFd),
		LeaveRunning:   proto.Bool(opt.LeaveRunning),
		ShellJob:       proto.Bool(opt.ShellJob),
		TcpEstablished: proto.Bool(opt.TCPEstablished),
		FileLocks:      proto.Bool(opt.FileLocks),
	}
	if opt.LogLevel != 0 {
		o.LogLevel = proto.Int32(opt.LogLevel)
	}
	if opt.LogFile != "" {
		o.LogFile = proto.String(opt.LogFile)
	}
	return o
}

// Dump writes a checkpoint of the process tree rooted at pid into dir.
//
// The process is STOPPED on success unless Options.LeaveRunning is set.
// That is the intended migration semantic: the image is the rollback
// artifact, so the source must not continue executing past the point
// the image captured.
func Dump(pid int, dir string, opt Options) error {
	// Image directory first, capability second: a bad directory is a
	// caller bug worth naming precisely even on a host that could never
	// have checkpointed anyway, and checking it first keeps the
	// validation path testable without privileges.
	d, err := openImagesDir(dir)
	if err != nil {
		return &Error{Op: "dump", Pid: pid, Dir: dir, Err: err}
	}
	defer func() { _ = d.Close() }()

	if err := Available(); err != nil {
		return &Error{Op: "dump", Pid: pid, Dir: dir, Err: err}
	}

	c, err := newClient()
	if err != nil {
		return &Error{Op: "dump", Pid: pid, Dir: dir, Err: err}
	}

	fd, err := imagesDirFd(d)
	if err != nil {
		return &Error{Op: "dump", Pid: pid, Dir: dir, Err: err}
	}
	subject, err := pidToInt32(pid)
	if err != nil {
		return &Error{Op: "dump", Pid: pid, Dir: dir, Err: err}
	}

	o := baseOpts(fd, opt)
	o.Pid = proto.Int32(subject)

	if err := c.Dump(o, criu.NoNotify{}); err != nil {
		return &Error{Op: "dump", Pid: pid, Dir: dir, Err: err}
	}
	return nil
}

// restoreNotify captures the PID criu reports for the restored tree.
//
// This is the only public route to that value: Criu.Restore returns
// bare error and discards CriuRestoreResp, so the PID arrives through a
// NOTIFY callback, which criu only emits when notify_scripts is set.
type restoreNotify struct {
	criu.NoNotify
	pid int32
}

func (n *restoreNotify) PostRestore(pid int32) error {
	n.pid = pid
	return nil
}

// Restore recreates a process tree from the checkpoint in dir and
// returns the PID of the restored root.
//
// Restore is expected to run inside a fresh PID namespace so that
// clone3(set_tid) can reclaim the dumped PIDs; in that case the
// returned PID equals the one that was dumped. The value is reported
// rather than assumed because the caller uses it to update a locator,
// and a locator built on an assumption is how ABA hazards start.
func Restore(dir string, opt Options) (int, error) {
	d, err := openImagesDir(dir)
	if err != nil {
		return 0, &Error{Op: "restore", Dir: dir, Err: err}
	}
	defer func() { _ = d.Close() }()

	if err := Available(); err != nil {
		return 0, &Error{Op: "restore", Dir: dir, Err: err}
	}

	c, err := newClient()
	if err != nil {
		return 0, &Error{Op: "restore", Dir: dir, Err: err}
	}

	fd, err := imagesDirFd(d)
	if err != nil {
		return 0, &Error{Op: "restore", Dir: dir, Err: err}
	}

	o := baseOpts(fd, opt)
	// Required for PostRestore to fire; without it criu completes the
	// restore and never reports the pid.
	o.NotifyScripts = proto.Bool(true)

	n := &restoreNotify{}
	if err := c.Restore(o, n); err != nil {
		return 0, &Error{Op: "restore", Dir: dir, Err: err}
	}
	if n.pid == 0 {
		return 0, &Error{Op: "restore", Dir: dir, Err: ErrNoRestoredPid}
	}
	return int(n.pid), nil
}
