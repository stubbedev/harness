{
  description = "Harness: a terminal-based AI coding assistant";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
  }:
    flake-utils.lib.eachDefaultSystem (
      system: let
        # FSL-1.1-MIT is source-available rather than open source, so nixpkgs
        # classifies it as unfree. Allowing it for this instance keeps
        # `nix run github:stubbedev/harness` working without every consumer
        # having to set allowUnfree themselves.
        pkgs = import nixpkgs {
          inherit system;
          config.allowUnfree = true;
        };
        # A Go tool can only parse the language version of the Go it was
        # built with. nixpkgs already builds golangci-lint and gopls with
        # go_1_27, so they read this module's generic methods.
        #
        # A standalone gofumpt is deliberately absent from the shell below.
        # nixpkgs still builds it with go_1_26, where it cannot parse
        # internal/app/app.go at all; and the release it packages (v0.12.0)
        # lays out internal/cmd/session.go differently than the v0.11.0
        # golangci-lint vendors and gates on. Two formatters in one shell is
        # the disagreement, not the cure: formatting goes through `just fmt`,
        # which runs the gate's own copy.

        rev = self.shortRev or self.dirtyShortRev or "unknown";
        # Nix has no version to read from the source tree, so builds from a
        # checkout are stamped with the commit date and hash. Tagged releases
        # are consumed through the Go module path, not from here.
        version = "0-unstable-${self.lastModifiedDate or "0"}";
      in {
        packages = rec {
          harness = pkgs.callPackage ./package.nix {inherit rev version;};
          default = harness;
        };

        apps = rec {
          harness = flake-utils.lib.mkApp {drv = self.packages.${system}.harness;};
          default = harness;
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            # Go toolchain
            go_1_27

            # Development tools. gopls and golangci-lint are built with
            # go_1_27 by nixpkgs, so they parse what the compiler accepts.
            gopls # Go language server
            golangci-lint # Linter, and the formatter behind `just fmt`
            just # Task runner
            delve # Go debugger

            # Additional tools
            git # Version control
            gh # GitHub CLI
            sqlc # SQL code generator
            prettier # Formats the stats page assets
          ];

          shellHook = ''
            # Set Go environment variables
            export CGO_ENABLED=0
          '';
        };

        formatter = pkgs.alejandra;
      }
    );
}
