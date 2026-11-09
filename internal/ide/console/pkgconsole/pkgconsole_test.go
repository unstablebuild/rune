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

package pkgconsole

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc"
)

type progressSample struct {
	progress, total int64
	units           string
}

type recordingProgressWriter struct {
	mu      sync.Mutex
	samples []progressSample
}

func (w *recordingProgressWriter) Progress(progress, total int64, units string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.samples = append(w.samples, progressSample{progress, total, units})
}

func (w *recordingProgressWriter) count() int {
	return len(w.get())
}

func (w *recordingProgressWriter) get() []progressSample {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]progressSample(nil), w.samples...)
}

func newHandlerForTest(t *testing.T) (*Handler, *idepkgtest.Notifications) {
	t.Helper()
	pkgs := idepkgtest.MakePackages(
		release.Package{
			Name: "go", Latest: "1",
			Notes:    "Go programming language.",
			Metadata: map[string]string{languageMetadataKey: "true"},
		},
		release.Package{Name: "ripgrep", Latest: "1", Notes: "Fast grep."},
	)
	bundles := idepkgtest.MakeBundles(
		[]release.Bundle{{Package: "go", Version: "1", Notes: "Go release one."}},
		[]release.Bundle{{Package: "ripgrep", Version: "1", Notes: "ripgrep release one."}},
	)
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	n := idepkgtest.NewNotifications(t)
	return New(Config{Manager: newManager(t, n, rm)}), n
}

var syncTick = func(fn func()) bool { fn(); return true }

// newManager builds an idepkg.Manager backed by the given mocks, rooted
// at a temp directory, auto-approving install prompts.
func newManager(
	t *testing.T, n *idepkgtest.Notifications, rm release.Manager,
) *idepkg.Manager {
	t.Helper()
	temp := t.TempDir()
	configPath := filepath.Join(temp, "config.yaml")
	return idepkg.NewManager(n, rm, storagestub.NewInMemoryService(),
		idepkgtest.TrustStore(),
		newFixtureScheme(temp), temp, configPath, fixturePrompter{},
		syncTick, term.NopInterrupter())
}

// fixturePrompter picks the first option of every prompt.
type fixturePrompter struct{}

func (fixturePrompter) Prompt(
	cfg browserapi.PromptConfig, h handler.PromptHandler,
) (browserapi.Window, error) {
	h.OnSelect(0, cfg.Options[0])
	return &fixtureWindow{}, h.OnClose()
}

type fixtureWindow struct{}

func (m *fixtureWindow) WindowID() uint64 { return 0 }

type fixtureScheme struct{ root string }

func newFixtureScheme(root string) *fixtureScheme { return &fixtureScheme{root: root} }

func (s *fixtureScheme) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(s.root, path)
}

func (s *fixtureScheme) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + s.resolve(path))
}
func (s *fixtureScheme) Root() string { return s.root }
func (s *fixtureScheme) NewFile(_ uintptr, _ string) workspaceapi.File {
	panic("not implemented")
}
func (s *fixtureScheme) Chroot(path string) (schemeapi.Scheme, error) {
	return newFixtureScheme(s.resolve(path)), nil
}
func (s *fixtureScheme) Watch(
	_ string, _ chan<- schemeapi.EventInfo, _ ...schemeapi.Event,
) (int, error) {
	panic("not implemented")
}
func (s *fixtureScheme) StopWatch(_ int) error { panic("not implemented") }
func (s *fixtureScheme) Create(filename string) (workspaceapi.File, error) {
	return os.Create(s.resolve(filename))
}
func (s *fixtureScheme) Open(filename string) (workspaceapi.File, error) {
	return os.Open(s.resolve(filename))
}
func (s *fixtureScheme) OpenFile(
	filename string, flag int, perm fs.FileMode,
) (workspaceapi.File, error) {
	return os.OpenFile(s.resolve(filename), flag, perm)
}
func (s *fixtureScheme) Stat(filename string) (fs.FileInfo, error) {
	return os.Stat(s.resolve(filename))
}
func (s *fixtureScheme) Rename(oldpath, newpath string) error {
	return os.Rename(s.resolve(oldpath), s.resolve(newpath))
}
func (s *fixtureScheme) Remove(filename string) error { return os.Remove(s.resolve(filename)) }
func (s *fixtureScheme) Join(elem ...string) string   { return filepath.Join(elem...) }
func (s *fixtureScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	return os.CreateTemp(s.resolve(dir), prefix)
}
func (s *fixtureScheme) Lstat(filename string) (fs.FileInfo, error) {
	return os.Lstat(s.resolve(filename))
}
func (s *fixtureScheme) Symlink(oldname, newname string) error {
	return os.Symlink(oldname, s.resolve(newname))
}
func (s *fixtureScheme) Readlink(link string) (string, error) {
	return os.Readlink(s.resolve(link))
}
func (s *fixtureScheme) ReadDir(path string) ([]fs.DirEntry, error) {
	return os.ReadDir(s.resolve(path))
}
func (s *fixtureScheme) MkdirAll(filename string, perm fs.FileMode) error {
	return os.MkdirAll(s.resolve(filename), perm)
}
func (s *fixtureScheme) StartCommand(
	_ context.Context, _ workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	panic("not implemented")
}
func (s *fixtureScheme) Signal(_ workspaceapi.Pid, _ syscall.Signal) error {
	panic("not implemented")
}
func (s *fixtureScheme) Close() error { return nil }

