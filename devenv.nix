{
  pkgs,
  config,
  ...
}: let
  # A Go tool can only parse the language version of the Go it was built
  # with, so every tool that reads this module's source is rebuilt with the
  # Go the module declares. languages.go does this for gopls, delve and
  # gotools (which carries modernize); swag is rebuilt here the same way.
  #
  # A standalone gofumpt is deliberately absent. The release nixpkgs packages
  # lays out internal/cmd/session.go differently than the copy golangci-lint
  # vendors and gates on. Two formatters in one shell is the disagreement,
  # not the cure: formatting goes through `just fmt`, which runs the gate's
  # own copy.
  buildGoModule = pkgs.buildGoModule.override {go = config.languages.go.package;};
in {
  languages.go = {
    enable = true;
    package = pkgs.go_1_27;
  };

  packages = with pkgs; [
    golangci-lint
    (go-swag.override {inherit buildGoModule;})
    sqlc
    prettier
    just
    git
    gh
  ];

  env.CGO_ENABLED = "0";
  dotenv.enable = true;
}
