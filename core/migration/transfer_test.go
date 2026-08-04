// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package migration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/goppydae/goblin/core/migration"
	"github.com/goppydae/goblin/core/transport"
)

// A real listener on the real ALPN. Testing the transfer over an
// in-memory pipe would prove the framing and nothing about whether
// goblin-ckpt is actually routable, which is half the point of W2.6.
type harness struct {
	listener *transport.SharedListener
	certDER  []byte
	source   *migration.Store
	dest     *migration.Store
}

func newHarness(t *testing.T, authorize migration.Authorizer) *harness {
	t.Helper()

	cert, err := transport.GenerateInsecureSelfSignedCert()
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}
	l, err := transport.NewSharedListener("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}, alwaysReady)
	if err != nil {
		t.Fatalf("NewSharedListener: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	conns, err := l.Register(transport.ALPNGoblinCkpt)
	if err != nil {
		t.Fatalf("Register(%s): %v", transport.ALPNGoblinCkpt, err)
	}

	h := &harness{
		listener: l,
		certDER:  cert.Certificate[0],
		source:   migration.NewStore(t.TempDir()),
		dest:     migration.NewStore(t.TempDir()),
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go migration.NewServer(h.source, authorize, nil).Serve(ctx, conns)

	return h
}

func (h *harness) dial(t *testing.T) *quic.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, h.listener.Addr().String(), &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 || !bytes.Equal(rawCerts[0], h.certDER) {
				return errors.New("server certificate does not match the pinned test certificate")
			}
			return nil
		},
		NextProtos: []string{transport.ALPNGoblinCkpt},
	}, &quic.Config{EnableDatagrams: true})
	if err != nil {
		t.Fatalf("dial %s: %v", transport.ALPNGoblinCkpt, err)
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "test done") })
	return conn
}

var testUUID = []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

func allowAll(_, _ []byte) error { return nil }

// seedImage writes a fake checkpoint into the source store.
func seedImage(t *testing.T, s *migration.Store, epoch uint64, files map[string][]byte) {
	t.Helper()
	dir, err := s.Create(testUUID, epoch)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}
}

func fetch(t *testing.T, h *harness, epoch uint64) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return migration.NewClient(h.dest).Fetch(ctx, h.dial(t), testUUID, epoch, []byte("token"))
}

func TestFetchRoundTrip(t *testing.T) {
	want := map[string][]byte{
		"core-1.img":  bytes.Repeat([]byte{0xAB}, 4096),
		"pages-1.img": bytes.Repeat([]byte{0xCD}, 65536),
		"inventory":   []byte("small file"),
	}
	h := newHarness(t, allowAll)
	seedImage(t, h.source, 1, want)

	dir, err := fetch(t, h, 1)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading fetched %s: %v", name, err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("%s: fetched %d bytes, want %d (content differs)", name, len(got), len(content))
		}
	}
}

// A half-written image must be recoverable by refetching the same key.
// That idempotence is the reason the destination pulls and the reason
// images are keyed by {instance_uuid, epoch} at all.
func TestRefetchOverwritesPartialImage(t *testing.T) {
	want := map[string][]byte{"core-1.img": bytes.Repeat([]byte{0xAB}, 8192)}
	h := newHarness(t, allowAll)
	seedImage(t, h.source, 7, want)

	// Simulate a transfer that died partway: the directory exists and
	// holds a short, corrupt version of a real image file.
	destDir, err := h.dest.Create(testUUID, 7)
	if err != nil {
		t.Fatalf("Create dest: %v", err)
	}
	partial := filepath.Join(destDir, "core-1.img")
	if err := os.WriteFile(partial, []byte("truncated garbage"), 0o600); err != nil {
		t.Fatalf("seeding partial: %v", err)
	}

	dir, err := fetch(t, h, 7)
	if err != nil {
		t.Fatalf("refetch after partial: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "core-1.img"))
	if err != nil {
		t.Fatalf("reading refetched image: %v", err)
	}
	if !bytes.Equal(got, want["core-1.img"]) {
		t.Fatalf("refetch left %d bytes, want %d: partial image was not replaced", len(got), len(want["core-1.img"]))
	}
}

func TestFetchUnknownEpochIsNotFound(t *testing.T) {
	h := newHarness(t, allowAll)
	seedImage(t, h.source, 1, map[string][]byte{"core-1.img": []byte("x")})

	if _, err := fetch(t, h, 99); !errors.Is(err, migration.ErrNotFound) {
		t.Fatalf("want ErrNotFound for an unknown epoch, got %v", err)
	}
}

func TestFetchRefusedWhenAuthorizerDenies(t *testing.T) {
	denied := errors.New("no migrate right")
	h := newHarness(t, func(_, _ []byte) error { return denied })
	seedImage(t, h.source, 1, map[string][]byte{"core-1.img": []byte("secret memory")})

	_, err := fetch(t, h, 1)
	if !errors.Is(err, migration.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

// A nil Authorizer must refuse rather than default open: anyone who can
// negotiate the ALPN would otherwise read any instance's memory image.
func TestNilAuthorizerRefuses(t *testing.T) {
	h := newHarness(t, nil)
	seedImage(t, h.source, 1, map[string][]byte{"core-1.img": []byte("secret memory")})

	if _, err := fetch(t, h, 1); !errors.Is(err, migration.ErrUnauthorized) {
		t.Fatalf("nil authorizer must refuse; got %v", err)
	}
}

func TestFetchRejectsMalformedUUID(t *testing.T) {
	h := newHarness(t, allowAll)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := migration.NewClient(h.dest).Fetch(ctx, h.dial(t), []byte{1, 2, 3}, 1, nil)
	if !errors.Is(err, migration.ErrBadUUID) {
		t.Fatalf("want ErrBadUUID for a 3-byte uuid, got %v", err)
	}
}

// alwaysReady: these tests are about migration transfer, not about
// phase-aware admission, so the listener behaves as a fully booted node.
func alwaysReady() bool { return true }
