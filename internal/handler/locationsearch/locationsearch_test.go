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

package locationsearch

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/handler/finder"
	"unstable.build/rune/internal/handler/search"
	"unstable.build/rune/internal/ide/vctrl/testgit"
	"unstable.build/rune/internal/workspace"
)

func TestParseLocationLine(t *testing.T) {
	fs := testFS(fileContent{
		"/main.go":      "x",
		"/C:\\foo\\bar": "x",
	})
	tests := []struct {
		name     string
		in       string
		wantPath string
		wantY    int
		wantX    int
		wantCol  bool
		wantOK   bool
	}{
		{name: "path only", in: "main.go", wantPath: "/main.go", wantOK: true},
		{name: "path:line", in: "main.go:42", wantPath: "/main.go", wantY: 41, wantOK: true},
		{name: "path:line:col", in: "main.go:42:5", wantPath: "/main.go", wantY: 41, wantX: 4, wantCol: true, wantOK: true},
		{name: "trim whitespace", in: "  main.go:1  ", wantPath: "/main.go", wantY: 0, wantOK: true},
		{name: "windows path", in: "C:\\foo\\bar:10:3", wantPath: "/C:\\foo\\bar", wantY: 9, wantX: 2, wantCol: true, wantOK: true},
		{
			name: "git grep -n --column",
			// `git grep -n --column foo` outputs lines like:
			// `path:line:col:matched line content`. We must parse the
			// path/line/col and ignore the matched-content suffix.
			in:       "main.go:42:5:    return foo",
			wantPath: "/main.go", wantY: 41, wantX: 4, wantCol: true, wantOK: true,
		},
		{
			name:     "grep -n line only with content",
			in:       "main.go:42:matched line content",
			wantPath: "/main.go", wantY: 41, wantOK: true,
		},
		{name: "empty", in: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := parseLocationLineDetailed(fs, tt.in)
			assert.Equal(t, tt.wantOK, p.ok)
			if !tt.wantOK {
				return
			}
			assert.Equal(t, tt.wantPath, p.uri.Path())
			assert.Equal(t, tt.wantY, p.coords.Y)
			assert.Equal(t, tt.wantX, p.coords.X)
			assert.Equal(t, tt.wantCol, p.hasColumn,
				"hasColumn mismatch for %q", tt.in)
		})
	}
}

func TestHandlerCloseClosesInner(t *testing.T) {
	h := newTestHandler(t, "echo a.go:1\necho a.go:2", fileContent{
		"/a.go": "line0\nline1",
	}, nil)
	require.NoError(t, h.Close())
}

// TestHandlerGitGrepIntegration drives the locationsearch handler
// against a real `git grep -n --column` process running in a
// throw-away git repo. It validates that the parser handles the
// `path:line:col:matched-content` format git grep emits and that the
// preview pane loads the actual file contents through the workspace
// FileSystem.
func TestHandlerGitGrepIntegration(t *testing.T) {
	requireGit(t)
	dir := newGitRepo(t, fileContent{
		"a.go": "line one\nlook for TODO here\nline three\n",
	})
	h := newSchemeHandler(t, dir, 4,
		"cd "+dir+" && git grep -n --column TODO")
	waitForScan(t, h)

	expected := strings.TrimPrefix(`
 line one               
 look for TODO here     
 line three             
                        
 ────────────────────── 
 ▐                  1/1 
 a.go:2:10:look for TOD 
                        `, "\n")
	handlertest.RunHandlerSequence(t, h, 24, 8, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: expected},
	})
}

// TestHandlerGrepAlias drives the handler with the shell expansion of
// the `:grep` alias (`grep -n -R $1`). The list entries are
// `path:line:matched-content` (no column), so the preview should load
// the file but leave the target line unhighlighted.
func TestHandlerGrepAlias(t *testing.T) {
	requireExec(t, "grep")
	dir := newWorkspaceDir(t, fileContent{
		"a.go": "TODO first\nbody line\nfooter line\n",
	})
	h := newSchemeHandler(t, dir, 4,
		"cd "+dir+" && grep -n -R TODO .")
	waitForScan(t, h)

	expected := strings.TrimPrefix(`
 TODO first             
 body line              
 footer line            
                        
 ────────────────────── 
 ▐                  1/1 
 ./a.go:1:TODO first    
                        `, "\n")
	handlertest.RunHandlerSequence(t, h, 24, 8, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: expected},
	})
	assert.False(t, h.highlightTargetLine,
		"grep alias has no column, target line must not be highlighted")
}

