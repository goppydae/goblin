{
  description = "GoPPydae Silo - Scenario management for GAPI and Goblin";

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
            ]))
            
            # Markdown linting
            nodePackages.markdownlint-cli2
          ];

          shellHook = ''
            echo "🎭 GoPPydae Silo - Scenario Management"
            echo ""
            echo "Available mage tasks:"
            echo "  mage cluster:build    - Build cluster image (no cache)"
            echo "  mage cluster:fresh    - Complete fresh build and start"
            echo "  mage cluster:restart  - Restart with fresh containers"
            echo "  mage cluster:tui      - Launch unified TUI"
            echo "  mage cluster:test     - Run automated tests"
            echo "  mage cluster:clean    - Remove all resources"
            echo ""
            echo "Run 'mage -l' to see all available tasks"
          '';
        };
      }
    );
}
