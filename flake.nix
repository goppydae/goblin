{
  description = "Goblin - Distributed Orchestrator for GAPI";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
        };
      in {
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            gotools
            gcc
            # Documentation
            pandoc
            python3Packages.mkdocs
            python3Packages.mkdocs-material
          ];

          shellHook = ''
            export GOBIN=$PWD/.bin
            export PATH=$GOBIN:$PATH
            echo "👹 Goblin Dev Shell Active"
            echo "Remember: Goblin depends on sibling '../gapi' via go.mod replace"
          '';
        };
      }
    );
}
