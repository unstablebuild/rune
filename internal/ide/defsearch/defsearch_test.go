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

package defsearch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/handler/finder"
	"unstable.build/rune/internal/handler/locationsearch"
	"unstable.build/rune/internal/ide/syntax/symboldb"
	"unstable.build/rune/internal/workspace"
)

const waitFor = 10 * time.Second

func TestManuals(t *testing.T) {
	var names []string
	for _, m := range Manuals() {
		names = append(names, m.Name)
		assert.Equal(t, synopsis, m.Synopsis)
	}
	assert.Equal(t, []string{FuncName, VarName, TypeName}, names)
	assert.Panics(t, func() { New("searchast", Config{}) })
}

func TestParseArgs(t *testing.T) {
	root, err := workspaceapi.CurrentUserHostURI("/work/repo")
	require.NoError(t, err)
	funcs := []symboldb.Kind{symboldb.KindFunction, symboldb.KindMethod}
	tests := []struct {
		name    string
		cmd     string
		args    []string
		want    symboldb.DefinitionQuery
		wantErr string
	}{
		{name: "functions", cmd: FuncName, want: symboldb.DefinitionQuery{Kinds: funcs}},
		{name: "variables", cmd: VarName, want: symboldb.DefinitionQuery{Kinds: []symboldb.Kind{symboldb.KindVar}}},
		{name: "types", cmd: TypeName, want: symboldb.DefinitionQuery{Kinds: []symboldb.Kind{symboldb.KindType}}},
		{
			name: "every flag", cmd: FuncName,
			args: []string{"-l", "go", "--lang=python", "-r", "internal", "--root=/work/repo/cmd/rune", "-n", "5"},
			want: symboldb.DefinitionQuery{
				Kinds: funcs, Langs: []string{"go", "python"}, Roots: []string{"internal", "cmd/rune"}, Limit: 5,
			},
		},
		{name: "help", cmd: VarName, args: []string{"-h"}, wantErr: "usage: searchvar"},
		{name: "positional", cmd: TypeName, args: []string{"Invoice"}, wantErr: "usage: searchtype"},
		{name: "unknown flag", cmd: FuncName, args: []string{"--nope"}, wantErr: "searchfunc: unknown flag: --nope"},
		{name: "negative limit", cmd: FuncName, args: []string{"-n", "-1"}, wantErr: "--limit must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New(tt.cmd, Config{Root: root}).parseArgs(tt.args)
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
		{args: nil},
		{args: []string{"go"}},
		{args: []string{"-"}, want: flagNames},
		{args: []string{"-l", "go", "--r"}, want: []string{"--root"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			it, _, err := New(FuncName, Config{}).Complete(context.Background(), textapi.Command{Name: FuncName, Args: tt.args})
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

func TestHandleCommandLists(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		args    []string
		want    []string
		notWant []string
	}{
		{
			name: "functions",
			cmd:  FuncName,
			want: []string{
				"billing/invoice.go:40:6: total",
				"billing/invoice.go:45:6: roundInvoiceTotal",
				"geo/route.go:3:6: roundTrip",
				" FUNCTIONS █▓▒░ 3 definitions in 2 files · ",
				" function · go · 1 of 2 in file ",
			},
			notWant: []string{"invoice.go:3:6"},
		},
		{
			name:    "types in a folder",
			cmd:     TypeName,
			args:    []string{"-r", "billing", "-l", "go"},
			want:    []string{"billing/invoice.go:3:6: Line", " TYPES █▓▒░ 1 definition in 1 file · ", "ms · go · in billing "},
			notWant: []string{"roundTrip"},
		},
		{
			name: "variables at the limit",
			cmd:  VarName,
			args: []string{"-n", "1"},
			want: []string{"billing/invoice.go:41:2: sum", "ms · stopped at the first definition "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.cmd, nil)
			require.NoError(t, h.run(tt.args...))
			out := draw(t, h.wm.waitPicker(t), tt.want...)
			for _, s := range tt.notWant {
				assert.NotContains(t, out, s)
			}
		})
	}
}

func TestHandleCommandFindsNothing(t *testing.T) {
	h := newHarness(t, TypeName, nil)
	require.NoError(t, h.run("-l", "python"))
	draw(t, h.wm.waitPicker(t), " TYPES █▓▒░ No definitions · ", "ms · python ")
}

func TestHandleCommandSearchFails(t *testing.T) {
	h := newHarness(t, FuncName, &stubSearcher{err: errors.New("boom")})
	require.NoError(t, h.run())
	draw(t, h.wm.waitPicker(t), " ERROR █▓▒░ Search failed: boom ")
}

func TestHandleCommandNotesIndexing(t *testing.T) {
	h := newHarness(t, FuncName, &stubSearcher{status: symboldb.Status{Loaded: true, Pending: 12}})
	require.NoError(t, h.run())
	draw(t, h.wm.waitPicker(t), " FUNCTIONS █▓▒░ No definitions · ", "ms · still indexing · 12 files left ")
}

func TestHandleCommandErrors(t *testing.T) {
	h := newHarness(t, VarName, nil)
	assert.ErrorContains(t, h.run("x"), "usage: searchvar")
	assert.Nil(t, h.wm.picker())
}

func TestNote(t *testing.T) {
	tests := []struct {
		status symboldb.Status
		want   string
	}{
		{status: symboldb.Status{Loaded: true, Scanned: true}, want: ""},
		{status: symboldb.Status{Loaded: true, Scanned: true, Pending: 3}, want: ""},
		{status: symboldb.Status{}, want: "still indexing"},
		{status: symboldb.Status{Loaded: true, Pending: 1}, want: "still indexing · 1 file left"},
		{status: symboldb.Status{Loaded: true, Pending: 40}, want: "still indexing · 40 files left"},
		{status: symboldb.Status{Loaded: true, Scanned: true, Err: "disk full"}, want: "indexing failed: disk full"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, note(tt.status))
	}
}

// harness runs a command over a workspace, with searcher, or a stub
// searcher holding the workspace's definitions when it is nil.
type harness struct {
	cmd     string
	wm      *stubWindowManager
	handler *Handler
}

var defs = []symboldb.Definition{
	{Path: "billing/invoice.go", Line: 3, Col: 6, Kind: symboldb.KindType, Lang: "go", Name: "Line"},
	{Path: "billing/invoice.go", Line: 40, Col: 6, Kind: symboldb.KindFunction, Lang: "go", Name: "total"},
	{Path: "billing/invoice.go", Line: 41, Col: 2, Kind: symboldb.KindVar, Lang: "go", Name: "sum"},
	{Path: "billing/invoice.go", Line: 45, Col: 6, Kind: symboldb.KindFunction, Lang: "go", Name: "roundInvoiceTotal"},
	// A name captured as two kinds is listed once.
	{Path: "billing/invoice.go", Line: 45, Col: 6, Kind: symboldb.KindMethod, Lang: "go", Name: "roundInvoiceTotal"},
	{Path: "geo/route.go", Line: 3, Col: 6, Kind: symboldb.KindFunction, Lang: "go", Name: "roundTrip"},
	{Path: "geo/route.go", Line: 4, Col: 2, Kind: symboldb.KindVar, Lang: "go", Name: "places"},
}

func newHarness(t *testing.T, cmd string, searcher Searcher) *harness {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"billing/invoice.go": "package billing\n\ntype Line int\n" + strings.Repeat("// pad\n", 36) +
			"func total(lines []Line) int {\n\tsum := 0\n\treturn roundInvoiceTotal(lines)\n}\n\n" +
			"func roundInvoiceTotal(lines []Line) int {\n\treturn 0\n}\n",
		"geo/route.go": "package geo\n\nfunc roundTrip(a, b Place) []Place {\n\tplaces := nil\n\treturn places\n}\n",
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
		searcher = &stubSearcher{defs: defs, status: symboldb.Status{Loaded: true, Scanned: true}}
	}
	h := &harness{cmd: cmd, wm: &stubWindowManager{}}
	h.handler = New(cmd, Config{
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
	return h.handler.HandleCommand(context.Background(), textapi.Command{Name: h.cmd, Args: args})
}

// draw renders the picker once it shows every one of want.
func draw(t *testing.T, picker *locationsearch.Handler, want ...string) string {
	t.Helper()
	const width, height = 130, 30
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

// stubSearcher filters defs as an index would.
type stubSearcher struct {
	defs   []symboldb.Definition
	err    error
	status symboldb.Status
}

func (s *stubSearcher) Definitions(_ context.Context, req symboldb.DefinitionQuery) (iterator.Iterator[symboldb.Definition], error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []symboldb.Definition
	for _, d := range s.defs {
		if req.Limit > 0 && len(out) == req.Limit {
			break
		}
		if !slices.Contains(req.Kinds, d.Kind) || len(req.Langs) > 0 && !slices.Contains(req.Langs, d.Lang) ||
			len(req.Roots) > 0 && !slices.ContainsFunc(req.Roots, func(r string) bool { return strings.HasPrefix(d.Path, r+"/") }) {
			continue
		}
		out = append(out, d)
	}
	return iterator.FromSlice(out), nil
}

func (s *stubSearcher) Status() symboldb.Status { return s.status }

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
