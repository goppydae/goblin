# Copyright (c) 2025 Steven Verhelle (enqack)
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
#
# SPDX-License-Identifier: MPL-2.0

{ lib, buildGoModule, makeWrapper, python3
  # gapiPkg is the built gapi package, and it is the source of the Python
  # ADK this package ships (GOBLIN-DIV-072, operator decision 46: whatever
  # gapi ships for agents, goblin ships).
  #
  # IT DEFAULTS TO null RATHER THAN BEING REQUIRED, and the reason is a
  # defect gapi already paid for. nix/module.nix does
  # `pkgs.callPackage ./package.nix { }` for services.goblin.package, and
  # a REQUIRED argument there is an EVALUATION failure of the NixOS
  # module - which `nix build .#` never reaches, because it does not
  # evaluate the module. gapi's PR #110 shipped exactly that and was
  # caught only by `nix flake check --no-build --all-systems`.
  #
  # A null default does NOT mean a silent degrade: the install phase
  # fails loudly below. The whole point of this entry is that a goblind
  # with no ADK looks healthy and cannot run Python agents, so trading a
  # build failure for a runtime one would reinstate the defect in a new
  # place. The flake always passes it.
, gapiPkg ? null
}:

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

  nativeBuildInputs = [ makeWrapper ];

  # THE PYTHON ADK IS PART OF THE PRODUCT, NOT A DEVELOPMENT CONVENIENCE,
  # and goblin shipped none of it (GOBLIN-DIV-072). REPRODUCED against the
  # store path before this was written: the packaged goblind logs
  #
  #   no Python ADK found; set GOBLIN_PY_ADK to its directory. Looked in:
  #   [/nix/store/...-goblin-0.1.0-proto2f/share/goblin/python ...]
  #
  # and CONTINUES. Discovery then cannot describe a *.py.service at all,
  # so the daemon reports a healthy start with no agents - the failure
  # this entry exists to remove is invisible at the point it happens.
  #
  # The path below is not chosen, it is the one the kernel's resolver
  # already probes: share/<product>/python beside the binary. The error
  # above named it, which is what made this a wiring job rather than a
  # design.
  #
  # COPIED FROM gapi'S BUILT OUTPUT, NOT REBUILT. gapi's package runs a
  # five-stage pipeline for this tree - gopy gen, goimports, a c-shared
  # Go build, a generated build.py, and a gcc link against python3-config
  # - pinned to C17 because the vendored gopy emits `typedef uint8_t
  # bool`. Reproducing those stages here is what DOCUMENTATION-GOALS goal
  # 6 forbids, and gapi's own package.nix already records why: the
  # $ORIGIN rpath invariant lived in ONE place and was still wrong for
  # months. A second copy would be a second place to be wrong, and only
  # one of them runs on any given day.
  postInstall = ''
    ${lib.optionalString (gapiPkg == null) ''
      echo "goblin's package was built without gapiPkg, so it would ship no" >&2
      echo "Python ADK and goblind could not describe or start a Python agent." >&2
      echo "Build via this repo's flake, or pass gapiPkg explicitly." >&2
      exit 1
    ''}
    mkdir -p $out/share/goblin
    cp -r ${toString gapiPkg}/share/gapi/python $out/share/goblin/python
    chmod -R u+w $out/share/goblin/python

    # __pycache__ embeds absolute paths and mtimes, so shipping it makes
    # the output depend on what imported what during gapi's build. gapi
    # strips it; strip it again here because the copy is a fresh chance to
    # reintroduce it.
    find $out/share/goblin/python -name '__pycache__' -type d -prune -exec rm -rf {} + || true

    # --set-default, not --set: an operator's GOBLIN_PY_ADK still wins.
    # python3 on PATH because the runner is executed as an interpreter,
    # and a goblind that resolves the tree but cannot run python has moved
    # the failure rather than fixed it.
    for b in goblind goblinctl; do
      wrapProgram $out/bin/$b \
        --set-default GOBLIN_PY_ADK $out/share/goblin/python \
        --prefix PATH : ${lib.makeBinPath [ python3 ]}
    done
  '';

  # THE FILE EXISTING IS NOT THE PROPERTY. GAPI-DIV-085 hid behind exactly
  # that: the tree looked complete and only the compiled extension beneath
  # it was missing, so an operator got `cannot import name '_adk'` at
  # agent start while the build saw nothing wrong. Import it instead.
  #
  # PYTHONDONTWRITEBYTECODE because installCheckPhase runs after fixup,
  # when $out is still writable - an unguarded import would write
  # __pycache__ into the store path it is checking. A gate must not mutate
  # its subject.
  doInstallCheck = true;
  installCheckPhase = ''
    runHook preInstallCheck

    if ! env PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=$out/share/goblin/python \
         ${python3.interpreter} -c 'import gapi.native.adk' 2>/dev/null; then
      echo "the shipped Python ADK does not import; a Python agent would fail at start" >&2
      env PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=$out/share/goblin/python \
        ${python3.interpreter} -c 'import gapi.native.adk' >&2 || true
      exit 1
    fi

    runHook postInstallCheck
  '';

  meta = with lib; {
    description = "Goblin - Distributed Orchestrator for GAPI";
    homepage = "https://github.com/goppydae/goblin";
    # MPL-2.0, per the root LICENSE file and the README. This said mit,
    # which is not the licence this code ships under.
    license = licenses.mpl20;
    # goblind is Linux-only - it supervises via cgroups, subreaper and
    # CRIU - and this file said nothing, so an aarch64-darwin build was
    # offered for a daemon that cannot run there. Surfaced by
    # GOBLIN-DIV-072: the darwin package now has no gapi package to take
    # its ADK from either, which made the silent offer into an error.
    platforms = platforms.linux;
  };
}
