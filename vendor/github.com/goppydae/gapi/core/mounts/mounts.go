// Package mounts bootstraps the virtual filesystem hierarchy during
// Phase 0 of a PID-1 boot (GAPI-DIV-027), before cgroups or the event
// bus exist. Ordering is load-bearing: cgroups v2 assumes the
// hierarchy at /sys/fs/cgroup. Container environments where the OCI
// runtime owns the mounts skip the phase via --no-early-mounts; the
// executor is also idempotent over premounted targets.
package mounts

import "fmt"

// MountSpec is one early mount.
type MountSpec struct {
	Source  string
	Target  string
	FSType  string
	Flags   uintptr
	Options string
}

// EarlyMounts is the Phase-0 mount table (gapi-architecture.md).
var EarlyMounts = []MountSpec{
	{"devtmpfs", "/dev", "devtmpfs", flagNoSuid, ""},
	{"proc", "/proc", "proc", flagNoSuid | flagNoDev | flagNoExec, ""},
	{"sysfs", "/sys", "sysfs", flagNoSuid | flagNoDev | flagNoExec, ""},
	{"cgroup2", "/sys/fs/cgroup", "cgroup2", flagNoSuid | flagNoDev | flagNoExec | flagRelatime, ""},
	{"tmpfs", "/run", "tmpfs", flagNoSuid | flagNoDev, "mode=0755"},
	{"tmpfs", "/tmp", "tmpfs", flagNoSuid | flagNoDev, ""},
}

// Mounter is the syscall surface, injectable so ordering and
// idempotence are unit-testable unprivileged.
type Mounter interface {
	Mount(source, target, fstype string, flags uintptr, data string) error
	IsMounted(target string) (bool, error)
	MkdirAll(path string) error
}

// MountEarly executes specs in declared order, skipping targets that
// are already mounted and failing closed on the first error -
// continuing past a missing /proc corrupts everything after it.
func MountEarly(m Mounter, specs []MountSpec) error {
	for _, spec := range specs {
		mounted, err := m.IsMounted(spec.Target)
		if err != nil {
			return fmt.Errorf("checking %s: %w", spec.Target, err)
		}
		if mounted {
			continue
		}
		if err := m.MkdirAll(spec.Target); err != nil {
			return fmt.Errorf("creating %s: %w", spec.Target, err)
		}
		if err := m.Mount(spec.Source, spec.Target, spec.FSType, spec.Flags, spec.Options); err != nil {
			return fmt.Errorf("mounting %s (%s): %w", spec.Target, spec.FSType, err)
		}
	}
	return nil
}
