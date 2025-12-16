{
  description = "Goblin - Distributed Orchestrator for GAPI";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    nixos-generators = {
      url = "github:nix-community/nixos-generators";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, flake-utils, nixos-generators }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
          config.allowUnfree = true;
        };
        
        goblin = pkgs.callPackage ./nix/package.nix {};
        
      in {
        # Package output
        packages = {
          default = goblin;
          goblin = goblin;
          
          # nixos-generators images for testing
          iso = nixos-generators.nixosGenerate {
            inherit system;
            modules = [ ./nix/generators/base.nix ];
            format = "iso";
          };
          
          vm = nixos-generators.nixosGenerate {
            inherit system;
            modules = [ ./nix/generators/base.nix ];
            format = "vm";
          };
          
          vm-nogui = nixos-generators.nixosGenerate {
            inherit system;
            modules = [ ./nix/generators/base.nix ];
            format = "vm-nogui";
          };
          
          qcow = nixos-generators.nixosGenerate {
            inherit system;
            modules = [ 
              ./nix/generators/base.nix
              { virtualisation.diskSize = 10 * 1024; }  # 10GB
            ];
            format = "qcow";
          };
          
          docker = nixos-generators.nixosGenerate {
            inherit system;
            modules = [ ./nix/generators/base.nix ];
            format = "docker";
          };
          
          lxc = nixos-generators.nixosGenerate {
            inherit system;
            modules = [ ./nix/generators/base.nix ];
            format = "lxc";
          };
        };
        
        # Development shell
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
            
            export PATH=${pkgs.gcc}/bin:${pkgs.go}/bin:$PATH

            if [ -n "$ZSH_VERSION" ]; then
              PROMPT="$PROMPT (goblin-dev)"
            else
              export PS1="$PS1 (goblin-dev)"
            fi

            echo "👹 Goblin Dev Shell Active"
            echo "Use 'mage build' to build binaries."
            echo ""
            echo "Available nixos-generators formats:"
            echo "  nix build .#iso          - Bootable ISO"
            echo "  nix build .#vm           - QEMU VM"
            echo "  nix build .#qcow         - QCOW2 image"
            echo "  nix build .#docker       - Docker image"
            echo "  nix build .#lxc          - LXC container"
          '';
        };
        
        # Apps for easy running
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
    ) // {
      # NixOS module
      nixosModules.default = import ./nix/module.nix;
      nixosModules.goblin = import ./nix/module.nix;
    };
}
