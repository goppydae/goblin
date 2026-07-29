{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.goblin;

  # DDR-11 sets the kernel floor at 5.9. Three syscalls jointly enable
  # the migration and signalling path: pidfd_open (5.3), clone3(set_tid)
  # (5.5), and CAP_CHECKPOINT_RESTORE (5.9, carved out of CAP_SYS_ADMIN
  # specifically for CRIU). Below 5.9, checkpoint/restore would have to
  # run with full CAP_SYS_ADMIN, which the design explicitly rejects.
  kernelFloor = "5.9";
in {
  options.services.goblin = {
    enable = mkEnableOption "Goblin Distributed Supervisor";

    package = mkOption {
      type = types.package;
      default = pkgs.callPackage ./package.nix { };
      description = "The Goblin package to use.";
    };

    enableMigration = mkOption {
      type = types.bool;
      default = true;
      description = ''
        Grant the capabilities and cgroup delegation that CRIU-based
        checkpoint/restore requires. Turning this off yields a goblind
        that can supervise but cannot migrate; it does not lower the
        kernel floor, since the signalling path needs it regardless.
      '';
    };
  };

  config = mkIf cfg.enable {
    assertions = [
      {
        assertion = versionAtLeast config.boot.kernelPackages.kernel.version kernelFloor;
        message = ''
          services.goblin requires Linux ${kernelFloor} or newer for
          CAP_CHECKPOINT_RESTORE and clone3(set_tid); this system has
          ${config.boot.kernelPackages.kernel.version}.
        '';
      }
    ];

    environment.systemPackages = [ cfg.package ];

    # Restore reclaims dumped PIDs via clone3(set_tid) inside a fresh PID
    # namespace, and a cramped pid_max makes that fail for no good reason.
    #
    # nixpkgs already sets this to the same value in
    # nixos/modules/system/boot/systemd.nix, but only under
    # mkIf is64bit and only at mkDefault, so stating it at mkDefault here
    # collides ("defined multiple times"). Priority 900 is stronger than
    # mkDefault (1000) and far weaker than mkForce (50): goblin declares
    # the requirement explicitly rather than inheriting it by accident,
    # covers non-64-bit hosts, and still yields to an operator who sets
    # the value deliberately.
    boot.kernel.sysctl."kernel.pid_max" = mkOverride 900 4194304;

    systemd.services.goblin = {
      description = "Goblin Supervisor";
      wantedBy = [ "multi-user.target" ];

      # criu must be on the unit's PATH, not merely installed system
      # wide. goblind resolves it with exec.LookPath, and a systemd unit
      # does not inherit a login shell's PATH - so without this the
      # module grants CAP_CHECKPOINT_RESTORE and then makes checkpointing
      # impossible, failing at dump time with "criu not found on PATH".
      # Found by the two-node migration test (GOBLIN-DIV-031).
      path = optionals cfg.enableMigration [ pkgs.criu ];
      serviceConfig = {
        ExecStart = "${cfg.package}/bin/goblind";
        Restart = "always";
      } // optionalAttrs cfg.enableMigration {
        # CAP_CHECKPOINT_RESTORE for clone3(set_tid) on restore,
        # CAP_NET_ADMIN to restore network state with the process, and
        # CAP_SYS_ADMIN because CRIU still needs it for parts of the
        # dump path that were never carved out.
        AmbientCapabilities = [
          "CAP_SYS_ADMIN"
          "CAP_CHECKPOINT_RESTORE"
          "CAP_NET_ADMIN"
        ];
        CapabilityBoundingSet = [
          "CAP_SYS_ADMIN"
          "CAP_CHECKPOINT_RESTORE"
          "CAP_NET_ADMIN"
        ];

        # goblind owns the cgroup subtree for the agents it supervises.
        # Without delegation systemd reclaims it and the supervisor
        # cannot account for or freeze what it started.
        Delegate = "yes";
      };
    };
  };
}
