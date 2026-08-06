// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package agentmgr

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"google.golang.org/protobuf/encoding/protodelim"

	"github.com/goppydae/gapi/internal/logattr"
	gapiv1 "github.com/goppydae/gapi/pkg/proto"
)

// controlSchemaVersion is the frame contract this supervisor knows.
//
// Checked rather than assumed: a version field no consumer reads
// documents nothing and prevents nothing, which is the standard
// AgentDescriptor's schema_version is already held to.
const controlSchemaVersion = 1

// controlPipe is the supervisor's end of an agent's control channel.
//
// The agent gets the write end as an inherited descriptor and the
// supervisor keeps the read end. Both are closed by the supervisor: the
// write end immediately after exec, or the reader never sees EOF when
// the child dies and the reading goroutine leaks for the daemon's life.
type controlPipe struct {
	r *os.File
	w *os.File
}

func newControlPipe() (*controlPipe, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create control pipe: %w", err)
	}
	return &controlPipe{r: r, w: w}, nil
}

// statusPublisher is what a control reader needs from an agent, and all
// it needs: the two publications an agent-originated frame can produce,
// plus the record the exit watcher reads.
//
// An interface rather than a concrete agent because GoAgent and
// PythonAgent differ in nothing that matters here - the frames are
// identical, which is the parity decision 37 buys by construction
// instead of by test.
type statusPublisher interface {
	publishStatusWithRunID(state, message, runID string)
	publishHeartbeat(hb *gapiv1.Heartbeat)
	// noteAnnouncedState records the last state the AGENT announced, so
	// the exit watcher can tell an orderly finish from an unowned death.
	// Neither component could answer that alone: this is the seam.
	noteAnnouncedState(state, runID string)
	// noteFrameSeen records that the child has spoken at all, which is
	// what tells a silent agent from a slow one when the start deadline
	// expires (GAPI-DIV-104). Separate from noteAnnouncedState because
	// the question is different: a heartbeat is speech but announces no
	// state, and a status frame this build refuses is still evidence
	// that the descriptor was opened and written to.
	noteFrameSeen()
}

// maxBadControlFrames bounds what one broken agent can cost.
//
// MEASURED before this existed: 1 MiB of 0x00 on the control descriptor
// produced 131 MB of ERROR log in 0.67s - a 125x amplification driven by
// a process readControl's own comment calls untrusted. Every zero byte
// decodes as a zero-length frame, fails the schema check, and logs.
//
// A budget rather than a rate limit: an agent writing frames this build
// cannot read is not going to start making sense, and continuing to read
// it buys nothing. The channel closes, the agent keeps its stdout logs,
// and the exit watcher still owns the process.
const maxBadControlFrames = 32

// terminalAnnouncedStates are the states an agent announcing one has
// classified its own exit with. The supervisor does not then re-classify
// it from the outside.
var terminalAnnouncedStates = map[string]bool{
	"STOPPED":   true,
	"FAILED":    true,
	"COMPLETED": true,
}

// readControl consumes an agent's typed lifecycle frames until EOF.
//
// GAPI-DIV-099: this REPLACES reading state off the child's stdout. The
// supervisor used to scan stdout for JSON and switch on an "event" key,
// which meant one stream carried both a protocol and arbitrary program
// output and which one a line WAS depended on whether it happened to
// parse. Nothing about that guess survives here: a frame is a frame, and
// stdout is now only ever logs.
//
// THE TOPIC DERIVES FROM THE ARM, which is GAPI-DIV-087's clause with
// its owner changed by decision 37 - the supervisor publishes now, so
// the supervisor is what chooses the topic.
// NO CONTEXT PARAMETER, deliberately. The reader ends when the pipe
// reaches EOF, which happens when the child exits and the supervisor's
// copy of the write end is closed - so its lifetime is the run's
// lifetime, exactly. The start context would be the wrong one: it bounds
// the START OPERATION and is done the moment Start returns
// (GAPI-DIV-028), which would cut the channel while the agent runs.
func readControl(r io.Reader, a statusPublisher, id string, log *slog.Logger) {
	br := bufio.NewReader(r)

	// bad counts frames this build could not act on. See
	// maxBadControlFrames: the budget is what stops an untrusted stream
	// from turning one byte into a kilobyte of log.
	bad := 0
	refuse := func(msg string) bool {
		bad++
		if bad > maxBadControlFrames {
			log.Error("closing agent control channel: too many unreadable frames",
				logattr.AgentID(id), slog.Int("budget", maxBadControlFrames))
			return false
		}
		log.Error(msg, logattr.AgentID(id))
		return true
	}

	for {
		var frame gapiv1.AgentControl
		if err := protodelim.UnmarshalFrom(br, &frame); err != nil {
			// EOF is the ordinary end of a run: the child exited and its
			// write end closed. ErrUnexpectedEOF is the same ending with
			// a frame half-written, which is what a SIGKILL mid-write
			// looks like - an ordinary kill, not a stream failure, and it
			// was reading as one. Anything else is a malformed stream and
			// is reported rather than swallowed: a control channel that
			// goes quiet for an unreadable reason is exactly the silence
			// GAPI-DIV-100 cost a day to.
			switch {
			case errors.Is(err, io.EOF):
			case errors.Is(err, io.ErrUnexpectedEOF):
				log.Info("agent control stream ended mid-frame",
					logattr.AgentID(id), logattr.Err(err))
			default:
				log.Error("agent control stream failed",
					logattr.AgentID(id), logattr.Err(err))
			}
			return
		}

		// SPEECH IS RECORDED BEFORE THE FRAME IS JUDGED (GAPI-DIV-104).
		// The question this answers is whether the child opened its
		// descriptor and wrote to it, and a frame this build refuses -
		// wrong schema version, unknown arm - answers it just as well as
		// one it acts on. Recording only acceptable frames would report
		// a version-skewed agent as SILENT, which is a different defect
		// with a different fix.
		a.noteFrameSeen()

		if frame.GetSchemaVersion() != controlSchemaVersion {
			if !refuse("refusing agent control frame of unknown schema version") {
				return
			}
			continue
		}

		switch ev := frame.GetEvent().(type) {
		case *gapiv1.AgentControl_Status:
			st := ev.Status
			if st.GetState() == "" {
				// An agent that announces a transition without naming the
				// state has announced nothing. Saying so beats publishing
				// a status the state machine will drop on arrival, which
				// is what the JSON path did for six of its eight events.
				if !refuse("agent status frame carries no state") {
					return
				}
				continue
			}
			// RECORDED BEFORE IT IS PUBLISHED. The exit watcher reads this
			// to tell a clean self-stop from an unowned death, and the
			// process may already be gone by the time the publish returns.
			a.noteAnnouncedState(st.GetState(), st.GetRunId())
			a.publishStatusWithRunID(st.GetState(), st.GetMessage(), st.GetRunId())

		case *gapiv1.AgentControl_Heartbeat:
			// The frame is FORWARDED. Decoding an agent's identity and
			// then publishing something it did not send is how the arm
			// came to carry a state the agent never announced.
			a.publishHeartbeat(ev.Heartbeat)

		default:
			// A frame whose arm this build does not know. The schema
			// version matched, so this is a producer bug rather than a
			// version skew, and it is worth a line either way.
			if !refuse("agent control frame carries no known event") {
				return
			}
		}
	}
}

