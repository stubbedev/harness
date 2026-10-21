{
  inputs,
  self,
  ...
}: {
  perSystem = {
    buildGoModule,
    lib,
    pkgs,
    self',
    system,
    ...
  }: let
    rev = self.shortRev or self.dirtyShortRev or "unknown";
    # Nix has no version to read from the source tree, so builds from a
    # checkout are stamped with the commit date and hash. Tagged releases
    # are consumed through the Go module path, not from here.
    version = "0-unstable-${self.lastModifiedDate or "0"}";
  in {
    # FSL-1.1-MIT is source-available rather than open source, so nixpkgs
    # classifies it as unfree. Allowing it for this instance keeps
    # `nix run github:stubbedev/harness` working without every consumer
    # having to set allowUnfree themselves.
    _module.args.pkgs = import inputs.nixpkgs {
      inherit system;
      config.allowUnfree = true;
    };

    packages = {
      harness = buildGoModule {
        pname = "harness";
        inherit version;

        src = lib.fileset.toSource {
          root = ../.;
          fileset = lib.fileset.difference (lib.fileset.gitTracked ../.) (
            lib.fileset.unions [
              ../flake.nix
              ../flake.lock
              ../nix
            ]
          );
        };

        vendorHash = "sha256-pLQrPNGQ/SdU6NTtRkaNE7WzsdZ+WLH9yNDNMd76Zes=";

        # Only the root command. Without this every main package in the tree is
        # installed, which puts internal/ui/logo/example on PATH as `example`.
        subPackages = ["."];

        # The SQLite driver used here is pure Go, so nothing needs cgo.
        env.CGO_ENABLED = 0;

        ldflags = [
          "-s"
          "-w"
          "-X github.com/stubbedev/harness/internal/version.Version=${version}"
          "-X github.com/stubbedev/harness/internal/version.Commit=${rev}"
          "-X github.com/stubbedev/harness/internal/version.BuildID=${rev}"
        ];

        # The test suite spawns language servers and PTYs; neither belongs in a
        # sandboxed build.
        doCheck = false;

        nativeBuildInputs = [pkgs.installShellFiles];

        postInstall = ''
          installShellCompletion --cmd harness \
            --bash <($out/bin/harness completion bash) \
            --zsh <($out/bin/harness completion zsh) \
            --fish <($out/bin/harness completion fish)

          $out/bin/harness man | gzip -c > harness.1.gz
          installManPage harness.1.gz
        '';

        meta = {
          description = "Terminal-based AI coding assistant";
          homepage = "https://github.com/stubbedev/harness";
          license = lib.licenses.fsl11Mit;
          mainProgram = "harness";
          platforms = lib.platforms.unix ++ lib.platforms.windows;
        };
      };
      default = self'.packages.harness;
    };

    apps = {
      harness = {
        type = "app";
        program = lib.getExe self'.packages.harness;
      };
      default = self'.apps.harness;
    };
  };
}
