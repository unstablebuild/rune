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

// Package goplstest locates the gopls binary that e2e suites run against.
package goplstest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// PinnedVersion is the gopls release the go language package ships, kept in
// sync with the tools submodule of rune-language-go. The e2e workflow installs
// the version read from this constant.
const PinnedVersion = "v0.23.0"

// Find returns the gopls binary on PATH, or else in ~/.rune/bin or ~/go/bin.
// It skips the test when there is none, and fails it when the binary found is
// not PinnedVersion: a suite run against a gopls the package does not ship
// validates behavior users never get.
func Find(t testing.TB) string {
	t.Helper()
	bin, err := exec.LookPath("gopls")
	if err != nil {
		bin = ""
		for _, p := range []string{
			filepath.Join(os.Getenv("HOME"), ".rune", "bin", "gopls"),
			filepath.Join(os.Getenv("HOME"), "go", "bin", "gopls"),
		} {
			if _, err := os.Stat(p); err == nil {
				bin = p
				break
			}
		}
	}
	if bin == "" {
		t.Skip("gopls not found, skipping e2e test")
	}
	out, err := exec.Command(bin, "version").CombinedOutput()
	require.NoErrorf(t, err, "%s version: %s", bin, out)
	first, _, _ := strings.Cut(string(out), "\n")
	require.Equalf(t, "golang.org/x/tools/gopls "+PinnedVersion, strings.TrimSpace(first),
		"%s diverges from the distributed pin %s; update the pin and e2e "+
			"expectations together", bin, PinnedVersion)
	return bin
}
