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

package pkgrpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"gopkg.in/yaml.v3"
	"unstable.build/rune/auth"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc/pkgrpcpb"
	"unstable.build/rune/internal/workspace"
)

// dial serves srv over an in-memory connection and returns a client
// whose notifications and prompts are recorded by events.
func dial(t *testing.T, register func(*grpc.Server), events *recorder) *Client {
	t.Helper()
	return NewClient(dialConn(t, register), events)
}

func dialConn(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	register(s)
	go debug.CapturePanicReport(func() { _ = s.Serve(lis) })
	t.Cleanup(s.Stop)
	cc, err := grpc.NewClient("passthrough:///pkgrpc",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

func serve(t *testing.T, pm idepkg.PackageManager) (*Client, *recorder) {
	t.Helper()
	events := &recorder{}
	return dial(t, func(s *grpc.Server) {
		pkgrpcpb.RegisterPackageManagerServer(s, NewServer(pm))
	}, events), events
}

// newHostScheme returns a temp data dir and the scheme of the host it
// is on.
func newHostScheme(t *testing.T) (schemeapi.Scheme, string) {
	t.Helper()
	dataDir := t.TempDir()
	uri, err := workspaceapi.CurrentUserHostURI(dataDir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	return scheme, dataDir
}

// newHost builds the package manager of a host rooted at a temp data
// dir, the way a headless host does.
func newHost(
	t *testing.T, packages map[string]release.Package, bundles map[string][]release.Bundle,
) (*idepkg.Manager, string) {
	t.Helper()
	scheme, dataDir := newHostScheme(t)
	rm := idepkgtest.NewReleaseManager(packages, bundles)
	pm, storage := idepkg.NewProvisioningManager(storagestub.NewInMemoryService(), rm,
		scheme, dataDir, filepath.Join(dataDir, "rune.yaml"), "",
		func() map[string]any { return nil }, idepkgtest.TrustStore())
	t.Cleanup(func() { _ = storage.Close() })
	return pm, dataDir
}

// recorder records, in order, the progress, notifications and prompts a
// client reports.
type recorder struct {
	mu      sync.Mutex
	events  []string
	prompts []idepkg.ConfigPrompt
	answers []func(bool)
}

func (r *recorder) add(ev string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *recorder) Progress(progress, total int64, units string) {
	r.add(fmt.Sprintf("progress %d/%d %s", progress, total, units))
}

func (r *recorder) Notify(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	r.add(fmt.Sprintf("notify %d %s", level, fmt.Sprintf(msg, args...)))
	return "", nil
}

func (r *recorder) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	r.add(fmt.Sprintf("notify-once %d %s", level, fmt.Sprintf(msg, args...)))
	return "", nil
}

func (r *recorder) UpdateNotificationProgress(string, string, int64, int64) error { return nil }

func (r *recorder) PromptConfig(p idepkg.ConfigPrompt, answer func(bool)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf("prompt %d", len(r.prompts)))
	r.prompts = append(r.prompts, p)
	r.answers = append(r.answers, answer)
}

func (r *recorder) prompt(t *testing.T, i int) idepkg.ConfigPrompt {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Greater(t, len(r.prompts), i, "prompt %d was not asked", i)
	return r.prompts[i]
}

func (r *recorder) answer(t *testing.T, i int, approved bool) {
	t.Helper()
	r.mu.Lock()
	require.Greater(t, len(r.answers), i, "prompt %d was not asked", i)
	answer := r.answers[i]
	r.mu.Unlock()
	answer(approved)
}

// await waits for the client to record ev.
func (r *recorder) await(t *testing.T, ev string) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Contains(c, r.recorded(), ev)
	}, 5*time.Second, 5*time.Millisecond)
}

type collected[T any] struct {
	vals []T
	err  error
}

func collect[T any](it iterator.Iterator[T], err error) collected[T] {
	if err != nil {
		return collected[T]{err: err}
	}
	vals, err := iterator.ToSlice(context.Background(), it)
	return collected[T]{vals: vals, err: err}
}

