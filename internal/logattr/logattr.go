// Package logattr holds goblin's typed log attribute constructors: each
// pins one key to one value type, so a dropped value, swapped pair, or
// wrong type is a compile error instead of a quietly wrong JSON field
// (go-manifesto section 8). Add new keys here, never inline at call
// sites. Key vocabulary is shared by convention with gapi's
// internal/logattr - same concept, same key string (agent_id, topic,
// err) - so cross-repo log queries line up.
package logattr

import (
	"log/slog"
	"strings"
	"time"
)

func NodeID(s string) slog.Attr     { return slog.String("node_id", s) }
func JobID(s string) slog.Attr      { return slog.String("job_id", s) }
func SpecID(s string) slog.Attr     { return slog.String("spec_id", s) }
func Reason(s string) slog.Attr     { return slog.String("reason", s) }
func Signum(n int) slog.Attr        { return slog.Int("signum", n) }
func InstanceID(s string) slog.Attr { return slog.String("instance_id", s) }
func AgentID(s string) slog.Attr    { return slog.String("agent_id", s) }
func Topic(s string) slog.Attr      { return slog.String("topic", s) }
func Addr(s string) slog.Attr       { return slog.String("addr", s) }
func Method(s string) slog.Attr     { return slog.String("method", s) }
func PanicValue(s string) slog.Attr { return slog.String("panic_value", s) }
func Member(s string) slog.Attr     { return slog.String("member", s) }
func Status(s string) slog.Attr     { return slog.String("status", s) }
func Source(s string) slog.Attr     { return slog.String("source", s) }
func Path(s string) slog.Attr       { return slog.String("path", s) }
func Leader(b bool) slog.Attr       { return slog.Bool("leader", b) }
func Count(n int) slog.Attr         { return slog.Int("count", n) }
func Scope(s string) slog.Attr      { return slog.String("scope", s) }
func Protocol(s string) slog.Attr   { return slog.String("protocol", s) }
func Event(s string) slog.Attr      { return slog.String("event", s) }
func Namespace(s string) slog.Attr  { return slog.String("namespace", s) }
func Key(s string) slog.Attr        { return slog.String("key", s) }
func From(s string) slog.Attr       { return slog.String("from", s) }
func To(s string) slog.Attr         { return slog.String("to", s) }
func Message(s string) slog.Attr    { return slog.String("message", s) }
func Type(s string) slog.Attr       { return slog.String("type", s) }
func Response(s string) slog.Attr   { return slog.String("response", s) }
func Bytes(n int) slog.Attr         { return slog.Int("bytes", n) }
func Attempt(n int) slog.Attr       { return slog.Int("attempt", n) }
func StreamType(n int) slog.Attr    { return slog.Int("stream_type", n) }
func CPUCores(n int) slog.Attr      { return slog.Int("cpu_cores", n) }
func MemoryMB(n uint64) slog.Attr   { return slog.Uint64("memory_mb", n) }

func Backoff(d time.Duration) slog.Attr { return slog.Duration("backoff", d) }

// Tier names a supervisor loop tier ("pre-userspace", "run") in
// shutdown records.
func Tier(s string) slog.Attr { return slog.String("tier", s) }

// Loops carries the names of supervisor loops that outlived their
// shutdown grace. Joined rather than slog.Any so the field is a plain
// string in every handler, and pre-sorted by the caller so the value is
// stable across runs.
func Loops(names []string) slog.Attr { return slog.String("loops", strings.Join(names, ",")) }

// Err pins the conventional "err" key; nil-safe for termini that log
// unconditionally.
func Err(err error) slog.Attr {
	if err == nil {
		return slog.String("err", "")
	}
	return slog.String("err", err.Error())
}
