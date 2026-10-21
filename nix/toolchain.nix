{
  perSystem = {pkgs, ...}: let
    go = pkgs.go_1_27;
  in {
    # The one Go pin: the package builds with it, and every Go tool in the dev
    # shell is rebuilt with it, since a Go tool can only parse the language
    # version of the Go it was built with.
    _module.args = {
      inherit go;
      buildGoModule = pkgs.buildGoModule.override {inherit go;};
    };
  };
}
