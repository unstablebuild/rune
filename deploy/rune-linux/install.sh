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

if [ "$(uname)" != Linux ]; then
    echo "Rune's Linux bundle installer requires Linux." >&2
    exit 1
fi
if [ "$#" -ne 2 ]; then
    echo "Usage: $0 <rune.app> <prefix>" >&2
    exit 1
fi

source=$1
prefix=$2
case "$prefix" in
    /*) ;;
    *) echo "Install prefix must be an absolute path." >&2; exit 1 ;;
esac
if [ ! -x "$source/bin/rune" ]; then
    echo "Missing executable: $source/bin/rune" >&2
    exit 1
fi

mkdir -p "$prefix"
stage=$(mktemp -d "$prefix/.rune-install.XXXXXX")
trap 'rm -rf "$stage"' EXIT
trap 'exit 1' HUP INT TERM
cp -R "$source" "$stage/rune.app"

# Exec has both desktop-entry string escaping and command-line quoting.
exec_path=$(printf '%s' "$prefix/rune.app/bin/rune" | sed 's/[\\"`$]/\\&/g; s/\\/\\\\/g; s/%/%%/g')
while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
        Exec=*) printf 'Exec="%s" %%F\n' "$exec_path" ;;
        *) printf '%s\n' "$line" ;;
    esac
done < "$stage/rune.app/share/applications/rune.desktop" > "$stage/rune.desktop"

# Replace rather than overwrite the running executable, and discard stale libs.
rm -rf "$prefix/rune.app"
mv "$stage/rune.app" "$prefix/rune.app"
mkdir -p "$prefix/bin" "$prefix/share/applications"
ln -sfnT "$prefix/rune.app/bin/rune" "$prefix/bin/rune"
install -m 644 "$stage/rune.desktop" "$prefix/share/applications/rune.desktop"
for size in 512x512 1024x1024; do
    icons="$prefix/share/icons/hicolor/$size/apps"
    mkdir -p "$icons"
    install -m 644 "$prefix/rune.app/share/icons/hicolor/$size/apps/rune.png" "$icons/rune.png"
done

if command -v update-desktop-database >/dev/null 2>&1; then
    update-desktop-database "$prefix/share/applications" || echo "Warning: could not refresh desktop database." >&2
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
    gtk-update-icon-cache -f -t "$prefix/share/icons/hicolor" || echo "Warning: could not refresh icon cache." >&2
fi
printf 'Installed Rune to %s/rune.app\nAdd %s/bin to PATH if needed.\n' "$prefix" "$prefix"