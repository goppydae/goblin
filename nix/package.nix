# Copyright (c) 2025 Steven Verhelle (enqack)
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
#
# SPDX-License-Identifier: MPL-2.0

{ lib, buildGoModule }:

buildGoModule rec {
  pname = "goblin";
  # Read from the root VERSION file, which is the single source of version
  # truth (magelib.Version reads the same file). This was hardcoded to
  # 0.0.1 while VERSION said 0.1.0-proto2.
  version = lib.fileContents ../VERSION;
  src = ../.;

  # vendorHash = null means "build from the committed vendor/ tree".
  vendorHash = null;

  # The committed go.work is copied into the sandbox by src = ../., but
  # its siblings (../gapi, ../magelib) are not, so go enters workspace
  # mode and fails to load them. Vendored builds must not use the
  # workspace; GOWORK=off is what CI already does for the same reason.
  env.GOWORK = "off";

  subPackages = [ "cmd/goblind" "cmd/goblinctl" ];

  # Without this the packaged binaries reported "version dev": the
  # Magefile stamps internal/version.Version and the Nix build did not
  # stamp anything at all, so 'nix build' produced an unversioned
  # artifact. Same defect gapi carried as GAPI-DIV-007.
  ldflags = [
    "-X github.com/goppydae/goblin/internal/version.Version=${version}"
  ];

  meta = with lib; {
    description = "Goblin - Distributed Orchestrator for GAPI";
    homepage = "https://github.com/goppydae/goblin";
    # MPL-2.0, per the root LICENSE file and the README. This said mit,
    # which is not the licence this code ships under.
    license = licenses.mpl20;
  };
}
