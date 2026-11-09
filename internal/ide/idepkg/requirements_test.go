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
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"unstable.build/rune/internal/debug"
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
				tt.filename, []byte(tt.data), "pkg", "1", "modal",
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

func TestInstallPackageRequirements(t *testing.T) {
	t.Parallel()

	const (
		parentID = "parent"
		depID    = "dep"
		leafID   = "leaf"
	)

	parentTar := func(t *testing.T, requirements string) []byte {
		return idepkgtest.MakeTarball(t, map[string]string{
			"config.yaml": "requirements:\n  - " + requirements + "\n" +
				"settings:\n  parentkey: on\n",
			"lib/parent.txt": "parent\n",
		})
	}
	depTar := func(t *testing.T) []byte {
		return idepkgtest.MakeTarball(t, map[string]string{
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

	t.Run("requirements are brought to their latest version", func(t *testing.T) {
		t.Parallel()
		install := func(version release.Version) func(*testing.T, *Manager) {
			return func(t *testing.T, m *Manager) {
				require.NoError(t, m.InstallPackageVersion(
					context.Background(), depID, version, repl.NopProgressWriter()))
			}
		}
		tests := []struct {
			name         string
			latest       release.Version
			depVersions  []release.Version
			setup        []func(*testing.T, *Manager)
			noReporter   bool
			wantErr      bool
			wantDep      release.Version
			wantReported []RequirementChange
			wantNotices  []string
		}{
			{
				name:        "missing requirement is installed and reported",
				latest:      "1",
				depVersions: []release.Version{"1"},
				wantDep:     "1",
				wantReported: []RequirementChange{
					{Package: depID, To: "1", RequiredBy: parentID},
				},
			},
			{
				name:        "outdated requirement is upgraded and reported",
				latest:      "2",
				depVersions: []release.Version{"1", "2"},
				setup:       []func(*testing.T, *Manager){install("1")},
				wantDep:     "2",
				wantReported: []RequirementChange{
					{Package: depID, From: "1", To: "2", RequiredBy: parentID},
				},
			},
			{
				name:        "without a reporter the upgrade is a notification",
				latest:      "2",
				depVersions: []release.Version{"1", "2"},
				setup:       []func(*testing.T, *Manager){install("1")},
				noReporter:  true,
				wantDep:     "2",
				wantNotices: []string{"Upgraded dep from 1 to 2, required by parent."},
			},
			{
				name:        "requirement at latest is left in place",
				latest:      "1",
				depVersions: []release.Version{"1"},
				setup:       []func(*testing.T, *Manager){install("1")},
				wantDep:     "1",
			},
			{
				name:        "pinned requirement is not switched",
				latest:      "2",
				depVersions: []release.Version{"1", "2"},
				setup: []func(*testing.T, *Manager){
					install("1"),
					install("2"),
					func(t *testing.T, m *Manager) {
						require.NoError(t, m.UsePackageVersion(context.Background(), depID, "1"))
					},
				},
				wantDep: "1",
			},
			{
				// dep's latest release is 2 but only 1 has a bundle.
				name:        "failed upgrade aborts the install",
				latest:      "2",
				depVersions: []release.Version{"1"},
				setup:       []func(*testing.T, *Manager){install("1")},
				wantErr:     true,
				wantDep:     "1",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				pkgs := idepkgtest.MakePackages(release.Package{Name: depID, Latest: tt.latest})
				depBundles := make([]release.Bundle, len(tt.depVersions))
				for i, v := range tt.depVersions {
					depBundles[i] = release.Bundle{Package: depID, Version: v}
				}
				versions := idepkgtest.MakeBundles(
					[]release.Bundle{{Package: parentID, Version: "1"}},
					depBundles,
				)
				m, n, rm, _ := newTestManager(t, pkgs, versions)
				rm.SetTarball(parentID, parentTar(t, depID))
				rm.SetTarball(depID, depTar(t))
				for _, setup := range tt.setup {
					setup(t, m)
				}
				n.Reset()

				reporter := newReportingProgressWriter()
				var pw repl.ProgressWriter = reporter
				if tt.noReporter {
					pw = repl.NopProgressWriter()
				}
				err := m.InstallPackageVersion(context.Background(), parentID, "1", pw)
				if tt.wantErr {
					require.Error(t, err)
					_, ok := versionInUse(t, m, parentID)
					assert.False(t, ok, "aborted install must not promote the package")
				} else {
					require.NoError(t, err)
					n.RequireNoErrorNotification()
				}

				version, ok := versionInUse(t, m, depID)
				require.True(t, ok)
				assert.Equal(t, tt.wantDep, version)
				assert.Equal(t, tt.wantReported, reporter.reported())
				assert.Equal(t, tt.wantNotices, requirementNotices(n))
			})
		}
	})

	t.Run("a requirement of a requirement reaches the caller's reporter", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(
			release.Package{Name: depID, Latest: "1"},
			release.Package{Name: leafID, Latest: "1"},
		)
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: parentID, Version: "1"}},
			[]release.Bundle{{Package: depID, Version: "1"}},
			[]release.Bundle{{Package: leafID, Version: "1"}},
		)
		m, n, rm, _ := newTestManager(t, pkgs, versions)
		rm.SetTarball(parentID, parentTar(t, depID))
		rm.SetTarball(depID, idepkgtest.MakeTarball(t, map[string]string{
			"config.yaml": "requirements:\n  - " + leafID + "\n",
			"lib/dep.txt": "dep\n",
		}))
		rm.SetTarball(leafID, idepkgtest.MakeTarball(t, map[string]string{
			"lib/leaf.txt": "leaf\n",
		}))

		pw := newReportingProgressWriter()
		require.NoError(t, m.InstallPackageVersion(context.Background(), parentID, "1", pw))
		n.RequireNoErrorNotification()

		assert.Equal(t, []RequirementChange{
			{Package: leafID, To: "1", RequiredBy: depID},
			{Package: depID, To: "1", RequiredBy: parentID},
		}, pw.reported())
		assert.Empty(t, requirementNotices(n))
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
		rm.SetTarball(parentID, idepkgtest.MakeTarball(t, map[string]string{
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

func TestInstallRequirementCycles(t *testing.T) {
	t.Parallel()
	pair := map[string]pkgSpec{"a": requiring("b"), "b": requiring("a")}
	triangle := map[string]pkgSpec{"a": requiring("b"), "b": requiring("c"), "c": requiring("a")}

	t.Run("one install", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name         string
			specs        map[string]pkgSpec
			wantReported []RequirementChange
		}{
			{
				name:         "two packages",
				specs:        pair,
				wantReported: []RequirementChange{{Package: "b", To: "1", RequiredBy: "a"}},
			},
			{
				name:  "three packages",
				specs: triangle,
				wantReported: []RequirementChange{
					{Package: "c", To: "1", RequiredBy: "b"},
					{Package: "b", To: "1", RequiredBy: "a"},
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				m, n, _ := newSpecManager(t, tt.specs)
				pw := newReportingProgressWriter()
				require.NoError(t, awaitInstall(t, installAsync(m, "a", "1", pw)))
				n.RequireNoErrorNotification()

				assertAllInUse(t, m, tt.specs)
				assert.Equal(t, tt.wantReported, pw.reported())
				assertNoInstallInFlight(t, m)
			})
		}
	})

	t.Run("concurrent installs", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name         string
			specs        map[string]pkgSpec
			installs     []string
			wantReported map[string][]RequirementChange
		}{
			{
				name:     "two installs of two packages",
				specs:    pair,
				installs: []string{"a", "b"},
			},
			{
				name:     "three installs of three packages",
				specs:    triangle,
				installs: []string{"a", "b", "c"},
			},
			{
				// Only c is not in flight in an install of its own.
				name:     "two installs of three packages",
				specs:    triangle,
				installs: []string{"a", "b"},
				wantReported: map[string][]RequirementChange{
					"b": {{Package: "c", To: "1", RequiredBy: "b"}},
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				held := make([]idepkgtest.Download, len(tt.installs))
				for i, pkgID := range tt.installs {
					held[i] = download(pkgID, "1")
				}
				m, n, rm := newSpecManager(t, tt.specs, held...)
				pws := make(map[string]*reportingProgressWriter)
				dones := make(map[string]<-chan error)
				for _, pkgID := range tt.installs {
					pws[pkgID] = newReportingProgressWriter()
					dones[pkgID] = installAsync(m, pkgID, "1", pws[pkgID])
				}
				// Every install is in flight before any of them reads its
				// requirements.
				for _, d := range held {
					rm.AwaitStarted(t, d)
				}
				for _, d := range held {
					rm.Release(d, nil)
				}
				for pkgID, done := range dones {
					require.NoError(t, awaitInstall(t, done), pkgID)
				}
				n.RequireNoErrorNotification()

				assertAllInUse(t, m, tt.specs)
				for pkgID, pw := range pws {
					assert.Equal(t, tt.wantReported[pkgID], pw.reported(), pkgID)
				}
				assertNoInstallInFlight(t, m)
			})
		}
	})
}

