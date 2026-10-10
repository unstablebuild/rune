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

package ide

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	sdkpkgrpc "github.com/unstablebuild/rune-go-sdk/api/pkgapi/pkgrpc"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc"
	"unstable.build/rune/internal/workspace"
)

// packageHostScheme is a workspace scheme on another machine whose
// host serves its package manager over cc.
type packageHostScheme struct {
	schemeapi.Scheme
	cc grpc.ClientConnInterface
}

func (s packageHostScheme) HostConn() (grpc.ClientConnInterface, bool) {
	return s.cc, true
}

// servePackages serves pm over an in-memory connection.
func servePackages(t *testing.T, pm idepkg.PackageManager) grpc.ClientConnInterface {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pkgrpc.NewServer(pm).Register(srv)
	go debug.CapturePanicReport(func() { _ = srv.Serve(lis) })
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

// listingHostPackageManager is a host whose registry publishes "go".
type listingHostPackageManager struct {
	*hostPackageManager
}

func (f listingHostPackageManager) ListPackages(
	context.Context, map[string]string,
) (iterator.Iterator[release.Package], error) {
	return iterator.FromSlice([]release.Package{{Name: "go"}}), nil
}

// openHostWorkspace opens a workspace on studio, a machine whose
// packages pm manages.
func openHostWorkspace(
	t *testing.T, pm idepkg.PackageManager,
) (*testWorkspaceManagerHandler, *workspaceHandler) {
	t.Helper()
	cc := servePackages(t, pm)
	cfg := defaultCfg()
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	manager := workspace.NewManager(config.NopConfig(), sched)
	require.NoError(t, manager.RegisterScheme(workspace.MemoryScheme,
		workspace.NewMemoryScheme))
	const scheme = "remote"
	require.NoError(t, manager.RegisterScheme(scheme, func(
		ctx context.Context, cfg config.Config, uri workspaceapi.URI,
	) (schemeapi.Scheme, error) {
		// The files live here; only the packages are studio's.
		local, err := workspaceapi.ParseURI(scheme + "://" + uri.Path())
		if err != nil {
			return nil, err
		}
		inner, err := workspace.NewInMemorySchemeFunc(scheme)(ctx, cfg, local)
		if err != nil {
			return nil, err
		}
		return packageHostScheme{Scheme: inner, cc: cc}, nil
	}))
	uri, err := workspaceapi.ParseURI(scheme + "://studio/workspace")
	require.NoError(t, err)
	m := newTestWorkspaceManagerHandlerWithManagerMu(t, manager, mu, drain,
		&uri, cfg, FuncExtensionsRunner(testRunnerFn), nil, t.TempDir(), nil,
		nopShutdownShaderConfig())
	t.Cleanup(func() { _ = m.Close() })

	m.mu.Lock()
	wh := m.workspaces[m.focus]
	m.mu.Unlock()
	require.NotNil(t, wh.hostPackages,
		"a workspace on another machine manages that machine's packages")
	return m, wh
}

func TestRemoteWorkspaceInstallsOnItsHost(t *testing.T) {
	host := listingHostPackageManager{newHostPackageManager("2")}
	m, wh := openHostWorkspace(t, host)

	t.Run("language tooling prompts here and installs there", func(t *testing.T) {
		ctx := context.Background()
		it, err := wh.hostPackages.LibDir(ctx, "go")
		require.NoError(t, err)
		defer func() { _ = it.Close() }()
		m.quiesce()

		h := newSafeHandler(m)
		h.Resize(40, 15)
		assert.Contains(t, handlertest.DrawHandler(h, 40, 15), "\"go\" on studio?")
		h.Handle(term.Event{Type: term.EventKey, Ch: 'Y'})

		paths, err := sdkiterator.ToSlice(ctx, it)
		require.NoError(t, err)
		assert.Equal(t, []string{"/home/studio/.rune/pkg/go/2/bin/go"}, paths)
		version, ok := host.installedVersion("go")
		require.True(t, ok)
		assert.Equal(t, release.Version("2"), version)
		_, ok = pkgVersionInUse(t, m.pkgmanager.pkg, "go")
		assert.False(t, ok, "nothing is installed on this machine")
	})

	t.Run("pkg lists the host's packages", func(t *testing.T) {
		m.mu.Lock()
		it, _, err := wh.ex.comp.CompleteCommand(t.Context(), textapi.Command{
			Name: "console", Args: []string{"pkg", "install", ""},
		})
		m.mu.Unlock()
		require.NoError(t, err)
		defer func() { _ = it.Close() }()
		names, err := sdkiterator.ToSlice(t.Context(), it)
		require.NoError(t, err)
		assert.Equal(t, []string{"go"}, names)
	})
}

func TestRemoteWorkspaceAsksAboutItsHostsConfig(t *testing.T) {
	dataDir := t.TempDir()
	uri, err := workspaceapi.CurrentUserHostURI(dataDir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("env:\n  GOROOT: /custom/go\n"), 0o644))
	host, storage := idepkg.NewProvisioningManager(storagestub.NewInMemoryService(),
		idepkgtest.NewReleaseManager(idepkgtest.MakePackages(), idepkgtest.MakeBundles(
			[]release.Bundle{{Package: "configpkg", Version: "2"}})),
		scheme, dataDir, configPath, "", func() map[string]any { return nil },
		idepkgtest.TrustStore())
	t.Cleanup(func() { _ = storage.Close() })
	m, wh := openHostWorkspace(t, host)

	require.NoError(t, wh.hostPackages.pm.InstallPackageVersion(
		context.Background(), "configpkg", "2", nil))
	m.quiesce()
	h := newSafeHandler(m)
	h.Resize(100, 30)
	screen := handlertest.DrawHandler(h, 100, 30)
	assert.Contains(t, screen, "On studio:")
	assert.Contains(t, screen, "wants to update your configuration")
	h.Handle(term.Event{Type: term.EventKey, Ch: 'a'})

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		data, err := os.ReadFile(configPath)
		require.NoError(c, err)
		assert.Contains(c, string(data), "GOROOT: $RUNE_DATADIR/pkg/configpkg/2/go")
	}, 5*time.Second, 5*time.Millisecond, "the approved change is applied on the host")
	_, err = os.Stat(filepath.Join(m.sixDir, "rune.yaml"))
	assert.ErrorIs(t, err, os.ErrNotExist, "nothing changes on this machine")
}

