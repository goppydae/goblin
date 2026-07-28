# GOBLIN-DIV-018 capstone: a real process is dumped, its image is moved
# over the real goblin-ckpt ALPN, and it is restored under its original
# PID - inside a guest that actually holds CAP_CHECKPOINT_RESTORE.
#
# This is the only test in the repo that exercises CRIU for real.
# Everything else about migration is unit-tested against fakes, which
# cannot tell you whether criu will dump the process you have.
#
# Scope, stated honestly: the transfer runs between two image stores
# over a real QUIC listener on the real ALPN, but over loopback inside
# one guest. It proves the dump/transfer/restore mechanism. It does NOT
# prove goblind's cluster orchestration - no Raft, no leader, no
# coordinator - because the coordinator's Proposer/NodeClient/
# ImagePuller have no production implementations yet.
{ pkgs, self }:

let
  # The migration package's own tests, compiled with the criu tag. Built
  # from the same source and vendor tree as the product, so this cannot
  # drift from what ships.
  migrationTest = pkgs.buildGoModule {
    pname = "goblin-migration-criu-test";
    version = "0.0.1";
    src = ../..;
    vendorHash = null;
    env.GOWORK = "off";

    # buildGoModule has no notion of "compile a test binary", so the
    # build and install phases are replaced rather than extended.
    buildPhase = ''
      runHook preBuild
      go test -c -tags criu -o migration-criu.test ./core/migration
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      install -Dm755 migration-criu.test $out/bin/migration-criu.test
      runHook postInstall
    '';
    doCheck = false;
  };
in
pkgs.testers.runNixOSTest {
  name = "goblin-criu-migration";

  nodes.node = { ... }: {
    imports = [ self.nixosModules.default ];
    services.goblin.enable = true;

    environment.systemPackages = [ pkgs.criu migrationTest ];

    # The test binary runs as root here, which is how it gets the
    # capability the module grants goblind in production.
    virtualisation.memorySize = 2048;
  };

  testScript = ''
    node.wait_for_unit("multi-user.target")

    # Preconditions, asserted rather than assumed: without these the
    # failure below would be indistinguishable from a real defect.
    print("KERNEL: " + node.succeed("uname -r"))
    check = node.succeed("criu check 2>&1 || true")
    assert "Looks good" in check, f"criu check failed in the guest: {check}"

    # -v so a failure names the assertion rather than just the package.
    out = node.succeed(
        "migration-criu.test -test.v -test.timeout=300s 2>&1"
    )
    print(out)
    assert "PASS" in out, "the criu migration test did not report PASS"
    assert "FAIL" not in out, f"the criu migration test reported a failure:\n{out}"
  '';
}
