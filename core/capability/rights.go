package capability

import (
	"fmt"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
)

// Orchestration rights occupy bits 8 and up. Bits 0-7 belong to the
// kernel (gapi core/crypto RightSignal*), which enforces them at the
// pidfd delivery boundary; the orchestrator never redefines them. The
// two namespaces are disjoint by construction, so a token minted for
// one class can never satisfy a requirement from the other.
const (
	RightAgentRegister uint64 = 1 << 8
	RightAgentScale    uint64 = 1 << 9
	RightAgentDelete   uint64 = 1 << 10
	RightNodeDrain     uint64 = 1 << 11
	RightJobSubmit     uint64 = 1 << 12
	RightJobMigrate    uint64 = 1 << 13
	RightEventPublish  uint64 = 1 << 14
)

// Verb names a mutating cluster operation. Read verbs have no entry:
// they are not grantable because they are not gated.
type Verb string

const (
	VerbAgentRegister Verb = "agent.register"
	VerbAgentScale    Verb = "agent.scale"
	VerbAgentDelete   Verb = "agent.delete"
	VerbNodeDrain     Verb = "node.drain"
	VerbJobSubmit     Verb = "job.submit"
	VerbJobMigrate    Verb = "job.migrate"
	VerbEventPublish  Verb = "event.publish"
)

// verbRights is the whole grantable surface. A verb absent from this
// table cannot be authorized - adding a mutating RPC without adding it
// here fails closed rather than running ungated.
var verbRights = map[Verb]uint64{
	VerbAgentRegister: RightAgentRegister,
	VerbAgentScale:    RightAgentScale,
	VerbAgentDelete:   RightAgentDelete,
	VerbNodeDrain:     RightNodeDrain,
	VerbJobSubmit:     RightJobSubmit,
	VerbJobMigrate:    RightJobMigrate,
	VerbEventPublish:  RightEventPublish,
}

// RightForVerb maps a mutating verb to the single right that authorizes
// it, mirroring the kernel's RightForSignal. Unknown verbs fail closed
// with the kernel's rights error, so callers branch on one typed error
// for both halves of the scheme.
func RightForVerb(v Verb) (uint64, error) {
	right, ok := verbRights[v]
	if !ok {
		return 0, fmt.Errorf("%w: verb %q is not grantable", gapicrypto.ErrTokenRights, v)
	}
	return right, nil
}