// TestHandlerTodoGrepAlias drives the handler with the shell expansion
// of the `:todogrep` alias (`grep -n -R -E (TODO|FIXME)`) against a
// repo containing both kinds of markers. It validates that the list
// contains every match and that typing a fuzzy-search query narrows
// the list to a single matching entry, which becomes the new
// preview target.
func TestHandlerTodoGrepAlias(t *testing.T) {
	requireExec(t, "grep")
	dir := newWorkspaceDir(t, fileContent{
		"a.go": "intro\nTODO first\nFIXME second\noutro\n",
	})
	h := newSchemeHandler(t, dir, 4,
		"cd "+dir+" && grep -n -R -E '(TODO|FIXME)' .")
	waitForScan(t, h)

	unfiltered := strings.TrimPrefix(`
 intro                  
 TODO first             
 FIXME second           
 outro                  
 ────────────────────── 
 ▐                  2/2 
 ./a.go:2:TODO first    
 ./a.go:3:FIXME second  `, "\n")
	filtered := strings.TrimPrefix(`
 intro                  
 TODO first             
 FIXME second           
 outro                  
 ────────────────────── 
 FIXME▐             1/2 
 ./a.go:3:FIXME second  
                        `, "\n")
	handlertest.RunHandlerSequence(t, h, 24, 8, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: unfiltered},
		{InputSequence: "FIXME", Expected: filtered},
	})
	sel, ok := h.Selection()
	assert.True(t, ok)
	assert.Equal(t, "./a.go:3:FIXME second", sel)
}

// TestHandlerConflictsAlias drives the handler with the shell
// expansion of the `:conflicts` alias against a file containing real
// merge-conflict markers. The alias uses ERE alternation so every
// git implementation lists the same three markers; with BRE `\|`
// Apple's git dropped the `^=======$` branch while GNU's kept it.
// Each entry includes a column, so the target line is highlighted.
func TestHandlerConflictsAlias(t *testing.T) {
	requireGit(t)
	const content = "before\n" +
		"<<<<<<< HEAD\n" +
		"theirs\n" +
		"=======\n" +
		"mine\n" +
		">>>>>>> branch\n" +
		"after\n"
	dir := newGitRepo(t, fileContent{"a.go": content})
	h := newSchemeHandler(t, dir, 7,
		"cd "+dir+
			` && git grep -n --column -E '^(<<<<<<<|=======$|>>>>>>>)'`)
	waitForScan(t, h)

	expected := strings.TrimPrefix(`
 before                 
 <<<<<<< HEAD           
 theirs                 
 =======                
 mine                   
 >>>>>>> branch         
 after                  
 ────────────────────── 
 ▐                  3/3 
 a.go:2:1:<<<<<<< HEAD  
 a.go:4:1:=======       `, "\n")
	handlertest.RunHandlerSequence(t, h, 24, 11, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: expected},
	})
	assert.True(t, h.highlightTargetLine,
		"conflicts alias includes a column, target line must be highlighted")
}

