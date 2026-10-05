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

package idepkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
)

func TestPkgConfigRequirements(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filename string
		data     string
		want     []string
		wantErr  bool
	}{
		{
			name:     "yaml list",
			filename: "config.yaml",
			data:     "requirements:\n  - python\n  - other\n",
			want:     []string{"python", "other"},
		},
		{
			name:     "yaml absent key",
			filename: "config.yaml",
			data:     "settings:\n  a: 1\n",
		},
		{
			name:     "yaml empty file",
			filename: "config.yaml",
			data:     "",
		},
		{
			name:     "star list",
			filename: "config.star",
			data:     `config["requirements"] = ["python"]` + "\n",
			want:     []string{"python"},
		},
		{
			name:     "star absent key",
			filename: "config.star",
			data:     `config["settings"] = {"a": 1}` + "\n",
		},
		{
			name:     "yaml scalar value is malformed",
			filename: "config.yaml",
			data:     "requirements: python\n",
			wantErr:  true,
		},
		{
			name:     "yaml non-string entry is malformed",
			filename: "config.yaml",
			data:     "requirements:\n  - 1\n",
			wantErr:  true,
		},
		{
			name:     "yaml empty string entry is malformed",
			filename: "config.yaml",
			data:     "requirements:\n  - \"\"\n",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := pkgConfigRequirements(
				tt.filename, []byte(tt.data), "pkg", "1", "/data", "modal",
			)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// makeReqPkgTarball builds a gzipped tar carrying the given files, used to
// exercise the `requirements:` install path end to end.
func makeReqPkgTarball(t *testing.T, files map[string]string) []byte {
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

type progressSample struct {
	progress, total int64
	units           string
}

type sampleRecordingProgressWriter struct {
	mu      sync.Mutex
	samples []progressSample
}

func (w *sampleRecordingProgressWriter) Progress(progress, total int64, units string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.samples = append(w.samples, progressSample{progress, total, units})
}

func (w *sampleRecordingProgressWriter) snapshot() []progressSample {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]progressSample(nil), w.samples...)
}

func TestInstallPackageRequirements(t *testing.T) {
	t.Parallel()

	const (
		parentID = "parent"
		depID    = "dep"
	)

	parentTar := func(t *testing.T, requirements string) []byte {
		return makeReqPkgTarball(t, map[string]string{
			"config.yaml": "requirements:\n  - " + requirements + "\n" +
				"settings:\n  parentkey: on\n",
			"lib/parent.txt": "parent\n",
		})
	}
	depTar := func(t *testing.T) []byte {
		return makeReqPkgTarball(t, map[string]string{
			"config.yaml": "settings:\n  depkey: on\n",
			"lib/dep.txt": "dep\n",
		})
	}

	t.Run("requirements install before the dependent package is promoted", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: depID, Latest: "1"})
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: parentID, Version: "1"}},
			[]release.Bundle{{Package: depID, Version: "1"}},
		)
		m, n, rm, _ := newTestManager(t, pkgs, versions)
		rm.SetTarball(parentID, parentTar(t, depID))
		rm.SetTarball(depID, depTar(t))

		err := m.InstallPackageVersion(
			context.Background(), parentID, "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		_, ok := versionInUse(t, m, depID)
		assert.True(t, ok, "requirement must be installed")
		_, ok = versionInUse(t, m, parentID)
		assert.True(t, ok, "dependent package must be installed")

		cfg := readUserConfigMap(t, m.configPath)
		assert.NotContains(t, cfg, "requirements",
			"requirements is package metadata and must not merge into the user config")
		settings, ok := cfg["settings"].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, settings, "parentkey")
		assert.Contains(t, settings, "depkey",
			"requirement config must be merged as part of the dependent install")
	})

	// Notification writers auto-dismiss when progress == total
	// (notifications.Container.UpdateProgress), so only the outermost
	// install may emit a terminal sample; a requirement's "done" on the
	// shared writer would close the dependent install's notification
	// while it is still promoting and merging config.
	t.Run("requirement install emits no terminal progress sample", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: depID, Latest: "1"})
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: parentID, Version: "1"}},
			[]release.Bundle{{Package: depID, Version: "1"}},
		)
		m, n, rm, _ := newTestManager(t, pkgs, versions)
		rm.SetTarball(parentID, parentTar(t, depID))
		rm.SetTarball(depID, depTar(t))

		pw := &sampleRecordingProgressWriter{}
		err := m.InstallPackageVersion(context.Background(), parentID, "1", pw)
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		samples := pw.snapshot()
		require.NotEmpty(t, samples)
		last := samples[len(samples)-1]
		assert.Equal(t, last.progress, last.total,
			"the outermost install must end with a terminal sample")
		var labeled bool
		for _, s := range samples[:len(samples)-1] {
			assert.NotEqual(t, s.progress, s.total,
				"no sample before the last may be terminal: %d/%d %q",
				s.progress, s.total, s.units)
			labeled = labeled || strings.Contains(s.units, "("+depID+")")
		}
		assert.True(t, labeled,
			"requirement samples must be labeled with the requirement ID")
	})

	t.Run("already-installed requirement is skipped without contacting the server", func(t *testing.T) {
		t.Parallel()
		// dep's latest release is 2 but only 1 has a bundle: resolving
		// or reinstalling the requirement would fail, so a successful
		// parent install proves the in-use check short-circuits.
		pkgs := idepkgtest.MakePackages(release.Package{Name: depID, Latest: "2"})
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: parentID, Version: "1"}},
			[]release.Bundle{{Package: depID, Version: "1"}},
		)
		m, n, rm, _ := newTestManager(t, pkgs, versions)
		rm.SetTarball(parentID, parentTar(t, depID))
		rm.SetTarball(depID, depTar(t))

		ctx := context.Background()
		require.NoError(t, m.InstallPackageVersion(
			ctx, depID, "1", repl.NopProgressWriter()))
		require.NoError(t, m.InstallPackageVersion(
			ctx, parentID, "1", repl.NopProgressWriter()))
		n.RequireNoErrorNotification()

		version, ok := versionInUse(t, m, depID)
		require.True(t, ok)
		assert.Equal(t, release.Version("1"), version,
			"installed requirement must not be upgraded")
	})

	t.Run("unresolvable requirement aborts the install", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: parentID, Version: "1"}},
		)
		m, _, rm, datadir := newTestManager(t, pkgs, versions)
		rm.SetTarball(parentID, parentTar(t, "missingdep"))

		err := m.InstallPackageVersion(
			context.Background(), parentID, "1", repl.NopProgressWriter())
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrPackageNotFound)

		_, ok := versionInUse(t, m, parentID)
		assert.False(t, ok, "aborted install must not promote the package")

		_, statErr := os.Stat(makeStagingDirname(datadir, parentID, "1"))
		assert.True(t, os.IsNotExist(statErr), "staging dir must be removed")

		var v pkgVersionValue
		key := m.makeDownloadKey(parentID, "1")
		require.Error(t, m.storage.Get(context.Background(), key, &v),
			"storage key must be cleared so a retry can start fresh")

		_, statErr = os.Stat(m.configPath)
		assert.True(t, os.IsNotExist(statErr),
			"no config merge must happen on an aborted install")
	})

	t.Run("failed requirement install aborts the install", func(t *testing.T) {
		t.Parallel()
		// dep resolves to version 1 but has no bundle to download.
		pkgs := idepkgtest.MakePackages(release.Package{Name: depID, Latest: "1"})
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: parentID, Version: "1"}},
		)
		m, _, rm, _ := newTestManager(t, pkgs, versions)
		rm.SetTarball(parentID, parentTar(t, depID))

		err := m.InstallPackageVersion(
			context.Background(), parentID, "1", repl.NopProgressWriter())
		require.Error(t, err)

		_, ok := versionInUse(t, m, parentID)
		assert.False(t, ok, "aborted install must not promote the package")
	})

	t.Run("cyclic requirements do not deadlock", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(
			release.Package{Name: parentID, Latest: "1"},
			release.Package{Name: depID, Latest: "1"},
		)
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: parentID, Version: "1"}},
			[]release.Bundle{{Package: depID, Version: "1"}},
		)
		m, n, rm, _ := newTestManager(t, pkgs, versions)
		rm.SetTarball(parentID, makeReqPkgTarball(t, map[string]string{
			"config.yaml":    "requirements:\n  - " + depID + "\n",
			"lib/parent.txt": "parent\n",
		}))
		rm.SetTarball(depID, makeReqPkgTarball(t, map[string]string{
			"config.yaml": "requirements:\n  - " + parentID + "\n",
			"lib/dep.txt": "dep\n",
		}))

		err := m.InstallPackageVersion(
			context.Background(), parentID, "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		_, ok := versionInUse(t, m, parentID)
		assert.True(t, ok)
		_, ok = versionInUse(t, m, depID)
		assert.True(t, ok)
	})

	t.Run("self requirement is ignored", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: parentID, Version: "1"}},
		)
		m, n, rm, _ := newTestManager(t, pkgs, versions)
		rm.SetTarball(parentID, parentTar(t, parentID))

		err := m.InstallPackageVersion(
			context.Background(), parentID, "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		_, ok := versionInUse(t, m, parentID)
		assert.True(t, ok)
	})

	t.Run("star config requirements are stripped from the merged config", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: depID, Latest: "1"})
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: parentID, Version: "1"}},
			[]release.Bundle{{Package: depID, Version: "1"}},
		)
		m, n, rm, _ := newTestManager(t, pkgs, versions)
		rm.SetTarball(parentID, makeReqPkgTarball(t, map[string]string{
			"config.star": `config["requirements"] = ["` + depID + `"]` + "\n" +
				`config["settings"] = {"parentkey": "on"}` + "\n",
			"lib/parent.txt": "parent\n",
		}))
		rm.SetTarball(depID, depTar(t))

		err := m.InstallPackageVersion(
			context.Background(), parentID, "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		_, ok := versionInUse(t, m, depID)
		assert.True(t, ok, "star-declared requirement must be installed")

		cfg := readUserConfigMap(t, m.configPath)
		assert.NotContains(t, cfg, "requirements")
		settings, ok := cfg["settings"].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, settings, "parentkey")
	})
}
