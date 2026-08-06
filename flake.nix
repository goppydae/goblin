# Copyright (c) 2025 Steven Verhelle (enqack)
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
#
# SPDX-License-Identifier: MPL-2.0

{
  description = "Goblin - Distributed Orchestrator for GAPI (GoPPydae Agent Process Infrastructure)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";

    # GOBLIN-DIV-072. goblind RESOLVES a Python ADK and shipped none, so a
    # packaged goblind could not describe or start a Python agent while
    # reporting a healthy start. Operator decision 46 closes that by
    # SHIPPING, not by deleting the resolution.
    #
    # A NIX INPUT IS THE ONLY MECHANISM THAT CAN CARRY IT. The ADK is a
    # Python package plus a compiled CPython extension, and `go mod
    # vendor` copies only Go packages - there is not one .py file under
    # vendor/. So the module system that already gives goblin the kernel's
    # Go code structurally cannot carry this one artifact.
    #
    # PINNED TO A TAG, matching go.mod. That is a SECOND pin of one thing,
    # which is the drift class GOBLIN-DIV-071 was about, so it is gated
    # rather than trusted: nix/adk-version-test asserts this input's
    # VERSION equals the version go.mod pins. They must agree, because the
    # extension wraps the kernel's adk/go and speaks its contract.
    #
    # follows nixpkgs deliberately: the extension is linked against one
    # specific CPython via python3-config, so two nixpkgs would mean an
    # ABI mismatch that appears at import time rather than at build time.
    # The cost is that goblin's CI builds gapi from source. That is a
    # CI-minutes concern with a known remedy - a dedicated adk-python
    # output in gapi - deliberately not taken yet, because if the ADK ever
    # moves to its own repository that work is discarded.
    gapi = {
      url = "github:goppydae/gapi/v0.1.0-proto2k";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, flake-utils, gapi }:
    # System-independent outputs live outside eachDefaultSystem. A NixOS
    # module is evaluated by the consuming system's module system, so it
    # must not be keyed by our system; nesting it inside eachDefaultSystem
    # would produce nixosModules.<system>.default, which no consumer can
    # import. Until this existed, nix/module.nix was reachable only by
    # relative path and nix flake check ran nothing.
    {
      # The module is WRAPPED rather than imported bare, so a flake
      # consumer's services.goblin.package is the one THIS flake builds -
      # the one carrying the Python ADK from the gapi input.
      #
      # module.nix's own default is `pkgs.callPackage ./package.nix { }`,
      # which cannot reach a flake input and therefore builds without
      # gapiPkg. That path now fails loudly instead of silently shipping
      # no ADK (GOBLIN-DIV-072), which is right for someone importing
      # nix/module.nix by path but wrong as the experience of using this
      # flake. mkDefault, so an operator pinning their own package still
      # wins.
      nixosModules.default = { pkgs, lib, ... }: {
        imports = [ ./nix/module.nix ];
        services.goblin.package =
          lib.mkDefault self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      };
      nixosModules.goblin = self.nixosModules.default;
    }
    //
    # An explicit system list, not eachDefaultSystem: nixpkgs 26.11 dropped
    # x86_64-darwin, which eachDefaultSystem still enumerates, so merely
    # instantiating pkgs for that platform throws and every output
    # disappears - including the ones that would have evaluated fine. This
    # list is also honest for a product whose nix/package.nix declares
    # platforms.linux. gapi hit this first (GAPI-DIV-035).
    flake-utils.lib.eachSystem [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" ] (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            # Go toolchain
            go
            gotools
            
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
          ]
          # DAEMON-SIDE ONLY. criu and libseccomp have no darwin build,
          # so listing them unconditionally made devShells.aarch64-darwin
          # impossible to instantiate - the system was declared and
          # unusable, exactly as it was in gapi (GAPI-DIV-064).
          #
          # The split is the product's: the control-plane client is
          # cross-platform and the supervisor is not, because cgroups v2,
          # PID 1 and CRIU are the point rather than an implementation
          # detail. A darwin shell builds both binaries, runs the unit
          # tests and lints; it cannot run goblind meaningfully or the
          # privileged suites, neither of which it could have anyway.
          ++ lib.optionals stdenv.hostPlatform.isLinux [
            criu
            libseccomp
          ];

          shellHook = ''
            # goppydae modules are private: skip proxy/sumdb, fetch direct.
            export GOPRIVATE=github.com/goppydae
            export GOBIN=$PWD/.bin
            export PATH=$GOBIN:$PATH

            # gcc 15 defaults to C23, where 'bool' is a keyword; the pinned
            # gopy's generated cgo preamble (typedef uint8_t bool) assumes
            # C17. Pin the dialect until gopy emits C23-safe code (kept
            # symmetric with gapi's shell).
            export CGO_CFLAGS=-std=gnu17

            if [ ! -x "$GOBIN/gopy" ]; then
              echo "Building pinned gopy from tools/gopy..."
              (cd tools/gopy && GOWORK=off go build -o "$GOBIN/gopy" github.com/go-python/gopy)
            fi

            echo "Goblin - Distributed Orchestrator"
            echo ""
            echo "Available mage tasks:"
            echo "  mage build          - Build goblind and goblinctl binaries"
            echo "  mage test           - Run all tests"
            echo "  mage testCluster    - Run cluster coordination tests"
            echo "  mage testScheduler  - Run scheduler tests"
            echo "  mage dev            - Start goblind in development mode"
            echo "  mage docs:html      - Generate HTML documentation"
            echo "  mage docs:man       - Generate Man pages"
            echo ""
            echo "Run 'mage -l' to see all available tasks"
          '';
        };

        # gapiPkg is passed HERE rather than defaulted inside package.nix,
        # because a flake input cannot be synthesised by callPackage. The
        # null default in package.nix exists only so nix/module.nix's own
        # `callPackage ./package.nix { }` still EVALUATES; a build without
        # this argument fails loudly rather than shipping no ADK.
        # NOT ADVERTISED ON DARWIN, and this took two rounds of
        # `nix flake check --all-systems` to get right - both of which
        # `nix build .#` passed, which is the whole argument for running
        # both.
        #
        # goblin's system list carries aarch64-darwin for the DEV SHELL,
        # where a darwin developer builds both binaries and runs unit
        # tests. But goblind is Linux-only, and gapi - correctly - exposes
        # no darwin package to take an ADK from. Referencing
        # gapi.packages.<system>.default unconditionally failed with
        # "attribute 'default' missing"; adding meta.platforms then failed
        # differently, as an unsupported-system refusal. Both are the
        # defects GAPI-DIV-068's --all-systems flag was added to catch,
        # reappearing here.
        #
        # So the package simply is not offered where it cannot be built.
        # The dev shell remains available on darwin; only the derivation
        # goes away.
        packages = {
          # EVERY system, including darwin. Operator decision 10 makes the
          # control client cross-platform, and the packaging did not
          # honour it: removing the darwin package to satisfy the check
          # above would have taken goblinctl with it, which is a
          # regression against the one binary a macOS operator wants.
          # Cross-compilation verified before advertising it.
          goblinctl = pkgs.callPackage ./nix/goblinctl.nix { };
        } // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux rec {
          default = pkgs.callPackage ./nix/package.nix {
            gapiPkg = gapi.packages.${system}.default;
          };
          goblin = default;
        };

        # VM-backed checks. These boot a real guest kernel, which is the
        # only way to assert the module's runtime properties (kernel
        # floor, capabilities, cgroup delegation, sysctls) rather than
        # merely evaluating them - and the only way to exercise CRIU,
        # which needs capabilities no unprivileged host process can hold.
        checks = import ./nix/checks.nix { inherit pkgs self; } // {
          # THE SECOND PIN, GATED. GOBLIN-DIV-072 takes gapi as a flake
          # input to get the Python ADK, while go.mod pins the same
          # module for the Go code - one thing pinned twice, which is
          # the drift class GOBLIN-DIV-071 was filed for.
          #
          # They MUST agree: the shipped extension wraps the kernel's
          # adk/go and speaks the contract of the kernel goblind links.
          # A goblind built against proto2k that ships proto2j's ADK is
          # a version skew no test would otherwise see, because both
          # halves work in isolation.
          #
          # This is not a VM test, so it lives here rather than in
          # checks.nix - that file is VM suites and is gated on Linux,
          # and a pin disagreeing is not a Linux-specific fact.
          adk-version-agrees = pkgs.runCommand "goblin-adk-version-agrees" { } ''
            pinned=$(sed -n 's|^[[:space:]]*github.com/goppydae/gapi v\(.*\)$|\1|p' ${./go.mod} | head -1)
            shipped=$(cat ${gapi}/VERSION)

            if [ -z "$pinned" ]; then
              echo "could not read the gapi version from go.mod; this gate is not gating" >&2
              exit 1
            fi

            if [ "$pinned" != "$shipped" ]; then
              echo "go.mod pins gapi $pinned but the flake input ships $shipped." >&2
              echo "goblind would link one kernel and carry another's Python ADK." >&2
              echo "Update inputs.gapi.url and go.mod together." >&2
              exit 1
            fi

            echo "gapi pin agrees in both places: $pinned" > $out
          '';
        };
      }
    );
}
