package supervisor

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/goppydae/goblin/internal/logattr"
)

// defaultShutdownGrace bounds a join when the operator configured no
// ShutdownGrace. It matches the PID-1 path's own default so the two
// halves of teardown cannot drift apart; both read it through
// Supervisor.shutdownGrace rather than repeating the literal.
const defaultShutdownGrace = 10 * time.Second

// Loop tiers.
//
// tierPreUserspace holds the Phase 0 loops - the reaper and the
// watchdog - and they MUST outlive the local teardown rather than be
// joined with everything else. shutdown.SystemShutdown calls StopAll,
// which kills children who then need reaping, and the watchdog has to
// keep petting until reboot(2) actually fires or the machine resets out
// from under its own shutdown. Joining them alongside the cluster loops
// would break the teardown they exist to serve.
//
// tierRun holds everything Run starts from Phase 1 onward. It is joined
// BEFORE the deferred consensus, membership and listener shutdowns, so
// no loop touches a resource after it is torn down.
const (
	tierPreUserspace = iota
	tierRun
	numTiers
)

// loopGroup tracks the supervisor's long-lived goroutines so shutdown
// waits for them instead of racing them (GOBLIN-DIV-038).
//
// It is owned by the Supervisor rather than by any phase because
// enablePid1 spawns into it before Phase 1 exists. That is also the
// constraint that keeps it usable by a later target model, which needs
// the group before any unit runs.
//
// It deliberately does NOT track the per-connection fan-out
// (quicServer.HandleConnection, handleQUICConn, handleQUICStream).
// That set is unbounded and short-lived by construction; bounding it
// needs per-connection cancellation plumbing that does not exist. The
// gap is recorded in GOBLIN-DIV-038's exit rather than papered over
// here.
type loopGroup struct {
	wg [numTiers]sync.WaitGroup

	// live names the loops that have not returned, so an expired grace
	// period reports WHICH loop hung rather than only that one did. The
	// count handles a name spawned more than once.
	mu   sync.Mutex
	live [numTiers]map[string]int
}

func newLoopGroup() *loopGroup {
	g := &loopGroup{}
	for i := range g.live {
		g.live[i] = make(map[string]int)
	}
	return g
}

// spawn runs fn as a tracked loop in tier. name should identify the
// loop rather than its call site: it is what join reports when the loop
// is still running at the end of the grace period.
func (g *loopGroup) spawn(tier int, name string, fn func()) {
	g.wg[tier].Add(1)
	g.mu.Lock()
	g.live[tier][name]++
	g.mu.Unlock()

	go func() {
		defer func() {
			g.mu.Lock()
			if g.live[tier][name] > 1 {
				g.live[tier][name]--
			} else {
				delete(g.live[tier], name)
			}
			g.mu.Unlock()
			g.wg[tier].Done()
		}()
		fn()
	}()
}

// join waits for tier's loops to return, bounded by d, and reports the
// names still running on expiry. A nil result means every loop stopped.
//
// Callers pass the configured grace; d <= 0 falls back to
// defaultShutdownGrace rather than degenerating into a non-blocking
// poll, because a zero timeout would report every loop as a straggler
// on an otherwise clean shutdown.
func (g *loopGroup) join(tier int, d time.Duration) []string {
	if d <= 0 {
		d = defaultShutdownGrace
	}

	done := make(chan struct{})
	go func() {
		g.wg[tier].Wait()
		close(done)
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-done:
		return nil
	case <-timer.C:
		return g.stragglers(tier)
	}
}

// stragglers returns the tier's live loop names, sorted so the report
// is deterministic across runs (go manifesto: deterministic by
// default).
func (g *loopGroup) stragglers(tier int) []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	names := make([]string, 0, len(g.live[tier]))
	for name := range g.live[tier] {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// joinTier joins one tier and logs whatever failed to stop.
//
// Background context on purpose: every caller runs after the run
// context is already cancelled, so threading it here would tag the
// shutdown record as cancelled and drop it on some handlers (Go
// manifesto section 11).
func (g *loopGroup) joinTier(tier int, label string, d time.Duration) {
	stragglers := g.join(tier, d)
	if len(stragglers) == 0 {
		return
	}
	slog.Default().LogAttrs(context.Background(), slog.LevelWarn,
		"shutdown grace expired with supervisor loops still running",
		logattr.Tier(label), logattr.Loops(stragglers), slog.Duration("grace", d))
}

// shutdownGrace is the single reading of the configured grace. Both
// halves of teardown - the loop joins here and the PID-1 local teardown
// - go through it so they cannot disagree about the bound.
func (s *Supervisor) shutdownGrace() time.Duration {
	if s.cfg.ShutdownGrace > 0 {
		return s.cfg.ShutdownGrace
	}
	return defaultShutdownGrace
}
