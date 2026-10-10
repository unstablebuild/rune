#!/bin/sh
# Copyright (C) 2017-2026 The Rune Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# This program is free software: you can redistribute it and/or modify
# it under the terms of the GNU General Public License as published by
# the Free Software Foundation, either version 3 of the License, or (at
# your option) any later version.
#
# This program is distributed in the hope that it will be useful, but
# WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
# General Public License for more details.
#
# You should have received a copy of the GNU General Public License
# along with this program. If not, see <https://www.gnu.org/licenses/>.

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' HUP INT TERM

# Keep cache refreshes isolated from the host desktop environment.
mkdir -p "$work/tools"
for tool in update-desktop-database gtk-update-icon-cache; do
    printf '#!/bin/sh\nexit 0\n' > "$work/tools/$tool"
    chmod +x "$work/tools/$tool"
done
export PATH="$work/tools:$PATH"

bundle="$work/source/rune.app"
prefix="$work/install space/.local"
mkdir -p "$bundle/bin" "$bundle/lib" "$bundle/share/applications"
printf '#!/bin/sh\nprintf "stub rune\\n"\n' > "$bundle/bin/rune"
chmod +x "$bundle/bin/rune"
cp "$root/deploy/rune-linux/rune.desktop" "$bundle/share/applications/"
for size in 512x512 1024x1024; do
    mkdir -p "$bundle/share/icons/hicolor/$size/apps"
    printf 'icon\n' > "$bundle/share/icons/hicolor/$size/apps/rune.png"
done
printf 'library\n' > "$bundle/lib/stale.so"

sh "$root/deploy/rune-linux/install.sh" "$bundle" "$prefix"
test "$("$prefix/bin/rune")" = 'stub rune'
test "$(readlink "$prefix/bin/rune")" = "$prefix/rune.app/bin/rune"
grep -Fx "Exec=\"$prefix/rune.app/bin/rune\" %F" "$prefix/share/applications/rune.desktop"
for size in 512x512 1024x1024; do
    cmp "$bundle/share/icons/hicolor/$size/apps/rune.png" "$prefix/share/icons/hicolor/$size/apps/rune.png"
done

rm "$bundle/lib/stale.so"
printf 'unrelated\n' > "$prefix/share/applications/other.desktop"
sh "$root/deploy/rune-linux/install.sh" "$bundle" "$prefix"
test ! -e "$prefix/rune.app/lib/stale.so"
test -f "$prefix/share/applications/other.desktop"
if sh "$root/deploy/rune-linux/install.sh" "$work/missing" "$prefix"; then
    echo 'FAIL: missing bundle accepted' >&2
    exit 1
fi
test "$("$prefix/bin/rune")" = 'stub rune'
if sh "$root/deploy/rune-linux/install.sh" "$bundle" relative; then
    echo 'FAIL: relative prefix accepted' >&2
    exit 1
fi

# A literal percent must not become a desktop-entry field code.
sh "$root/deploy/rune-linux/install.sh" "$bundle" "$work/100% local"
grep -Fx "Exec=\"$work/100%% local/rune.app/bin/rune\" %F" "$work/100% local/share/applications/rune.desktop"

# Dry runs traverse the actual recursive Make targets, without a Go build.
for arch in amd64 arm64; do
    for target in rune-app-linux rune-install-linux; do
        make -n -C "$root" "$target" GO=true BUILD_DATE=2026-01-01T00:00:00Z \
            UNAME=Linux HOST_ARCH="$arch" PREFIX="$prefix" > "$work/make-output"
        grep -F "GOOS=linux GOARCH=$arch" "$work/make-output"
        grep -F "target/rune_linux_$arch/rune.app/bin/rune" "$work/make-output"
        if grep -F 'docker ' "$work/make-output"; then
            echo 'FAIL: local target invokes Docker' >&2
            exit 1
        fi
        if [ "$target" = rune-install-linux ]; then
            grep -F "install.sh\" \"$root/target/rune_linux_$arch/rune.app\" \"$prefix\"" "$work/make-output"
        fi
    done
done
echo 'PASS: Linux local build/install'