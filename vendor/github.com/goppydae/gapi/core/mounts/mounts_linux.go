//go:build linux

package mounts

import (
	"bufio"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Mount flags aliased from the platform so the spec table stays
// portable source.
const (
	flagNoSuid   = uintptr(unix.MS_NOSUID)
	flagNoDev    = uintptr(unix.MS_NODEV)
	flagNoExec   = uintptr(unix.MS_NOEXEC)
	flagRelatime = uintptr(unix.MS_RELATIME)
)

// SysMounter is the real mount(2) surface.
type SysMounter struct{}

func (SysMounter) Mount(source, target, fstype string, flags uintptr, data string) error {
	return unix.Mount(source, target, fstype, flags, data)
}

// IsMounted scans /proc/self/mountinfo for the target; when /proc is
// not yet mounted (the true cold-boot case) nothing is mounted, and
// the scan failing with not-exist reports exactly that.
func (SysMounter) IsMounted(target string) (bool, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// mountinfo field 5 (index 4) is the mount point.
		if len(fields) > 4 && fields[4] == target {
			return true, nil
		}
	}
	return false, sc.Err()
}

func (SysMounter) MkdirAll(path string) error {
	// Mountpoint permissions are shadowed by the mounted filesystem the
	// moment the mount lands; 0o750 on the underlying dir satisfies the
	// security gate without affecting the mounted view. If the mount
	// fails, boot fails closed anyway (MountEarly).
	return os.MkdirAll(path, 0o750)
}
