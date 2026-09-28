cask "rune" do
  arch arm: "arm64", intel: "amd64"

  version "1.2.1"
  sha256 arm:   "27d0f75a44962f105a67c60db570976583d8cc990209d6e1b0e4e700779bedfc",
         intel: "192087f208010c14f74a9a47d08deeb50c096627a2331a5004f7113ecbd0bee7"

  url "https://github.com/unstablebuild/rune/releases/download/v#{version}/Rune-v#{version}-darwin-#{arch}.dmg"
  name "Rune"
  desc "Rune is a fast, GPU-accelerated, full-featured IDE and terminal multiplexer, suitable both for automatic and manual programming."
  homepage "https://rune.build/"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on macos: :ventura

  auto_updates true

  app "Rune.app"
  binary "#{appdir}/Rune.app/Contents/MacOS/rune"

  zap trash: "~/.rune"
end