func (*fixtureScheme) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (s *fixtureScheme) NewPty(_ context.Context) (workspaceapi.Pty, error) {
	panic("not implemented")
}
func (s *fixtureScheme) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) error {
	panic("not implemented")
}

// install runs the install subcommand; the manager installs synchronously.
func install(t *testing.T, h *Handler, _ *idepkgtest.Notifications, pw repl.ProgressWriter, args ...string) {
	t.Helper()
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: append([]string{"install"}, args...)}, pw)
	require.NoError(t, err)
}

func TestInstallReportsProgressAndSucceeds(t *testing.T) {
	t.Parallel()
	h, n := newHandlerForTest(t)
	pw := &recordingProgressWriter{}
	install(t, h, n, pw, "go", "1")
	assert.Positive(t, pw.count(), "install must report progress through the writer")

	// After install, the version should be in use.
	cur, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"current", "go"}},
		repl.NopProgressWriter())
	require.NoError(t, err)
	v, ok := cur.Next(context.Background())
	require.True(t, ok)
	require.NotNil(t, v)
}

func TestInstallLatestResolvesVersion(t *testing.T) {
	t.Parallel()
	h, n := newHandlerForTest(t)
	install(t, h, n, repl.NopProgressWriter(), "go")
	cur, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"current", "go"}},
		repl.NopProgressWriter())
	require.NoError(t, err)
	v, ok := cur.Next(context.Background())
	require.True(t, ok)
	require.NotNil(t, v)
}

func TestInstallMissingPackageNameErrors(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerForTest(t)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"install"}},
		repl.NopProgressWriter())
	require.Error(t, err)
}

func TestInstallGitURLNamesPackageID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args []string
		want string
	}{
		{
			args: []string{"https://github.com/ernestrc/rune-test-private-repo"},
			want: `install package: package id: ` +
				`"https://github.com/ernestrc/rune-test-private-repo" is a URL; ` +
				`use github.com/ernestrc/rune-test-private-repo`,
		},
		{
			args: []string{"git@github.com:ernestrc/rune-test-private-repo"},
			want: `install package: package id: ` +
				`"git@github.com:ernestrc/rune-test-private-repo" is an SSH URL; ` +
				`use github.com/ernestrc/rune-test-private-repo; to enable SSH ` +
				`for the git CLI fallback, run: git config --global ` +
				`url."git@github.com:".insteadOf https://github.com/`,
		},
		{
			args: []string{"https://github.com/owner/repo.git", "v1.0.0"},
			want: `package id: "https://github.com/owner/repo.git" is a URL; ` +
				`use github.com/owner/repo`,
		},
	}
	for _, tc := range cases {
		h, _ := newHandlerForTest(t)
		_, err := h.HandleCommand(context.Background(),
			repl.Command{Name: CommandName, Args: append([]string{"install"}, tc.args...)},
			repl.NopProgressWriter())
		assert.EqualError(t, err, tc.want, "args=%q", tc.args)
	}
}

