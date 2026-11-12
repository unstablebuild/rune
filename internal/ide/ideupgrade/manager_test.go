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

package ideupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/handler"
)

func newTestManager(t *testing.T, srv *httptest.Server, current string, now func() time.Time) *Manager {
	t.Helper()
	storage := storagestub.NewInMemoryService()
	cfg := Config{
		CurrentVersion: current,
		Arch:           "darwin-arm64",
		ManifestURL:    srv.URL,
		Storage:        storage,
		HTTPClient:     srv.Client(),
		Now:            now,
		InitialDelay:   time.Millisecond,
		CheckPeriod:    24 * time.Hour,
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	}
	mgr, err := newWithPlatformOps(cfg, &fakePlatformOps{})
	require.NoError(t, err)
	return mgr
}

func TestManagerFetchManifest(t *testing.T) {
	manifest := Manifest{
		Version:  "v1.2.3",
		OS:       "darwin",
		Arch:     "arm64",
		Filename: "Rune-v1.2.3.dmg",
		URL:      "https://example.invalid/Rune-v1.2.3.dmg",
		SHA256:   "abc",
		Size:     42,
	}

	var requested atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested.Add(1)
		require.Equal(t, "/manifest-darwin-arm64.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	t.Cleanup(srv.Close)

	tests := []struct {
		name    string
		current string
		setup   func(*Manager)
		wantHas bool
	}{
		{
			name:    "newer version returns hit",
			current: "v1.0.0",
			wantHas: true,
		},
		{
			name:    "same version is no-op",
			current: "v1.2.3",
			wantHas: false,
		},
		{
			name:    "skipped version is no-op",
			current: "v1.0.0",
			setup: func(m *Manager) {
				m.skipVersion(context.Background(), "v1.2.3")
			},
			wantHas: false,
		},
		{
			name:    "remind_after in future is no-op",
			current: "v1.0.0",
			setup: func(m *Manager) {
				m.remindLater(context.Background())
			},
			wantHas: false,
		},
		{
			name:    "empty current version skips",
			current: "",
			wantHas: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := func() time.Time { return time.Unix(1_700_000_000, 0) }
			mgr := newTestManager(t, srv, tc.current, now)
			if tc.setup != nil {
				tc.setup(mgr)
			}
			got, has, err := mgr.fetchManifest(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.wantHas, has)
			if tc.current != "" && tc.wantHas {
				require.Equal(t, manifest.Version, got.Version)
			}
		})
	}
}

func TestManagerFetchManifestErrors(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantErr    string
		wantHasHit bool
	}{
		{
			name: "404 is treated as no manifest",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.NotFound(w, nil)
			},
			wantErr:    "",
			wantHasHit: false,
		},
		{
			// GCS returns 403 (not 404) when a public-read object
			// does not exist, because the caller doesn't have
			// permission to enumerate the bucket. Treat it the
			// same as 404 so a not-yet-published manifest is a
			// silent "you're up to date" rather than a noisy
			// surfaced error.
			name: "403 is treated as no manifest",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "Forbidden", http.StatusForbidden)
			},
			wantErr:    "",
			wantHasHit: false,
		},
		{
			name: "500 is an error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
			wantErr: "status 500",
		},
		{
			name: "missing required fields is an error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"version":"v9.9.9"}`))
			},
			wantErr: "missing required fields",
		},
		{
			name: "not json at all is a decode error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`not a json document <html>...`))
			},
			wantErr: "decode manifest",
		},
		{
			name: "empty body is a decode error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
			},
			wantErr: "decode manifest",
		},
		{
			name: "truncated json is a decode error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"version":"v9.9.9","url":"https://x/y"`))
			},
			wantErr: "decode manifest",
		},
		{
			name: "wrong field type is a decode error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// `size` should be an int but is a string.
				_, _ = w.Write([]byte(
					`{"version":"v9","url":"u","sha256":"s","size":"big"}`))
			},
			wantErr: "decode manifest",
		},
		{
			name: "missing version is a required-field error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(
					`{"url":"https://x/y","sha256":"abc"}`))
			},
			wantErr: "missing required fields",
		},
		{
			name: "missing url is a required-field error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(
					`{"version":"v9","sha256":"abc"}`))
			},
			wantErr: "missing required fields",
		},
		{
			name: "missing sha256 is a required-field error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(
					`{"version":"v9","url":"https://x/y"}`))
			},
			wantErr: "missing required fields",
		},
		{
			name: "oversized body is truncated and fails to decode",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Open a JSON object then write >1 MiB of padding inside
				// a string value. fetchManifest caps reads at 1 MiB so
				// the body never closes, producing a decode error.
				_, _ = w.Write([]byte(`{"version":"v9","url":"u","sha256":"s","changelog":"`))
				pad := make([]byte, 1<<20)
				for i := range pad {
					pad[i] = 'a'
				}
				_, _ = w.Write(pad)
				_, _ = w.Write([]byte(`"}`))
			},
			wantErr: "decode manifest",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(tc.handler)
			t.Cleanup(srv.Close)
			now := func() time.Time { return time.Unix(1_700_000_000, 0) }
			mgr := newTestManager(t, srv, "v1.0.0", now)
			_, has, err := mgr.fetchManifest(context.Background())
			if tc.wantErr == "" {
				require.NoError(t, err)
				require.Equal(t, tc.wantHasHit, has)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestManagerThrottle(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			`{"version":"v9","os":"darwin","arch":"arm64","filename":"f","url":"https://example.invalid/f","sha256":"s"}`))
	}))
	t.Cleanup(srv.Close)

	now := time.Unix(1_700_000_000, 0)
	clock := now
	mgr := newTestManager(t, srv, "v1", func() time.Time { return clock })
	require.False(t, mgr.isThrottled(context.Background()))

	mgr.persistChecked(context.Background())
	require.True(t, mgr.isThrottled(context.Background()))

	clock = now.Add(25 * time.Hour)
	require.False(t, mgr.isThrottled(context.Background()))
}

