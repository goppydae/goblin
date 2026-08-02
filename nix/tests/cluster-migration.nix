# GOBLIN-DIV-031 item 3: a running process live-migrated between TWO
# SEPARATE MACHINES, driven through the operator-facing goblinctl verb.
#
# Two NixOS guests on a virtual LAN, each running its own goblind with
# its own listener, data directory and gossip identity. The image
# crosses a real network between two kernels; nothing here is loopback
# and nothing is faked.
#
# The Go cluster harness is deliberately NOT used: it spawns its nodes
# as local child processes, so it cannot span machines. This test drives
# the real binaries exactly as an operator would, which also means the
# CLI surface is under test rather than an in-process shortcut.
{ pkgs, self }:

let
  # goblind, goblinctl and the sleeper fixture, from the same source and
  # vendor tree as the product. The guests have no Go toolchain.
  bins = pkgs.buildGoModule {
    pname = "goblin-cluster-bins";
    version = "0.0.1";
    src = ../..;
    vendorHash = null;
    env.GOWORK = "off";
    buildPhase = ''
      runHook preBuild
      go build -o goblind ./cmd/goblind
      go build -o goblinctl ./cmd/goblinctl
      go build -o sleeper ./test/cluster/fixtures/sleeper
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      install -Dm755 goblind $out/bin/goblind
      install -Dm755 goblinctl $out/bin/goblinctl
      install -Dm755 sleeper $out/agents/sleeper
      runHook postInstall
    '';
    doCheck = false;
  };

  sleeperSpec = pkgs.writeText "sleeper-spec.yaml" ''
    name: sleeper-spec
    type: sleeper
    replicas: 1
  '';

  # Both guests are identical: either can host the instance, and the
  # test discovers which one actually does rather than assuming.
  guest = { ... }: {
    imports = [ self.nixosModules.default ];
    # The module supplies the kernel floor, capability set and sysctls.
    # goblind is launched by the test script, so the unit stays off.
    services.goblin.enable = false;

    # openssl generates the operator key below; the guests have no Go
    # toolchain to run the crypto package's own keygen.
    environment.systemPackages = [ pkgs.criu pkgs.openssl bins ];
    virtualisation.memorySize = 3072;
    virtualisation.cores = 2;
    networking.firewall.enable = false;
  };