func (c collected[T]) must(t *testing.T) []T {
	t.Helper()
	require.NoError(t, c.err)
	return c.vals
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	pkgs := idepkgtest.MakePackages(
		release.Package{Name: "go", Latest: "2", Notes: "Go.",
			Metadata: map[string]string{"language": "true"}, CreatedAt: created},
		release.Package{Name: "configpkg"},
	)
	bundles := idepkgtest.MakeBundles(
		[]release.Bundle{
			{Package: "go", Version: "1", Notes: "one", CreatedAt: created},
			{Package: "go", Version: "2", Notes: "two"},
		},
		[]release.Bundle{{Package: "configpkg", Version: "1"}},
	)
	pm, dataDir := newHost(t, pkgs, bundles)
	c, _ := serve(t, pm)
	ctx := context.Background()

	sortPackages := func(ps []release.Package) []release.Package {
		sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
		return ps
	}
	installed := func() []string {
		ids := collect(c.ListInstalledPackages(ctx)).must(t)
		sort.Strings(ids)
		return ids
	}
	inUse := func(pkgID string) release.Version {
		v, ok, err := c.PackageVersionInUse(ctx, pkgID)
		require.NoError(t, err)
		if !ok {
			return ""
		}
		return v
	}

	latest, err := c.LatestVersion(ctx, "go")
	require.NoError(t, err)
	assert.Equal(t, release.Version("2"), latest)

	wantPkg, err := pm.DescribePackage(ctx, "go")
	require.NoError(t, err)
	gotPkg, err := c.DescribePackage(ctx, "go")
	require.NoError(t, err)
	assert.Equal(t, wantPkg, gotPkg)
	assert.True(t, gotPkg.CreatedAt.Equal(created))

	wantBundle, err := pm.DescribeRelease(ctx, "go", "1")
	require.NoError(t, err)
	gotBundle, err := c.DescribeRelease(ctx, "go", "1")
	require.NoError(t, err)
	assert.Equal(t, wantBundle.Notes, gotBundle.Notes)
	assert.Equal(t, wantBundle.Metadata["pgp-signing-key-id"],
		gotBundle.Metadata["pgp-signing-key-id"])
	assert.True(t, gotBundle.CreatedAt.Equal(created))

	assert.Equal(t,
		sortPackages(collect(pm.ListPackages(ctx, nil)).must(t)),
		sortPackages(collect(c.ListPackages(ctx, map[string]string{"k": "v"})).must(t)))
	assert.Equal(t,
		collect(pm.ListPackageVersions(ctx, "go", nil)).must(t),
		collect(c.ListPackageVersions(ctx, "go", nil)).must(t))

	assert.Empty(t, installed())
	assert.Empty(t, inUse("go"))
	_, err = c.LibDir(ctx, "go")
	require.ErrorIs(t, err, idepkg.ErrNotInstalled)

	progress := &recorder{}
	require.NoError(t, c.InstallPackageVersion(ctx, "go", "1", progress))
	assert.NotEmpty(t, progress.recorded())
	require.NoError(t, c.InstallPackageVersion(ctx, "go", "2", nil))
	require.NoError(t, c.InstallPackageVersion(ctx, "configpkg", "1", nil))
	assert.Equal(t, []string{"configpkg", "go"}, installed())
	assert.Equal(t, release.Version("2"), inUse("go"))
	versions := collect(c.ListInstalledPackageVersions(ctx, "go")).must(t)
	assert.ElementsMatch(t, []release.Version{"1", "2"}, versions)

	want := collect(pm.LibDir(ctx, "go")).must(t)
	got := collect(c.LibDir(ctx, "go")).must(t)
	assert.ElementsMatch(t, want, got)
	assert.Contains(t, got, filepath.Join(dataDir, "lib", "go", "bin", "gopls"))

	require.NoError(t, c.UsePackageVersion(ctx, "go", "1"))
	assert.Equal(t, release.Version("1"), inUse("go"))
	require.ErrorIs(t, c.DeletePackageVersion(ctx, "go", "1", false), idepkg.ErrVersionInUse)
	require.NoError(t, c.DeletePackageVersion(ctx, "go", "2", false))
	assert.Equal(t, []release.Version{"1"}, collect(c.ListInstalledPackageVersions(ctx, "go")).must(t))
	require.NoError(t, c.DeletePackage(ctx, "go"))
	require.ErrorIs(t, c.DeletePackage(ctx, "go"), idepkg.ErrNotInstalled)
	assert.Equal(t, []string{"configpkg"}, installed())
}