func TestUseAndRemove(t *testing.T) {
	t.Parallel()
	h, n := newHandlerForTest(t)
	install(t, h, n, repl.NopProgressWriter(), "go", "1")

	useIt, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"use", "go", "1"}},
		repl.NopProgressWriter())
	// "1" is already in use after install.
	require.Error(t, err)
	require.Nil(t, useIt)

	rmIt, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"remove", "go"}},
		repl.NopProgressWriter())
	require.NoError(t, err)
	v, ok := rmIt.Next(context.Background())
	require.True(t, ok)
	require.NotNil(t, v)
}

func TestRemoveNotInstalledErrors(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerForTest(t)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"remove", "go"}},
		repl.NopProgressWriter())
	require.ErrorContains(t, err, "not installed")
}

func TestUpdateCheckNoUpdates(t *testing.T) {
	t.Parallel()
	h, n := newHandlerForTest(t)
	install(t, h, n, repl.NopProgressWriter(), "go", "1")

	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"update-check"}},
		repl.NopProgressWriter())
	require.NoError(t, err)
	v, ok := it.Next(context.Background())
	require.True(t, ok)
	require.NotNil(t, v)
}

func TestUnknownSubcommandErrors(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerForTest(t)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"nope"}},
		repl.NopProgressWriter())
	require.ErrorContains(t, err, "unknown command")
}

func TestNoArgsShowsUsage(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerForTest(t)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: nil},
		repl.NopProgressWriter())
	require.NoError(t, err)
	v, ok := it.Next(context.Background())
	require.True(t, ok)
	require.NotNil(t, v)
}

func TestCompleteTopLevel(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerForTest(t)
	it, err := h.Complete(context.Background(), CommandName, nil)
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.ElementsMatch(t, subcommandNames, names)
}

func TestCompleteFiltersByPrefix(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerForTest(t)
	it, err := h.Complete(context.Background(), CommandName, []string{"up"})
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"update-all", "update-check"}, names)
}

func TestCompleteInstallDelegatesToPackages(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerForTest(t)
	it, err := h.Complete(context.Background(), CommandName, []string{"install", ""})
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Contains(t, names, "ripgrep")
	assert.NotContains(t, names, "go")
}

func TestCompleteInstallLangFiltersLanguagePackages(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerForTest(t)
	it, err := h.Complete(context.Background(), CommandName,
		[]string{"install", "--lang", ""})
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Contains(t, names, "go")
	assert.NotContains(t, names, "ripgrep")
}

func TestCompleteInstallLangCompletesVersions(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerForTest(t)
	it, err := h.Complete(context.Background(), CommandName,
		[]string{"install", "--lang", "go", ""})
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Contains(t, names, "1")
}

func TestCompleteInstallSuggestsLangFlag(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerForTest(t)
	it, err := h.Complete(context.Background(), CommandName,
		[]string{"install", "--"})
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"--lang"}, names)
}

func TestInstallLangFlagStrippedFromPackageName(t *testing.T) {
	t.Parallel()
	h, n := newHandlerForTest(t)
	install(t, h, n, repl.NopProgressWriter(), "--lang", "go")
	cur, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"current", "go"}},
		repl.NopProgressWriter())
	require.NoError(t, err)
	v, ok := cur.Next(context.Background())
	require.True(t, ok)
	require.NotNil(t, v)
}

func TestNewPanicsOnNilManager(t *testing.T) {
	t.Parallel()
	defer func() {
		assert.NotNil(t, recover(), "expected panic for nil manager")
	}()
	_ = New(Config{Manager: nil})
}

// hostPackageManager stands in for a package manager on another host. It
// implements only what the tested subcommands call; any other method
// panics on the nil embedded interface.
type hostPackageManager struct {
	idepkg.PackageManager
	mu        sync.Mutex
	latest    release.Version
	inUse     map[string]release.Version
	installed []string
	// requirements are the requirement changes the install of a package
	// makes and reports.
	requirements map[string][]idepkg.RequirementChange
	// upgradedElsewhere holds what the install of a package returns,
	// without changing it, when the install of a package requiring it
	// upgrades it instead: idepkg.ErrAlreadyInstalled when that install
	// already did, and nil while it is in flight.
	upgradedElsewhere map[string]error
	failures          map[string]error
}

func (f *hostPackageManager) LatestVersion(context.Context, string) (release.Version, error) {
	return f.latest, nil
}

