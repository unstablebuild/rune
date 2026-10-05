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

//go:build linux && e2e

package ideupgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
)

// makeTarGz returns the bytes of a gzipped tar containing the given
// path -> content map (mode 0o755 for everything).
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for path, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     path,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestLinuxE2E_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping linux e2e in short mode")
	}
	const marker = "rune-linux-e2e"
	tarData := makeTarGz(t, map[string]string{
		"bin/rune":          "#!/bin/sh\necho " + marker + "\n",
		"share/rune/README": "hello",
	})
	sum := sha256.Sum256(tarData)

	manifestArch := runtime.GOOS + "-" + runtime.GOARCH
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/rune.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(tarData)
	})
	mux.HandleFunc("/manifest-"+manifestArch+".json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Manifest{
			Version:  "v0.42.1",
			OS:       runtime.GOOS,
			Arch:     runtime.GOARCH,
			Filename: "rune.tar.gz",
			URL:      srv.URL + "/rune.tar.gz",
			SHA256:   hex.EncodeToString(sum[:]),
			Size:     int64(len(tarData)),
		})
	})

	installRoot := t.TempDir()
	cliRoot := t.TempDir()
	cacheRoot := t.TempDir()

	// Seed the existing install so detectRunningInstall accepts the
	// launching binary path.
	existingBinDir := filepath.Join(installRoot, "rune.app", "bin")
	require.NoError(t, os.MkdirAll(existingBinDir, 0o755))
	existingBinary := filepath.Join(existingBinDir, "rune")
	require.NoError(t, os.WriteFile(existingBinary,
		[]byte("#!/bin/sh\necho old\n"), 0o755))

	mgr, err := newWithPlatformOps(Config{
		CurrentVersion:   "v0.42.0",
		Arch:             manifestArch,
		ManifestURL:      srv.URL,
		Storage:          storagestub.NewInMemoryService(),
		HTTPClient:       srv.Client(),
		Executable:       func() (string, error) { return existingBinary, nil },
		InstallRoot:      installRoot,
		AppName:          "rune.app",
		CLISymlinkPath:   filepath.Join(cliRoot, "rune"),
		CLIBinaryRelPath: filepath.Join("bin", "rune"),
		CacheDir:         cacheRoot,
		BackupRetention:  1,
		Notifications:    &recordingNotifications{},
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	}, linuxPlatformOps{httpClient: srv.Client()})
	require.NoError(t, err)

	manifest, _, err := mgr.fetchManifest(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v0.42.1", manifest.Version)

	require.NoError(t, mgr.Upgrade(context.Background(), manifest, nil))

	got, err := os.ReadFile(filepath.Join(installRoot, "rune.app", "bin", "rune"))
	require.NoError(t, err)
	require.Contains(t, string(got), marker)

	target, err := os.Readlink(filepath.Join(cliRoot, "rune"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(installRoot, "rune.app", "bin", "rune"), target)
}

func TestLinuxE2E_RealReleaseTarballLayout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping linux e2e in short mode")
	}
	const marker = "rune-linux-e2e-nested"
	tarData := makeTarGz(t, map[string]string{
		"rune.app/bin/rune":                        "#!/bin/sh\necho " + marker + "\n",
		"rune.app/share/applications/rune.desktop": "[Desktop Entry]\nExec=rune\n",
		"rune.app/share/rune/README":               "hello",
	})
	sum := sha256.Sum256(tarData)

	manifestArch := runtime.GOOS + "-" + runtime.GOARCH
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/rune.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(tarData)
	})
	mux.HandleFunc("/manifest-"+manifestArch+".json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Manifest{
			Version:  "v0.42.1",
			OS:       runtime.GOOS,
			Arch:     runtime.GOARCH,
			Filename: "rune.tar.gz",
			URL:      srv.URL + "/rune.tar.gz",
			SHA256:   hex.EncodeToString(sum[:]),
			Size:     int64(len(tarData)),
		})
	})

	installRoot := t.TempDir()
	cliRoot := t.TempDir()
	cacheRoot := t.TempDir()

	existingBinDir := filepath.Join(installRoot, "rune.app", "bin")
	require.NoError(t, os.MkdirAll(existingBinDir, 0o755))
	existingBinary := filepath.Join(existingBinDir, "rune")
	require.NoError(t, os.WriteFile(existingBinary,
		[]byte("#!/bin/sh\necho old\n"), 0o755))

	cliSymlink := filepath.Join(cliRoot, "rune")
	require.NoError(t, os.Symlink(existingBinary, cliSymlink))

	mgr, err := newWithPlatformOps(Config{
		CurrentVersion:   "v0.42.0",
		Arch:             manifestArch,
		ManifestURL:      srv.URL,
		Storage:          storagestub.NewInMemoryService(),
		HTTPClient:       srv.Client(),
		Executable:       func() (string, error) { return existingBinary, nil },
		InstallRoot:      installRoot,
		AppName:          "rune.app",
		CLISymlinkPath:   cliSymlink,
		CLIBinaryRelPath: filepath.Join("bin", "rune"),
		CacheDir:         cacheRoot,
		BackupRetention:  1,
		Notifications:    &recordingNotifications{},
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	}, linuxPlatformOps{httpClient: srv.Client()})
	require.NoError(t, err)

	manifest, _, err := mgr.fetchManifest(context.Background())
	require.NoError(t, err)
	require.NoError(t, mgr.Upgrade(context.Background(), manifest, nil))

	// New binary lands at <install>/rune.app/bin/rune, never nested.
	got, err := os.ReadFile(filepath.Join(installRoot, "rune.app", "bin", "rune"))
	require.NoError(t, err)
	require.Contains(t, string(got), marker)

	_, err = os.Stat(filepath.Join(installRoot, "rune.app", "rune.app"))
	require.True(t, os.IsNotExist(err),
		"upgrade must not produce a nested rune.app/rune.app directory")

	_, err = os.Stat(filepath.Join(installRoot, "rune.app.new"))
	require.True(t, os.IsNotExist(err), "staging dir must be cleaned up")

	// CLI symlink (== .desktop Exec= absolute path) resolves to the new
	// binary on disk.
	resolved, err := filepath.EvalSymlinks(cliSymlink)
	require.NoError(t, err)
	wantResolved, err := filepath.EvalSymlinks(filepath.Join(installRoot, "rune.app", "bin", "rune"))
	require.NoError(t, err)
	require.Equal(t, wantResolved, resolved)
}