// unsupportedHost is a host running a Rune without package management.
type unsupportedHost struct {
	idepkg.PackageManager
}

func (unsupportedHost) LibDir(context.Context, string) (iterator.Iterator[string], error) {
	return nil, pkgrpc.ErrUnsupported
}

func TestPkgManagerOnOldHost(t *testing.T) {
	n := idepkgtest.NewNotifications(t)
	pm := newPkgManager(unsupportedHost{}, "studio", nil, n,
		storagestub.NewInMemoryService(), nil, func(fn func()) bool { fn(); return true },
		term.NopInterrupter(), false)
	defer func() { _ = pm.Close() }()

	_, err := pm.LibDir(context.Background(), "go")
	require.ErrorIs(t, err, storageapi.ErrNotFound,
		"language tooling falls back to the host's PATH")
	var msgs []string
	for _, noti := range n.Active() {
		assert.Equal(t, browserapi.LevelWarn, noti.Level)
		msgs = append(msgs, noti.Msg)
	}
	assert.Equal(t, []string{"Update Rune on studio to install packages there."}, msgs)
}

// unpublishedHost is a host whose platform has no build of any package.
type unpublishedHost struct {
	idepkg.PackageManager
}

func (unpublishedHost) LibDir(context.Context, string) (iterator.Iterator[string], error) {
	return nil, idepkg.ErrNotInstalled
}

func (unpublishedHost) LatestVersion(_ context.Context, pkgID string) (release.Version, error) {
	return "", fmt.Errorf("package %q does not exist: %w", pkgID, idepkg.ErrPackageNotFound)
}

// localPackages is this machine's manager with installed in use.
type localPackages struct {
	idepkg.PackageManager
	installed []string
}

func (l localPackages) PackageVersionInUse(
	_ context.Context, pkgID string,
) (release.Version, bool, error) {
	if slices.Contains(l.installed, pkgID) {
		return "1", true, nil
	}
	return "", false, nil
}

