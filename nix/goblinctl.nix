# Copyright (c) 2025 Steven Verhelle (enqack)
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
#
# SPDX-License-Identifier: MPL-2.0

# goblinctl alone, buildable everywhere.
#
# OPERATOR DECISION 10: the control-plane client is cross-platform; the
# daemon is not. That was true of the CODE and false of the PACKAGING -
# neither repo shipped a control binary a macOS operator could build.
# nix/package.nix ships both binaries and the Python ADK and is
# platforms.linux, because goblind supervises through cgroups, the
# subreaper and CRIU, and because the ADK it carries comes from gapi's
# package, which exposes no darwin output at all.
#
# So the client is packaged separately rather than by relaxing that one.
# VERIFIED BY CROSS-COMPILING before this file existed: GOOS=darwin
# GOARCH=arm64 CGO_ENABLED=0 builds ./cmd/goblinctl clean. Advertising a
# package for a platform without checking is the defect GAPI-DIV-068's
# --all-systems flag exists to catch, and this repo produced two more
# instances of it the same day.
#
# WHAT THIS DELIBERATELY DOES NOT CARRY: the Python ADK. Shipping it
# needs gapi's packaged tree (GOBLIN-DIV-072) and gapi has no darwin
# package to take it from. The verbs that need a local ADK - describing
# or building an agent - therefore degrade here, while the control verbs
# a remote operator actually wants (cluster status, ping, tui, the
# lifecycle set) do not touch it. That is the honest split rather than a
# quietly incomplete one; if it needs closing, it closes by gapi gaining
# a darwin ADK output, not by this file guessing.
{ lib, buildGoModule }:

buildGoModule rec {
  pname = "goblinctl";
  version = lib.fileContents ../VERSION;
  src = ../.;

  vendorHash = null;

  # Same reason as nix/package.nix: src carries go.work but not its
  # siblings, so go would enter workspace mode and fail to resolve them.
  env.GOWORK = "off";

  subPackages = [ "cmd/goblinctl" ];

  ldflags = [
    "-X github.com/goppydae/goblin/internal/version.Version=${version}"
  ];

  meta = with lib; {
    description = "goblinctl - control-plane client for the Goblin distributed orchestrator";
    homepage = "https://github.com/goppydae/goblin";
    license = licenses.mpl20;
    # Linux AND darwin, unlike the full package. This is the whole point
    # of the file existing.
    platforms = platforms.linux ++ platforms.darwin;
  };
}