func TestManifestEndpoint(t *testing.T) {
	tests := []struct {
		base, arch, want string
	}{
		{"https://github.com/unstablebuild/rune/releases/latest/download",
			"darwin-arm64",
			"https://github.com/unstablebuild/rune/releases/latest/download/manifest-darwin-arm64.json"},
		{"https://github.com/unstablebuild/rune/releases/latest/download/",
			"linux-amd64",
			"https://github.com/unstablebuild/rune/releases/latest/download/manifest-linux-amd64.json"},
		// Any base-path prefix is preserved so callers can host the
		// downloads CDN under a sub-path if needed.
		{"https://example.test/cdn", "linux-arm64",
			"https://example.test/cdn/manifest-linux-arm64.json"},
	}
	for _, tc := range tests {
		t.Run(tc.base+"+"+tc.arch, func(t *testing.T) {
			got, err := manifestEndpoint(tc.base, tc.arch)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)

			// Sanity-check the result is parseable.
			_, err = url.Parse(got)
			require.NoError(t, err)
			require.True(t, strings.HasSuffix(got, "/manifest-"+tc.arch+".json"))
		})
	}
}

func TestNewRequiresFields(t *testing.T) {
	_, err := New(Config{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "Storage")

	_, err = New(Config{Storage: storagestub.NewInMemoryService()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ManifestURL")

	_, err = New(Config{
		Storage:          storagestub.NewInMemoryService(),
		ManifestURL:      "https://example.invalid",
		ScheduleNextTick: func(fn func()) bool { fn(); return true },
		Prompter:         &countingPrompter{},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "Starter")
}

func TestNewRejectsNonHTTPSManifestURL(t *testing.T) {
	_, err := New(Config{
		CurrentVersion: "v1.0.0",
		ManifestURL:    "http://example.com",
		Storage:        storagestub.NewInMemoryService(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "https")
}

func TestNewRejectsHTTPLoopback(t *testing.T) {
	// An attacker with the ability to bind a service on the user's
	// machine (malicious dev tool, hostile port-forward) must not
	// be able to feed Rune a poisoned manifest. There is no
	// loopback exemption; tests stand up TLS servers instead.
	for _, base := range []string{
		"http://127.0.0.1:8080",
		"http://localhost:8080",
		"http://[::1]:8080",
	} {
		t.Run(base, func(t *testing.T) {
			_, err := New(Config{
				CurrentVersion: "v1.0.0",
				ManifestURL:    base,
				Storage:        storagestub.NewInMemoryService(),
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), "https")
		})
	}
}

func TestFetchManifestRejectsNonHTTPSArtifactURL(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"version":"v9.9.9","url":"http://attacker.example/Rune.dmg","sha256":"abc"}`))
	}))
	t.Cleanup(srv.Close)
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	mgr := newTestManager(t, srv, "v1.0.0", now)
	_, _, err := mgr.fetchManifest(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "https")
}

func TestFetchManifestRejectsDowngrade(t *testing.T) {
	// CDN compromised to serve a notarized-but-older Rune. Manager
	// must not treat that as an upgrade.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Manifest{
			Version:  "v1.1.0",
			Filename: "Rune.dmg",
			URL:      "https://example.invalid/Rune.dmg",
			SHA256:   "abc",
		})
	}))
	t.Cleanup(srv.Close)
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	mgr := newTestManager(t, srv, "v1.2.0", now)
	_, has, err := mgr.fetchManifest(context.Background())
	require.NoError(t, err)
	require.False(t, has, "downgrade must not be offered")
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"older", "v1.1.0", "v1.2.0", -1},
		{"newer", "v1.2.0", "v1.1.0", 1},
		{"equal", "v1.2.0", "v1.2.0", 0},
		// `git describe --tags` output ranks as a semver
		// pre-release of its base tag, but it is really N commits
		// *past* that tag.
		{"dev build past tag", "v1.2.0-4-gbb09675b", "v1.2.0", 1},
		{"tag before dev build", "v1.2.0", "v1.2.0-4-gbb09675b", -1},
		{"dirty dev build past tag", "v1.2.0-4-gbb09675b-dirty", "v1.2.0", 1},
		{"more commits past same tag", "v1.2.0-9-gbb09675b", "v1.2.0-4-gaaaaaaaa", 1},
		{"same commits past same tag", "v1.2.0-4-gbb09675b", "v1.2.0-4-gbb09675b", 0},
		{"dev build below next release", "v1.2.0-4-gbb09675b", "v1.2.1", -1},
		{"dev build above previous release", "v1.2.0-4-gbb09675b", "v1.1.9", 1},
		// A real pre-release tag still sorts below its release.
		{"prerelease tag", "v1.2.0-rc1", "v1.2.0", -1},
		{"dev build past prerelease tag", "v1.2.0-rc1-2-gbb09675b", "v1.2.0-rc1", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, compareVersions(tc.a, tc.b))
		})
	}
}

func TestFetchManifestRejectsUpgradeToOwnBaseTag(t *testing.T) {
	// Dev builds report `git describe --tags`, which is the base tag
	// plus the commits since it. Publishing that base tag must not
	// look like an upgrade to a client already past it.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Manifest{
			Version:  "v1.2.0",
			Filename: "Rune.dmg",
			URL:      "https://example.invalid/Rune.dmg",
			SHA256:   "abc",
		})
	}))
	t.Cleanup(srv.Close)
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	mgr := newTestManager(t, srv, "v1.2.0-4-gbb09675b", now)
	_, has, err := mgr.fetchManifest(context.Background())
	require.NoError(t, err)
	require.False(t, has, "dev build past the manifest tag must not be offered")
}

func TestFetchManifestRejectsBelowMinSupported(t *testing.T) {
	// Client is older than min_supported_version, so the user must
	// upgrade out of band. Surface an error so the manager's
	// notification path explains the situation.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Manifest{
			Version:             "v2.0.0",
			MinSupportedVersion: "v1.5.0",
			Filename:            "Rune.dmg",
			URL:                 "https://example.invalid/Rune.dmg",
			SHA256:              "abc",
		})
	}))
	t.Cleanup(srv.Close)
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	mgr := newTestManager(t, srv, "v1.0.0", now)
	_, _, err := mgr.fetchManifest(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "min")
}

func TestFetchManifestAcceptsAtMinSupported(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Manifest{
			Version:             "v2.0.0",
			MinSupportedVersion: "v1.5.0",
			Filename:            "Rune.dmg",
			URL:                 "https://example.invalid/Rune.dmg",
			SHA256:              "abc",
		})
	}))
	t.Cleanup(srv.Close)
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	mgr := newTestManager(t, srv, "v1.5.0", now)
	_, has, err := mgr.fetchManifest(context.Background())
	require.NoError(t, err)
	require.True(t, has)
}

// countingPrompter counts the prompts opened and leaves them unanswered.
type countingPrompter struct {
	n atomic.Int32
}

func (p *countingPrompter) Prompt(
	browserapi.PromptConfig, handler.PromptHandler,
) (browserapi.Window, error) {
	p.n.Add(1)
	return nil, nil
}

func TestManagerStopsOfferingUpgradesOnceUpgraded(t *testing.T) {
	ctx := context.Background()
	fake := &fakePlatformOps{
		archiveContent: []byte("artifact"),
		dmgPayload:     map[string]string{"Contents/MacOS/rune": "new"},
		archiveLayout:  map[string]string{"bin/rune": "new"},
	}
	sum := sha256.Sum256(fake.archiveContent)
	manifest := Manifest{
		Version: "v9.9.9",
		URL:     "https://example.invalid/rune-v9.9.9",
		SHA256:  hex.EncodeToString(sum[:]),
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	t.Cleanup(srv.Close)
	bin := seedInstall(t, t.TempDir())
	prompter := &countingPrompter{}
	mgr, err := newWithPlatformOps(Config{
		CurrentVersion:   "v9.9.0",
		ManifestURL:      srv.URL,
		HTTPClient:       srv.Client(),
		Storage:          storagestub.NewInMemoryService(),
		Executable:       func() (string, error) { return bin, nil },
		CLISymlinkPath:   filepath.Join(t.TempDir(), "rune"),
		CacheDir:         t.TempDir(),
		Prompter:         prompter,
		Starter:          &countingStarter{},
		ScheduleNextTick: func(fn func()) bool { fn(); return true },
	}, fake)
	require.NoError(t, err)

	require.NoError(t, mgr.Upgrade(ctx, manifest, nil))

	mgr.tick(ctx)
	require.Zero(t, prompter.n.Load())
	_, _, err = mgr.Check(ctx)
	require.ErrorIs(t, err, errRestartRequired)
}
