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

package grammarfixture

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/workspace"
)

// Packages is a syntax.PkgManager whose packages are installed by a
// real idepkg.Manager into a temporary data directory, so parsers read
// package files with the layout and lib dir listing of a real install.
type Packages struct {
	m        *idepkg.Manager
	releases *idepkgtest.ReleaseManager
}

// NewPackages returns a package manager with nothing installed whose
// release server offers each package ID at the listed versions.
func NewPackages(t *testing.T, versions map[string][]string) *Packages {
	t.Helper()
	dataDir := t.TempDir()
	uri, err := workspaceapi.ParseURI("file://" + dataDir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	bundles := make(map[string][]release.Bundle, len(versions))
	for pkgID, vs := range versions {
		for _, v := range vs {
			bundles[pkgID] = append(bundles[pkgID],
				release.Bundle{Package: pkgID, Version: release.Version(v)})
		}
	}
	releases := idepkgtest.NewReleaseManager(nil, bundles)
	m := idepkg.NewManager(idepkgtest.NewNotifications(t), releases,
		storagestub.NewInMemoryService(), idepkgtest.TrustStore(), scheme, dataDir,
		filepath.Join(dataDir, "config.yaml"), nil,
		func(fn func()) bool { fn(); return true }, term.NopInterrupter())
	return &Packages{m: m, releases: releases}
}

// LibDir implements syntax.PkgManager.
func (p *Packages) LibDir(ctx context.Context, pkgID string) (iterator.Iterator[string], error) {
	return p.m.LibDir(ctx, pkgID)
}

// Install installs version of pkgID and makes it the version in use.
// files maps slash-separated paths relative to the package root to
// their contents.
func (p *Packages) Install(t *testing.T, pkgID, version string, files map[string][]byte) {
	t.Helper()
	p.releases.SetTarball(pkgID, tarball(t, files))
	require.NoError(t, p.m.InstallPackageVersion(
		context.Background(), pkgID, release.Version(version), nil))
}

// Use makes an installed version of pkgID the version in use.
func (p *Packages) Use(t *testing.T, pkgID, version string) {
	t.Helper()
	require.NoError(t, p.m.UsePackageVersion(
		context.Background(), pkgID, release.Version(version)))
}

// Uninstall removes an installed version of pkgID. Removing the version
// in use leaves pkgID uninstalled.
func (p *Packages) Uninstall(t *testing.T, pkgID, version string) {
	t.Helper()
	require.NoError(t, p.m.DeletePackageVersion(
		context.Background(), pkgID, release.Version(version), true))
}

// Files returns the paths LibDir lists for the version of pkgID in use.
func (p *Packages) Files(t testing.TB, pkgID string) []string {
	t.Helper()
	ctx := context.Background()
	it, err := p.LibDir(ctx, pkgID)
	require.NoError(t, err)
	files, err := iterator.ToSlice(ctx, it)
	_ = it.Close()
	require.NoError(t, err)
	return files
}

// PackageFiles returns the contents of the grammar package built from
// the fixtures in langDir, keyed by file name: the host parser and
// every query file next to it.
func PackageFiles(t testing.TB, langDir string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	for _, path := range LibDir(t, langDir) {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		files[filepath.Base(path)] = data
	}
	return files
}

func tarball(t testing.TB, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	require.NoError(t, err)
	tw := tar.NewWriter(gz)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		data := files[name]
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(data)),
		}))
		_, err := tw.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}
