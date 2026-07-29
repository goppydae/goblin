# GOBLIN-DIV-018: the module's migration prerequisites, asserted from
# inside a booted guest rather than by evaluating Nix.
#
# The distinction matters. Evaluating the module proves only that we
# wrote some attributes down; systemd is free to ignore, rename, or
# silently drop what it does not understand. Every assertion here reads
# the property back off the running system.
#
# None of this requires goblind to start successfully: unit properties
# and sysctls are readable regardless of whether the service stays up.
{ pkgs, self }:

pkgs.testers.runNixOSTest {
  name = "goblin-module-hardening";

  nodes = {
    # Default configuration: migration prerequisites granted.
    node = { ... }: {
      imports = [ self.nixosModules.default ];
      services.goblin.enable = true;
    };

    # The opt-out must actually drop the capabilities, not merely stop
    # advertising them. A negative case is the only thing that proves
    # the option is wired to anything.
    plain = { ... }: {
      imports = [ self.nixosModules.default ];
      services.goblin.enable = true;
      services.goblin.enableMigration = false;
    };
  };

  testScript = ''
    start_all()
    node.wait_for_unit("multi-user.target")
    plain.wait_for_unit("multi-user.target")

    # --- DDR-11 kernel floor -------------------------------------------
    # The module asserts >= 5.9 at evaluation; confirm the guest that
    # actually booted clears it, so the assertion is not vacuous.
    release = node.succeed("uname -r").strip()
    major, minor = (int(x) for x in release.split(".")[:2])
    assert (major, minor) >= (5, 9), f"kernel {release} is below the 5.9 floor"

    # --- CRIU is usable with what the module grants ---------------------
    # The point of the capability set: criu check must pass in the guest.
    criu_out = node.succeed("${pkgs.criu}/bin/criu check 2>&1 || true")
    assert "Looks good" in criu_out, f"criu check did not pass: {criu_out}"

    # --- AmbientCapabilities -------------------------------------------
    caps = node.succeed(
        "systemctl show goblin.service -p AmbientCapabilities --value"
    )
    for cap in ["cap_sys_admin", "cap_checkpoint_restore", "cap_net_admin"]:
        assert cap in caps.lower(), f"{cap} missing from AmbientCapabilities: {caps}"

    # --- criu reachable by the unit --------------------------------------
    # Granting the capability without putting criu on the unit's PATH
    # produces a service that is allowed to checkpoint and cannot: it
    # fails at dump time with "criu not found on PATH". Asserted against
    # the unit's environment, not the system profile, because that is
    # what goblind's exec.LookPath actually searches.
    unit_env = node.succeed(
        "systemctl show goblin.service -p Environment --value"
    )
    assert "criu" in unit_env, (
        "criu is not on the goblin.service PATH; migration would fail at dump: " + unit_env
    )

    # --- cgroup delegation ---------------------------------------------
    delegate = node.succeed(
        "systemctl show goblin.service -p Delegate --value"
    ).strip()
    assert delegate == "yes", f"Delegate is {delegate!r}, want 'yes'"

    # --- kernel.pid_max -------------------------------------------------
    pid_max = int(node.succeed("cat /proc/sys/kernel/pid_max").strip())
    assert pid_max == 4194304, f"kernel.pid_max is {pid_max}, want 4194304"

    # --- the opt-out actually opts out ----------------------------------
    plain_caps = plain.succeed(
        "systemctl show goblin.service -p AmbientCapabilities --value"
    ).strip()
    assert "cap_checkpoint_restore" not in plain_caps.lower(), (
        "enableMigration = false still granted CAP_CHECKPOINT_RESTORE: "
        + plain_caps
    )

    # The floor is not conditional on migration being enabled: the
    # signalling path needs pidfd regardless.
    plain_release = plain.succeed("uname -r").strip()
    p_major, p_minor = (int(x) for x in plain_release.split(".")[:2])
    assert (p_major, p_minor) >= (5, 9), (
        f"kernel {plain_release} is below the 5.9 floor"
    )
  '';
}