func TestInstallWaitsForRequirementsInFlight(t *testing.T) {
	t.Parallel()
	errDownload := errors.New("connection reset")
	dep := func(versions ...release.Version) pkgSpec {
		return pkgSpec{latest: "2", versions: versions}
	}
	tests := []struct {
		name  string
		specs map[string]pkgSpec
		// first is installed first, and stays in flight at the download
		// of held until every waiter waits.
		first, held idepkgtest.Download
		releaseErr  error
		// waiters are installed at version 1 one at a time, each once
		// the previous one waits.
		waiters      []string
		wantErr      []string
		wantInUse    map[string]release.Version
		wantReported map[string][]RequirementChange
	}{
		{
			name:      "the other install brings the latest version",
			specs:     map[string]pkgSpec{"parent": requiring("dep"), "dep": dep("1", "2")},
			first:     download("dep", "2"),
			held:      download("dep", "2"),
			waiters:   []string{"parent"},
			wantInUse: map[string]release.Version{"parent": "1", "dep": "2"},
		},
		{
			name:      "the older version the other install brings is upgraded",
			specs:     map[string]pkgSpec{"parent": requiring("dep"), "dep": dep("1", "2")},
			first:     download("dep", "1"),
			held:      download("dep", "1"),
			waiters:   []string{"parent"},
			wantInUse: map[string]release.Version{"parent": "1", "dep": "2"},
			wantReported: map[string][]RequirementChange{
				"parent": {{Package: "dep", From: "1", To: "2", RequiredBy: "parent"}},
			},
		},
		{
			name: "the requirement is in flight as the requirement of another install",
			specs: map[string]pkgSpec{
				"parent": requiring("dep"), "other": requiring("dep"), "dep": dep("2"),
			},
			first:     download("other", "1"),
			held:      download("dep", "2"),
			waiters:   []string{"parent"},
			wantInUse: map[string]release.Version{"parent": "1", "other": "1", "dep": "2"},
			wantReported: map[string][]RequirementChange{
				"other": {{Package: "dep", To: "2", RequiredBy: "other"}},
			},
		},
		{
			name: "a chain of installs waiting for each other is not a cycle",
			specs: map[string]pkgSpec{
				"a": requiring("b"), "b": requiring("c"), "c": requiring(),
			},
			first:     download("c", "1"),
			held:      download("c", "1"),
			waiters:   []string{"b", "a"},
			wantInUse: map[string]release.Version{"a": "1", "b": "1", "c": "1"},
		},
		{
			name:       "the waiter installs the requirement the other install failed to",
			specs:      map[string]pkgSpec{"parent": requiring("dep"), "dep": dep("2")},
			first:      download("dep", "2"),
			held:       download("dep", "2"),
			releaseErr: errDownload,
			waiters:    []string{"parent"},
			wantErr:    []string{"dep"},
			wantInUse:  map[string]release.Version{"parent": "1", "dep": "2"},
			wantReported: map[string][]RequirementChange{
				"parent": {{Package: "dep", To: "2", RequiredBy: "parent"}},
			},
		},
		{
			name:       "the waiter fails when it cannot install the requirement either",
			specs:      map[string]pkgSpec{"parent": requiring("dep"), "dep": dep()},
			first:      download("dep", "2"),
			held:       download("dep", "2"),
			releaseErr: errDownload,
			waiters:    []string{"parent"},
			wantErr:    []string{"dep", "parent"},
			wantInUse:  map[string]release.Version{"parent": "", "dep": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, n, rm := newSpecManager(t, tt.specs, tt.held)
			pws := make(map[string]*reportingProgressWriter)
			dones := make(map[string]<-chan error)
			install := func(d idepkgtest.Download) {
				pws[d.Package] = newReportingProgressWriter()
				dones[d.Package] = installAsync(m, d.Package, d.Version, pws[d.Package])
			}
			install(tt.first)
			rm.AwaitStarted(t, tt.held)
			for i, pkgID := range tt.waiters {
				install(download(pkgID, "1"))
				awaitWaitingInstalls(t, m, i+1)
			}
			for _, pkgID := range tt.waiters {
				_, ok := versionInUse(t, m, pkgID)
				assert.False(t, ok, "%s must not be promoted before its requirement", pkgID)
			}

			rm.Release(tt.held, tt.releaseErr)
			for pkgID, done := range dones {
				err := awaitInstall(t, done)
				if slices.Contains(tt.wantErr, pkgID) {
					assert.Error(t, err, pkgID)
				} else {
					assert.NoError(t, err, pkgID)
				}
			}
			n.RequireNoErrorNotification()

			for pkgID, want := range tt.wantInUse {
				got, _ := versionInUse(t, m, pkgID)
				assert.Equal(t, want, got, pkgID)
			}
			for pkgID, pw := range pws {
				assert.Equal(t, tt.wantReported[pkgID], pw.reported(), pkgID)
			}
			assertNoInstallInFlight(t, m)
		})
	}
}

