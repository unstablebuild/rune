#!/bin/bash
# Test the real publisher without making a network upload.
set -eu

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/runectl-dist-version-XXXXXX")"
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/repo" "$work/fakebin" "$work/bundle/bin" "$work/config"
(
	cd "$work/repo"
	git init -q
	git config user.name tester
	git config user.email tester@example.com
	git config commit.gpgSign false
	git remote add origin https://example.com/rune.git
	git commit -q --allow-empty -m seed
	git tag v0.14.0
)
echo binary >"$work/bundle/bin/runectl"
tar -czf "$work/runectl.tar.gz" -C "$work/bundle" .

cat >"$work/fakebin/bluectl" <<'EOF'
#!/bin/bash
printf '%s\n' "$@" >"$BLUECTL_TEST_ARGS"
EOF
chmod +x "$work/fakebin/bluectl"

export BLUECTL_TEST_ARGS="$work/upload-args"
export BLUECTL_CONFIG_DIR="$work/config"
export BLUE_RELEASE_TAR="$work/runectl.tar.gz"
export BLUE_TARGET_OS=linux
export BLUE_TARGET_ARCH=amd64
export BLUE_PGP_KEY=test-key
export BLUE_PGP_KEYRING="$work/keyring.gpg"

# A missing version must fail before invoking bluectl, even with a valid Rune tag.
if (cd "$work/repo" && unset RUNECTL_VERSION && PATH="$work/fakebin:$PATH" bash "$repo_root/cmd/runectl/dist.sh" >"$work/missing-output" 2>&1); then
	echo "FAIL: missing RUNECTL_VERSION was accepted" >&2
	exit 1
fi
if [[ -e "$BLUECTL_TEST_ARGS" ]]; then
	echo "FAIL: bluectl ran without RUNECTL_VERSION" >&2
	exit 1
fi
if ! grep -q 'RUNECTL_VERSION' "$work/missing-output"; then
	echo "FAIL: missing version error did not explain RUNECTL_VERSION" >&2
	exit 1
fi

# The package version is independent of the Rune tag; metadata still records
# the actual Rune source tag and selected target platform.
(cd "$work/repo" && RUNECTL_VERSION=v1.1.3 PATH="$work/fakebin:$PATH" bash "$repo_root/cmd/runectl/dist.sh")
args=()
while IFS= read -r arg; do
	args+=("$arg")
done <"$BLUECTL_TEST_ARGS"
contains_arg() {
	local arg
	for arg in "${args[@]}"; do
		[[ "$arg" == "$1" ]] && return 0
	done
	return 1
}
count=${#args[@]}
if [[ "${args[0]}" != '-c' || "${args[1]}" != "$BLUECTL_CONFIG_DIR" || "${args[2]}" != 'release' || "${args[3]}" != 'upload' ]] ||
	[[ "${args[$((count - 3))]}" != 'runectl' || "${args[$((count - 2))]}" != 'v1.1.3' || "${args[$((count - 1))]}" != "$BLUE_RELEASE_TAR" ]] ||
	! contains_arg 'git-tag=v0.14.0' || ! contains_arg 'target-os=linux' || ! contains_arg 'target-arch=amd64'; then
	printf 'FAIL: wrong upload arguments: %s\n' "${args[*]}" >&2
	exit 1
fi

echo 'PASS: runectl version is explicit and independent of the Rune tag.'
