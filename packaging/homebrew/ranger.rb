# Homebrew formula for ranger.
#
# This is a template. Every tagged release runs .github/workflows/release.yml,
# which fills in the version and per-platform sha256 values and attaches a
# ready-to-use ranger.rb to the GitHub Release. Copy that generated file into
# your Homebrew tap (e.g. Formula/ranger.rb in kd14/homebrew-tap), then:
#
#   brew install kd14/tap/ranger
#
# Note: homebrew-core already ships a "ranger" (the file manager). In your own
# tap the name is fine; if you also use that tool, rename this formula/binary.
class Ranger < Formula
  desc "Wireless survey tool: scans Wi-Fi, BLE, and mDNS into a live dashboard"
  homepage "https://github.com/Kamalesh-Seervi/ranger"
  version "REPLACE_VERSION"

  on_macos do
    on_arm do
      url "https://github.com/Kamalesh-Seervi/ranger/releases/download/vREPLACE_VERSION/ranger_REPLACE_VERSION_darwin_arm64.tar.gz"
      sha256 "REPLACE_DARWIN_ARM64"
    end
    on_intel do
      url "https://github.com/Kamalesh-Seervi/ranger/releases/download/vREPLACE_VERSION/ranger_REPLACE_VERSION_darwin_amd64.tar.gz"
      sha256 "REPLACE_DARWIN_AMD64"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/Kamalesh-Seervi/ranger/releases/download/vREPLACE_VERSION/ranger_REPLACE_VERSION_linux_arm64.tar.gz"
      sha256 "REPLACE_LINUX_ARM64"
    end
    on_intel do
      url "https://github.com/Kamalesh-Seervi/ranger/releases/download/vREPLACE_VERSION/ranger_REPLACE_VERSION_linux_amd64.tar.gz"
      sha256 "REPLACE_LINUX_AMD64"
    end
  end

  def install
    bin.install "ranger"
  end

  test do
    assert_match "-listen", shell_output("#{bin}/ranger -h 2>&1")
  end
end
