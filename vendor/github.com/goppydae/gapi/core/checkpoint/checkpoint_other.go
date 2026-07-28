//go:build !linux

package checkpoint

// Available refuses off Linux. There is deliberately no fallback: CRIU
// restore reclaims dumped PIDs through clone3(set_tid) in a fresh PID
// namespace, and nothing outside Linux offers that. A best-effort
// "restart it over there" would silently break the one invariant
// migration exists to preserve - that the instance UUID keeps pointing
// at the same running process (research section 4.4).
func Available() error { return ErrUnsupported }

// Dump refuses off Linux.
func Dump(pid int, dir string, opt Options) error {
	return &Error{Op: "dump", Pid: pid, Dir: dir, Err: ErrUnsupported}
}

// Restore refuses off Linux.
func Restore(dir string, opt Options) (int, error) {
	return 0, &Error{Op: "restore", Dir: dir, Err: ErrUnsupported}
}