// fakePackageManager answers through its function fields; any other
// method panics on the nil embedded interface.
type fakePackageManager struct {
	idepkg.PackageManager
	libDir  func(context.Context, string) (iterator.Iterator[string], error)
	latest  func(context.Context, string) (release.Version, error)
	install func(context.Context, string, release.Version, repl.ProgressWriter) error
	use     func(context.Context, string, release.Version) error
}

func (f *fakePackageManager) LibDir(ctx context.Context, pkgID string) (iterator.Iterator[string], error) {
	return f.libDir(ctx, pkgID)
}

func (f *fakePackageManager) LatestVersion(ctx context.Context, pkgID string) (release.Version, error) {
	return f.latest(ctx, pkgID)
}

func (f *fakePackageManager) InstallPackageVersion(
	ctx context.Context, pkgID string, version release.Version, pw repl.ProgressWriter,
) error {
	return f.install(ctx, pkgID, version, pw)
}

func (f *fakePackageManager) UsePackageVersion(
	ctx context.Context, pkgID string, version release.Version,
) error {
	return f.use(ctx, pkgID, version)
}

func TestLibDirBatchesPaths(t *testing.T) {
	t.Parallel()
	paths := make([]string, 3*pathsPerMessage+7)
	for i := range paths {
		paths[i] = fmt.Sprintf("/home/studio/.rune/lib/go/%d", i)
	}
	c, _ := serve(t, &fakePackageManager{
		libDir: func(context.Context, string) (iterator.Iterator[string], error) {
			return iterator.FromSlice(paths), nil
		},
	})
	assert.Equal(t, paths, collect(c.LibDir(context.Background(), "go")).must(t))
}

func TestInstallEventsKeepTheirOrder(t *testing.T) {
	t.Parallel()
	var ui idepkg.UI
	c, events := serve(t, &fakePackageManager{
		install: func(_ context.Context, pkgID string, _ release.Version, pw repl.ProgressWriter) error {
			// The host's UI is the install's progress writer.
			ui = pw.(idepkg.UI)
			pw.Progress(1, 3, "B")
			_, _ = ui.Notify(browserapi.LevelInfo, "applied %s configuration", pkgID)
			pw.Progress(2, 3, "B")
			_, _ = ui.NotifyOnce(browserapi.LevelWarn, "100%% %s", "done")
			pw.Progress(3, 3, "B")
			return nil
		},
	})

	require.NoError(t, c.InstallPackageVersion(context.Background(), "go", "1", events))
	assert.Equal(t, []string{
		"progress 1/3 B",
		fmt.Sprintf("notify %d applied go configuration", browserapi.LevelInfo),
		"progress 2/3 B",
		fmt.Sprintf("notify-once %d 100%% done", browserapi.LevelWarn),
		"progress 3/3 B",
	}, events.recorded())

	// A notice after the install ended has no client to reach.
	_, _ = ui.Notify(browserapi.LevelInfo, "late")
	assert.Len(t, events.recorded(), 5)
}

// hostGOROOT reads the GOROOT the host's user config sets.
func hostGOROOT(t *testing.T, dataDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "rune.yaml"))
	require.NoError(t, err)
	var cfg struct {
		Env struct {
			GOROOT string `yaml:"GOROOT"`
		} `yaml:"env"`
	}
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	return cfg.Env.GOROOT
}

