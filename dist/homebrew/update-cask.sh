#!/usr/bin/env bash
# Regenerate Casks/rune.rb from the published darwin release manifests.
#
# cmd/rune/dist.sh uploads manifest-darwin-<arch>.json alongside each
# DMG, so version and sha256 come straight from the release itself
# rather than being recomputed here. Commit the result to the Homebrew
# tap repository.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
release_repo="${RELEASE_REPO:-unstablebuild/rune}"
host="${DOWNLOAD_HOST:-https://github.com/${release_repo}/releases/latest/download}"

field() {
	printf '%s' "$1" | python3 -c \
		'import json, sys; print(json.load(sys.stdin)[sys.argv[1]])' "$2"
}

arm_manifest=$(curl -fsSL "$host/manifest-darwin-arm64.json")
intel_manifest=$(curl -fsSL "$host/manifest-darwin-amd64.json")

arm_version=$(field "$arm_manifest" version)
intel_version=$(field "$intel_manifest" version)
if [ "$arm_version" != "$intel_version" ]; then
	echo "ERROR: manifest versions differ: arm64=$arm_version amd64=$intel_version" >&2
	echo "       Publish both darwin artifacts before updating the cask." >&2
	exit 1
fi

version=${arm_version#v}
arm_sha=$(field "$arm_manifest" sha256)
intel_sha=$(field "$intel_manifest" sha256)
description='Rune is a fast, GPU-accelerated, full-featured IDE and terminal multiplexer, suitable both for automatic and manual programming.'

cat > "$here/Casks/rune.rb" <<EOF
cask "rune" do
  arch arm: "arm64", intel: "amd64"

  version "$version"
  sha256 arm:   "$arm_sha",
         intel: "$intel_sha"

  url "https://github.com/$release_repo/releases/download/v#{version}/Rune-v#{version}-darwin-#{arch}.dmg"
  name "Rune"
  desc "$description"
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
EOF

echo "Wrote $here/Casks/rune.rb (version $version)"
