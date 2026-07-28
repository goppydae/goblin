{ lib, buildGoModule }:

buildGoModule {
  pname = "goblin";
  version = "0.0.1";
  src = ../.;

  # vendorHash = null means "build from the committed vendor/ tree".
  vendorHash = null;

  # The committed go.work is copied into the sandbox by src = ../., but
  # its siblings (../gapi, ../magelib) are not, so go enters workspace
  # mode and fails to load them. Vendored builds must not use the
  # workspace; GOWORK=off is what CI already does for the same reason.
  env.GOWORK = "off";

  subPackages = [ "cmd/goblind" "cmd/goblinctl" ];

  meta = with lib; {
    description = "Goblin - Distributed Orchestrator for GAPI";
    homepage = "https://github.com/goppydae/goblin";
    license = licenses.mit;
  };
}
