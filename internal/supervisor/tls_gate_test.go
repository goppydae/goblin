package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRun_ProductionModeRequiresTLS verifies the fail-closed TLS posture:
// production mode with no cert/key material must refuse to start instead
// of silently falling back to an unverified ephemeral certificate.
func TestRun_ProductionModeRequiresTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sup := New(Config{
		NodeID:         "tls-gate-test",
		ProductionMode: true,
	})

	err := sup.Run(ctx)
	if err == nil {
		t.Fatal("Run() in production mode without TLS should fail closed")
	}
	if !strings.Contains(err.Error(), "production mode requires TLS") {
		t.Errorf("Run() error = %v, want the production TLS gate error", err)
	}
}
