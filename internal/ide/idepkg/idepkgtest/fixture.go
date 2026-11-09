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

package idepkgtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
)

// MakePackages makes a map[string]release.Package from the following list of pkgs.
func MakePackages(pkgs ...release.Package) map[string]release.Package {
	ret := make(map[string]release.Package)
	for _, pkg := range pkgs {
		ret[pkg.Name] = pkg
	}
	return ret
}

// MakeBundles makes a map[string][]release.Bundle from the following list of bundles.
func MakeBundles(bundles ...[]release.Bundle) map[string][]release.Bundle {
	ret := make(map[string][]release.Bundle)
	for _, bs := range bundles {
		ret[bs[0].Package] = bs
	}
	return ret
}

// MakeTarball makes the gzipped tarball of a package bundle holding
// files, keyed by their path in the bundle.
func MakeTarball(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	return buf.Bytes()
}
