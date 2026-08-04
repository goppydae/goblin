// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package migration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/quic-go/quic-go"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// Authorizer decides whether a fetch may proceed. It is a hook rather
// than a hardcoded check because the capability rights bitmap is
// orchestration policy and lives above this package; the transfer only
// needs a yes or no.
//
// A nil Authorizer refuses every fetch. Defaulting open would mean any
// peer that negotiated the ALPN could read any instance's memory image.
type Authorizer func(token, instanceUUID []byte) error

// Server answers checkpoint fetches from this node's image store.
//
// It is the SOURCE side of a migration. It never initiates: the
// destination pulls, so the node being torn down carries no retry
// responsibility.
type Server struct {
	store     *Store
	authorize Authorizer
	log       *slog.Logger
}

// NewServer builds a fetch server over store.
func NewServer(store *Store, authorize Authorizer, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{store: store, authorize: authorize, log: log}
}

// Serve consumes connections accepted for the goblin-ckpt ALPN until
// ctx is done. It returns only when the channel closes or ctx ends;
// callers run it in its own goroutine.
func (s *Server) Serve(ctx context.Context, conns <-chan *quic.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case conn, ok := <-conns:
			if !ok {
				return
			}
			go s.serveConn(ctx, conn)
		}
	}
}

func (s *Server) serveConn(ctx context.Context, conn *quic.Conn) {
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return // peer closed, or ctx ended; nothing actionable
		}
		go s.serveStream(ctx, stream)
	}
}

func (s *Server) serveStream(ctx context.Context, stream *quic.Stream) {
	defer func() { _ = stream.Close() }()

	var req goblinv1.CheckpointFetchRequest
	if err := readMessage(stream, &req); err != nil {
		s.log.LogAttrs(ctx, slog.LevelWarn, "checkpoint fetch: unreadable request",
			slog.String("error", err.Error()))
		return
	}

	status, files := s.evaluate(ctx, &req)
	resp := &goblinv1.CheckpointFetchResponse{Status: status}
	for _, f := range files {
		resp.Files = append(resp.Files, &goblinv1.CheckpointFile{Name: f.Name, Size: f.Size})
	}
	if err := writeMessage(stream, resp); err != nil {
		s.log.LogAttrs(ctx, slog.LevelWarn, "checkpoint fetch: writing response header",
			slog.String("error", err.Error()))
		return
	}
	if status != goblinv1.CheckpointFetchStatus_CHECKPOINT_FETCH_STATUS_OK {
		return
	}

	dir, err := s.store.Dir(req.GetInstanceUuid(), req.GetCheckpointEpoch())
	if err != nil {
		return // already validated in evaluate; nothing left to say
	}
	if err := s.writeFiles(stream, dir, files); err != nil {
		// The header promised these bytes and we cannot deliver them.
		// Reset rather than closing cleanly, so the destination sees a
		// failure instead of a short image that decodes as complete.
		stream.CancelWrite(codeTransferFailed)
		s.log.LogAttrs(ctx, slog.LevelWarn, "checkpoint fetch: sending image",
			slog.String("error", err.Error()))
	}
}

// codeTransferFailed marks a transfer the source could not finish. The
// destination must treat a reset mid-image as truncation, never as a
// complete transfer.
const codeTransferFailed quic.StreamErrorCode = 0x200

// evaluate authorizes the request and resolves its manifest, returning
// the status to report. It never returns a manifest alongside a
// non-OK status.
func (s *Server) evaluate(ctx context.Context, req *goblinv1.CheckpointFetchRequest) (goblinv1.CheckpointFetchStatus, []FileInfo) {
	if s.authorize == nil {
		s.log.LogAttrs(ctx, slog.LevelError, "checkpoint fetch: no authorizer configured; refusing")
		return goblinv1.CheckpointFetchStatus_CHECKPOINT_FETCH_STATUS_UNAUTHORIZED, nil
	}
	if err := s.authorize(req.GetCapabilityToken(), req.GetInstanceUuid()); err != nil {
		s.log.LogAttrs(ctx, slog.LevelWarn, "checkpoint fetch: refused",
			slog.String("error", err.Error()))
		return goblinv1.CheckpointFetchStatus_CHECKPOINT_FETCH_STATUS_UNAUTHORIZED, nil
	}

	files, err := s.store.Manifest(req.GetInstanceUuid(), req.GetCheckpointEpoch())
	switch {
	case err == nil:
		return goblinv1.CheckpointFetchStatus_CHECKPOINT_FETCH_STATUS_OK, files
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrBadUUID):
		return goblinv1.CheckpointFetchStatus_CHECKPOINT_FETCH_STATUS_NOT_FOUND, nil
	default:
		s.log.LogAttrs(ctx, slog.LevelError, "checkpoint fetch: reading manifest",
			slog.String("error", err.Error()))
		return goblinv1.CheckpointFetchStatus_CHECKPOINT_FETCH_STATUS_INTERNAL, nil
	}
}

// writeFiles streams each manifest file's contents in manifest order.
//
// Exactly the declared byte count is sent per file: if a file changed
// size since the manifest was taken, the transfer fails here rather
// than shifting every subsequent file's framing.
func (s *Server) writeFiles(w io.Writer, dir string, files []FileInfo) error {
	for _, f := range files {
		want, err := declaredSize(f.Size)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, f.Name)
		src, err := os.Open(path) //nolint:gosec // name comes from our own directory listing
		if err != nil {
			return err
		}
		n, err := io.Copy(w, io.LimitReader(src, want))
		_ = src.Close()
		if err != nil {
			return err
		}
		if n != want {
			return ErrTruncated
		}
	}
	return nil
}