// TestHandlerGitChangesAlias drives the handler with the shell
// expansion of the `:gitchanges` alias against a real git repo
// containing both an in-place edit (one hunk in the middle of a
// committed file) and a brand-new file (one hunk starting at line
// 1). The awk script reads `git diff -U0` from a sub-pipe, so the
// finder spawns one process whose stdout already contains the
// hunk locations. Each entry carries column 1, so the target line
// is visually highlighted in the preview.
func TestHandlerGitChangesAlias(t *testing.T) {
	requireGit(t)
	requireExec(t, "awk")
	dir := newGitRepo(t, fileContent{
		// f.go is committed; its diff will produce two single-line
		// hunks at the post-image lines marked Z below.
		"f.go": "alpha\nbeta\ngamma\ndelta\nepsilon\n",
	})
	// Edit two non-adjacent lines so -U0 emits two distinct hunks.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.go"),
		[]byte("alpha\nbetaZ\ngamma\ndeltaZ\nepsilon\n"), 0o644))
	// Add a brand-new file and stage it so it appears in
	// `git diff HEAD`.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "g.go"),
		[]byte("hello\nworld\n"), 0o644))
	testgit.Run(t, dir, "add", "g.go")

	const awkScript = `awk 'BEGIN { cmd = "git diff HEAD --no-color -U0";` +
		` while ((cmd | getline line) > 0) { if (substr(line, 1, 6) ==` +
		` "+++ b/") f = substr(line, 7); else if (substr(line, 1, 2)` +
		` == "@@") { match(line, /[+][0-9]+/);` +
		` print f ":" substr(line, RSTART+1, RLENGTH-1) ":1:" line` +
		` } } }'`
	h := newSchemeHandler(t, dir, 5, "cd "+dir+" && "+awkScript)
	waitForScan(t, h)

	// f.go's two hunks (post-image lines 2 and 4) and g.go's one
	// hunk (post-image line 1) all show up. Focus starts on the
	// last entry, which is g.go:1:1.
	expected := strings.TrimPrefix(`
 alpha                  
 betaZ                  
 gamma                  
 deltaZ                 
 epsilon                
 ────────────────────── 
 ▐                  3/3 
 f.go:2:1:@@ -2 +2 @@ a 
 f.go:4:1:@@ -4 +4 @@ g 
 g.go:1:1:@@ -0,0 +1,2  `, "\n")
	handlertest.RunHandlerSequence(t, h, 24, 10, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: expected},
	})
	assert.True(t, h.highlightTargetLine,
		"gitchanges alias includes a column, target line must be highlighted")
}

func TestHandlerSelectionAndPreview(t *testing.T) {
	fc := fileContent{"/a.go": "alpha\nbeta\ngamma"}
	cfg := DefaultConfig()
	cfg.PreviewContextLines = 4
	h := newTestHandler(t, "echo 'a.go:1'", fc, nil, cfg)
	t.Cleanup(func() { _ = h.Close() })
	waitForScan(t, h)

	expected := strings.TrimPrefix(`
 alpha                  
 beta                   
 gamma                  
                        
 ────────────────────── 
 ▐                  1/1 
 a.go:1                 
                        `, "\n")
	handlertest.RunHandlerSequence(t, h, 24, 8, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: expected},
	})

	sel, ok := h.Selection()
	assert.True(t, ok)
	assert.Equal(t, "a.go:1", sel)
}

func TestHandlerNoParseableLines(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PreviewContextLines = 4
	h := newTestHandler(t, "echo 'garbage'", fileContent{}, nil, cfg)
	t.Cleanup(func() { _ = h.Close() })
	waitForScan(t, h)

	// "garbage" parses as a path and resolves through stubFS to a
	// missing file, so previewCells stays nil and the preview area
	// renders as blank rows above the separator and list.
	expected := strings.TrimPrefix(`
                        
                        
                        
                        
 ────────────────────── 
 ▐                  1/1 
 garbage                
                        `, "\n")
	handlertest.RunHandlerSequence(t, h, 24, 8, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: expected},
	})
	assert.Nil(t, h.previewCells)
}

func TestHandlerHighlightApplied(t *testing.T) {
	fc := fileContent{"/a.go": "alpha\nbeta"}
	parser := &stubParser{
		highlightFn: func(_ workspaceapi.URI, _ string) (
			iterator.Iterator[textapi.Location], error,
		) {
			return iterator.FromSlice([]textapi.Location{
				{
					From: term.Coordinates{X: 0, Y: 0},
					To:   term.Coordinates{X: 4, Y: 0},
					Attr: term.Attributes{Fg: term.ColorGreen},
				},
			}), nil
		},
	}
	// Construct without parser to avoid spawning the async highlight
	// goroutine; then attach the parser and invoke loadHighlights
	// directly from the test goroutine for race-free assertions.
	h := newTestHandler(t, "echo 'a.go:1'", fc, nil)
	t.Cleanup(func() { _ = h.Close() })
	waitForScan(t, h)
	h.Resize(20, 8)

	h.loadPreview("a.go:1")
	require.NotNil(t, h.previewCells)

	h.parser = parser
	uri, _, ok := parseLocationLine(h.fs, "a.go:1")
	require.True(t, ok)
	baseCells := term.CloneCells(h.previewCells)
	h.loadHighlights(uri, "alpha\nbeta", baseCells)

	for x := range 4 {
		assert.Equal(t, term.ColorGreen, h.previewCells[0][x].Fg, "char %d should be green", x)
	}
}