func (f *hostPackageManager) InstallPackageVersion(
	_ context.Context, pkgID string, version release.Version, pw repl.ProgressWriter,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.upgradedElsewhere[pkgID]; ok {
		return err
	}
	if err, ok := f.failures[pkgID]; ok {
		return err
	}
	// Mirrors idepkg.Manager: each install restarts its phases at zero.
	pw.Progress(1, 4, "MiB downloaded")
	pw.Progress(3, 4, "MiB downloaded")
	pw.Progress(1, 2, "MiB extracted")
	for _, c := range f.requirements[pkgID] {
		f.inUse[c.Package] = c.To
		if r, ok := pw.(idepkg.RequirementReporter); ok {
			r.ReportRequirement(c)
		}
	}
	pw.Progress(1, 1, "done")
	f.inUse[pkgID] = version
	f.installed = append(f.installed, pkgID)
	return nil
}

func (f *hostPackageManager) PackageVersionInUse(
	_ context.Context, pkgID string,
) (release.Version, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.inUse[pkgID]
	return v, ok, nil
}

func (f *hostPackageManager) ListInstalledPackages(
	context.Context,
) (blueiterator.Iterator[string], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return blueiterator.FromSlice(append([]string(nil), f.installed...)), nil
}

func (f *hostPackageManager) DescribePackage(
	_ context.Context, pkgID string,
) (release.Package, error) {
	return release.Package{Name: pkgID, Latest: f.latest}, nil
}

func TestHandlerUsesAnyPackageManager(t *testing.T) {
	t.Parallel()
	pm := &hostPackageManager{latest: "2", inUse: map[string]release.Version{}}
	h := New(Config{Manager: pm})
	ctx := context.Background()

	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"install", "go", "1"}, want: "Installed go@1"},
		{args: []string{"current", "go"}, want: "Version 1 of package go is in use"},
		{args: []string{"update-check"}, want: "go: 1 → 2"},
		{args: []string{"install", "go"}, want: "Installed go@2"},
		{args: []string{"update-check"}, want: "All packages are up to date."},
	}
	for _, tc := range tests {
		pw := &recordingProgressWriter{}
		it, err := h.HandleCommand(ctx, repl.Command{Name: CommandName, Args: tc.args}, pw)
		require.NoError(t, err, tc.args)
		out, err := iterator.ToSlice(ctx, it)
		require.NoError(t, err, tc.args)
		require.Len(t, out, 1, tc.args)
		assert.Contains(t, renderText(t, out[0]), tc.want, tc.args)
	}
}

func TestUpdateAllAggregatesProgress(t *testing.T) {
	t.Parallel()
	pm := &hostPackageManager{
		latest:    "2",
		inUse:     map[string]release.Version{"go": "1", "node": "1", "ripgrep": "2"},
		installed: []string{"go", "node", "ripgrep"},
	}
	h := New(Config{Manager: pm})
	ctx := context.Background()
	pw := &recordingProgressWriter{}

	it, err := h.HandleCommand(ctx,
		repl.Command{Name: CommandName, Args: []string{"update-all"}}, pw)
	require.NoError(t, err)
	out, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, out, 1)
	text := renderText(t, out[0])
	assert.Contains(t, text, "go: updated to 2")
	assert.Contains(t, text, "node: updated to 2")
	assert.Contains(t, text, "ripgrep: already at latest version (2)")

	samples := pw.get()
	require.NotEmpty(t, samples)
	for i := 1; i < len(samples); i++ {
		prev, cur := samples[i-1], samples[i]
		assert.Equal(t, prev.total, cur.total, "total changed at %d: %v", i, samples)
		assert.GreaterOrEqual(t, cur.progress, prev.progress,
			"progress went backwards at %d: %v", i, samples)
	}
	assert.Equal(t, progressSample{100, 100, "(2/2 packages)"}, samples[len(samples)-1])
}

func TestInstallPrintsRequirementChanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		changes []idepkg.RequirementChange
		want    []string
	}{
		{
			name: "none",
			want: []string{"Installed typescript@2"},
		},
		{
			name: "installed",
			changes: []idepkg.RequirementChange{
				{Package: "javascript", To: "2", RequiredBy: "typescript"},
			},
			want: []string{
				"Installed typescript@2",
				"Installed javascript@2 (required by typescript)",
			},
		},
		{
			name: "upgraded",
			changes: []idepkg.RequirementChange{
				{Package: "tsx", From: "1", To: "2", RequiredBy: "typescript"},
			},
			want: []string{
				"Installed typescript@2",
				"Upgraded tsx from 1 to 2 (required by typescript)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pm := &hostPackageManager{
				latest: "2",
				inUse:  map[string]release.Version{},
				requirements: map[string][]idepkg.RequirementChange{
					"typescript": tt.changes,
				},
			}
			h := New(Config{Manager: pm})
			ctx := context.Background()
			it, err := h.HandleCommand(ctx, repl.Command{
				Name: CommandName, Args: []string{"install", "typescript"},
			}, &recordingProgressWriter{})
			require.NoError(t, err)
			out, err := iterator.ToSlice(ctx, it)
			require.NoError(t, err)
			require.Len(t, out, 1)
			text := renderText(t, out[0])
			for _, want := range tt.want {
				assert.Contains(t, text, want)
			}
			if len(tt.changes) == 0 {
				assert.NotContains(t, text, "required by")
			}
		})
	}
}

func TestUpdateAllPrintsRequirementChanges(t *testing.T) {
	t.Parallel()
	upgraded := []string{
		"typescript: updated to 2",
		"Upgraded tsx from 1 to 2 (required by typescript)",
		"tsx: updated to 2",
	}
	tests := []struct {
		name          string
		tsxResult     error
		typescriptErr error
		want          []string
		wantErr       bool
	}{
		{
			name:      "typescript's update upgraded tsx before tsx's own",
			tsxResult: fmt.Errorf("version 2 of package tsx: %w", idepkg.ErrAlreadyInstalled),
			want:      upgraded,
		},
		{
			name: "tsx's own update returned while typescript's was upgrading tsx",
			want: upgraded,
		},
		{
			name:          "typescript's update failed to upgrade tsx",
			typescriptErr: errors.New("download failed"),
			want: []string{
				"typescript: error: update package version: download failed",
				"tsx: error: update package version: tsx is still at 1",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pm := &hostPackageManager{
				latest:    "2",
				inUse:     map[string]release.Version{"typescript": "1", "tsx": "1"},
				installed: []string{"typescript", "tsx"},
				requirements: map[string][]idepkg.RequirementChange{
					"typescript": {{Package: "tsx", From: "1", To: "2", RequiredBy: "typescript"}},
				},
				upgradedElsewhere: map[string]error{"tsx": tt.tsxResult},
				failures:          map[string]error{},
			}
			if tt.typescriptErr != nil {
				pm.failures["typescript"] = tt.typescriptErr
			}
			h := New(Config{Manager: pm})
			ctx := context.Background()

			it, err := h.HandleCommand(ctx,
				repl.Command{Name: CommandName, Args: []string{"update-all"}},
				&recordingProgressWriter{})
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			out, err := iterator.ToSlice(ctx, it)
			require.NoError(t, err)
			require.Len(t, out, 1)
			text := renderText(t, out[0])
			for _, want := range tt.want {
				assert.Contains(t, text, want)
			}
			if !tt.wantErr {
				assert.NotContains(t, text, "error")
			}
		})
	}
}

func TestInstallPackagesRequiringEachOther(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		requirements map[string][]string
		want         []string
	}{
		{
			name:         "two packages",
			requirements: map[string][]string{"a": {"b"}, "b": {"a"}},
			want:         []string{"Installed a@1", "Installed b@1 (required by a)"},
		},
		{
			name:         "three packages",
			requirements: map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"a"}},
			want: []string{
				"Installed a@1",
				"Installed c@1 (required by b)",
				"Installed b@1 (required by a)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			versions := make(map[string][]release.Version)
			for pkgID := range tt.requirements {
				versions[pkgID] = []release.Version{"1"}
			}
			rm := releaseServer(versions)
			setRequirements(t, rm, tt.requirements)
			n := idepkgtest.NewNotifications(t)
			m := newManager(t, n, rm)

			r := awaitPkgCommand(t, startPkgCommand(New(Config{Manager: m}), "install", "a"))
			require.NoError(t, r.err)
			n.RequireNoErrorNotification()

			assertRequirementLines(t, outputText(t, r.it), tt.want)
			for pkgID := range tt.requirements {
				assertInUse(t, m, pkgID, "1")
			}
		})
	}
}

