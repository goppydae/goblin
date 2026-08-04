# Copyright (c) 2025 Steven Verhelle (enqack)
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
#
# SPDX-License-Identifier: MPL-2.0

# Legacy shell.nix for non-flake users
# For flake users, use: nix develop
# This file mirrors the flake.nix dev shell; the two must not disagree.
# The flake is the source of truth (see goppydae-docs design/build-environment.md).
{ pkgs ? import <nixpkgs> { } }:

pkgs.mkShell {
  buildInputs = with pkgs; [
    # Go toolchain
    go
    gotools # for goimports

    # CGO and build essentials
    gcc
    pkg-config
    pam
    openssl

    # Protocol Buffers
    protobuf
    protoc-gen-go
    protoc-gen-go-grpc
    buf

    # Lint and security gate
    golangci-lint
    gosec
    govulncheck

    # Documentation toolchain
    mkdocs
    pandoc

    # Checkpoint/restore (Goblin owns migration; see GOBLIN-DIV-018)
    criu
    libseccomp

    # Container orchestration
    podman
    podman-compose

    # Utilities
    rsync
    mage

    # Python verification tools
    (python3.withPackages (ps: with ps; [
      pytest
      jsonschema
      pybindgen
    ]))

    # Markdown linting
    markdownlint-cli2
  ];

  shellHook = ''
            # goppydae modules are private: skip proxy/sumdb, fetch direct.
            export GOPRIVATE=github.com/goppydae
    export GOBIN=$PWD/.bin
    export PATH=$GOBIN:$PATH

    # gcc 15 defaults to C23, where 'bool' is a keyword; the pinned gopy's
    # generated cgo preamble (typedef uint8_t bool) assumes C17. Pin the
    # dialect until gopy emits C23-safe code (kept symmetric with gapi's
    # shell).
    export CGO_CFLAGS=-std=gnu17

    if [ ! -x "$GOBIN/gopy" ]; then
      echo "Building pinned gopy from tools/gopy..."
      (cd tools/gopy && GOWORK=off go build -o "$GOBIN/gopy" github.com/go-python/gopy)
    fi

    echo "Goblin - Distributed Orchestrator (legacy shell.nix)"
    echo "Run 'mage -l' to see all available tasks"
  '';
}
