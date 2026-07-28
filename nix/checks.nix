# Flake checks for goblin. Each entry is a NixOS VM test: it boots a
# guest and asserts against the running system, not against evaluated
# Nix. See nix/tests/ for the individual suites.
#
# Run all of them with 'nix flake check'; run one with
# 'nix build .#checks.x86_64-linux.<name>'.
{ pkgs, self }:

{
  module-boot = import ./tests/module-boot.nix { inherit pkgs self; };
}
