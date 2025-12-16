{ config, pkgs, ... }:

{
  # Basic configuration for generated images
  networking.hostName = "goblin-node";
  services.openssh.enable = true;
  services.openssh.settings.PermitRootLogin = "yes";
  
  # Enable Goblin by default in these images
  imports = [ ../module.nix ];
  services.goblin.enable = true;
  
  # Add some tools
  environment.systemPackages = with pkgs; [
    vim
    curl
    htop
  ];
  
  system.stateVersion = "23.11";
}
