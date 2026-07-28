package capability

import (
	"errors"
	"testing"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
)

func TestRightForVerb_CoversEveryDeclaredVerb(t *testing.T) {
	for _, v := range []Verb{
		VerbAgentRegister, VerbAgentScale, VerbAgentDelete,
		VerbNodeDrain, VerbJobSubmit, VerbJobMigrate, VerbEventPublish,
	} {
		right, err := RightForVerb(v)
		if err != nil {
			t.Errorf("%s: %v", v, err)
			continue
		}
		if right == 0 {
			t.Errorf("%s: right is zero", v)
		}
	}
}

func TestRightForVerb_UnknownVerbFailsClosed(t *testing.T) {
	_, err := RightForVerb("agent.exfiltrate")
	if !errors.Is(err, gapicrypto.ErrTokenRights) {
		t.Fatalf("err = %v, want ErrTokenRights", err)
	}
}

func TestRights_AreDistinctAndDisjointFromKernelBits(t *testing.T) {
	// Bits 0-7 are the kernel's. If an orchestration right ever landed
	// there, a signal token would satisfy an orchestration requirement.
	const kernelMask = uint64(0xFF)
	seen := map[uint64]Verb{}
	for verb, right := range verbRights {
		if right&kernelMask != 0 {
			t.Errorf("%s uses a kernel-reserved bit (%#x)", verb, right)
		}
		if other, dup := seen[right]; dup {
			t.Errorf("%s and %s share bit %#x", verb, other, right)
		}
		seen[right] = verb
	}

	// The concrete overlap that matters: no signal right satisfies any
	// orchestration right, and vice versa.
	signalMask := gapicrypto.RightSignalTerm | gapicrypto.RightSignalKill | gapicrypto.RightSignalUser
	for verb, right := range verbRights {
		if right&signalMask != 0 {
			t.Errorf("%s overlaps a signal right", verb)
		}
	}
}
