package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/goppydae/goblin/internal/logattr"
)

// voterAdmitter is the slice of the consensus engine that voter admission
// needs; *consensus.Consensus satisfies it.
type voterAdmitter interface {
	AddVoter(id, address string) error
	IsLeader() bool
}

const (
	voterRetryInitialBackoff = 100 * time.Millisecond
	voterRetryMaxBackoff     = 5 * time.Second
	voterRetryTimeout        = 2 * time.Minute
)

// ErrNotAdmitting reports that this node never held leadership during
// the admission window: it is not an error condition - every node sees
// every Serf join, and only the leader's attempt is supposed to land.
var ErrNotAdmitting = errors.New("node never held leadership during the admission window; the leader admits the voter")

// addVoterWithRetry admits a voter with exponential backoff until success,
// timeout, context cancellation, or leadership loss. A Serf join is not
// fire-and-forget: dropping AddVoter on a leader change leaves the node in
// gossip but out of consensus (review R6). A node that is not (yet) the
// leader WAITS rather than bailing: at boot, join events land milliseconds
// before the bootstrap node wins its first election, and gating admission
// on instantaneous leadership silently orphans every early joiner (the
// three-independent-rafts failure mode the 2b e2e exposed).
//
// Residual gap (recorded, not solved here): if leadership moves after the
// join event fired, no node retries on the new leader; periodic voter-set
// reconciliation against membership is the durable fix.
func addVoterWithRetry(ctx context.Context, c voterAdmitter, name, addr string) error {
	return addVoterWithRetryOpts(ctx, c, name, addr,
		voterRetryInitialBackoff, voterRetryMaxBackoff, voterRetryTimeout)
}

func addVoterWithRetryOpts(ctx context.Context, c voterAdmitter, name, addr string, initial, max, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	backoff := initial
	var lastErr error
	everLed := false
	for attempt := 1; ; attempt++ {
		switch {
		case c.IsLeader():
			everLed = true
			lastErr = c.AddVoter(name, addr)
			if lastErr == nil {
				slog.Default().LogAttrs(ctx, slog.LevelInfo, "raft voter added", logattr.Member(name), logattr.Addr(addr), logattr.Attempt(attempt))
				return nil
			}
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "add voter attempt failed, retrying", logattr.Member(name), logattr.Attempt(attempt), logattr.Err(lastErr), logattr.Backoff(backoff))
		case everLed:
			if lastErr != nil {
				return fmt.Errorf("lost leadership while adding voter %s (attempt %d, last error: %w); the current leader must admit it", name, attempt, lastErr)
			}
			return fmt.Errorf("lost leadership while adding voter %s; the current leader must admit it", name)
		}

		select {
		case <-ctx.Done():
			if !everLed {
				return ErrNotAdmitting
			}
			return fmt.Errorf("adding voter %s at %s: %w (last error: %w)", name, addr, ctx.Err(), lastErr)
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > max {
			backoff = max
		}
	}
}