func TestHandlerDimensions(t *testing.T) {
	cmd := "echo 'a.go:1'"
	h := newTestHandler(t, cmd, fileContent{"/a.go": "alpha"}, nil)
	t.Cleanup(func() { _ = h.Close() })

	w, ht := h.Dimensions()
	assert.Equal(t, defaultMinPreviewWidth, w)
	assert.Equal(t, defaultMaxListHeight+defaultPreviewContextLines+defaultSeparatorHeight, ht)
}

// TestHandlerHighlightTargetLine asserts the preview only highlights
// the target line when the input location specifies a column. Without
// a column the target line defaults to the first line of the file
// and a reverse-video bar would be misleading.
func TestHandlerHighlightTargetLine(t *testing.T) {
	fc := fileContent{"/a.go": "alpha\nbeta\ngamma"}
	h := newTestHandler(t, "echo 'a.go:2'", fc, nil)
	t.Cleanup(func() { _ = h.Close() })
	waitForScan(t, h)
	h.Resize(20, 8)

	h.loadPreview("a.go:2")
	require.NotNil(t, h.previewCells)
	assert.False(t, h.highlightTargetLine,
		"no column => target line should not be visually highlighted")

	h.loadPreview("a.go:2:1")
	require.NotNil(t, h.previewCells)
	assert.True(t, h.highlightTargetLine,
		"with column => target line should be visually highlighted")
}