func TestUpdateAllPackagesRequiringEachOther(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// installed are installed at version 1, and require these
		// packages from version 2 on.
		installed map[string][]string
		// added are not installed, and publish version 2 only.
		added map[string][]string
		want  []string
	}{
		{
			name:      "two updates requiring each other",
			installed: map[string][]string{"a": {"b"}, "b": {"a"}},
			want:      []string{"a: updated to 2", "b: updated to 2"},
		},
		{
			name:      "three updates in a cycle",
			installed: map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"a"}},
			want:      []string{"a: updated to 2", "b: updated to 2", "c: updated to 2"},
		},
		{
			name:      "two updates sharing a requirement that updates too",
			installed: map[string][]string{"a": {"c"}, "b": {"c"}, "c": nil},
			want:      []string{"a: updated to 2", "b: updated to 2", "c: updated to 2"},
		},
		{
			name:      "an update requiring a new package that requires it back",
			installed: map[string][]string{"a": {"d"}},
			added:     map[string][]string{"d": {"a"}},
			want:      []string{"a: updated to 2", "Installed d@2 (required by a)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			versions := make(map[string][]release.Version)
			var held []idepkgtest.Download
			for pkgID := range tt.installed {
				versions[pkgID] = []release.Version{"1", "2"}
				held = append(held, idepkgtest.Download{Package: pkgID, Version: "2"})
			}
			for pkgID := range tt.added {
				versions[pkgID] = []release.Version{"2"}
			}
			rm := releaseServer(versions)
			hrm := idepkgtest.NewHeldReleaseManager(rm, held...)
			n := idepkgtest.NewNotifications(t)
			m := newManager(t, n, hrm)
			for pkgID := range tt.installed {
				setRequirements(t, rm, map[string][]string{pkgID: nil})
				require.NoError(t, m.InstallPackageVersion(context.Background(), pkgID, "1", nil))
			}
			setRequirements(t, rm, tt.installed)
			setRequirements(t, rm, tt.added)

			done := startPkgCommand(New(Config{Manager: m}), "update-all")
			// Every update is in flight before any of them reads its
			// requirements.
			for _, d := range held {
				hrm.AwaitStarted(t, d)
			}
			for _, d := range held {
				hrm.Release(d, nil)
			}
			r := awaitPkgCommand(t, done)
			require.NoError(t, r.err)
			n.RequireNoErrorNotification()

			assertRequirementLines(t, outputText(t, r.it), tt.want)
			for pkgID := range tt.installed {
				assertInUse(t, m, pkgID, "2")
			}
			for pkgID := range tt.added {
				assertInUse(t, m, pkgID, "2")
			}
		})
	}
}

// releaseServer returns a release server publishing each package at its
// versions, the last of them as its latest.
func releaseServer(versions map[string][]release.Version) *idepkgtest.ReleaseManager {
	var pkgs []release.Package
	var bundles [][]release.Bundle
	for pkgID, vs := range versions {
		pkgs = append(pkgs, release.Package{Name: pkgID, Latest: vs[len(vs)-1]})
		bs := make([]release.Bundle, len(vs))
		for i, v := range vs {
			bs[i] = release.Bundle{Package: pkgID, Version: v}
		}
		bundles = append(bundles, bs)
	}
	return idepkgtest.NewReleaseManager(
		idepkgtest.MakePackages(pkgs...), idepkgtest.MakeBundles(bundles...))
}

// setRequirements makes rm serve every version of each package of
// requirements requiring its requirements.
func setRequirements(
	t *testing.T, rm *idepkgtest.ReleaseManager, requirements map[string][]string,
) {
	t.Helper()
	for pkgID, reqs := range requirements {
		files := map[string]string{"lib/" + pkgID + ".txt": pkgID + "\n"}
		if len(reqs) > 0 {
			files["config.yaml"] = "requirements:\n  - " + strings.Join(reqs, "\n  - ") + "\n"
		}
		rm.SetTarball(pkgID, idepkgtest.MakeTarball(t, files))
	}
}

