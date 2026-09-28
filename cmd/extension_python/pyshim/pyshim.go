// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

// Package pyshim owns the venv-aware python/python3 shims the Python
// extension installs into <dataDir>/python/bin. The shims are consumed
// by every Rune terminal (the dir is first on PATH) and by the debugger
// launch template (debugger.python.launch.python), so the writer lives
// in its own package where both the extension and integration tests can
// use it.
package pyshim

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// names are the interpreter entrypoints shadowed by Rune-owned shims
// in Dir(dataDir), which is first on the Rune PATH. uv's own interpreter
// links live in <dataDir>/python/uvbin, which is kept off PATH entirely,
// so the shim can fall back to the managed interpreter without it ever
// shadowing the user's.
var names = []string{"python", "python3"}

// Dir returns the directory the shims are written to.
func Dir(dataDir string) string {
	return path.Join(dataDir, "python", "bin")
}

// FallbackPath returns the uv-managed interpreter the shims exec when
// no venv applies: the `python3` link uv installs into
// UV_PYTHON_BIN_DIR (<dataDir>/python/uvbin per the package config).
func FallbackPath(dataDir string) string {
	return path.Join(dataDir, "python", "uvbin", "python3")
}

// Write installs the venv-aware python/python3 shims under
// Dir(dataDir). It is idempotent and self-healing: every bootstrap
// rewrites the shims, which also migrates existing installs where uv's
// interpreter symlinks used to occupy python/bin.
func Write(fs workspaceapi.FileSystem, dataDir string) error {
	binDir := Dir(dataDir)
	if err := fs.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create shim dir: %w", err)
	}
	body := []byte(script(dataDir))
	for _, name := range names {
		p := path.Join(binDir, name)
		// Remove before create: on upgraded installs the path is a uv
		// symlink into the managed interpreter, and opening through it
		// would truncate the interpreter itself. A failed removal of an
		// existing entry aborts for the same reason.
		if err := fs.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove old %s: %w", p, err)
		}
		f, err := fs.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return fmt.Errorf("create shim %s: %w", p, err)
		}
		_, werr := f.Write(body)
		cerr := f.Close()
		if werr != nil {
			return fmt.Errorf("write shim %s: %w", p, werr)
		}
		if cerr != nil {
			return fmt.Errorf("close shim %s: %w", p, cerr)
		}
	}
	return nil
}

// script renders the POSIX shim body. Resolution order: an activated
// venv, the nearest enclosing project .venv walking up from $PWD (which
// makes launches monorepo-correct per invocation), the user's own
// interpreter on PATH, then the uv-managed interpreter embedded at write
// time. PATH entries under dataDir are skipped: the shim dir is itself
// on PATH, so scanning it would re-enter the shim.
func script(dataDir string) string {
	fallback := shQuote(FallbackPath(dataDir))
	dd := shQuote(dataDir)
	return `#!/bin/sh
# Rune-managed Python shim: prefer the activated venv, then the nearest
# enclosing project venv, then the user's interpreter on PATH, then the
# uv-managed interpreter.
if [ -n "$VIRTUAL_ENV" ] && [ -x "$VIRTUAL_ENV/bin/python" ]; then
	exec "$VIRTUAL_ENV/bin/python" "$@"
fi
d=$PWD
while :; do
	if [ -x "$d/.venv/bin/python" ]; then
		exec "$d/.venv/bin/python" "$@"
	fi
	n=$(dirname "$d")
	[ "$n" = "$d" ] && break
	d=$n
done
datadir=` + dd + `
oldifs=$IFS
IFS=:
for name in python3 python; do
	for p in $PATH; do
		[ -n "$p" ] || continue
		case $p in
		"$datadir" | "$datadir"/*) continue ;;
		esac
		if [ -f "$p/$name" ] && [ -x "$p/$name" ]; then
			IFS=$oldifs
			exec "$p/$name" "$@"
		fi
	done
done
IFS=$oldifs
fallback=` + fallback + `
if [ -x "$fallback" ]; then
	exec "$fallback" "$@"
fi
echo "rune: no Python found; install one or run 'python enable' in Rune" >&2
exit 127
`
}

// shQuote single-quotes s for safe embedding in a POSIX shell script.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