in
pkgs.testers.runNixOSTest {
  name = "goblin-cluster-migration";

  nodes.node1 = guest;
  nodes.node2 = guest;

  # testScript takes `nodes` so the guests' addresses come from the
  # driver itself rather than being rediscovered inside the guests.
  testScript = { nodes, ... }: ''
    import re

    start_all()
    node1.wait_for_unit("multi-user.target")
    node2.wait_for_unit("multi-user.target")

    for m in (node1, node2):
        check = m.succeed("criu check 2>&1 || true")
        assert "Looks good" in check, f"criu check failed: {check}"

    # Guest addresses, straight from the test driver.
    #
    # Three attempts at discovering these inside the guests were wrong in
    # three different ways: the first global IPv4 is QEMU's user-mode NAT
    # (10.0.2.15, identical on every guest); getent hosts returns IPv6
    # first (2001:db8:1::1, which has no valid host:port form without
    # brackets); and a machine resolving its OWN hostname gets the
    # loopback alias 127.0.0.2. The driver already knows the answer, so
    # ask it instead of inferring.
    ip1 = "${nodes.node1.networking.primaryIPAddress}"
    ip2 = "${nodes.node2.networking.primaryIPAddress}"
    assert ip1 != ip2, f"both guests are {ip1}; they are not on a shared network"
    print(f"node1={ip1} node2={ip2}")

    # An operator key, on every node, BEFORE any mutating verb.
    #
    # Without one the registry stays empty and goblind refuses
    # agent.register with PERMISSION_DENIED - which is the operator key
    # registry (GOBLIN-DIV-015 piece 1) working as designed, and which
    # goblind warns about at startup naming this exact remedy. This test
    # predates the registry and went stale the day it landed; nothing
    # caught that because no workflow ran the check (GOBLIN-DIV-037).
    #
    # core/crypto.SavePublic writes the hex of a raw Ed25519 public key
    # with no trailing newline, and LoadPublic hex-decodes the file
    # whole, so a newline here would be a decode error. The last 32
    # bytes of an Ed25519 SubjectPublicKeyInfo DER are that raw key.
    def write_operator_key(machine, hexkey):
        machine.succeed(f"printf %s {hexkey} > /tmp/operator.pub")

    node1.succeed("openssl genpkey -algorithm ed25519 -out /tmp/operator.key")
    operator_hex = node1.succeed(
        "openssl pkey -in /tmp/operator.key -pubout -outform DER "
        "| tail -c 32 | od -An -tx1 | tr -d ' \\n'"
    ).strip()
    assert len(operator_hex) == 64, f"operator key is {len(operator_hex)} hex chars, want 64: {operator_hex!r}"
    # Both nodes get the SAME key, as the Go harness does: the registry
    # is replicated, so this seeds one identity rather than two.
    write_operator_key(node1, operator_hex)
    write_operator_key(node2, operator_hex)
    print(f"operator key seeded on both guests: {operator_hex[:16]}...")

    def start_goblind(machine, node_id, ip, join=None):
        join_arg = f"--join {join}:29000" if join else ""
        machine.succeed("mkdir -p /var/lib/goblin")
        # GOBLIN_AGENT_PATH fences discovery to the fixture dir. The name
        # follows goblind's PRODUCT identity (GOBLIN-DIV-055); it carried
        # the kernel's own namespace before that. Nix
        # cannot compose it through the kernel the way the Go harness
        # does, so nix_env_names_test.go asserts this literal against the
        # composed name - without that, this file is the one fence site
        # no PR-path job executes.
        machine.succeed(
            "GOBLIN_AGENT_PATH=${bins}/agents "
            # criu on PATH, for the same reason the module sets it: a
            # transient unit does not inherit a login shell's PATH, and
            # goblind resolves criu with exec.LookPath.
            f"systemd-run --unit=goblind-{node_id} --setenv=GOBLIN_AGENT_PATH=${bins}/agents "
            f"--setenv=PATH=${pkgs.criu}/bin:/run/current-system/sw/bin "
            f"${bins}/bin/goblind start --id {node_id} "
            # --advertise-addr is a bare HOST: the port comes from
            # AdvertisePort, which defaults to the listen port. Passing
            # host:port here made goblind resolve the whole string as a
            # hostname and exit with "no such host".
            f"--listen-addr 0.0.0.0:29000 --advertise-addr {ip} "
            f"--data /var/lib/goblin/raft --log-format json --log-level debug "
            f"--operator-key /tmp/operator.pub "
            f"{join_arg}"
        )

    # A goblind that exits immediately must fail HERE with its own log,
    # not thirty seconds later inside wait_for_open_port with nothing to
    # read. Every startup bug in this test so far presented as a timeout.
    def assert_running(machine, node_id):
        machine.sleep(2)
        state = machine.succeed(
            f"systemctl is-active goblind-{node_id} || true"
        ).strip()
        if state != "active":
            log = machine.succeed(f"journalctl -u goblind-{node_id} --no-pager | tail -30")
            raise AssertionError(f"goblind-{node_id} is {state}:\n{log}")

    # wait_for_open_port probes TCP; goblind's control plane is QUIC,
    # which is UDP. There is no TCP listener on 29000 and never will be,
    # so the TCP wait blocked forever against a perfectly healthy node.
    def wait_listening(machine, node_id):
        machine.wait_until_succeeds("ss -uln | grep -q ':29000'", timeout=60)
        assert_running(machine, node_id)

    # node1 seeds; node2 joins it.
    start_goblind(node1, "node1", ip1)
    assert_running(node1, "node1")
    wait_listening(node1, "node1")
    start_goblind(node2, "node2", ip2, join=ip1)
    assert_running(node2, "node2")
    wait_listening(node2, "node2")

    def ctl(machine, args, target_ip):
        out = machine.succeed(
            f"${bins}/bin/goblinctl --api-addr {target_ip}:29000 --tls-insecure {args} 2>&1"
        )
        # cobra prints help and exits 0 for a command that does not
        # exist, so machine.succeed cannot catch a wrong path. That is
        # how "agent register" - the verbs actually live under "cluster
        # agent" - silently did nothing while the test waited for an
        # instance that was never scheduled.
        if "Usage:" in out and "Available Commands:" in out:
            raise AssertionError(f"goblinctl {args!r} is not a command; it printed help:\n{out}")
        return out

    # Both nodes visible to the cluster before anything is scheduled.
    def members_settled(_):
        out = ctl(node1, "cluster status", ip1)
        return out.count("node1") >= 1 and out.count("node2") >= 1

    with node1.nested("waiting for both members"):
        retry(members_settled)

    # Schedule one instance, so there is exactly one thing to move.
    node1.succeed("cp ${sleeperSpec} /tmp/sleeper-spec.yaml")
    print("register: " + ctl(node1, "cluster agent register /tmp/sleeper-spec.yaml", ip1))
    # What the cluster believes exists, before any waiting. If the spec
    # did not land or scheduling never happened, that shows HERE rather
    # than as a silent retry loop.
    print("specs after register:\n" + ctl(node1, "cluster agent list", ip1))
    print("instances after register:\n" + ctl(node1, "cluster agent instances", ip1))

    instance_line = re.compile(
        r"^([0-9a-f-]{36})\s+\S+\s+(\S+)\s+(\S+)$", re.MULTILINE
    )

    def read_instance():
        out = ctl(node1, "cluster agent instances sleeper-spec", ip1)
        m = instance_line.search(out)
        if not m:
            return None
        return {"uuid": m.group(1), "node": m.group(2), "state": m.group(3), "raw": out}

    # Bounded, and it PRINTS what it saw. A retry loop that reports only
    # "timed out" is what turned each of the previous failures into an
    # apparent hang.
    def await_instance(predicate, what, attempts=60):
        last = ""
        for _ in range(attempts):
            out = ctl(node1, "cluster agent instances sleeper-spec", ip1)
            last = out
            m = instance_line.search(out)
            if m and predicate(m):
                return {"uuid": m.group(1), "node": m.group(2), "state": m.group(3), "raw": out}
            node1.sleep(1)
        raise AssertionError(f"timed out {what}; last listing was:\n{last}")

    before = await_instance(lambda m: m.group(3) == "running", "waiting for the instance to run")

    source, uuid = before["node"], before["uuid"]
    dest = "node2" if source == "node1" else "node1"
    print(f"instance {uuid} running on {source}; migrating to {dest}")

    # --- the migration, through the operator-facing verb ---------------
    out = ctl(node1, f"cluster migrate-instance {uuid} {dest}", ip1)
    print(f"migrate-instance: {out}")

    after = await_instance(
        lambda m: m.group(2) == dest and m.group(3) == "running",
        f"waiting for the instance to land on {dest}",
    )

    # The identity survives the move. A different UUID would mean the
    # process was replaced, not migrated.
    assert after["uuid"] == uuid, (
        f"instance UUID changed across migration: {uuid} -> {after['uuid']}"
    )
    assert after["node"] == dest, f"instance is on {after['node']}, want {dest}"
    assert after["state"] == "running", f"instance is {after['state']}, want running"

    # Exactly one instance: a migration that leaves a copy behind is a
    # split brain, not a move.
    running = [
        line for line in after["raw"].splitlines() if line.strip().endswith("running")
    ]
    assert len(running) == 1, f"{len(running)} instances running after migrating one:\n{after['raw']}"

    # The process really is on the destination guest.
    dest_machine = node2 if dest == "node2" else node1
    dest_machine.succeed("pgrep -f sleeper")
    source_machine = node1 if dest == "node2" else node2
    source_machine.fail("pgrep -f sleeper")

    print(f"MIGRATION OK: {uuid} moved {source} -> {dest} across machines")
  '';
}
