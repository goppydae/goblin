package logging

import (
	"fmt"
	"os"

	"github.com/goppydae/gapi/core/product"
)

// kmsg priorities (syslog levels used by /dev/kmsg).
const (
	KmsgErr  = 3
	KmsgWarn = 4
	KmsgInfo = 6
)

// kmsgLineLimit is the kernel's line cap; longer messages truncate.
const kmsgLineLimit = 976

// Kmsg is the Phase-0 progressive logger (GAPI-DIV-027): before the
// event bus and structured logging exist, boot narrates directly to
// /dev/kmsg. The path is injectable for tests; writes are best-effort
// by design - the logger must never be able to fail the boot it is
// narrating.
type Kmsg struct {
	path string
}

// NewKmsg opens nothing: the device is opened per line, matching the
// fire-and-forget Phase-0 usage.
func NewKmsg(path string) *Kmsg {
	if path == "" {
		path = "/dev/kmsg"
	}
	return &Kmsg{path: path}
}

// Log writes one prioritized line, truncated to the kmsg limit.
func (k *Kmsg) Log(priority int, msg string) {
	// The tag follows the DAEMON, not the kernel: an operator running
	// `goblind --pid1` reads "goblind:" in dmesg, not the name of a
	// library they were never told about (GAPI-DIV-061).
	line := fmt.Sprintf("<%d>%s: %s", priority, product.Daemon(), msg)
	if len(line) > kmsgLineLimit {
		line = line[:kmsgLineLimit]
	}
	f, err := os.OpenFile(k.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(f, line)
	_ = f.Close()
}
