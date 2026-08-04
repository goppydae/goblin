# Copyright (c) 2025 Steven Verhelle (enqack)
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
#
# SPDX-License-Identifier: MPL-2.0

# Proves the flake's nixosModules output is importable and evaluates
# into a bootable system carrying a real goblin package.
#
# Deliberately does NOT assert that goblind reaches an active state:
# that depends on runtime configuration this test does not supply, and
# asserting it here would couple a plumbing check to service startup.
# The DIV-018 hardening assertions (kernel floor, AmbientCapabilities,
# cgroup Delegate, kernel.pid_max) land in a sibling test, which can
# read them off the unit without goblind having to start.
{ pkgs, self }:

pkgs.testers.runNixOSTest {
  name = "goblin-module-boot";

  nodes.node = { ... }: {
    imports = [ self.nixosModules.default ];
    services.goblin.enable = true;
  };

  testScript = ''
    node.wait_for_unit("multi-user.target")

    # The unit exists and runs the packaged binary. Reading ExecStart
    # off the unit proves the module wired a buildable package through,
    # which was false until nix/package.nix learned GOWORK=off.
    node.succeed("systemctl cat goblin.service")
    exec_start = node.succeed(
        "systemctl show goblin.service -p ExecStart --value"
    )
    assert "goblind" in exec_start, (
        "goblin.service ExecStart does not run goblind: " + exec_start
    )

    # Both binaries reach the system profile via environment.systemPackages.
    node.succeed("test -x $(command -v goblind)")
    node.succeed("test -x $(command -v goblinctl)")
  '';
}
