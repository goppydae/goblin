package migration

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/quic-go/quic-go"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// Client pulls checkpoint images into this node's image store.
//
// It is the DESTINATION side of a migration. Pull puts flow control and
// retry on the node that will run the instance next, rather than on the
// one being torn down, and the {instance_uuid, epoch} key makes a retry
// idempotent: refetching simply overwrites the same directory.
type Client struct {
	store *Store
}

// NewClient builds a fetch client writing into store.
func NewClient(store *Store) *Client { return &Client{store: store} }

// Fetch pulls one checkpoint over conn and returns the local directory
// it landed in.
//
// On any failure the directory is left in place rather than cleaned up:
// the key is still valid, a retry refetches into it, and deleting a
// partial image would discard the only evidence of what went wrong.
// Callers must treat a returned error as "this directory is not
// restorable", not as "nothing was written".
func (c *Client) Fetch(ctx context.Context, conn *quic.Conn, instanceUUID []byte, epoch uint64, token []byte) (string, error) {
	dir, err := c.store.Create(instanceUUID, epoch)
	if err != nil {
		return "", err
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return dir, fmt.Errorf("migration: opening fetch stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	req := &goblinv1.CheckpointFetchRequest{
		InstanceUuid:    instanceUUID,
		CheckpointEpoch: epoch,
		CapabilityToken: token,
	}
	if err := writeMessage(stream, req); err != nil {
		return dir, err
	}
	// The source answers only after the request is complete; without
	// this the peer waits for more request bytes and both sides stall.
	if err := stream.Close(); err != nil {
		return dir, fmt.Errorf("migration: closing request side: %w", err)
	}

	var resp goblinv1.CheckpointFetchResponse
	if err := readMessage(stream, &resp); err != nil {
		return dir, err
	}
	if resp.GetStatus() != goblinv1.CheckpointFetchStatus_CHECKPOINT_FETCH_STATUS_OK {
		return dir, statusError(resp.GetStatus())
	}
	if len(resp.GetFiles()) == 0 {
		return dir, fmt.Errorf("%w: source reported OK with no files", ErrManifest)
	}

	if err := c.receiveFiles(stream, dir, resp.GetFiles()); err != nil {
		return dir, err
	}
	return dir, nil
}

// statusError maps a non-OK status onto a typed local error, so callers
// branch on errors.Is rather than on the wire enum leaking upward.
func statusError(s goblinv1.CheckpointFetchStatus) error {
	switch s {
	case goblinv1.CheckpointFetchStatus_CHECKPOINT_FETCH_STATUS_NOT_FOUND:
		return ErrNotFound
	case goblinv1.CheckpointFetchStatus_CHECKPOINT_FETCH_STATUS_UNAUTHORIZED:
		return ErrUnauthorized
	case goblinv1.CheckpointFetchStatus_CHECKPOINT_FETCH_STATUS_INTERNAL:
		return ErrSourceInternal
	default:
		// UNSPECIFIED lands here: a response from a writer we do not
		// understand must not read as success.
		return fmt.Errorf("migration: unrecognized fetch status %v", s)
	}
}

// receiveFiles reads exactly the declared bytes for each manifest entry.
//
// Completion is decided by the manifest, never by the stream closing.
// That is the whole reason sizes are declared up front: a connection
// that drops mid-image would otherwise produce a short file that criu
// would later fail to parse, far from the cause.
func (c *Client) receiveFiles(r io.Reader, dir string, files []*goblinv1.CheckpointFile) error {
	for _, f := range files {
		// The declared size is untrusted input from the peer; bound it
		// before it becomes an io limit.
		want, err := declaredSize(f.GetSize())
		if err != nil {
			return err
		}
		path, err := safeJoin(dir, f.GetName())
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("migration: creating %s: %w", f.GetName(), err)
		}
		n, err := io.Copy(dst, io.LimitReader(r, want))
		closeErr := dst.Close()
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrTruncated, f.GetName(), err)
		}
		if closeErr != nil {
			return fmt.Errorf("migration: closing %s: %w", f.GetName(), closeErr)
		}
		if n != want {
			return fmt.Errorf("%w: %s: got %d of %d bytes", ErrTruncated, f.GetName(), n, want)
		}
	}
	return nil
}
