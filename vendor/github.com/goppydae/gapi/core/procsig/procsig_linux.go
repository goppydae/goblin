//go:build linux

package procsig

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// RequirePidfd reports whether this kernel can pin a process with
// pidfd_open, by opening a pidfd on this process and closing it again.
//
// It exists because gapi's signal safety is INHERITED rather than
// asserted (GAPI-DIV-016). os.Process signals through
// pidfd_send_signal against a handle os/exec bound at fork, which is a
// stronger guarantee than this package's epoch check - a handle cannot
// refer to a recycled PID at all. But if the kernel has no pidfd,
// os/exec silently falls back to a raw kill by PID and nothing
// anywhere notices. A supervisor that signals the wrong process
// because the kernel was older than it assumed should refuse to boot,
// not discover it during a stop.
//
// Probing self is deliberate: it needs no target to exist and no
// privilege beyond signalling ourselves, so a false negative cannot
// come from the target rather than the kernel.
func RequirePidfd() error {
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return fmt.Errorf("%w: pidfd_open(self): %w", ErrPidfdUnsupported, err)
	}
	if cerr := unix.Close(fd); cerr != nil {
		return fmt.Errorf("procsig: closing probe pidfd: %w", cerr)
	}
	return nil
}

// StartEpoch returns the process's start time in clock ticks since
// boot (/proc/<pid>/stat field 22). Recorded at spawn, it uniquely
// identifies a PID incarnation on one boot of one node.
func StartEpoch(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("%w: pid %d", ErrProcessGone, pid)
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("%w: pid %d", ErrProcessGone, pid)
		}
		return 0, fmt.Errorf("reading stat for pid %d: %w", pid, err)
	}
	return parseStartEpoch(string(data))
}

// parseStartEpoch extracts field 22 from /proc/<pid>/stat. The comm
// field (2) may contain spaces and parentheses, so fields are counted
// from AFTER the last ')': state is overall field 3, starttime overall
// field 22 - index 19 after the parenthesis.
func parseStartEpoch(stat string) (uint64, error) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 || end+2 >= len(stat) {
		return 0, fmt.Errorf("malformed stat line %q", stat)
	}
	fields := strings.Fields(stat[end+2:])
	if len(fields) < 20 {
		return 0, fmt.Errorf("stat line has %d post-comm fields, want >= 20", len(fields))
	}
	epoch, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing starttime %q: %w", fields[19], err)
	}
	return epoch, nil
}

// Identify captures a process's runtime locator: pid, start epoch, and
// pid-namespace inode (/proc/<pid>/ns/pid). Nodes call this after
// spawning an instance and publish it over gossip.
func Identify(pid int) (ProcessIdentity, error) {
	epoch, err := StartEpoch(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	var st unix.Stat_t
	if err := unix.Stat(fmt.Sprintf("/proc/%d/ns/pid", pid), &st); err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
			return ProcessIdentity{}, fmt.Errorf("%w: pid %d", ErrProcessGone, pid)
		}
		return ProcessIdentity{}, fmt.Errorf("stat pid namespace of %d: %w", pid, err)
	}
	return ProcessIdentity{Pid: pid, StartEpoch: epoch, PidNsInode: st.Ino}, nil
}

// Signal delivers sig to pid if and only if the process's start epoch
// matches. The check-pin-recheck order makes it race-free: the epoch
// is checked, the process is pinned with pidfd_open, and the epoch is
// re-checked while pinned - a PID recycled between the first check and
// the pin shows a different epoch on the recheck and is refused. The
// signal then goes to the pinned process via pidfd_send_signal, never
// to a raw PID.
func Signal(pid int, startEpoch uint64, sig syscall.Signal) error {
	current, err := StartEpoch(pid)
	if err != nil {
		return err
	}
	if current != startEpoch {
		return fmt.Errorf("%w: pid %d epoch %d, expected %d", ErrStaleEpoch, pid, current, startEpoch)
	}

	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("%w: pid %d", ErrProcessGone, pid)
		}
		return fmt.Errorf("pidfd_open pid %d: %w", pid, err)
	}
	defer func() { _ = unix.Close(fd) }()

	// Recheck while pinned: if this still matches, the pinned process
	// is the intended incarnation.
	current, err = StartEpoch(pid)
	if err != nil {
		return err
	}
	if current != startEpoch {
		return fmt.Errorf("%w: pid %d recycled during pin (epoch %d, expected %d)", ErrStaleEpoch, pid, current, startEpoch)
	}

	if err := unix.PidfdSendSignal(fd, sig, nil, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("%w: pid %d exited during delivery", ErrProcessGone, pid)
		}
		return fmt.Errorf("pidfd_send_signal pid %d sig %d: %w", pid, sig, err)
	}
	return nil
}
