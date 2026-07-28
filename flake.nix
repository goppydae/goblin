{
  description = "Goblin - Distributed Orchestrator for GAPI (GoPPydae Agent Programming Interface)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    # System-independent outputs live outside eachDefaultSystem. A NixOS
    # module is evaluated by the consuming system's module system, so it
    # must not be keyed by our system; nesting it inside eachDefaultSystem
    # would produce nixosModules.<system>.default, which no consumer can
    # import. Until this existed, nix/module.nix was reachable only by
    # relative path and nix flake check ran nothing.
    {
      nixosModules.default = import ./nix/module.nix;
      nixosModules.goblin = self.nixosModules.default;
    }
    //
    flake-utils.lib.eachDefaultSystem (system:
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

            # gcc 15 defaults to C23, where 'bool' is a keyword; the pinned
            # gopy's generated cgo preamble (typedef uint8_t bool) assumes
            # C17. Pin the dialect until gopy emits C23-safe code (kept
            # symmetric with gapi's shell).
            export CGO_CFLAGS=-std=gnu17

            if [ ! -x "$GOBIN/gopy" ]; then
              echo "Building pinned gopy from tools/gopy..."
              (cd tools/gopy && GOWORK=off go build -o "$GOBIN/gopy" github.com/go-python/gopy)
            fi

            echo "👺 Goblin - Distributed Orchestrator"
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

        packages.default = pkgs.callPackage ./nix/package.nix { };
        packages.goblin = self.packages.${system}.default;

        # VM-backed checks. These boot a real guest kernel, which is the
        # only way to assert the module's runtime properties (kernel
        # floor, capabilities, cgroup delegation, sysctls) rather than
        # merely evaluating them - and the only way to exercise CRIU,
        # which needs capabilities no unprivileged host process can hold.
        checks = import ./nix/checks.nix { inherit pkgs self; };
      }
    );
}
