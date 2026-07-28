//go:build !linux

package procsig

import "syscall"

// StartEpoch is unavailable off Linux: there is no /proc start-time
// source, and the epoch guard is only sound with pidfd semantics.
func StartEpoch(pid int) (uint64, error) {
	return 0, ErrUnsupported
}

// Identify is unavailable off Linux: the locator fields come from
// /proc.
func Identify(pid int) (ProcessIdentity, error) {
	return ProcessIdentity{}, ErrUnsupported
}

// Signal refuses off Linux: no fallback by design - a kill() fallback
// reopens the PID-reuse race the epoch guard exists to close.
func Signal(pid int, startEpoch uint64, sig syscall.Signal) error {
	return ErrUnsupported
}
