# Copyright (c) 2025 Steven Verhelle (enqack)
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
#
# SPDX-License-Identifier: MPL-2.0

# GOBLIN-DIV-037: the target that actually runs test/cluster's
# `cluster && criu` tests.
#
# TestTwoNodeLiveMigration and TestMigrateToSameNodeRefused are tagged
# `cluster && criu` and fail rather than skip without
# CAP_CHECKPOINT_RESTORE, so no unprivileged runner can execute them.
# Before this check existed they were compiled by no invocation at all -
# the live-migration surface shipped with tests nothing built.
#
# The harness already anticipated this: GOBLIN_TEST_BIN_DIR exists so a
# guest with no Go toolchain can run the suite against binaries staged
# from the derivation. The hook had no caller. This is the caller.
#
# One guest, not two. The Go harness spawns its nodes as local child
# processes, so the two-node cluster lives inside this single machine on
# loopback. The cross-MACHINE migration is a different test with a
# different point (nix/tests/cluster-migration.nix), and it drives the
# CLI rather than the harness.
{ pkgs, self }:

let
  # Test binary and its fixtures, from the same source and vendor tree
  # as the product. GOWORK=off so the derivation resolves through
  # vendor/ exactly as the no-replace CI job does, not through the
  # sibling go.work that only exists in a dev checkout.
  bins = pkgs.buildGoModule {
    pname = "goblin-cluster-criu-test";
    version = "0.0.1";
    src = ../..;
    vendorHash = null;
    env.GOWORK = "off";

    # buildGoModule has no notion of "compile a test binary", so the
    # build and install phases are replaced rather than extended.
    buildPhase = ''
      runHook preBuild
      go test -c -tags cluster,criu -o cluster-criu.test ./test/cluster
      go build -o goblind ./cmd/goblind
      go build -o sleeper ./test/cluster/fixtures/sleeper
      runHook postBuild
    '';

    # The staged layout is the one GOBLIN_TEST_BIN_DIR expects: goblind
    # at the root, fixture agents under agents/.
    installPhase = ''
      runHook preInstall
      install -Dm755 cluster-criu.test $out/bin/cluster-criu.test
      install -Dm755 goblind $out/stage/goblind
      install -Dm755 sleeper $out/stage/agents/sleeper
      runHook postInstall
    '';
    doCheck = false;
  };
in
pkgs.testers.runNixOSTest {
  name = "goblin-cluster-criu-migration";

  nodes.node = { ... }: {
    # The unit stays off: this suite launches and supervises its own
    # goblind processes, and a module-managed daemon would contend for
    # the same ports and data directory.
    imports = [ self.nixosModules.default ];
    services.goblin.enable = false;

    environment.systemPackages = [ pkgs.criu bins ];

    # Two goblind processes, their agents, and criu dumping one of them.
    virtualisation.memorySize = 4096;
    virtualisation.cores = 4;
    networking.firewall.enable = false;
  };

  testScript = ''
    node.wait_for_unit("multi-user.target")

    # Preconditions asserted, not assumed. checkpoint.Available() fails
    # the tests without criu on PATH or CAP_CHECKPOINT_RESTORE in
    # CapEff, and a precondition failure should name itself here rather
    # than surface as a test failure fifteen minutes later.
    check = node.succeed("criu check 2>&1 || true")
    assert "Looks good" in check, f"criu check failed in the guest: {check}"

    caps = node.succeed("grep CapEff /proc/self/status")
    print(f"guest capabilities: {caps.strip()}")

    # NO -test.run FILTER. This entry was opened because a target
    # narrowed itself with -run and silently stopped covering the
    # surface its name promised; reintroducing a filter here would
    # rebuild the same trap one directory over. The binary carries every
    # cluster-tagged test plus the two criu ones, and all of them run.
    out = node.succeed(
        "GOBLIN_TEST_BIN_DIR=${bins}/stage "
        "${bins}/bin/cluster-criu.test -test.v -test.timeout=20m 2>&1",
        timeout=1800,
    )
    print(out)

    # succeed() already fails on a nonzero exit. This catches the other
    # direction: a binary that runs nothing at all still exits 0, and a
    # vacuous pass is the failure mode this entry is about.
    for name in ("TestTwoNodeLiveMigration", "TestMigrateToSameNodeRefused"):
        assert f"--- PASS: {name}" in out, (
            f"{name} did not report PASS; the criu-tagged tests were not executed:\n{out}"
        )

    print("CLUSTER CRIU MIGRATION OK: both criu-tagged tests executed and passed")
  '';
}
