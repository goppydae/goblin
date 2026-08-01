package agentmgr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goppydae/gapi/core/procsig"
	"github.com/goppydae/gapi/internal/logattr"
)

// Stop support for adopted (CRIU-restored) processes, GAPI-DIV-046.
//
// An adopted process is the one place in gapi that holds a bare PID with
// no os.Process handle: it is not a child of this program, so there is
// no exec.Cmd to signal and no cmd.Wait to observe. Delivery therefore
// goes through core/procsig, whose start-epoch guard is the only
// protection available against signalling a recycled PID, and exit is
// observed through /proc rather than through wait(2).
//
// The epoch is captured at adopt() time and stored (see checkpoint.go).
// Reading it here instead would compare the process to itself and could
// never fail, which would make the guard decorative.

const (
	// adoptedKillGrace bounds the wait after SIGKILL escalation. It
	// matches the child path in GoAgent.Stop/PythonAgent.Stop.
	adoptedKillGrace = 750 * time.Millisecond
	// adoptedPollInterval is how often /proc is sampled while waiting
	// for an adopted process to exit. There is no wait(2) to block on.
	adoptedPollInterval = 10 * time.Millisecond
)

// adoptedWatchInterval is how often the adopted exit watcher samples
// /proc. It is deliberately slower than adoptedPollInterval: that one
// bounds a wait inside Stop, this one runs for the adopted process's
// whole lifetime, so the cost is paid forever rather than for a few
// hundred milliseconds. A var rather than a const only so tests can
// shorten it; nothing in production writes it.
var adoptedWatchInterval = 250 * time.Millisecond

// stopAdopted terminates the process identified by (pid, epoch): SIGTERM
// under the epoch guard, then SIGKILL through the same guarded path if
// ctx expires first.
//
// Errors are data. ErrProcessGone is a successful stop - Stop's contract
// is that the process is not running, and it is not. ErrStaleEpoch is
// returned to the caller: the PID was recycled, so the supervised
// process exited unobserved and the recorded identity now names a
// stranger. There is deliberately no retry on ErrStaleEpoch (see
// procsig's package comment); re-resolution is the caller's job.
func stopAdopted(ctx context.Context, pid int, epoch uint64) error {
	if err := procsig.Signal(pid, epoch, syscall.SIGTERM); err != nil {
		if errors.Is(err, procsig.ErrProcessGone) {
			return nil
		}
		return err
	}

	if waitAdoptedExit(ctx, pid, epoch) {
		return nil
	}

	if err := procsig.Signal(pid, epoch, syscall.SIGKILL); err != nil &&
		!errors.Is(err, procsig.ErrProcessGone) {
		return err
	}
	grace, cancel := context.WithTimeout(context.Background(), adoptedKillGrace)
	defer cancel()
	waitAdoptedExit(grace, pid, epoch)
	return context.DeadlineExceeded
}

// adoptedStopStatus maps stopAdopted's result onto the lifecycle status
// the child path publishes for the same outcome. settled reports whether
// the adopted identity may be dropped: false means an unexpected failure
// the caller must see with its recorded state untouched.
func adoptedStopStatus(err error) (state, message string, settled bool) {
	switch {
	case err == nil:
		return "STOPPED", "process exited", true
	case errors.Is(err, context.DeadlineExceeded):
		return "STOPPED", "killed after timeout", true
	case errors.Is(err, procsig.ErrStaleEpoch):
		// The PID was recycled: our process is gone, and it did not go
		// because we stopped it. FAILED is what the child exit watcher
		// publishes for an exit the supervisor did not initiate. The
		// identity is dropped so nothing else (Pid, Checkpoint) can act
		// on a pid that now belongs to a stranger.
		return "FAILED", "adopted pid recycled before stop", true
	default:
		return "", "", false
	}
}

// waitAdoptedExit polls until the adopted process is no longer running
// or ctx expires, and reports whether it is gone.
func waitAdoptedExit(ctx context.Context, pid int, epoch uint64) bool {
	ticker := time.NewTicker(adoptedPollInterval)
	defer ticker.Stop()
	for {
		if adoptedHasExited(pid, epoch) {
			return true
		}
		select {
		case <-ctx.Done():
			return adoptedHasExited(pid, epoch)
		case <-ticker.C:
		}
	}
}

