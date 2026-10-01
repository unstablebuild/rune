#!/usr/bin/env bash
# Print the Homebrew cask for a published release, built from the
# manifest-<os>-<arch>.json files cmd/rune/dist.sh uploads next to each
# artifact, so version and sha256 come from the release itself.
#
#   CASK_TOKEN    rune for our tap (default); rune-ide for homebrew/cask, where
#                 a homebrew/core formula already owns the name "rune"
#   RELEASE_TAG   release to read, e.g. v1.3.0; defaults to the latest release
#   RELEASE_REPO  defaults to unstablebuild/rune
set -euo pipefail

token="${CASK_TOKEN:-rune}"
release_repo="${RELEASE_REPO:-unstablebuild/rune}"
if [ -n "${RELEASE_TAG:-}" ]; then
	host="https://github.com/${release_repo}/releases/download/${RELEASE_TAG}"
else
	host="https://github.com/${release_repo}/releases/latest/download"
fi

manifest() {
	if ! curl -fsSL "$host/manifest-$1.json"; then
		echo "ERROR: $host/manifest-$1.json is not published." >&2
		exit 1
	fi
}

field() {
	printf '%s' "$1" | python3 -c \
		'import json, sys; print(json.load(sys.stdin)[sys.argv[1]])' "$2"
}

darwin_arm64=$(manifest darwin-arm64)
darwin_amd64=$(manifest darwin-amd64)
linux_arm64=$(manifest linux-arm64)
linux_amd64=$(manifest linux-amd64)

version=$(field "$darwin_arm64" version)
for m in "$darwin_amd64" "$linux_arm64" "$linux_amd64"; do
	if [ "$(field "$m" version)" != "$version" ]; then
		echo "ERROR: the release manifests disagree on the version; republish them." >&2
		exit 1
	fi
done
version=${version#v}

# brew style caps desc at 80 characters with no trailing period, so this is
# the short form of the package description rather than the full sentence.
cat <<EOF
cask "$token" do
  arch arm: "arm64", intel: "amd64"

  version "$version"
  sha256 arm:          "$(field "$darwin_arm64" sha256)",
         intel:        "$(field "$darwin_amd64" sha256)",
         arm64_linux:  "$(field "$linux_arm64" sha256)",
         x86_64_linux: "$(field "$linux_amd64" sha256)"

  on_macos do
    url "https://github.com/$release_repo/releases/download/v#{version}/Rune-v#{version}-darwin-#{arch}.dmg",
        verified: "github.com/$release_repo/"

    depends_on macos: :ventura

    app "Rune.app"
    binary "#{appdir}/Rune.app/Contents/MacOS/rune"

    zap trash: [
      "~/.rune",
      "~/Library/Caches/rune",
      "~/Library/Preferences/dev.rune.plist",
    ]
  end
  on_linux do
    url "https://github.com/$release_repo/releases/download/v#{version}/rune-v#{version}-linux-#{arch}.tar.gz",
        verified: "github.com/$release_repo/"

    binary "rune.app/bin/rune"

    zap trash: [
      "~/.cache/rune",
      "~/.rune",
    ]
  end

  name "Rune"
  desc "Fast, GPU-accelerated, full-featured IDE and terminal multiplexer"
  homepage "https://rune.build/"

  livecheck do
    url :url
    strategy :github_latest
  end

  auto_updates true
end
EOF