func TestInstallOfPackageInFlightReturnsAtOnce(t *testing.T) {
	t.Parallel()
	specs := map[string]pkgSpec{"dep": requiring()}
	m, n, rm := newSpecManager(t, specs, download("dep", "1"))
	first := installAsync(m, "dep", "1", nil)
	rm.AwaitStarted(t, download("dep", "1"))

	require.NoError(t, awaitInstall(t, installAsync(m, "dep", "1", nil)))
	_, ok := versionInUse(t, m, "dep")
	assert.False(t, ok, "the second install must not wait for the first")

	rm.Release(download("dep", "1"), nil)
	require.NoError(t, awaitInstall(t, first))
	n.RequireNoErrorNotification()
	assertAllInUse(t, m, specs)
	assertNoInstallInFlight(t, m)
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

type reportingProgressWriter struct {
	repl.ProgressWriter
	mu      sync.Mutex
	changes []RequirementChange
}

func newReportingProgressWriter() *reportingProgressWriter {
	return &reportingProgressWriter{ProgressWriter: repl.NopProgressWriter()}
}

func (w *reportingProgressWriter) ReportRequirement(c RequirementChange) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.changes = append(w.changes, c)
}

func (w *reportingProgressWriter) reported() []RequirementChange {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]RequirementChange(nil), w.changes...)
}

