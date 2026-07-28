package ident

import (
	"testing"
)

func TestNamespace_IsFixedAndVersion5(t *testing.T) {
	// The namespace is the root of every derived identity; if it ever
	// moves, every stable UUID in every cluster changes with it.
	// uuidv5(NameSpaceDNS, "goppydae")
	const want = "d3fb033f-b2bb-5d2a-ab9e-15b74660956c"
	if got := String(Namespace()); got != want {
		t.Fatalf("namespace = %s, want %s (changing it re-identifies every named resource)", got, want)
	}
}

func TestNewV5_IsDeterministicAcrossCalls(t *testing.T) {
	a := NewV5("spec/web")
	b := NewV5("spec/web")
	if String(a) != String(b) {
		t.Fatalf("same name yielded %s and %s", String(a), String(b))
	}
	if len(a) != 16 {
		t.Fatalf("got %d bytes, want 16", len(a))
	}
}

func TestNewV5_SeparatesNames(t *testing.T) {
	if String(NewV5("spec/web")) == String(NewV5("spec/api")) {
		t.Fatal("distinct names collided")
	}
	// Kind prefixes keep namespaces apart: a node and a spec may share
	// an operator-facing name without sharing an identity.
	if String(SpecUUID("web")) == String(NodeUUID("web")) {
		t.Fatal("spec and node with the same name collided")
	}
}

func TestNewV5_TagsVersion5(t *testing.T) {
	b := NewV5("spec/web")
	if v := b[6] >> 4; v != 5 {
		t.Errorf("version nibble = %d, want 5", v)
	}
	if variant := b[8] >> 6; variant != 0b10 {
		t.Errorf("variant bits = %b, want 10", variant)
	}
}

func TestSpecUUID_MatchesTheDocumentedName(t *testing.T) {
	// The derivation is a contract other tools reimplement, so the
	// exact name string is pinned, not just its stability.
	if String(SpecUUID("web")) != String(NewV5("spec/web")) {
		t.Error("SpecUUID must derive from \"spec/<name>\"")
	}
	if String(NodeUUID("n1")) != String(NewV5("node/n1")) {
		t.Error("NodeUUID must derive from \"node/<name>\"")
	}
}