// awaitControlDrain blocks until this run's control reader has seen EOF,
// or the grace expires.
//
// THE EXIT WATCHER MUST NOT CLASSIFY AN EXIT BEFORE THE CHANNEL IS
// DRAINED. The child's exit is what closes the channel, so a frame the
// agent wrote on its way out is still in the pipe at the instant Wait
// returns. Deciding then is a race between a pipe and a goroutine, and
// it is the STOPPED frame that loses it.
//
// Bounded, because EOF needs every copy of the write end closed: a child
// that forked a grandchild holding the descriptor would otherwise hang
// the watcher for as long as the grandchild lives. The expiry is
// reported, since a descriptor outliving its process is itself a finding.
func awaitControlDrain(done <-chan struct{}, id string, log *slog.Logger) {
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(controlDrainGrace):
		log.Warn("agent control channel still open after exit",
			logattr.AgentID(id), slog.Duration("waited", controlDrainGrace))
	}
}

// controlDrainGrace bounds that wait. EOF follows the child's last
// descriptor closing, which is ordinarily immediate.
const controlDrainGrace = 2 * time.Second

// The two runners' halves of the seam, kept in one file so that a change
// to either is a change made in sight of the other. The parity decision
// 37 buys is by construction; these four methods are where it would be
// lost if it were going to be.

func (a *GoAgent) noteAnnouncedState(state, runID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.announcedState, a.announcedRunID = state, runID
}

// noteFrameSeen records the first frame of this run and when it arrived.
// FIRST, not last: the interval that matters is exec to first speech,
// and a later frame overwriting it would measure the most recent
// heartbeat instead.
func (a *GoAgent) noteFrameSeen() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.spoke {
		a.spoke = true
		a.firstFrameAt = time.Now()
	}
}

// HasSpoken implements lifecycle.SpeechReporter.
func (a *GoAgent) HasSpoken() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.spoke
}

// FirstFrameLatency reports exec to first control frame for the current
// run, or 0 if the child has not spoken. It exists to be MEASURED: the
// start deadline is 10s by default while the test harness allows 120s,
// and nothing in the tree records which is right (GAPI-DIV-104).
func (a *GoAgent) FirstFrameLatency() time.Duration {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.spoke || a.spawnedAt.IsZero() {
		return 0
	}
	return a.firstFrameAt.Sub(a.spawnedAt)
}

// announcedOwnExit reports whether the agent classified this run's exit
// itself. Caller holds mu.
func (a *GoAgent) announcedOwnExitLocked(runID string) bool {
	return a.announcedRunID == runID && terminalAnnouncedStates[a.announcedState]
}

func (a *PythonAgent) noteAnnouncedState(state, runID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.announcedState, a.announcedRunID = state, runID
}

// noteFrameSeen is GoAgent's, for the other runner.
func (a *PythonAgent) noteFrameSeen() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.spoke {
		a.spoke = true
		a.firstFrameAt = time.Now()
	}
}

// HasSpoken implements lifecycle.SpeechReporter.
func (a *PythonAgent) HasSpoken() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.spoke
}

// FirstFrameLatency is GoAgent's, for the other runner. The two
// languages are exactly what the measurement needs to compare.
func (a *PythonAgent) FirstFrameLatency() time.Duration {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.spoke || a.spawnedAt.IsZero() {
		return 0
	}
	return a.firstFrameAt.Sub(a.spawnedAt)
}

// announcedOwnExitLocked is GoAgent's, for the other runner. Caller holds mu.
func (a *PythonAgent) announcedOwnExitLocked(runID string) bool {
	return a.announcedRunID == runID && terminalAnnouncedStates[a.announcedState]
}
