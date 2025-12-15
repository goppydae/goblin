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
        
        # Simple package definition for now, or just shell
        goblin = pkgs.buildGoModule {
          pname = "goblin";
          version = "0.0.1";
          src = ./.;
          vendorHash = null; # or "..." if vendored
          subPackages = [ "cmd/goblind" "cmd/goblinctl" ];
        };

      in {
        packages = {
          default = goblin;
          goblin = goblin;
        };
        
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            gotools
            gcc
            mage
            # Protobuf
            protobuf
            protoc-gen-go
            protoc-gen-go-grpc
            # Documentation
            pandoc
            python3Packages.mkdocs
            python3Packages.mkdocs-material
          ];

          shellHook = ''
            export GOBIN=$PWD/.bin
            export PATH=$GOBIN:$PATH
            
            # Add tools to PATH explicitly if needed, but mkShell handles it.
            export PATH=${pkgs.gcc}/bin:${pkgs.go}/bin:$PATH

            echo "👹 Goblin Dev Shell Active"
            echo "Use 'mage build' to build binaries."
          '';
        };
        
        apps = {
          default = {
            type = "app";
            program = "${goblin}/bin/goblind";
          };
          goblind = {
            type = "app";
            program = "${goblin}/bin/goblind";
          };
          goblinctl = {
            type = "app";
            program = "${goblin}/bin/goblinctl";
          };
        };
      }
    );
}
