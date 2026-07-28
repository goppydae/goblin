//go:build !linux

package shutdown

import "errors"

// Placeholder command values off Linux; the real SysCalls surface is
// Linux-only, so these only ever feed test fakes.
const (
	CmdPowerOff = 0x4321fedc
	CmdRestart  = 0x1234567
	CmdHalt     = 0xcdef0123

	mntDetach = 0x2
)

// ErrUnsupported: init teardown is a Linux obligation.
var ErrUnsupported = errors.New("shutdown: linux-only")

// SysCalls refuses off Linux.
type SysCalls struct{}

func (SysCalls) Sync()                                  {}
func (SysCalls) Unmount(target string, flags int) error { return ErrUnsupported }
func (SysCalls) Reboot(cmd int) error                   { return ErrUnsupported }
