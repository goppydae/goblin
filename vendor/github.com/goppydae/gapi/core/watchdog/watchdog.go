// Package watchdog keeps a hardware (or software) watchdog timer fed
// (GAPI-DIV-027): if the supervisor hangs, the kicks stop and the
// device resets the machine. A deliberate stop performs the magic
// close ('V') so the device disarms instead of firing.
package watchdog

import (
	"context"
	"fmt"
	"os"
	"time"
)

// Watchdog is one open keepalive device.
type Watchdog struct {
	f        *os.File
	interval time.Duration
}

// Open opens the watchdog device (injectable path; /dev/watchdog on
// hardware). The kick interval must be well below the device timeout.
func Open(device string, interval time.Duration) (*Watchdog, error) {
	// O_APPEND is harmless on the character device (offsets are
	// ignored) and makes the byte stream observable on file-backed
	// test devices.
	f, err := os.OpenFile(device, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil, fmt.Errorf("open watchdog device %s: %w", device, err)
	}
	return &Watchdog{f: f, interval: interval}, nil
}

// Kick resets the device timer.
func (w *Watchdog) Kick() error {
	_, err := w.f.Write([]byte("1"))
	return err
}

// Run kicks on the interval until ctx ends, then magic-closes. The
// supervisor health loop owns ctx: a hung supervisor stops kicking,
// which is the point.
func (w *Watchdog) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = w.Close()
			return
		case <-ticker.C:
			// A failed kick has no in-band remedy; the device firing is
			// the escalation path.
			_ = w.Kick()
		}
	}
}

// Close disarms the device (magic close) and releases it.
func (w *Watchdog) Close() error {
	if _, err := w.f.Write([]byte("V")); err != nil {
		_ = w.f.Close()
		return err
	}
	return w.f.Close()
}
