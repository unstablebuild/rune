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

package pkgshell

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
)

type recordingProgressWriter struct {
	mu      sync.Mutex
	samples int
}

func (w *recordingProgressWriter) Progress(_, _ int64, _ string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.samples++
}

func (w *recordingProgressWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.samples
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
	mgr := newManager(t, n, rm)
	uc := idepkg.NewUpdateChecker(mgr)
	t.Cleanup(func() { _ = uc.Close() })
	return New(Config{Manager: mgr, UpdateChecker: uc}), n
}

var syncTick = func(fn func()) bool { fn(); return true }

// newManager builds an idepkg.Manager backed by the given mocks, rooted
// at a temp directory, auto-approving install prompts.
func newManager(
	t *testing.T, n *idepkgtest.Notifications, rm *idepkgtest.ReleaseManager,
) *idepkg.Manager {
	t.Helper()
	temp := t.TempDir()
	configPath := filepath.Join(temp, "config.yaml")
	return idepkg.NewManager(n, rm, storagestub.NewInMemoryService(),
		idepkgtest.TrustStore(),
		newFixtureScheme(temp), temp, configPath, &fixtureWindowManager{},
		syncTick, term.NopInterrupter())
}

type fixtureWindowManager struct{}

func (m *fixtureWindowManager) Focus() (browserapi.Window, error) { return &fixtureWindow{}, nil }
func (m *fixtureWindowManager) Split(
	_ browserapi.Orientation, _ browserapi.Window, _ browserapi.Handler,
) (browserapi.Window, error) {
	return &fixtureWindow{}, nil
}
func (m *fixtureWindowManager) Floating(
	h browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	h.Resize(70, 20)
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	return &fixtureWindow{}, nil
}
func (m *fixtureWindowManager) Bar(_ browserapi.BarConfig, _ tui.Handler) error { return nil }
func (m *fixtureWindowManager) Tab(
	_ workspaceapi.URI, _ rune, _ string, _ browserapi.Handler,
) (browserapi.Handler, error) {
	return nil, nil
}
func (m *fixtureWindowManager) SetWindowContent(_ browserapi.Window, _ browserapi.Handler) error {
	return nil
}
func (m *fixtureWindowManager) CloseWindow(_ browserapi.Window) error { return nil }

func (m *fixtureWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

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
