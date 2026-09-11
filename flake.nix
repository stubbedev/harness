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

            # Development tools
            gopls # Go language server
            golangci-lint # Linter
            gofumpt # Formatter (stricter than gofmt)
            just # Task runner
            delve # Go debugger

            # Additional tools
            git # Version control
            gh # GitHub CLI
            sqlc # SQL code generator
            nodePackages.prettier # Formats the stats page assets
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
