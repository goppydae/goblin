package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRun_NetworkGateTimesOutLoudly verifies GOBLIN-DIV-011/R13: with the
// gate enabled and no network agent publishing agent.network.running, Run
// fails within the bound with a diagnostic error - it never hangs.
func TestRun_NetworkGateTimesOutLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("full node boot; skipped in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sup := New(Config{
		NodeID:             "gate-test",
		SerfAddr:           "127.0.0.1:39710",
		RaftAddr:           "127.0.0.1:39720",
		APIAddr:            "127.0.0.1:39700",
		RaftDir:            t.TempDir(),
		NetworkGateTimeout: 2 * time.Second,
	})

	err := sup.Run(ctx)
	if err == nil {
		t.Fatal("Run() should fail when the network gate expires")
	}
	if !strings.Contains(err.Error(), "network-readiness gate") {
		t.Errorf("Run() error = %v, want the network-readiness gate diagnosis", err)
	}
	if ctx.Err() != nil {
		t.Error("gate did not respect its own bound: outer test deadline hit first")
	}
}
