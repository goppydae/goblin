# Copyright (c) 2025 Steven Verhelle (enqack)
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
#
# SPDX-License-Identifier: MPL-2.0

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
