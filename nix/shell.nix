{
  perSystem = {
    buildGoModule,
    go,
    lib,
    pkgs,
    ...
  }: let
    # Rebuild a nixpkgs Go tool against the pinned toolchain: every
    # buildGo*Module argument its override takes gets ours.
    withPinnedGo = pkg:
      pkg.override (lib.mapAttrs (_: _: buildGoModule)
        (lib.filterAttrs (name: _: lib.match "buildGo.*Module" name != null)
          (lib.functionArgs pkg.override)));
  in {
    # A standalone gofumpt is deliberately absent. The release nixpkgs packages
    # lays out internal/cmd/session.go differently than the copy golangci-lint
    # vendors and gates on. Two formatters in one shell is the disagreement,
    # not the cure: formatting goes through `just fmt`, which runs the gate's
    # own copy.
    devShells.default = pkgs.mkShellNoCC {
      packages =
        [
          go
          pkgs.golangci-lint
          pkgs.sqlc
          pkgs.prettier
          pkgs.just
          pkgs.git
          pkgs.gh
        ]
        ++ map withPinnedGo [
          pkgs.go-swag
          pkgs.gopls
          pkgs.delve
          pkgs.gotools
          pkgs.gomodifytags
          pkgs.impl
          pkgs.go-tools
          pkgs.gotests
          pkgs.iferr
        ];

      env = {
        CGO_ENABLED = "0";
        # nixpkgs' go defaults to GOTOOLCHAIN=auto, which would fetch whatever
        # toolchain a go.mod line asks for; the shell holds to the pinned one.
        GOTOOLCHAIN = "local";
      };
    };
  };
}