// TestHandlerScanSpinnerAnimates asserts that while the inner
// finder is still scanning, Draw renders a component.Animation
// frame in the freed top-right cell (innerW-1) on the search-bar
// row, and that the inner finder is rendered into the remaining
// width-1 columns so its match-counter does not overlap the
// spinner. Once the scan completes the spinner cell becomes blank
// and the inner finder reclaims the full width on the next render.
func TestHandlerScanSpinnerAnimates(t *testing.T) {
	requireExec(t, "sh")
	requireExec(t, "sleep")
	// Run a real subprocess via newSchemeHandler (through
	// workspace.NewFileScheme) so the shell `sleep` actually
	// blocks. The in-process localExecutor used by other tests is
	// synchronous and would close ScanDone before New returns,
	// preventing us from observing the spinner.
	dir := newWorkspaceDir(t, fileContent{"a.go": "x"})
	uri, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	clients := finder.Clients{
		ResourceOpener: stubResourceOpener{},
		WindowManager:  stubWindowManager{},
		Interrupter:    term.NopInterrupter(),
		Notifications:  stubNotifications{},
		FileSystem:     scheme,
		Executor:       schemeExecutor{scheme: scheme},
	}
	cfg := DefaultConfig()
	cfg.PreviewContextLines = 1
	cfg.ListConfig = search.ListConfig{
		Algo:        search.FuzzyMatch,
		Interrupter: term.NopInterrupter(),
		// Async so scanData runs concurrently with our render.
		SyncSearch: false,
	}
	// 500ms sleep guarantees the scan goroutine is still running
	// when we render right after New returns.
	const cmd = "sleep 0.5 && echo a.go:1"
	h, err := New(context.Background(), clients, stubWindow(0),
		cfg, nil, syncTick, cmd, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	const width, height = 20, 8
	h.Resize(width, height)

	require.True(t, h.scanRunning.Load(),
		"watcher goroutine must be running while scanData is active")
	require.NotNil(t, h.scanAnim,
		"scanAnim must be initialised while scanning")

	// Render once. The animation cell sits at column width-1 of
	// the search-bar row, which is at row previewH+separatorH. The
	// frame char is whatever ProgressAnimationFrames is at index 0
	// on the very first Draw.
	w := term.NewStringWriter(width, height)
	h.Draw(w)
	require.NoError(t, w.Flush())
	out := w.String()
	rows := strings.Split(out, "\n")
	require.GreaterOrEqual(t, len(rows), 3)
	searchBarRow := rows[h.previewH+h.cfg.SeparatorHeight]
	require.Len(t, []rune(searchBarRow), width)
	spinnerCell := []rune(searchBarRow)[width-1]
	assert.True(t, containsRune(progressAnimationFrames(), spinnerCell),
		"top-right cell must hold a ProgressAnimationFrames glyph; "+
			"got %q in row %q", string(spinnerCell), searchBarRow)

	// The inner finder is rendered into width-1 columns so the
	// match-counter `/` (and the matches/total digits) sit to the
	// LEFT of the spinner cell, never on it.
	assert.NotContains(t, string(spinnerCell), "/",
		"the spinner cell must not contain the match-counter slash")

	// Wait for the command to finish and the scan to drain.
	waitForScan(t, h)
	deadline := time.Now().Add(time.Second)
	for h.scanRunning.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	assert.False(t, h.scanRunning.Load(),
		"spinner must stop once the inner ScanDone channel closes")

	// After scan completes the inner finder must reclaim the full
	// width on the next Resize. The `/` separator from the
	// match-counter therefore lands somewhere on the search-bar
	// row and the spinner frame is gone.
	h.Resize(width, height)
	w2 := term.NewStringWriter(width, height)
	h.Draw(w2)
	require.NoError(t, w2.Flush())
	out2 := w2.String()
	assert.Contains(t, out2, "/",
		"after scanning the match-counter must render a literal `/`")
	rows2 := strings.Split(out2, "\n")
	searchBarRow2 := rows2[h.previewH+h.cfg.SeparatorHeight]
	cells2 := []rune(searchBarRow2)
	assert.False(t,
		containsRune(progressAnimationFrames(), cells2[len(cells2)-1]),
		"after scanning the top-right cell must not contain a "+
			"spinner frame; got %q", searchBarRow2)
}

// progressAnimationFrames returns the SDK's progress-animation
// frames concatenated into a single string for membership tests.
func progressAnimationFrames() string {
	frames, _ := component.ProgressAnimationFrames()
	return strings.Join(frames, "")
}

func containsRune(s string, r rune) bool {
	return strings.ContainsRune(s, r)
}

// --- helpers --------------------------------------------------------------

func newTestHandler(
	t *testing.T, command string, fc fileContent, parser syntaxapi.Parser,
	cfgs ...Config,
) *Handler {
	t.Helper()
	clients := finder.Clients{
		ResourceOpener: stubResourceOpener{},
		WindowManager:  stubWindowManager{},
		Interrupter:    term.NopInterrupter(),
		Notifications:  stubNotifications{},
		FileSystem:     testFS(fc),
		Executor:       &localExecutor{},
	}
	cfg := DefaultConfig()
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	cfg.ListConfig = search.ListConfig{
		Algo:        search.FuzzyMatch,
		Interrupter: term.NopInterrupter(),
		SyncSearch:  true,
	}
	h, err := New(context.Background(), clients, stubWindow(0),
		cfg, parser, syncTick, command, nil)
	require.NoError(t, err)
	return h
}

func waitForScan(t *testing.T, h *Handler) {
	t.Helper()
	w, ok := h.inner.(finder.ScanWaiter)
	require.True(t, ok, "inner finder must expose ScanWaiter for tests")
	select {
	case <-w.ScanDone():
	case <-time.After(2 * time.Second):
		t.Fatalf("scan did not complete within deadline")
	}
	// After ScanDone fires, readCommand and the list consumer may
	// still be flushing data asynchronously. Poll until Selection
	// reports a populated list, then call DrainList to flush any
	// remaining queued matches into the visible list before
	// returning so callers see the complete result set.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := h.inner.Selection(); ok {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.DrainList()
	// Wait for the scan-spinner watcher goroutine to observe
	// ScanDone and flip scanRunning to false so subsequent
	// renders use the full inner-finder width. Without this,
	// tests can race the watcher and end up rendering with the
	// spinner cell still reserved.
	h.stopScanSpinner()
}

var syncTick = func(fn func()) bool { fn(); return true }

type fileContent map[string]string

// requireExec skips the test when the named binary is not on PATH.
func requireExec(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not available", name)
	}
}

func requireGit(t *testing.T) { requireExec(t, "git") }