func TestPkgManagerWarnsWhenHostCannotInstallLocalPackage(t *testing.T) {
	tests := []struct {
		name  string
		local []string
		want  []string
	}{
		{
			name:  "installed here",
			local: []string{"python"},
			want: []string{"The python package is not available for studio's platform. " +
				"Install its tools on studio's PATH to use them there."},
		},
		{name: "not installed here", local: nil, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := idepkgtest.NewNotifications(t)
			pm := newPkgManager(unpublishedHost{}, "studio",
				localPackages{installed: tc.local}, n,
				storagestub.NewInMemoryService(), nil,
				func(fn func()) bool { fn(); return true },
				term.NopInterrupter(), false)
			defer func() { _ = pm.Close() }()

			_, err := pm.LibDir(context.Background(), "python")
			require.ErrorIs(t, err, storageapi.ErrNotFound,
				"language tooling falls back to the host's PATH")
			var msgs []string
			for _, noti := range n.Active() {
				assert.Equal(t, browserapi.LevelWarn, noti.Level)
				msgs = append(msgs, noti.Msg)
			}
			assert.Equal(t, tc.want, msgs)
		})
	}
}

func TestPackageHostName(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{uri: "ssh://me@studio:2222/src", want: "studio"},
		{uri: "ssh://studio/src", want: "studio"},
		{uri: "rune://studio/src", want: "studio"},
	}
	for _, tc := range tests {
		t.Run(tc.uri, func(t *testing.T) {
			uri, err := workspaceapi.ParseURI(tc.uri)
			require.NoError(t, err)
			assert.Equal(t, tc.want, packageHostName(uri))
		})
	}
}

// extensionTools serves pkgs the way an extension reaches them and
// returns the lookup a language extension's bring-up gets for them.
func extensionTools(t *testing.T, pkgs *pkgManager, tools ...string) *langext.Tools {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	sdkpkgrpc.RegisterPackagesServer(srv, sdkpkgrpc.NewServer(extensionPackages{pkgs: pkgs}))
	go debug.CapturePanicReport(func() { _ = srv.Serve(lis) })
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })
	inst := extensionInstaller{pkgs: sdkpkgrpc.NewClient(cc)}
	return langext.NewInitializer(t.Context(), nil, nil, inst, langext.ProjectConfig{
		LanguageID: "python", Tools: tools,
	}).Tools()
}

// extensionInstaller reaches packages over the service alone, like an
// extension whose install root provisions nothing.
type extensionInstaller struct {
	pkgs pkgapi.Manager
}

func (i extensionInstaller) Packages(context.Context) pkgapi.Manager { return i.pkgs }

func (extensionInstaller) FindInstalledExecutable(context.Context, string) (string, error) {
	return "", os.ErrNotExist
}

func TestExtensionResolvesHostPackage(t *testing.T) {
	tests := []struct {
		name    string
		answer  term.Event
		want    string
		wantErr error
	}{
		{
			name:   "user installs it",
			answer: term.Event{Type: term.EventKey, Ch: 'Y'},
			want:   "/home/studio/.rune/pkg/python/2/bin/python",
		},
		{
			name:    "user declines",
			answer:  term.Event{Type: term.EventKey, Ch: 'N'},
			wantErr: pkgapi.ErrNotInstalled,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, wh := openHostWorkspace(t, listingHostPackageManager{newHostPackageManager("2")})
			tools := extensionTools(t, wh.hostPackages, "python")

			got := make(chan error, 1)
			var python string
			go debug.CapturePanicReport(func() {
				var err error
				python, err = tools.Find(t.Context(), "python")
				got <- err
			})

			h := newSafeHandler(m)
			h.Resize(40, 15)
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				m.quiesce()
				assert.Contains(c, handlertest.DrawHandler(h, 40, 15), "\"python\" on studio?")
			}, 5*time.Second, 5*time.Millisecond)
			h.Handle(tc.answer)

			err := <-got
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, python)
		})
	}
}

func TestExtensionPackagesReportsMissingPackageAsNotInstalled(t *testing.T) {
	pm := newPkgManager(unpublishedHost{}, "studio", localPackages{}, idepkgtest.NewNotifications(t),
		storagestub.NewInMemoryService(), nil,
		func(fn func()) bool { fn(); return true }, term.NopInterrupter(), false)
	defer func() { _ = pm.Close() }()
	tools := extensionTools(t, pm, "python")

	_, err := tools.Find(t.Context(), "python")
	require.ErrorIs(t, err, pkgapi.ErrNotInstalled)
}