func TestLinuxE2E_ChecksumMismatchRollsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping linux e2e in short mode")
	}
	tarData := makeTarGz(t, map[string]string{
		"bin/rune": "#!/bin/sh\necho new\n",
	})

	manifestArch := runtime.GOOS + "-" + runtime.GOARCH
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/rune.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tarData)
	})
	mux.HandleFunc("/manifest-"+manifestArch+".json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Manifest{
			Version:  "v2",
			OS:       runtime.GOOS,
			Arch:     runtime.GOARCH,
			Filename: "rune.tar.gz",
			URL:      srv.URL + "/rune.tar.gz",
			SHA256:   strings.Repeat("0", 64),
			Size:     int64(len(tarData)),
		})
	})

	installRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(installRoot, "rune.app", "bin"), 0o755))
	existing := filepath.Join(installRoot, "rune.app", "bin", "rune")
	require.NoError(t, os.WriteFile(existing, []byte("#!/bin/sh\necho old\n"), 0o755))

	mgr, err := newWithPlatformOps(Config{
		CurrentVersion:   "v1",
		Arch:             manifestArch,
		ManifestURL:      srv.URL,
		Storage:          storagestub.NewInMemoryService(),
		HTTPClient:       srv.Client(),
		Executable:       func() (string, error) { return existing, nil },
		InstallRoot:      installRoot,
		AppName:          "rune.app",
		CLISymlinkPath:   filepath.Join(t.TempDir(), "rune"),
		CLIBinaryRelPath: filepath.Join("bin", "rune"),
		CacheDir:         t.TempDir(),
		BackupRetention:  1,
		Notifications:    &recordingNotifications{},
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	}, linuxPlatformOps{httpClient: srv.Client()})
	require.NoError(t, err)

	manifest, _, err := mgr.fetchManifest(context.Background())
	require.NoError(t, err)
	require.Error(t, mgr.Upgrade(context.Background(), manifest, nil))

	// Existing install untouched.
	got, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Contains(t, string(got), "old")
}
