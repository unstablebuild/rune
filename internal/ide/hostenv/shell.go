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

package hostenv

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	// ShellFragment is the POSIX shell file, in the shell rc directory,
	// that re-applies Rune's environment. bash and zsh source it after the
	// user's startup files.
	ShellFragment = "env.sh"
	// FishFragment is the fish counterpart of ShellFragment.
	FishFragment = "env.fish"
)

type envVar struct {
	name, value string
}

// shellName matches the variable names every supported shell can assign.
var shellName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// posixPathFunctions edit PATH with plain parameter expansion rather than
// word splitting, which zsh does not do on $PATH.
const posixPathFunctions = `__rune_path_prepend() {
	if [ -z "${PATH}" ]; then
		PATH=$1
		return
	fi
	__rune_rest=$PATH
	__rune_out=$1
	while :; do
		__rune_entry=${__rune_rest%%:*}
		[ "$__rune_entry" = "$1" ] || __rune_out=$__rune_out:$__rune_entry
		case $__rune_rest in
		*:*) __rune_rest=${__rune_rest#*:} ;;
		*) break ;;
		esac
	done
	PATH=$__rune_out
}
__rune_path_append() {
	case ":${PATH}:" in
	*":$1:"*) ;;
	*) PATH=${PATH:+$PATH:}$1 ;;
	esac
}
`

const fragmentHeader = "# Rune's environment on this host, which Rune terminals apply after the\n" +
	"# shell's own startup files. Rune rewrites this file; do not edit it.\n"

func writeShellFragments(dir string, vars []envVar, prefix, suffix []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeFileIfChanged(filepath.Join(dir, ShellFragment),
		posixFragment(vars, prefix, suffix)); err != nil {
		return err
	}
	return writeFileIfChanged(filepath.Join(dir, FishFragment),
		fishFragment(vars, prefix, suffix))
}

func posixFragment(vars []envVar, prefix, suffix []string) []byte {
	var b strings.Builder
	b.WriteString(fragmentHeader)
	for _, v := range vars {
		if shellName.MatchString(v.name) {
			b.WriteString("export " + v.name + "=" + posixQuote(v.value) + "\n")
		}
	}
	b.WriteString(posixPathFunctions)
	for _, dir := range slices.Backward(prefix) {
		b.WriteString("__rune_path_prepend " + posixQuote(dir) + "\n")
	}
	for _, dir := range suffix {
		b.WriteString("__rune_path_append " + posixQuote(dir) + "\n")
	}
	b.WriteString("export PATH\n" +
		"unset -f __rune_path_prepend __rune_path_append\n" +
		"unset __rune_rest __rune_out __rune_entry\n")
	return []byte(b.String())
}

func fishFragment(vars []envVar, prefix, suffix []string) []byte {
	var b strings.Builder
	b.WriteString(fragmentHeader)
	for _, v := range vars {
		if shellName.MatchString(v.name) {
			b.WriteString("set -gx " + v.name + " " + fishQuote(v.value) + "\n")
		}
	}
	quoted := func(dirs []string) string {
		var out []string
		for _, d := range dirs {
			out = append(out, fishQuote(d))
		}
		return strings.Join(out, " ")
	}
	// The block scopes the loop variables. fish_add_path is not used because
	// it skips directories that do not exist yet, unlike the POSIX fragment.
	b.WriteString("begin\n")
	if len(prefix) > 0 {
		b.WriteString("    for __rune_dir in " + quoted(reversed(prefix)) + "\n" +
			"        set -l __rune_rest\n" +
			"        for __rune_entry in $PATH\n" +
			"            test \"$__rune_entry\" = \"$__rune_dir\"; or set -a __rune_rest $__rune_entry\n" +
			"        end\n" +
			"        set -gx PATH $__rune_dir $__rune_rest\n" +
			"    end\n")
	}
	if len(suffix) > 0 {
		b.WriteString("    for __rune_dir in " + quoted(suffix) + "\n" +
			"        contains -- $__rune_dir $PATH; or set -gx -a PATH $__rune_dir\n" +
			"    end\n")
	}
	b.WriteString("end\n")
	return []byte(b.String())
}

func reversed(s []string) []string {
	out := slices.Clone(s)
	slices.Reverse(out)
	return out
}

func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func fishQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

func writeFileIfChanged(name string, data []byte) error {
	if got, err := os.ReadFile(name); err == nil && bytes.Equal(got, data) {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) //nolint:errcheck
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}