// newWorkspaceDir materializes files into a fresh temp directory and
// returns its absolute path. Use this when the test does not need a
// git repo on top of the contents.
func newWorkspaceDir(t *testing.T, fc fileContent) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range fc {
		full := filepath.Join(dir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return dir
}

// newGitRepo materializes files into a fresh temp directory, then
// initializes a git repo and commits them so `git grep` works. Paths
// in fc are interpreted relative to the temp directory. Returns the
// repo's absolute path.
func newGitRepo(t *testing.T, fc fileContent) string {
	t.Helper()
	requireGit(t)
	dir := newWorkspaceDir(t, fc)
	files := make([]string, 0, len(fc))
	for path := range fc {
		files = append(files, path)
	}
	gitArgs := [][]string{{"init", "-q"}}
	gitArgs = append(gitArgs, append([]string{"add", "--"}, files...))
	gitArgs = append(gitArgs, []string{
		"-c", "user.email=test@example.com",
		"-c", "user.name=test",
		"commit", "-q", "-m", "init",
	})
	for _, args := range gitArgs {
		testgit.Run(t, dir, args...)
	}
	return dir
}

// newSchemeHandler builds a locationsearch handler whose executor and
// filesystem are backed by a real workspace.NewFileScheme rooted at
// dir. The handler is registered for cleanup via t.Cleanup.
func newSchemeHandler(t *testing.T, dir string, previewLines int, command string) *Handler {
	t.Helper()
	uri, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	clients := finder.Clients{
		ResourceOpener: stubResourceOpener{},
		WindowManager:  stubWindowManager{},
		Interrupter:    term.NopInterrupter(),
		Notifications:  stubNotifications{},
		FileSystem:     scheme,
		Executor:       schemeExecutor{scheme: scheme},
	}
	cfg := DefaultConfig()
	cfg.PreviewContextLines = previewLines
	cfg.ListConfig = search.ListConfig{
		Algo:        search.FuzzyMatch,
		Interrupter: term.NopInterrupter(),
		SyncSearch:  true,
	}
	h, err := New(context.Background(), clients, stubWindow(0),
		cfg, nil, syncTick, command, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	return h
}

type stubWindow uint64

func (w stubWindow) WindowID() uint64 { return uint64(w) }

type stubResourceOpener struct{}

func (stubResourceOpener) Open(workspaceapi.URI) (browserapi.Handler, error) {
	return stubBrowserHandler{}, nil
}

type stubBrowserHandler struct{}

func (stubBrowserHandler) Handle(term.Event) (bool, bool) { return false, false }
func (stubBrowserHandler) Resize(int, int)                {}
func (stubBrowserHandler) Draw(term.Writer)               {}
func (stubBrowserHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}
func (stubBrowserHandler) Selection() (string, bool) { return "", false }
func (stubBrowserHandler) Close() error              { return nil }

type stubWindowManager struct{}

func (stubWindowManager) Focus() (browserapi.Window, error) { return stubWindow(0), nil }
func (stubWindowManager) Split(browserapi.Orientation, browserapi.Window, browserapi.Handler) (browserapi.Window, error) {
	return stubWindow(0), nil
}
func (stubWindowManager) Floating(browserapi.Floating, browserapi.FloatingConfig) (browserapi.Window, error) {
	return stubWindow(0), nil
}
func (stubWindowManager) Bar(browserapi.BarConfig, tui.Handler) error { return nil }
func (stubWindowManager) Tab(workspaceapi.URI, rune, string, browserapi.Handler) (browserapi.Handler, error) {
	return stubBrowserHandler{}, nil
}
func (stubWindowManager) SetWindowContent(browserapi.Window, browserapi.Handler) error { return nil }
func (stubWindowManager) CloseWindow(browserapi.Window) error                          { return nil }

func (stubWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

type stubNotifications struct{}

func (stubNotifications) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}
func (stubNotifications) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}
func (stubNotifications) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

type stubFS struct {
	openFileFn func(string, int, os.FileMode) (workspaceapi.File, error)
}

func (s *stubFS) URI(path string) (workspaceapi.URI, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return workspaceapi.ParseURI("file://" + path)
}
func (s *stubFS) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	if s.openFileFn != nil {
		return s.openFileFn(path, flag, mode)
	}
	return nil, os.ErrNotExist
}
func (s *stubFS) Remove(string) error                   { return nil }
func (s *stubFS) Stat(string) (os.FileInfo, error)      { return nil, os.ErrNotExist }
func (s *stubFS) ReadDir(string) ([]os.DirEntry, error) { return nil, nil }
func (s *stubFS) MkdirAll(string, os.FileMode) error    { return nil }

