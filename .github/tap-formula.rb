class Harness < Formula
  desc "Terminal-based AI coding assistant"
  homepage "https://github.com/stubbedev/harness"
  version "@@VERSION@@"
  license "FSL-1.1-MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "@@BASE_URL@@/harness_darwin_amd64"
      sha256 "@@SHA_DARWIN_AMD64@@"
    else
      url "@@BASE_URL@@/harness_darwin_arm64"
      sha256 "@@SHA_DARWIN_ARM64@@"
    end
  end

  on_linux do
    if Hardware::CPU.intel?
      url "@@BASE_URL@@/harness_linux_amd64"
      sha256 "@@SHA_LINUX_AMD64@@"
    else
      url "@@BASE_URL@@/harness_linux_arm64"
      sha256 "@@SHA_LINUX_ARM64@@"
    end
  end

  def install
    bin.install asset => "harness"
    chmod 0555, bin/"harness"
  end

  def test
    assert_match version.to_s, shell_output("#{bin}/harness --version")
  end

  def asset
    os = OS.mac? ? "darwin" : "linux"
    arch = Hardware::CPU.intel? ? "amd64" : "arm64"
    "harness_#{os}_#{arch}"
  end
end
