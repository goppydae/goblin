# Legacy shell.nix for non-flake users
# For flake users, use: nix develop
{ pkgs ? import <nixpkgs> { config.allowUnfree = true; } }:

pkgs.mkShell {
  buildInputs = with pkgs; [
    go
    gcc
    mage
    openssl
    protobuf
    protoc-gen-go
    protoc-gen-go-grpc
    pam
    pkg-config
    python3
    (python3.withPackages (ps: with ps; [
      pytest
      jsonschema
    ]))
  ];

  shellHook = ''
    # Explicitly add build inputs to PATH
    export PATH=${pkgs.gcc}/bin:${pkgs.go}/bin:${pkgs.python3}/bin:$PATH

    export GOBIN=$PWD/.bin
    export PATH=$GOBIN:$PATH

    if [ -n "$ZSH_VERSION" ]; then
      PROMPT="$PROMPT (nix-shell)"
    else
      export PS1="$PS1 (nix-shell)"
    fi
    echo "Welcome to the GoPPydae dev shell. Goblin stands ready."
  '';
}
