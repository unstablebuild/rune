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

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDarwinAppBundlesPinMinimumOSVersion(t *testing.T) {
	repoRoot := repoRootDir(t)
	makefile := readFileString(t, filepath.Join(repoRoot, "cmd", "rune", "Makefile"))

	cases := []struct {
		name      string
		floorVar  string
		plistPath string
	}{
		{
			name:      "arm64",
			floorVar:  "DARWIN_ARM64_MIN_MACOS",
			plistPath: filepath.Join(repoRoot, "extra", "osx", "Rune.app", "Contents", "Info.plist"),
		},
		{
			name:      "amd64",
			floorVar:  "DARWIN_AMD64_MIN_MACOS",
			plistPath: filepath.Join(repoRoot, "extra", "osx", "Info.amd64.plist"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			floor := makefileVar(t, makefile, tc.floorVar)
			plist := readFileString(t, tc.plistPath)
			got := plistString(t, plist, "LSMinimumSystemVersion")
			require.Equalf(t, floor, got,
				"%s LSMinimumSystemVersion must match Makefile %s; "+
					"otherwise the bundle inherits the build SDK's minimum OS",
				tc.name, tc.floorVar)
		})
	}
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoErrorf(t, err, "read %s", path)
	return string(b)
}

// makefileVar returns the value assigned to name via `name ?= value`
// or `name = value`, ignoring surrounding whitespace.
func makefileVar(t *testing.T, makefile, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\s*\??=\s*(\S+)`)
	m := re.FindStringSubmatch(makefile)
	require.Lenf(t, m, 2, "could not find %s assignment in Makefile", name)
	return m[1]
}

// plistString returns the <string> value following the given <key> in
// a property list document.
func plistString(t *testing.T, plist, key string) string {
	t.Helper()
	re := regexp.MustCompile(
		`(?s)<key>` + regexp.QuoteMeta(key) + `</key>\s*<string>(.*?)</string>`)
	m := re.FindStringSubmatch(plist)
	require.Lenf(t, m, 2, "could not find <key>%s</key> in plist", key)
	return m[1]
}
