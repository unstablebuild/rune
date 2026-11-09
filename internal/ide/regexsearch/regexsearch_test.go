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

package regexsearch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/codesearch"
	"unstable.build/rune/internal/handler/finder"
	"unstable.build/rune/internal/handler/locationsearch"
	"unstable.build/rune/internal/workspace"
)

const waitFor = 10 * time.Second

func TestParseArgs(t *testing.T) {
	root, err := workspaceapi.CurrentUserHostURI("/work/repo")
	require.NoError(t, err)
	tests := []struct {
		name    string
		args    []string
		want    searchapi.RegexpRequest
		wantErr string
	}{
		{
			name: "pattern",
			args: []string{`func\s+New`},
			want: searchapi.RegexpRequest{Pattern: `func\s+New`, Limit: defaultLimit},
		},
		{
			name: "words are joined by a space",
			args: []string{"retry", "backoff"},
			want: searchapi.RegexpRequest{Pattern: "retry backoff", Limit: defaultLimit},
		},
		{
			name: "every flag",
			args: []string{"-i", "-r", "internal", "--root=/work/repo/cmd", "-n", "5", "ret"},
			want: searchapi.RegexpRequest{Pattern: "ret", IgnoreCase: true, Roots: []string{"internal", "cmd"}, Limit: 5},
		},
		{
			name: "plain text",
			args: []string{"-F", "a.b(c)"},
			want: searchapi.RegexpRequest{Pattern: `a\.b\(c\)`, Limit: defaultLimit},
		},
		{
			name: "dash dash",
			args: []string{"--", "-n", "flag"},
			want: searchapi.RegexpRequest{Pattern: "-n flag", Limit: defaultLimit},
		},
		{
			name: "spaces",
			args: []string{"  "},
			want: searchapi.RegexpRequest{Pattern: "  ", Limit: defaultLimit},
		},
		{name: "no pattern", args: nil, wantErr: "usage: searchregex"},
		{name: "help", args: []string{"-h"}, wantErr: "usage: searchregex"},
		{name: "unknown flag", args: []string{"--nope", "q"}, wantErr: "searchregex: unknown flag: --nope"},
		{name: "negative limit", args: []string{"-n", "-1", "q"}, wantErr: "--limit must not be negative"},
		{name: "invalid pattern", args: []string{"a("}, wantErr: "searchregex: error parsing regexp: missing closing )"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args, root)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestComplete(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{args: nil, want: nil},
		{args: []string{"retry"}, want: nil},
		{args: []string{"-"}, want: flagNames},
		{args: []string{"retry", "--r"}, want: []string{"--root"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			it, _, err := New(Config{}).Complete(context.Background(), textapi.Command{Name: Name, Args: tt.args})
			require.NoError(t, err)
			got, err := blueiterator.ToSlice(context.Background(), it)
			require.NoError(t, err)
			if tt.want == nil {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHandleCommandSearches(t *testing.T) {
	h := newHarness(t, nil)
	require.NoError(t, h.run("-i", "-r", "billing", "ROUND\\w+"))

	out := draw(t, h.wm.waitPicker(t),
		"billing/invoice.go:42:9: return roundInvoiceTotal(lines)",
		"billing/invoice.go:45:6: func roundInvoiceTotal(lines []Line) int {",
		"\n  return roundInvoiceTotal(lines)",
		" REGEXP █▓▒░ 2 lines in 1 file · ",
		"ms · ignore case · in billing ",
		" 1 of 2 in file · 42:9 ")
	assert.Contains(t, out, "2/2", "the list counts the matches")
	assert.NotContains(t, out, "geo/route.go")
}

func TestHandleCommandStopsAtTheLimit(t *testing.T) {
	h := newHarness(t, nil)
	require.NoError(t, h.run("-n", "1", "round"))
	draw(t, h.wm.waitPicker(t), "ms · stopped at the first line ", "billing/invoice.go:42:9:")
}

func TestHandleCommandFindsNothing(t *testing.T) {
	h := newHarness(t, nil)
	require.NoError(t, h.run("palindrome"))
	draw(t, h.wm.waitPicker(t), " REGEXP █▓▒░ Nothing matches “palindrome” · ")
}

func TestHandleCommandSearchFails(t *testing.T) {
	h := newHarness(t, &stubSearcher{err: errors.New("boom")})
	require.NoError(t, h.run("retry"))
	draw(t, h.wm.waitPicker(t), " ERROR █▓▒░ Search failed: boom ")
}

func TestHandleCommandNotesAMissingIndex(t *testing.T) {
	h := newHarness(t, &stubSearcher{status: codesearch.Status{Building: true}})
	require.NoError(t, h.run("retry"))
	draw(t, h.wm.waitPicker(t), " REGEXP █▓▒░ Nothing matches “retry” · ", "ms · still indexing ")
}

func TestHandleCommandErrors(t *testing.T) {
	h := newHarness(t, nil)
	assert.ErrorIs(t, h.run(), errUsage)
	assert.ErrorContains(t, h.run("a("), "missing closing )")
	assert.Nil(t, h.wm.picker())
}

func TestNote(t *testing.T) {
	tests := []struct {
		status codesearch.Status
		want   string
	}{
		{status: codesearch.Status{Ready: true}, want: ""},
		{status: codesearch.Status{}, want: ""},
		{status: codesearch.Status{Building: true}, want: "still indexing"},
		{status: codesearch.Status{Err: "disk full"}, want: "indexing failed: disk full"},
	}
	for _, tt := range tests {
		h := New(Config{Searcher: &stubSearcher{status: tt.status}})
		assert.Equal(t, tt.want, h.note())
	}
}

// harness runs the command over a workspace, with searcher, or an index
// of the workspace when it is nil.
type harness struct {
	wm      *stubWindowManager
	handler *Handler
}

func newHarness(t *testing.T, searcher Searcher) *harness {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"billing/invoice.go": "package billing\n\n" + strings.Repeat("// pad\n", 37) +
			"func total(lines []Line) int {\n\tsum := 0\n\treturn roundInvoiceTotal(lines)\n}\n\n" +
			"func roundInvoiceTotal(lines []Line) int {\n\treturn 0\n}\n",
		"geo/route.go": "package geo\n\nfunc roundTrip(a, b Place) []Place {\n\treturn nil\n}\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	root, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	fsys, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fsys.Close() })
	if searcher == nil {
		idx := codesearch.Open(codesearch.Config{
			Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index"),
			Notify: stubNotifications{}, Schedule: func(fn func()) bool { fn(); return true },
		})
		t.Cleanup(func() { _ = idx.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), waitFor)
		defer cancel()
		require.NoError(t, idx.Wait(ctx))
		searcher = idx
	}

	h := &harness{wm: &stubWindowManager{}}
	h.handler = New(Config{
		Root: root, Searcher: searcher,
		Clients: finder.Clients{
			ResourceOpener: stubResourceOpener{}, WindowManager: h.wm, Interrupter: term.NopInterrupter(),
			FileSystem: fsys,
		},
		ScheduleNextTick: func(fn func()) bool { fn(); return true },
	})
	t.Cleanup(func() {
		if p := h.wm.picker(); p != nil {
			_ = p.Close()
		}
	})
	return h
}

func (h *harness) run(args ...string) error {
	return h.handler.HandleCommand(context.Background(), textapi.Command{Name: Name, Args: args})
}

// draw renders the picker once it shows every one of want.
func draw(t *testing.T, picker *locationsearch.Handler, want ...string) string {
	t.Helper()
	const width, height = 100, 30
	var out string
	ok := assert.Eventually(t, func() bool {
		picker.Resize(width, height)
		w := term.NewStringWriter(width, height)
		picker.Draw(w)
		require.NoError(t, w.Flush())
		out = w.String()
		for _, s := range want {
			if !strings.Contains(out, s) {
				return false
			}
		}
		return true
	}, waitFor, 10*time.Millisecond)
	require.True(t, ok, "%q not all in:\n%s", want, out)
	return out
}

type stubSearcher struct {
	err    error
	status codesearch.Status
}

func (s *stubSearcher) RegexpSearch(context.Context, searchapi.RegexpRequest) (iterator.Iterator[searchapi.RegexpResult], error) {
	if s.err != nil {
		return nil, s.err
	}
	return iterator.FromSlice[searchapi.RegexpResult](nil), nil
}

func (s *stubSearcher) Status() codesearch.Status { return s.status }

type stubWindow uint64

func (w stubWindow) WindowID() uint64 { return uint64(w) }

type stubWindowManager struct {
	mu       sync.Mutex
	floating *locationsearch.Handler
}

func (m *stubWindowManager) picker() *locationsearch.Handler {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.floating
}

func (m *stubWindowManager) waitPicker(t *testing.T) *locationsearch.Handler {
	t.Helper()
	require.Eventually(t, func() bool { return m.picker() != nil }, waitFor, 10*time.Millisecond)
	return m.picker()
}

func (m *stubWindowManager) Focus() (browserapi.Window, error) { return stubWindow(1), nil }
func (m *stubWindowManager) Split(browserapi.Orientation, browserapi.Window, browserapi.Handler) (browserapi.Window, error) {
	return stubWindow(2), nil
}

func (m *stubWindowManager) Floating(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.floating = h.(*locationsearch.Handler)
	return stubWindow(3), nil
}
func (m *stubWindowManager) Bar(browserapi.BarConfig, tui.Handler) error { return nil }
func (m *stubWindowManager) Tab(workspaceapi.URI, rune, string, browserapi.Handler) (browserapi.Handler, error) {
	return nil, errors.New("no tabs")
}
func (m *stubWindowManager) SetWindowContent(browserapi.Window, browserapi.Handler) error { return nil }
func (m *stubWindowManager) CloseWindow(browserapi.Window) error                          { return nil }
func (m *stubWindowManager) SetTabStatus(workspaceapi.URI, browserapi.TabStatus) error    { return nil }

type stubResourceOpener struct{}

func (stubResourceOpener) Open(workspaceapi.URI) (browserapi.Handler, error) {
	return nil, errors.New("not opened")
}
func (stubResourceOpener) Show(workspaceapi.URI) error { return nil }

type stubNotifications struct{}

func (stubNotifications) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}
func (stubNotifications) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}
func (stubNotifications) UpdateNotificationProgress(string, string, int64, int64) error { return nil }