func testFS(content fileContent) *stubFS {
	return &stubFS{
		openFileFn: func(path string, _ int, _ os.FileMode) (workspaceapi.File, error) {
			text, ok := content[path]
			if !ok {
				return nil, os.ErrNotExist
			}
			return newStringFile(text), nil
		},
	}
}

type stringFile struct{ *strings.Reader }

func newStringFile(s string) *stringFile         { return &stringFile{strings.NewReader(s)} }
func (f *stringFile) Close() error               { return nil }
func (f *stringFile) Name() string               { return "" }
func (f *stringFile) Stat() (os.FileInfo, error) { return nil, nil }
func (f *stringFile) Sync() error                { return nil }
func (f *stringFile) Truncate(int64) error       { return nil }
func (f *stringFile) Fd() uintptr                { return 0 }
func (f *stringFile) Write([]byte) (int, error)  { return 0, os.ErrPermission }
func (f *stringFile) WriteAt([]byte, int64) (int, error) {
	return 0, os.ErrPermission
}

type stubParser struct {
	highlightFn func(workspaceapi.URI, string) (iterator.Iterator[textapi.Location], error)
}

func (p *stubParser) Search(string, []string, ...string) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *stubParser) ResolveSymbol(context.Context, string, syntaxapi.Progress) (
	iterator.Iterator[syntaxapi.Match], error,
) {
	return iterator.Empty[syntaxapi.Match](), nil
}
func (p *stubParser) ListReferencedSymbols(context.Context) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}
func (p *stubParser) SearchNode(syntaxapi.NodeCaptureName, ...string) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *stubParser) Query(workspaceapi.URI, string, []string) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *stubParser) QueryNode(workspaceapi.URI, syntaxapi.NodeCaptureName) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *stubParser) Highlight(uri workspaceapi.URI, content string) (
	iterator.Iterator[textapi.Location], error,
) {
	if p.highlightFn != nil {
		return p.highlightFn(uri, content)
	}
	return iterator.Empty[textapi.Location](), nil
}

// localExecutor implements workspaceapi.Executor by spawning a real
// shell subprocess for the command string used in finder tests.
type localExecutor struct {
	mu   sync.Mutex
	next workspaceapi.Pid
}

func (e *localExecutor) Start(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	if cmd.Path == "" {
		return 0, errors.New("empty path")
	}
	var script string
	if len(cmd.Args) >= 2 {
		script = cmd.Args[1]
	}
	// Run the script body synchronously; this guarantees that all
	// stdout has been written by the time Start returns. The
	// finder's readCommand still drains the pipe asynchronously, but
	// the data is already buffered.
	writeScript(cmd.Stdout, script)
	if c, ok := cmd.Stdout.(io.Closer); ok {
		_ = c.Close()
	}
	if c, ok := cmd.Stderr.(io.Closer); ok {
		_ = c.Close()
	}
	go func() {
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- nil
		}
	}()
	e.mu.Lock()
	e.next++
	pid := e.next
	e.mu.Unlock()
	return pid, nil
}

func (e *localExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (e *localExecutor) Close() error                                  { return nil }

// writeScript expands a tiny subset of shell `echo X\necho Y` and
// `echo X` patterns into stdout writes. It is intentionally minimal
// to avoid spawning real shells from tests.
func writeScript(out io.Writer, script string) {
	if out == nil {
		return
	}
	for line := range strings.SplitSeq(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		const prefix = "echo "
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		payload := strings.TrimSpace(line[len(prefix):])
		payload = strings.Trim(payload, "'\"")
		_, _ = io.WriteString(out, payload+"\n")
	}
}

// schemeExecutor adapts a schemeapi.Scheme to a workspaceapi.Executor
// so the integration test can drive a real `git grep` subprocess
// against a workspace.NewFileScheme.
type schemeExecutor struct {
	scheme interface {
		StartCommand(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error)
		Signal(workspaceapi.Pid, syscall.Signal) error
	}
}

func (e schemeExecutor) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return e.scheme.StartCommand(ctx, cmd)
}

func (e schemeExecutor) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	return e.scheme.Signal(pid, sig)
}

func (e schemeExecutor) Close() error { return nil }
