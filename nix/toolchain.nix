{
  perSystem = {
    inputs',
    pkgs,
    ...
  }: let
    go = inputs'.devtools.packages.go;
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
