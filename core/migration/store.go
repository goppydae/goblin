// Package migration moves CRIU checkpoint images between nodes and owns
// everything the kernel deliberately does not know about them: where
// images live on disk, how they are keyed, and how they travel
// (GOBLIN-DIV-018).
//
// The split is the silo's mechanism/policy line. gapi's core/checkpoint
// dumps and restores against a local directory and knows nothing about
// nodes or keys; this package decides which directory, names it by
// {instance_uuid, checkpoint_epoch} per research section 4.4, and
// serves it to whichever node is taking the instance over.
package migration

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// Typed failures. The destination branches on these to decide between
// refetching, re-electing a source, and giving up, so they must be
// distinguishable without reading prose.
var (
	// ErrNotFound: no image exists for this {uuid, epoch}.
	ErrNotFound = errors.New("migration: no image for this instance and epoch")
	// ErrBadUUID: an instance UUID that is not 16 bytes. Rejected at the
	// boundary rather than being hex-encoded into a nonsense path.
	ErrBadUUID = errors.New("migration: instance uuid must be 16 bytes")
	// ErrTargetNotReady: the destination cannot accept a migration and
	// said so before anything was done to the source (GOBLIN-DIV-048).
	// Distinct from every error below it, and the distinction is the
	// point: this one means the instance is still running where it was.
	ErrTargetNotReady = errors.New("migration: destination is not ready to accept")
	// ErrTruncated: the transfer ended before the manifest was
	// satisfied. Distinct from a read error: the image is incomplete
	// but the key is still valid, so a retry is the right response.
	ErrTruncated = errors.New("migration: image transfer truncated")
	// ErrManifest: the manifest itself is unusable (empty, or naming a
	// file that escapes the image directory).
	ErrManifest = errors.New("migration: unusable manifest")
	// ErrUnauthorized: the source refused the fetch. Distinct from
	// ErrNotFound so a caller cannot probe for image existence by
	// reading the error, and so retrying with a fresh token is
	// distinguishable from retrying against another node.
	ErrUnauthorized = errors.New("migration: fetch refused by source")
	// ErrSourceInternal: the source could not read its own image. The
	// key may be fine; another replica of the image may serve it.
	ErrSourceInternal = errors.New("migration: source failed to read its image")
)

// uuidLen is the wire width of an instance UUID (UUIDv7, RFC 9562).
const uuidLen = 16

// Store is the on-disk layout of checkpoint images for one node.
//
// Layout is <root>/<uuid-hex>/<epoch>/. The UUID comes first so every
// checkpoint of one instance sits together for audit, and the epoch is
// a directory rather than a filename suffix so criu can own the
// directory's contents without this package knowing what it writes.
type Store struct {
	root string
}

// NewStore roots a store at dir.
func NewStore(dir string) *Store { return &Store{root: dir} }

// Root is the directory this store owns.
func (s *Store) Root() string { return s.root }

// Dir is the image directory for one checkpoint. It does not create
// anything; callers that intend to write use Create.
func (s *Store) Dir(instanceUUID []byte, epoch uint64) (string, error) {
	if len(instanceUUID) != uuidLen {
		return "", fmt.Errorf("%w: got %d bytes", ErrBadUUID, len(instanceUUID))
	}
	return filepath.Join(s.root, hex.EncodeToString(instanceUUID), strconv.FormatUint(epoch, 10)), nil
}

// Create makes the image directory for one checkpoint and returns it.
//
// Deliberately not idempotent about contents: it will happily hand back
// an existing directory, because a retried pull refetching into the
// same key is the intended behaviour and is what makes {uuid, epoch}
// keying worth having.
func (s *Store) Create(instanceUUID []byte, epoch uint64) (string, error) {
	dir, err := s.Dir(instanceUUID, epoch)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("migration: creating image dir: %w", err)
	}
	return dir, nil
}

// Manifest lists the regular files of one checkpoint, sorted by name so
// that a source and a destination agree on transfer order without
// exchanging it.
//
// Subdirectories are not descended: criu writes a flat image directory,
// and silently flattening a nested tree would produce name collisions
// that only appear under load.
func (s *Store) Manifest(instanceUUID []byte, epoch uint64) ([]FileInfo, error) {
	dir, err := s.Dir(instanceUUID, epoch)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, dir)
		}
		return nil, fmt.Errorf("migration: reading image dir: %w", err)
	}

	files := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("migration: stat %s: %w", e.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		size, err := observedSize(info.Size())
		if err != nil {
			return nil, fmt.Errorf("migration: sizing %s: %w", e.Name(), err)
		}
		files = append(files, FileInfo{Name: e.Name(), Size: size})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: %s has no image files", ErrNotFound, dir)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// FileInfo is one image file in a manifest.
type FileInfo struct {
	Name string
	Size uint64
}

// maxImageFile bounds a single declared image file at 1 TiB. The
// manifest arrives from another node, so a declared size is untrusted
// input: without a cap, a size near 2^63 would make the int64 the
// io.LimitReader wants negative, and a negative limit reads nothing.
const maxImageFile = 1 << 40

// declaredSize narrows a manifest-declared size to the int64 the io
// helpers take, refusing anything that could not be a real image file.
func declaredSize(size uint64) (int64, error) {
	if size > maxImageFile {
		return 0, fmt.Errorf("%w: declared file size %d exceeds %d", ErrManifest, size, uint64(maxImageFile))
	}
	return int64(size), nil
}

// observedSize widens a byte count reported by the io helpers. Negative
// is impossible from a successful copy, but the conversion is checked
// rather than assumed so a future refactor cannot make it silently
// enormous.
func observedSize(n int64) (uint64, error) {
	if n < 0 {
		return 0, fmt.Errorf("migration: negative byte count %d", n)
	}
	return uint64(n), nil
}

// safeJoin resolves name inside dir, refusing anything that escapes it.
//
// The manifest arrives over the network from another node. Even between
// nodes that trust each other, a name like "../../etc/shadow" must not
// be writable, so the check lives at the boundary rather than relying
// on the peer being well behaved.
func safeJoin(dir, name string) (string, error) {
	if name == "" || filepath.Base(name) != name {
		return "", fmt.Errorf("%w: file name %q is not a bare name", ErrManifest, name)
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("%w: file name %q", ErrManifest, name)
	}
	return filepath.Join(dir, name), nil
}