// adoptedHasExited reports whether the process identified by (pid,
// epoch) has stopped running.
//
// A vanished /proc entry is neither necessary nor sufficient:
//   - /proc/<pid>/stat SURVIVES the exit for as long as the process is a
//     zombie - exited, not yet waited on. In production an adopted
//     process reparents to the supervisor's subreaper and the reap loop
//     clears it, so the window is short but real; a loop that waits only
//     for the entry to disappear can spin against a corpse until its
//     deadline and then escalate to SIGKILL for nothing.
//   - the entry can also be reused. If the PID was recycled the start
//     epoch no longer matches, and our process is gone regardless of who
//     occupies the slot now.
//
// So exit is: no entry, an entry with a different start epoch, or an
// entry in state Z (zombie) or X (dead).
func adoptedHasExited(pid int, epoch uint64) bool {
	state, current, err := procStat(pid)
	if err != nil {
		// No entry means reaped and gone. Any other read failure is not
		// evidence of exit, so keep waiting: the caller's context bounds
		// the loop and the escalation is epoch-guarded either way.
		return errors.Is(err, os.ErrNotExist)
	}
	if current != epoch {
		return true
	}
	return state == 'Z' || state == 'X'
}

// procStat returns the state character and the start epoch from
// /proc/<pid>/stat in a single read - two reads could straddle an exit
// and pair a live state with a recycled epoch.
//
// The parse mirrors procsig.parseStartEpoch, which is unexported and
// returns only the epoch: fields are counted from after the last ')'
// because the comm field can contain spaces and parentheses. State is
// overall field 3 (index 0 after comm), starttime is field 22 (index 19).
func procStat(pid int) (state byte, epoch uint64, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	line := string(data)
	end := strings.LastIndexByte(line, ')')
	if end < 0 || end+2 >= len(line) {
		return 0, 0, fmt.Errorf("agentmgr: malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(line[end+2:])
	if len(fields) < 20 || fields[0] == "" {
		return 0, 0, fmt.Errorf("agentmgr: /proc/%d/stat has %d post-comm fields, want >= 20", pid, len(fields))
	}
	epoch, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("agentmgr: parsing starttime for pid %d: %w", pid, err)
	}
	return fields[0][0], epoch, nil
}

// Exit watching for adopted processes, GAPI-DIV-048.
//
// A spawned child's claim on a PID expires by itself: the exit watcher in
// Start returns from cmd.Wait and cleanupAfterExit clears cmd, so Pid()
// stops reporting it. An adopted process had no such path - adoptedPid
// was cleared only by Stop, so a process that exited on its own left the
// agent claiming a PID forever, and NotifyExited (which matches by PID
// equality alone, after the process is reaped and no epoch is left to
// compare) attributed the next reap of that recycled PID to this agent.
//
// The claim therefore has to expire on its own. adoptedWatch is the
// adopted equivalent of that child exit watcher: it polls the SAME
// guarded liveness check Stop uses - adoptedHasExited compares the stored
// start epoch while the process still exists, so a recycled PID reads as
// gone rather than as still-ours - and clears the claim when it is.

// adoptedWatch is a running exit watcher for one adopted process. It is
// cancellable and joinable: cancel stops the poll loop, done closes when
// the goroutine has returned. Never start one without a path that joins
// it (see stop).
type adoptedWatch struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// newAdoptedWatch starts the exit watcher for (pid, epoch) and calls
// onExit exactly once, from the watcher goroutine, when that process is
// gone. The goroutine returns on exactly two events: the exit is
// detected, or the watch is cancelled. onExit is expected to take the
// agent's mutex, so stop() must not be called with that mutex held.
func newAdoptedWatch(pid int, epoch uint64, onExit func()) *adoptedWatch {
	// Read the interval here, in the caller's goroutine, so a test that
	// shortens it is ordered against the start rather than racing it.
	interval := adoptedWatchInterval
	ctx, cancel := context.WithCancel(context.Background())
	w := &adoptedWatch{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if adoptedHasExited(pid, epoch) {
				onExit()
				return
			}
		}
	}()
	return w
}

// stop cancels the watch and waits for its goroutine to return, so no
// watcher outlives the agent state it writes to.
//
// It MUST NOT be called with the agent's mutex held: the goroutine may be
// blocked acquiring that mutex inside onExit, and waiting for it from
// under the lock would deadlock. Nil-safe (no watch to join) and
// idempotent (cancel and a closed channel both tolerate repeats). The
// watcher never joins itself - it has already decided to return by the
// time onExit runs, and closing done is the last thing it does.
func (w *adoptedWatch) stop() {
	if w == nil {
		return
	}
	w.cancel()
	<-w.done
}

// takeAdoptedWatchLocked detaches the exit watcher from the agent. Called
// with a.mu held; the caller must join the returned watch with stop()
// AFTER releasing the lock.
func (a *GoAgent) takeAdoptedWatchLocked() *adoptedWatch {
	w := a.adoptedWatch
	a.adoptedWatch = nil
	return w
}

// dropAdoptedLocked clears the adopted claim and detaches its watcher,
// which the caller joins after releasing a.mu. This is agent teardown
// (Reset): the claim must not survive the handles it names.
func (a *GoAgent) dropAdoptedLocked() *adoptedWatch {
	a.adoptedPid = 0
	a.adoptedEpoch = 0
	return a.takeAdoptedWatchLocked()
}

