package supervisor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAdmitter fails AddVoter a configured number of times, then succeeds.
// leaderUntil bounds how many AddVoter attempts happen while still leader
// (0 = always leader).
type fakeAdmitter struct {
	mu          sync.Mutex
	failures    int
	calls       int
	leaderUntil int
}

func (f *fakeAdmitter) AddVoter(id, address string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failures {
		return errors.New("node is not the leader")
	}
	return nil
}

func (f *fakeAdmitter) IsLeader() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.leaderUntil == 0 || f.calls < f.leaderUntil
}

func (f *fakeAdmitter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestAddVoterWithRetry_SucceedsAfterTransientFailures(t *testing.T) {
	f := &fakeAdmitter{failures: 2}

	err := addVoterWithRetryOpts(context.Background(), f, "node-2", "127.0.0.1:29020",
		time.Millisecond, 4*time.Millisecond, time.Second)
	if err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	if got := f.callCount(); got != 3 {
		t.Fatalf("AddVoter called %d times, want 3", got)
	}
}

func TestAddVoterWithRetry_TimesOut(t *testing.T) {
	f := &fakeAdmitter{failures: 1 << 30}

	err := addVoterWithRetryOpts(context.Background(), f, "node-2", "127.0.0.1:29020",
		time.Millisecond, 2*time.Millisecond, 25*time.Millisecond)
	if err == nil {
		t.Fatal("want timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded in chain, got %v", err)
	}
	if got := f.callCount(); got < 2 {
		t.Fatalf("AddVoter called %d times, want at least 2 (it must retry)", got)
	}
}

func TestAddVoterWithRetry_StopsOnLeadershipLoss(t *testing.T) {
	f := &fakeAdmitter{failures: 1 << 30, leaderUntil: 2}

	err := addVoterWithRetryOpts(context.Background(), f, "node-2", "127.0.0.1:29020",
		time.Millisecond, 2*time.Millisecond, time.Second)
	if err == nil {
		t.Fatal("want leadership-loss error, got nil")
	}
	if !strings.Contains(err.Error(), "lost leadership") {
		t.Fatalf("want leadership-loss error, got %v", err)
	}
}

func TestAddVoterWithRetry_HonorsContextCancel(t *testing.T) {
	f := &fakeAdmitter{failures: 1 << 30}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := addVoterWithRetryOpts(ctx, f, "node-2", "127.0.0.1:29020",
		time.Millisecond, 2*time.Millisecond, time.Second)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled in chain, got %v", err)
	}
}
