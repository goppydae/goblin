{
  description = "Goblin - Distributed Orchestrator for GAPI (GoPPydae Agent Programming Interface)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
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
            
            # Container orchestration
            podman
            podman-compose
            
            # Utilities
            rsync
            mage
            
            # Python verification tools
            (python3.withPackages (ps: with ps; [
              pytest
              mdformat
              mdformat-gfm
              mdformat-frontmatter
              mdformat-footnote
              jsonschema
              pybindgen
            ]))
            
            # Markdown linting
            nodePackages.markdownlint-cli2
          ];

          shellHook = ''
            export GOBIN=$PWD/.bin
            export PATH=$GOBIN:$PATH

            if ! command -v gopy &> /dev/null; then
              echo "Installing gopy..."
              go install github.com/go-python/gopy@latest
            fi

            echo "👺 Goblin - Distributed Orchestrator"
            echo ""
            echo "Available mage tasks:"
            echo "  mage build          - Build goblind and goblinctl binaries"
            echo "  mage test           - Run all tests"
            echo "  mage testCluster    - Run cluster coordination tests"
            echo "  mage testMigration  - Run data migration tests"
            echo "  mage dev            - Start goblind in development mode"
            echo "  mage docs:html      - Generate HTML documentation"
            echo "  mage docs:man       - Generate Man pages"
            echo ""
            echo "Run 'mage -l' to see all available tasks"
          '';
        };
      }
    );
}
