{ lib, buildGoModule }:

buildGoModule {
  pname = "goblin";
  version = "0.0.1";
  src = ../.;
  vendorHash = null; 
  subPackages = [ "cmd/goblind" "cmd/goblinctl" ];

  meta = with lib; {
    description = "Goblin - Distributed Orchestrator for GAPI";
    homepage = "https://github.com/goppydae/goblin";
    license = licenses.mit;
  };
}
