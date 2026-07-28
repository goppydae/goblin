package supervisor

import (
	"os"
	"strings"
	"testing"

	"github.com/goppydae/goblin/core/migration"
)

// fakePidAgent is the minimum captureIdentity consumes: something that
// reports a pid. A CRIU-restored agent looks exactly like this to the
// supervisor - the pid is adopted rather than spawned, which is the
// whole reason GoAgent.Pid falls back to its adopted pid.
type fakePidAgent struct {
	pid     int
	running bool
}

func (f fakePidAgent) Pid() (int, bool) { return f.pid, f.running }

// The locator move IS the migration. A restored process must reach the
// tracker the same way a freshly started one does, or the heartbeat
// publishes a stale pid and signals keep routing to the old node.
func TestCaptureIdentityRecordsRestoredPid(t *testing.T) {
	n := &NodeRPC{tracker: newInstanceTracker()}

	// A real pid, so procsig.Identify can resolve a start epoch: the
	// epoch is the delivery guard, and a test using a fabricated pid
	// would exercise only the fallback path.
	self := os.Getpid()
	n.captureIdentity("inst-1", fakePidAgent{pid: self, running: true})

	info, ok := n.tracker.Get("inst-1")
	if !ok {
		t.Fatal("captureIdentity recorded nothing")
	}
	if info.Pid != self {
		t.Errorf("tracked pid = %d, want %d", info.Pid, self)
	}
	if info.StartEpoch == 0 {
		t.Error("start epoch is zero; the signal delivery guard would fail closed for a live process")
	}
}

// A runner reporting no process must not be recorded. Publishing pid 0
// would make the heartbeat advertise a locator for something that is
// not there.
func TestCaptureIdentityIgnoresNotRunning(t *testing.T) {
	n := &NodeRPC{tracker: newInstanceTracker()}
	n.captureIdentity("inst-1", fakePidAgent{pid: 0, running: false})

	if _, ok := n.tracker.Get("inst-1"); ok {
		t.Error("a non-running agent was recorded in the tracker")
	}
}

// Migration RPCs must refuse rather than invent a directory when the
// node was never configured with an image store.
func TestMigrationRPCsRefuseWithoutStore(t *testing.T) {
	n := &NodeRPC{tracker: newInstanceTracker()}

	var resp string
	err := n.CheckpointAgentInstance(&CheckpointAgentRequest{InstanceID: "inst-1"}, &resp)
	if err == nil {
		t.Error("checkpoint accepted with no agent manager or store")
	}

	err = n.RestoreAgentInstance(&RestoreAgentRequest{InstanceID: "inst-1"}, &resp)
	if err == nil {
		t.Error("restore accepted with no agent manager or store")
	}
}

// The store is keyed by {instance_uuid, epoch}: two epochs of one
// instance must not collide, or a retried migration would overwrite the
// image it might need to roll back to.
func TestImageStoreSeparatesEpochs(t *testing.T) {
	store := migration.NewStore(t.TempDir())
	uuid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	first, err := store.Create(uuid, 1)
	if err != nil {
		t.Fatalf("Create epoch 1: %v", err)
	}
	second, err := store.Create(uuid, 2)
	if err != nil {
		t.Fatalf("Create epoch 2: %v", err)
	}
	if first == second {
		t.Fatalf("epochs 1 and 2 share a directory: %s", first)
	}
	if !strings.HasPrefix(second, store.Root()) {
		t.Errorf("image dir %s escaped the store root %s", second, store.Root())
	}
}