// adoptedExited runs when the watcher sees the adopted process go, and is
// the whole point of GAPI-DIV-048: it drops the PID claim so Pid() reports
// not-running and NotifyExited can no longer match this agent.
//
// (pid, epoch) is what the watcher was started on. It is re-checked
// against the current claim because Stop and a fresh adopt can both
// replace it while the watcher is in flight; stopping means Stop owns
// this exit and will publish its own status, exactly as the child exit
// watcher defers to Stop. FAILED mirrors what that watcher publishes for
// an exit the supervisor did not initiate.
func (a *GoAgent) adoptedExited(pid int, epoch uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping || a.adoptedPid != pid || a.adoptedEpoch != epoch {
		return
	}
	rid := a.nextRunID
	a.adoptedPid = 0
	a.adoptedEpoch = 0
	slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "adopted process exited unexpectedly",
		logattr.Module("agentmgr"), logattr.AgentID(a.id), slog.Int("pid", pid))
	a.publishStatusWithRunID("FAILED", "adopted process exited unexpectedly", rid)
	a.cleanupAfterExit()
}

// takeAdoptedWatchLocked mirrors GoAgent.takeAdoptedWatchLocked.
func (a *PythonAgent) takeAdoptedWatchLocked() *adoptedWatch {
	w := a.adoptedWatch
	a.adoptedWatch = nil
	return w
}

// dropAdoptedLocked mirrors GoAgent.dropAdoptedLocked.
func (a *PythonAgent) dropAdoptedLocked() *adoptedWatch {
	a.adoptedPid = 0
	a.adoptedEpoch = 0
	return a.takeAdoptedWatchLocked()
}

// adoptedExited mirrors GoAgent.adoptedExited; the two runners are a
// parity contract and an adopted process must behave identically under
// either.
func (a *PythonAgent) adoptedExited(pid int, epoch uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping || a.adoptedPid != pid || a.adoptedEpoch != epoch {
		return
	}
	rid := a.nextRunID
	a.adoptedPid = 0
	a.adoptedEpoch = 0
	slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "adopted process exited unexpectedly",
		logattr.Module("agentmgr"), logattr.AgentID(a.id), slog.Int("pid", pid))
	a.publishStatusWithRunID("FAILED", "adopted process exited unexpectedly", rid)
	a.cleanupAfterExit()
}

// stopAdoptedLocked stops GoAgent's adopted process. It is called with
// a.mu held and releases it, mirroring the lock discipline of the child
// path: the lock is dropped around the wait so status publication cannot
// self-deadlock. The observable lifecycle matches a spawned agent - same
// status transitions, same cleanup, same lazy-activation re-arm.
func (a *GoAgent) stopAdoptedLocked(ctx context.Context) error {
	pid, epoch, rid := a.adoptedPid, a.adoptedEpoch, a.nextRunID
	// Detach the exit watcher under the lock, join it after releasing it.
	// A watcher already in flight can only observe stopping=true, since
	// it is set in this same critical section and the watcher must take
	// a.mu to act - so it defers to Stop rather than publishing its own
	// exit. Joining then guarantees it is not still running when Stop
	// returns, and joining with the lock DOWN is what keeps that join
	// from deadlocking against a watcher blocked on a.mu.
	w := a.takeAdoptedWatchLocked()
	a.stopping = true
	a.mu.Unlock()
	w.stop()

	err := stopAdopted(ctx, pid, epoch)

	a.mu.Lock()
	defer a.mu.Unlock()
	state, message, settled := adoptedStopStatus(err)
	if !settled {
		a.stopping = false
		return err
	}
	a.adoptedPid = 0
	a.adoptedEpoch = 0
	a.publishStatusWithRunID(state, message, rid)
	a.cleanupAfterExit()
	if a.listenSpec != "" && a.trafficHandler != nil {
		_ = a.armLocked()
	}
	return err
}

// stopAdoptedLocked mirrors GoAgent.stopAdoptedLocked; the two runners
// are a parity contract and an adopted process must behave identically
// under either.
func (a *PythonAgent) stopAdoptedLocked(ctx context.Context) error {
	pid, epoch, rid := a.adoptedPid, a.adoptedEpoch, a.nextRunID
	w := a.takeAdoptedWatchLocked()
	a.stopping = true
	a.mu.Unlock()
	w.stop()

	err := stopAdopted(ctx, pid, epoch)

	a.mu.Lock()
	defer a.mu.Unlock()
	state, message, settled := adoptedStopStatus(err)
	if !settled {
		a.stopping = false
		return err
	}
	a.adoptedPid = 0
	a.adoptedEpoch = 0
	a.publishStatusWithRunID(state, message, rid)
	a.cleanupAfterExit()
	if a.listenSpec != "" && a.trafficHandler != nil {
		_ = a.armLocked()
	}
	return err
}
