{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.goblin;
in {
  options.services.goblin = {
    enable = mkEnableOption "Goblin Distributed Supervisor";
    
    package = mkOption {
      type = types.package;
      default = pkgs.callPackage ./package.nix {};
      description = " The Goblin package to use.";
    };
  };

  config = mkIf cfg.enable {
    environment.systemPackages = [ cfg.package ];
    
    systemd.services.goblin = {
      description = "Goblin Supervisor";
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        ExecStart = "${cfg.package}/bin/goblind";
        Restart = "always";
      };
    };
  };
}