func TestInstallAsksTheClient(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages()
	bundles := idepkgtest.MakeBundles([]release.Bundle{
		{Package: "configpkg", Version: "1"},
		{Package: "configpkg", Version: "2"},
	})
	pm, dataDir := newHost(t, pkgs, bundles)
	c, events := serve(t, pm)
	ctx := context.Background()
	applied := func(level browserapi.NotificationLevel) string {
		return fmt.Sprintf("notify %d applied configpkg configuration updates. "+
			"Restart the program to load the changes.", level)
	}
	v1GOROOT := filepath.Join(dataDir, "pkg", "configpkg", "1", "go")
	v2GOROOT := filepath.Join(dataDir, "pkg", "configpkg", "2", "go")

	require.NoError(t, c.InstallPackageVersion(ctx, "configpkg", "1", nil))
	assert.Equal(t, []string{applied(browserapi.LevelInfo)}, events.recorded(),
		"settings the user does not have are applied without asking")
	assert.Equal(t, v1GOROOT, hostGOROOT(t, dataDir))

	require.NoError(t, c.InstallPackageVersion(ctx, "configpkg", "2", nil))
	p := events.prompt(t, 0)
	assert.Contains(t, p.Message, "GOROOT: "+v2GOROOT)
	assert.Equal(t, []idepkg.PromptOption{{Label: "Allow", Key: 'a'}, {Label: "Deny", Key: 'd'}},
		p.Options)
	assert.Equal(t, v1GOROOT, hostGOROOT(t, dataDir), "nothing changes before the user answers")
	events.answer(t, 0, true)
	events.await(t, applied(browserapi.LevelSuccess))
	assert.Equal(t, v2GOROOT, hostGOROOT(t, dataDir))

	require.NoError(t, c.UsePackageVersion(ctx, "configpkg", "1"))
	events.prompt(t, 1)
	events.answer(t, 1, false)
	// The host ends the request once nothing is left to answer, which
	// frees it for the next one.
	require.NoError(t, c.UsePackageVersion(ctx, "configpkg", "2"))
	assert.Equal(t, v2GOROOT, hostGOROOT(t, dataDir), "a denied change is not applied")
	inUse, _, err := c.PackageVersionInUse(ctx, "configpkg")
	require.NoError(t, err)
	assert.Equal(t, release.Version("2"), inUse)
}

// ownUI is the UI of the user of a GUI node, who did not ask for what
// its peers install.
type ownUI struct {
	browserapi.WindowManager
	t *testing.T
}

func (u ownUI) Floating(browserapi.Floating, browserapi.FloatingConfig) (browserapi.Window, error) {
	u.t.Error("a peer's install must not prompt the node's own user")
	return nil, errors.New("unexpected prompt")
}

func TestPeerInstallAsksThePeer(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages()
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: "configpkg", Version: "2"}})
	scheme, dataDir := newHostScheme(t)
	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("env:\n  GOROOT: /custom/go\n"), 0o644))
	own := idepkgtest.NewNotifications(t)
	storage := storagestub.NewInMemoryService()
	t.Cleanup(func() { _ = storage.Close() })
	pm := idepkg.NewManager(own, idepkgtest.NewReleaseManager(pkgs, bundles), storage,
		idepkgtest.TrustStore(), scheme, dataDir, configPath, ownUI{t: t},
		func(fn func()) bool { fn(); return true }, term.NopInterrupter())
	c, events := serve(t, pm)

	require.NoError(t, c.InstallPackageVersion(context.Background(), "configpkg", "2", nil))
	events.prompt(t, 0)
	events.answer(t, 0, true)
	events.await(t, fmt.Sprintf("notify %d applied configpkg configuration updates. "+
		"Restart the program to load the changes.", browserapi.LevelSuccess))
	assert.Equal(t, filepath.Join(dataDir, "pkg", "configpkg", "2", "go"), hostGOROOT(t, dataDir))
	assert.Empty(t, own.Active(), "nothing is notified to the node's own user")
}

func TestAnswerAfterTheHostLeft(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages()
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: "configpkg", Version: "2"}})
	pm, dataDir := newHost(t, pkgs, bundles)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "rune.yaml"),
		[]byte("env:\n  GOROOT: /custom/go\n"), 0o644))
	cc := dialConn(t, func(s *grpc.Server) { NewServer(pm).Register(s) })
	events := &recorder{}
	c := NewClient(cc, events)

	require.NoError(t, c.InstallPackageVersion(context.Background(), "configpkg", "2", nil))
	events.prompt(t, 0)
	require.NoError(t, cc.Close())
	events.answer(t, 0, true)
	events.await(t, fmt.Sprintf("notify %d apply configuration: "+
		"the connection to the host was lost", browserapi.LevelError))
	assert.Equal(t, "/custom/go", hostGOROOT(t, dataDir))
}

