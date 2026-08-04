// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package migration

import (
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

// Stream framing for the goblin-ckpt ALPN.
//
// Control messages are length-delimited protobuf; image bytes are then
// written raw, in manifest order, with the manifest's declared sizes as
// the only framing. Framing every file would cost nothing in
// correctness and something in throughput, and the sizes are already
// needed so the destination can tell a complete transfer from a
// connection that simply closed.

// maxControlMessage bounds a control frame. The manifest is the largest
// thing that travels this way and is a few hundred entries at most; the
// cap exists so a hostile or confused peer cannot make the receiver
// allocate arbitrarily before any of it has been validated.
const maxControlMessage = 1 << 20 // 1 MiB

// frameLen narrows a marshalled message length to the wire's uint32.
//
// The bound lives in its own function so the check and the conversion
// sit together where a reader - and the analyzer - can see that the
// narrowing cannot wrap. Writing the guard inline against len() does
// not work: staticcheck correctly rejects a negative test on len, and
// gosec does not connect a separate guard to the conversion.
func frameLen(n int) (uint32, error) {
	if n < 0 || n > maxControlMessage {
		return 0, fmt.Errorf("migration: control message %d bytes outside 0..%d", n, maxControlMessage)
	}
	return uint32(n), nil
}

func writeMessage(w io.Writer, m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return fmt.Errorf("migration: marshaling control message: %w", err)
	}
	n, err := frameLen(len(b))
	if err != nil {
		return err
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], n)
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("migration: writing frame header: %w", err)
	}
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("migration: writing control message: %w", err)
	}
	return nil
}

func readMessage(r io.Reader, m proto.Message) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return fmt.Errorf("migration: reading frame header: %w", err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxControlMessage {
		return fmt.Errorf("migration: control message %d bytes exceeds cap", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return fmt.Errorf("migration: reading control message: %w", err)
	}
	if err := proto.Unmarshal(b, m); err != nil {
		return fmt.Errorf("migration: decoding control message: %w", err)
	}
	return nil
}