// requirementNotices returns the notifications that report requirement
// changes, leaving out the config merge notices of the installs.
func requirementNotices(n *idepkgtest.Notifications) []string {
	var notices []string
	for _, msg := range notificationMessages(n) {
		if strings.Contains(msg, ", required by ") {
			notices = append(notices, msg)
		}
	}
	return notices
}

func installAsync(
	m *Manager, pkgID string, version release.Version, pw repl.ProgressWriter,
) <-chan error {
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		done <- m.InstallPackageVersion(context.Background(), pkgID, version, pw)
	})
	return done
}

func awaitInstall(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the install did not end")
		return nil
	}
}

// pkgSpec is a package the release server publishes. Every published
// version requires requirements.
type pkgSpec struct {
	latest       release.Version
	versions     []release.Version
	requirements []string
}

// requiring is a package publishing version 1 only.
func requiring(requirements ...string) pkgSpec {
	return pkgSpec{latest: "1", versions: []release.Version{"1"}, requirements: requirements}
}

func download(pkgID string, version release.Version) idepkgtest.Download {
	return idepkgtest.Download{Package: pkgID, Version: version}
}

// newSpecManager returns a Manager installing the packages of specs from
// a release server that holds the first download of each of held.
func newSpecManager(
	t *testing.T, specs map[string]pkgSpec, held ...idepkgtest.Download,
) (*Manager, *idepkgtest.Notifications, *idepkgtest.HeldReleaseManager) {
	t.Helper()
	var pkgs []release.Package
	var bundles [][]release.Bundle
	for pkgID, s := range specs {
		pkgs = append(pkgs, release.Package{Name: pkgID, Latest: s.latest})
		if len(s.versions) == 0 {
			continue
		}
		bs := make([]release.Bundle, len(s.versions))
		for i, v := range s.versions {
			bs[i] = release.Bundle{Package: pkgID, Version: v}
		}
		bundles = append(bundles, bs)
	}
	m, n, rm, _ := newTestManager(t,
		idepkgtest.MakePackages(pkgs...), idepkgtest.MakeBundles(bundles...))
	for pkgID, s := range specs {
		files := map[string]string{"lib/" + pkgID + ".txt": pkgID + "\n"}
		if len(s.requirements) > 0 {
			files["config.yaml"] = "requirements:\n  - " +
				strings.Join(s.requirements, "\n  - ") + "\n"
		}
		rm.SetTarball(pkgID, idepkgtest.MakeTarball(t, files))
	}
	hrm := idepkgtest.NewHeldReleaseManager(rm, held...)
	m.m = hrm
	return m, n, hrm
}

// awaitWaitingInstalls fails t unless n install trees soon wait for an
// install in flight in another.
func awaitWaitingInstalls(t *testing.T, m *Manager, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		m.iterators.Lock()
		defer m.iterators.Unlock()
		return len(m.iterators.waiting) == n
	}, 10*time.Second, 5*time.Millisecond, "%d installs must wait for a requirement", n)
}

func assertAllInUse(t *testing.T, m *Manager, specs map[string]pkgSpec) {
	t.Helper()
	for pkgID, s := range specs {
		version, ok := versionInUse(t, m, pkgID)
		if assert.True(t, ok, "%s must be in use", pkgID) {
			assert.Equal(t, s.latest, version, pkgID)
		}
	}
}

// assertNoInstallInFlight asserts the install gate let go of every
// install, which would otherwise keep the next install of its package
// from ever starting.
func assertNoInstallInFlight(t *testing.T, m *Manager) {
	t.Helper()
	m.iterators.Lock()
	defer m.iterators.Unlock()
	assert.Empty(t, m.iterators.m, "installs in flight")
	assert.Empty(t, m.iterators.waiting, "installs waiting")
}