func TestErrorsKeepTheirKind(t *testing.T) {
	t.Parallel()
	tests := []error{
		idepkg.ErrNotInstalled,
		idepkg.ErrPackageNotFound,
		idepkg.ErrVersionNotFound,
		idepkg.ErrAlreadyInstalled,
		idepkg.ErrVersionInUse,
		idepkg.ErrServerUnavailable,
		idepkg.ErrArtifactMissing,
		idepkg.ErrForbidden,
		idepkg.ErrNoReleases,
		auth.ErrNotAuthenticated,
	}
	for _, sentinel := range tests {
		t.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()
			hostErr := fmt.Errorf("package %q: %w", "go", sentinel)
			c, _ := serve(t, &fakePackageManager{
				latest: func(context.Context, string) (release.Version, error) {
					return "", hostErr
				},
				libDir: func(context.Context, string) (iterator.Iterator[string], error) {
					return nil, hostErr
				},
				install: func(context.Context, string, release.Version, repl.ProgressWriter) error {
					return hostErr
				},
			})
			ctx := context.Background()

			_, err := c.LatestVersion(ctx, "go")
			require.ErrorIs(t, err, sentinel)
			assert.Equal(t, hostErr.Error(), err.Error())
			_, err = c.LibDir(ctx, "go")
			require.ErrorIs(t, err, sentinel)
			require.ErrorIs(t, c.InstallPackageVersion(ctx, "go", "1", nil), sentinel)
		})
	}
}

func TestOldHostIsUnsupported(t *testing.T) {
	t.Parallel()
	c := dial(t, func(*grpc.Server) {}, &recorder{})
	ctx := context.Background()
	_, err := c.LibDir(ctx, "go")
	require.ErrorIs(t, err, ErrUnsupported)
	_, err = c.LatestVersion(ctx, "go")
	require.ErrorIs(t, err, ErrUnsupported)
	require.ErrorIs(t, c.InstallPackageVersion(ctx, "go", "1", nil), ErrUnsupported)
}

func TestOneInstallAtATime(t *testing.T) {
	t.Parallel()
	var (
		mu      sync.Mutex
		running int
		maxRun  int
	)
	entered := make(chan string, 2)
	release1 := make(chan struct{})
	c, _ := serve(t, &fakePackageManager{
		install: func(_ context.Context, pkgID string, _ release.Version, _ repl.ProgressWriter) error {
			mu.Lock()
			running++
			maxRun = max(maxRun, running)
			mu.Unlock()
			entered <- pkgID
			<-release1
			mu.Lock()
			running--
			mu.Unlock()
			return nil
		},
	})

	errs := make(chan error, 2)
	for _, pkgID := range []string{"go", "rust"} {
		go debug.CapturePanicReport(func() {
			errs <- c.InstallPackageVersion(context.Background(), pkgID, "1", nil)
		})
	}
	<-entered
	select {
	case pkgID := <-entered:
		t.Fatalf("install of %s started while another was running", pkgID)
	case <-time.After(100 * time.Millisecond):
	}
	close(release1)
	<-entered
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, maxRun)
}

func TestCancellationMidStream(t *testing.T) {
	t.Parallel()
	c, _ := serve(t, &fakePackageManager{
		libDir: func(context.Context, string) (iterator.Iterator[string], error) {
			// The iterator of a package whose install is still running.
			return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
				<-ctx.Done()
				return "", false, ctx.Err()
			}, func() error { return nil }), nil
		},
		install: func(ctx context.Context, _ string, _ release.Version, pw repl.ProgressWriter) error {
			pw.Progress(1, 2, "B")
			<-ctx.Done()
			return ctx.Err()
		},
	})

	t.Run("LibDir", func(t *testing.T) {
		t.Parallel()
		it, err := c.LibDir(context.Background(), "go")
		require.NoError(t, err)
		defer it.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, ok := it.Next(ctx)
		assert.False(t, ok)
		require.ErrorIs(t, it.Err(), context.DeadlineExceeded)
	})
	t.Run("Install", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		pw := &cancelOnProgress{cancel: cancel}
		err := c.InstallPackageVersion(ctx, "go", "1", pw)
		require.ErrorIs(t, err, context.Canceled)
	})
}

// cancelOnProgress cancels the install as soon as it reports progress.
type cancelOnProgress struct {
	once   sync.Once
	cancel context.CancelFunc
}

func (p *cancelOnProgress) Progress(int64, int64, string) {
	p.once.Do(p.cancel)
}

func TestUnknownErrorsKeepTheirMessage(t *testing.T) {
	t.Parallel()
	err := errors.New("boom")
	assert.Equal(t, "rpc error: code = Unknown desc = boom", toStatus(err).Error())
	assert.NoError(t, toStatus(nil))
	assert.NoError(t, fromStatus(nil))
}
