{
  lib,
  buildGo127Module,
  installShellFiles,
  # Set by the flake from `self.shortRev or self.dirtyShortRev`; falls back to
  # the placeholder the binary uses when nothing was stamped in.
  rev ? "unknown",
  version ? "0-unstable",
}:
buildGo127Module {
  pname = "harness";
  inherit version;

  src = lib.cleanSource ./.;

  vendorHash = "sha256-CKrUOFa6T7i2ay6pW1IgedF1Vp17DibogiWmYnhCjOU=";

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

  nativeBuildInputs = [installShellFiles];

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
}
