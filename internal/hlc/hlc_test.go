package hlc_test

import (
	"testing"

	"github.com/goppydae/goblin/internal/hlc"
)

func TestNow_MonotonicUnderFrozenPhysicalClock(t *testing.T) {
	c := hlc.NewWithSource("node-1", func() int64 { return 1000 })
	prev := c.Now()
	for i := 0; i < 100; i++ {
		next := c.Now()
		if !next.After(prev) {
			t.Fatalf("timestamp regressed at %d: %+v !after %+v", i, next, prev)
		}
		prev = next
	}
}

func TestNow_AdvancesWithPhysicalClock(t *testing.T) {
	phys := int64(1000)
	c := hlc.NewWithSource("node-1", func() int64 { return phys })
	first := c.Now()
	phys = 2000
	second := c.Now()
	if second.Wall != 2000 || second.Counter != 0 {
		t.Fatalf("advanced-clock timestamp = %+v, want wall 2000 counter 0", second)
	}
	if !second.After(first) {
		t.Fatalf("%+v should be after %+v", second, first)
	}
}

func TestAfter_TotalOrder(t *testing.T) {
	a := hlc.Timestamp{Wall: 1, Counter: 0, Node: "a"}
	b := hlc.Timestamp{Wall: 1, Counter: 1, Node: "a"}
	c := hlc.Timestamp{Wall: 2, Counter: 0, Node: "a"}
	d := hlc.Timestamp{Wall: 1, Counter: 0, Node: "b"}
	if !b.After(a) || !c.After(b) || !c.After(a) {
		t.Fatal("ordering violated")
	}
	// Same wall+counter, distinct nodes: deterministic tiebreak.
	if !d.After(a) || a.After(d) {
		t.Fatal("node tiebreak not a strict order")
	}
	if a.After(a) {
		t.Fatal("a timestamp is after itself")
	}
}

// A receiver that has observed a remote timestamp must mint later
// timestamps than it - the property last-writer-wins depends on.
func TestObserve_ReceiverMintsAfterRemote(t *testing.T) {
	c := hlc.NewWithSource("receiver", func() int64 { return 1000 })
	remote := hlc.Timestamp{Wall: 5000, Counter: 3, Node: "sender"}
	c.Observe(remote)
	local := c.Now()
	if !local.After(remote) {
		t.Fatalf("post-observe local %+v not after remote %+v", local, remote)
	}
}