type commandResult struct {
	it  iterator.Iterator[component.Responsive]
	err error
}

func startPkgCommand(h *Handler, args ...string) <-chan commandResult {
	done := make(chan commandResult, 1)
	go debug.CapturePanicReport(func() {
		it, err := h.HandleCommand(context.Background(),
			repl.Command{Name: CommandName, Args: args}, &recordingProgressWriter{})
		done <- commandResult{it, err}
	})
	return done
}

func awaitPkgCommand(t *testing.T, done <-chan commandResult) commandResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the command did not end")
		return commandResult{}
	}
}

// outputText renders the one component a command outputs.
func outputText(t *testing.T, it iterator.Iterator[component.Responsive]) string {
	t.Helper()
	out, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Len(t, out, 1)
	return renderText(t, out[0])
}

// assertRequirementLines asserts text holds each of want, and lists no
// requirement change want does not.
func assertRequirementLines(t *testing.T, text string, want []string) {
	t.Helper()
	var changes int
	for _, w := range want {
		assert.Contains(t, text, w)
		if strings.Contains(w, "required by") {
			changes++
		}
	}
	assert.Equal(t, changes, strings.Count(text, "required by"), text)
	assert.NotContains(t, text, "error")
}

func assertInUse(t *testing.T, m idepkg.PackageManager, pkgID string, want release.Version) {
	t.Helper()
	got, ok, err := m.PackageVersionInUse(context.Background(), pkgID)
	require.NoError(t, err)
	if assert.True(t, ok, "%s must be in use", pkgID) {
		assert.Equal(t, want, got, pkgID)
	}
}

// oldHost runs a Rune that does not serve package management.
type oldHost struct {
	idepkg.PackageManager
}

func (oldHost) LatestVersion(context.Context, string) (release.Version, error) {
	return "", pkgrpc.ErrUnsupported
}

func TestHandlerOnOldHost(t *testing.T) {
	t.Parallel()
	h := New(Config{Manager: oldHost{}, Host: "studio"})
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"install", "go"}},
		&recordingProgressWriter{})
	require.EqualError(t, err, "Update Rune on studio to install packages there.")
}

func renderText(t *testing.T, c component.Responsive) string {
	t.Helper()
	const width = 80
	height := c.Height(width)
	w := term.NewStringWriter(width, height)
	require.NoError(t, w.Clear(term.Attributes{}))
	c.Resize(width, height)
	c.Draw(w)
	require.NoError(t, w.Flush())
	return w.String()
}

func TestDescribeMarkdown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		wantErr string
		wants   []string
	}{
		{
			name:  "explicit version",
			args:  []string{"ripgrep", "1"},
			wants: []string{"# ripgrep", "**1**", "Fast grep.", "ripgrep release one."},
		},
		{
			name:  "resolves latest",
			args:  []string{"go"},
			wants: []string{"# go", "**1**", "Go programming language.", "Go release one.", "language"},
		},
		{
			name:    "missing package name",
			args:    nil,
			wantErr: "package name is missing",
		},
		{
			name:    "unknown package",
			args:    []string{"nope"},
			wantErr: "not found",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, _ := newHandlerForTest(t)
			it, err := h.handleDescribe(context.Background(), tc.args)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Nil(t, it)
				return
			}
			require.NoError(t, err)
			v, ok := it.Next(context.Background())
			require.True(t, ok)
			require.NotNil(t, v)
		})
	}
}

func TestDescribeMarkdownBuilder(t *testing.T) {
	t.Parallel()
	pkg := release.Package{
		Name:     "go",
		Notes:    "Go programming language.",
		Metadata: map[string]string{languageMetadataKey: "true"},
	}
	md := describeMarkdown(pkg, "1", "Go release one.")
	assert.Contains(t, md, "# go")
	assert.Contains(t, md, "**1**")
	assert.Contains(t, md, "## Package notes")
	assert.Contains(t, md, "Go programming language.")
	assert.Contains(t, md, "## Release 1 notes")
	assert.Contains(t, md, "Go release one.")
	assert.Contains(t, md, "language")

	empty := describeMarkdown(release.Package{Name: "x"}, "2", "")
	assert.Contains(t, empty, "_No package notes._")
	assert.Contains(t, empty, "_No release notes._")
}
