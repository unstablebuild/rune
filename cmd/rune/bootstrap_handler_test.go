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

package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/tcell/v3"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/handler/quickmenu"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/starlarkconfig"
	"unstable.build/rune/internal/sounds"
	"unstable.build/rune/internal/term/gui"
	"unstable.build/rune/internal/term/gui/glassbar"
)

// fakeHandler counts calls to each tui.Handler method so tests can
// assert delegation behavior.
type fakeHandler struct {
	resizeCalls    int
	lastResizeW    int
	lastResizeH    int
	drawCalls      int
	cursorCalls    int
	selectionCalls int
	handleCalls    int
	lastEv         term.Event

	cursorCoord term.Coordinates
	cursorStyle term.CursorStyle
	cursorShow  bool

	selectionText string
	selectionOK   bool

	handleExit    bool
	handleHandled bool
}

func (f *fakeHandler) Resize(w, h int) {
	f.resizeCalls++
	f.lastResizeW = w
	f.lastResizeH = h
}

func (f *fakeHandler) Draw(term.Writer) { f.drawCalls++ }

func (f *fakeHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	f.cursorCalls++
	return f.cursorCoord, f.cursorStyle, f.cursorShow
}

func (f *fakeHandler) Selection() (string, bool) {
	f.selectionCalls++
	return f.selectionText, f.selectionOK
}

func (f *fakeHandler) Handle(ev term.Event) (exit, handled bool) {
	f.handleCalls++
	f.lastEv = ev
	return f.handleExit, f.handleHandled
}

func TestIsBootstrappedFollowsConfigPath(t *testing.T) {
	dataDir := t.TempDir()
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.yaml")

	require.False(t, isBootstrapped(cfgPath),
		"a missing resolved config must bootstrap")

	require.NoError(t, os.WriteFile(cfgPath, []byte("{}\n"), 0o644))
	require.True(t, isBootstrapped(cfgPath),
		"the resolved config living outside the data dir must count")

	require.NoError(t, os.WriteFile(
		filepath.Join(dataDir, configStarFilename), []byte("config = {}\n"), 0o644))
	starPath := filepath.Join(cfgDir, configStarFilename)
	require.False(t, isBootstrapped(starPath),
		"a config in the data dir does not count when the resolved path points elsewhere")
}

func TestWritePresetConfigUsesResolvedConfigPath(t *testing.T) {
	dataDir := t.TempDir()
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "rune", configFilename)
	b := &bootstrapHandler{
		dataDir:      dataDir,
		configPath:   cfgPath,
		chosenEditor: editorVim,
	}

	require.NoError(t, b.writePresetConfig())
	_, err := os.Stat(cfgPath)
	require.NoError(t, err, "preset must land at the resolved config path")
	_, err = os.Stat(filepath.Join(dataDir, configFilename))
	require.ErrorIs(t, err, os.ErrNotExist,
		"the data dir must not shadow the resolved config path")

	require.True(t, isBootstrapped(cfgPath),
		"a fresh install must not re-bootstrap on the next launch")
}

func TestBootstrapHandlerDelegates(t *testing.T) {
	inner := &fakeHandler{
		cursorCoord:   term.Coordinates{X: 3, Y: 4},
		cursorShow:    true,
		selectionText: "selected",
		selectionOK:   true,
		handleHandled: true,
	}
	b := &bootstrapHandler{inner: inner}

	b.Resize(80, 24)
	require.Equal(t, 1, inner.resizeCalls)
	require.Equal(t, 80, inner.lastResizeW)
	require.Equal(t, 24, inner.lastResizeH)

	b.Draw(nil)
	require.Equal(t, 1, inner.drawCalls)

	c, _, show := b.Cursor()
	require.Equal(t, 1, inner.cursorCalls)
	require.Equal(t, term.Coordinates{X: 3, Y: 4}, c)
	require.True(t, show)

	sel, ok := b.Selection()
	require.Equal(t, 1, inner.selectionCalls)
	require.Equal(t, "selected", sel)
	require.True(t, ok)

	ev := term.Event{Type: term.EventKey}
	exit, handled := b.Handle(ev)
	require.Equal(t, 1, inner.handleCalls)
	require.Equal(t, ev, inner.lastEv)
	require.False(t, exit)
	require.True(t, handled)
}

func TestBootstrapHandlerPointerLeaves(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		realIDE *ide.IDE
	}{
		{name: "bootstrapping"},
		{name: "configured", realIDE: &ide.IDE{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, size := range [][2]int{{60, 15}, {1, 1}} {
				inner := &fakeHandler{handleHandled: true}
				b := &bootstrapHandler{inner: inner, realIDE: tt.realIDE}
				handlertest.RunPointerLeaves(t, b, size[0], size[1])
			}
		})
	}
}

func TestBootstrapHandlerPointerLeavesConfiguredIDE(t *testing.T) {
	b := newConfiguredBootstrapForEnvTest(t, configFilename,
		"editor:\n  mode: modal\n", t.TempDir())
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		handlertest.RunPointerLeaves(t, b, size[0], size[1])
	}
}

func TestBootstrapHandlerForwardsPointerLeave(t *testing.T) {
	t.Parallel()
	leaves := []term.Event{
		{Type: term.EventMouse, MouseX: -1, MouseY: 5},
		{Type: term.EventMouse, MouseX: 60, MouseY: 5},
		{Type: term.EventMouse, MouseX: 30, MouseY: -340},
		{Type: term.EventMouse, MouseX: 340, MouseY: 340},
	}
	tests := []struct {
		name    string
		realIDE *ide.IDE
	}{
		{name: "bootstrapping"},
		{name: "configured", realIDE: &ide.IDE{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, leave := range leaves {
				inner := &fakeHandler{handleHandled: true}
				b := &bootstrapHandler{inner: inner, realIDE: tt.realIDE}
				b.Resize(60, 15)
				exit, handled := b.Handle(leave)
				require.False(t, exit, "a pointer leave must not exit")
				require.True(t, handled)
				require.Equal(t, 1, inner.handleCalls,
					"the leave must reach the inner handler")
				require.Equal(t, leave, inner.lastEv,
					"the leave must be forwarded unchanged")
			}
		})
	}
}

func TestBootstrapHandlerSwapInner(t *testing.T) {
	a := &fakeHandler{}
	b := &fakeHandler{}
	bh := &bootstrapHandler{inner: a, chosenEditor: editorVim}
	bh.inner = b

	ev := term.Event{Type: term.EventKey}
	_, _ = bh.Handle(ev)
	require.Equal(t, 0, a.handleCalls, "old inner should not receive events after swap")
	require.Equal(t, 1, b.handleCalls, "new inner should receive events after swap")

	_, _ = bh.Handle(ev)
	require.Equal(t, 2, b.handleCalls)
}

func TestBootstrapHandlerResizesAfterSwap(t *testing.T) {
	pre := &fakeHandler{}
	post := &fakeHandler{}

	bh := &bootstrapHandler{inner: pre}
	bh.Resize(120, 40)
	require.Equal(t, 1, pre.resizeCalls)
	require.Equal(t, 120, pre.lastResizeW)
	require.Equal(t, 40, pre.lastResizeH)

	// Simulate the post-swap assignment + explicit Resize that
	// performSwap does for the configured IDE.
	bh.inner = post
	if bh.lastResizeW > 0 && bh.lastResizeH > 0 {
		bh.inner.Resize(bh.lastResizeW, bh.lastResizeH)
	}
	require.Equal(t, 1, post.resizeCalls, "new inner must be Resized after swap")
	require.Equal(t, 120, post.lastResizeW)
	require.Equal(t, 40, post.lastResizeH)
}

func TestAttachGUIInstallsQuickMenu(t *testing.T) {
	var published []term.Event
	bh := &bootstrapHandler{
		inner: &fakeHandler{},
		publishEvent: func(ev term.Event) bool {
			published = append(published, ev)
			return true
		},
		quickMenu: []ide.QuickMenuButton{
			{Symbol: "plus", Title: "Open Project", Command: []string{"workspaceopen"}},
		},
	}

	bh.attachGUI(nil, false)
	require.Len(t, published, 1)
	require.Equal(t, term.EventInterrupt, published[0].Type)
	require.NotNil(t, published[0].UserFunc,
		"attachGUI must publish a quick menu install without waiting for a Resize")
}

func TestQuickMenuCellsWithoutButtons(t *testing.T) {
	b := &bootstrapHandler{}
	require.Zero(t, b.quickMenuCells(),
		"an empty quick menu must not reserve a grid column")
}

func TestQuickMenuToggleCollapsesReservedColumn(t *testing.T) {
	if !glassbar.Supported() {
		t.Skip("no native quick menu on this platform")
	}
	b := &bootstrapHandler{
		publishEvent: func(term.Event) bool { return true },
		quickMenu: []ide.QuickMenuButton{
			{Symbol: "plus", Title: "Open Project", Command: []string{"workspaceopen"}},
		},
	}

	require.True(t, b.quickMenuVisible())
	require.Equal(t, quickMenuColumnCells, b.quickMenuCells())
	require.Len(t, b.quickMenuButtons(), 1)

	b.setQuickMenuVisible(false)
	require.False(t, b.quickMenuVisible())
	require.Zero(t, b.quickMenuCells(), "hiding must give the column back")
	require.Empty(t, b.quickMenuButtons(), "hiding must clear the native buttons")

	b.setQuickMenuVisible(true)
	require.True(t, b.quickMenuVisible())
	require.Equal(t, quickMenuColumnCells, b.quickMenuCells())
}

func TestQuickMenuUnavailableWithoutButtons(t *testing.T) {
	b := &bootstrapHandler{publishEvent: func(term.Event) bool { return true }}
	require.False(t, b.quickMenuAvailable())

	b.setQuickMenuVisible(true)
	require.False(t, b.quickMenuVisible())
	require.Zero(t, b.quickMenuCells())
}

func TestReadQuickMenuKeepsValidEntries(t *testing.T) {
	terminal := `{"symbol": "terminal", "title": "New Terminal", "command": "terminalnew"}`
	tests := []struct {
		name        string
		config      string
		wantButtons []ide.QuickMenuButton
		wantSounds  quickmenu.Sounds
	}{
		{
			name: "invalid buttons are dropped",
			config: `config["gui"]["quick_menu"] = [` + terminal + `, ` +
				`{"symbol": "not a symbol", "command": "help"}]`,
			wantButtons: []ide.QuickMenuButton{{Symbol: "terminal",
				Title: "New Terminal", Command: []string{"terminalnew"}}},
		},
		{
			name: "a sound from sounds is kept and an unknown one plays none",
			config: `config["gui"]["quick_menu"] = [` + terminal + `]` + "\n" +
				`config["sounds"]["tick"] = "/tick.wav"` + "\n" +
				`config["gui"]["quick_menu_sounds"] = {"hover": "tick", "click": "tikc"}`,
			wantButtons: []ide.QuickMenuButton{{Symbol: "terminal",
				Title: "New Terminal", Command: []string{"terminalnew"}}},
			wantSounds: quickmenu.Sounds{Hover: "tick"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.star")
			require.NoError(t, os.WriteFile(path, []byte(tt.config+"\n"), 0o644))
			buttons, soundNames := readQuickMenu(path)
			assert.Equal(t, tt.wantButtons, buttons)
			assert.Equal(t, tt.wantSounds, soundNames)
		})
	}
}

func TestNewQuickMenuBarPlaysTheConfiguredSounds(t *testing.T) {
	idle := "file:///nonexistent/help.png"
	menu := []ide.QuickMenuButton{{Title: "Help", Command: []string{"help"},
		Sprite: ide.QuickMenuSprite{Idle: idle, Hover: idle, Pressed: idle}}}
	var played, activated []string
	bar := newQuickMenuBar(menu, quickmenu.Sounds{Hover: "tick", Click: "clack"},
		nil, storagestub.NewInMemoryService(), func(term.Event) bool { return true },
		sounds.PlayerFunc(func(name string) { played = append(played, name) }),
		func(id string) { activated = append(activated, id) })
	t.Cleanup(func() { assert.NoError(t, bar.Close()) })
	bar.Resize(80, 24)

	for _, ev := range []term.Event{
		{Type: term.EventMouse, MouseX: 76, MouseY: 4},
		{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 76, MouseY: 4},
		{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 76, MouseY: 4},
	} {
		bar.Handle(ev)
	}
	assert.Equal(t, []string{"tick", "clack"}, played)
	assert.Equal(t, []string{"help"}, activated)
}

func TestQuickMenuSpritesHaveRuneDrawTheBar(t *testing.T) {
	stub := stubOpenPanel(t)
	b := newConfiguredBootstrapForEnvTest(t, configStarFilename,
		`config["gui"]["quick_menu"] = [`+
			`{"title": "Open Project", "command": "workspaceopen", `+
			`"sprite": {"idle": "file:///nonexistent/open.png"}},`+
			`]`+"\n", t.TempDir())
	require.NotNil(t, b.quickMenuBar)
	assert.True(t, b.quickMenuVisible(), "Rune draws the bar on every platform")
	assert.Equal(t, quickMenuColumnCells, b.realIDE.RightInset())
	assert.Empty(t, b.quickMenuButtons(), "the native bar must stay empty")

	handle := func(key term.Key, x, y int) bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		_, handled := b.Handle(term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y})
		return handled
	}
	b.mu.Lock()
	b.Resize(80, 24)
	b.mu.Unlock()
	// The bar fills the last 5 columns from row 3, and the first button
	// is right below its top.
	require.True(t, handle(term.MouseLeft, 76, 4))
	require.True(t, handle(term.MouseRelease, 76, 4))
	require.Len(t, stub.opts, 1, "the click must run the button's command")
	assert.Equal(t, appMenuPanelCommands["workspaceopen"], stub.opts[0])

	b.mu.Lock()
	b.setQuickMenuVisible(false)
	b.mu.Unlock()
	assert.Zero(t, b.realIDE.RightInset())
	handle(term.MouseLeft, 76, 4)
	handle(term.MouseRelease, 76, 4)
	assert.Len(t, stub.opts, 1, "a hidden bar must not take clicks")
}

// pointingHandler shows the text pointer for every mouse event.
type pointingHandler struct{ fakeHandler }

func (p *pointingHandler) Handle(ev term.Event) (exit, handled bool) {
	term.SetPointerShape(ev, term.PointerText)
	return p.fakeHandler.Handle(ev)
}

func TestQuickMenuBarRoutesTheMouse(t *testing.T) {
	inner := &pointingHandler{}
	idle := "file:///nonexistent/help.png"
	menu := []ide.QuickMenuButton{{Title: "Help", Command: []string{"help"},
		Sprite: ide.QuickMenuSprite{Idle: idle, Hover: idle, Pressed: idle}}}
	storage := storagestub.NewInMemoryService()
	publish := func(term.Event) bool { return true }
	b := &bootstrapHandler{
		inner:        inner,
		storage:      storage,
		publishEvent: publish,
		quickMenu:    menu,
		quickMenuBar: newQuickMenuBar(menu, quickmenu.Sounds{}, nil, storage, publish,
			sounds.Silent, func(string) {}),
	}
	t.Cleanup(func() { assert.NoError(t, b.Close()) })
	b.Resize(80, 24)

	steps := []struct {
		name          string
		key           term.Key
		x, y          int
		handled       bool
		reachesIDE    bool
		shape         term.PointerShape
		hideBeforeHit bool
	}{
		{name: "a motion over the bar", x: 76, y: 4,
			reachesIDE: true, shape: term.PointerLink},
		{name: "a motion over the editor", x: 10, y: 10,
			reachesIDE: true, shape: term.PointerText},
		{name: "a press on a button", key: term.MouseLeft, x: 76, y: 4,
			handled: true, shape: term.PointerLink},
		{name: "its drag off the button", key: term.MouseLeft, x: 10, y: 10,
			handled: true},
		{name: "its release off the button", key: term.MouseRelease, x: 10, y: 10,
			handled: true},
		{name: "a press in the editor", key: term.MouseLeft, x: 10, y: 10,
			reachesIDE: true, shape: term.PointerText},
		{name: "its release over the bar", key: term.MouseRelease, x: 76, y: 4,
			reachesIDE: true, shape: term.PointerText},
		{name: "a press on a hidden bar", key: term.MouseLeft, x: 76, y: 4,
			reachesIDE: true, shape: term.PointerText, hideBeforeHit: true},
	}
	for _, s := range steps {
		if s.hideBeforeHit {
			b.setQuickMenuVisible(false)
		}
		calls := inner.handleCalls
		p := new(term.Pointer)
		_, handled := b.Handle(term.Event{
			Type: term.EventMouse, Key: s.key, MouseX: s.x, MouseY: s.y,
			Context: term.ContextWithPointer(context.Background(), p),
		})
		assert.Equal(t, s.handled, handled, s.name)
		assert.Equal(t, s.reachesIDE, inner.handleCalls > calls, "%s reaches the IDE", s.name)
		shape, _ := p.Shape()
		assert.Equal(t, s.shape, shape, "%s pointer", s.name)
	}
}

func TestApplyInitialThemeAttrHoldsEventLoopLock(t *testing.T) {
	b := newConfiguredBootstrapForEnvTest(t, configFilename,
		"editor:\n  mode: modal\n", t.TempDir())

	done := make(chan struct{})
	b.mu.Lock()
	go func() {
		defer close(done)
		b.applyInitialThemeAttr(b.realIDE)
	}()

	select {
	case <-done:
		b.mu.Unlock()
		t.Fatal("applyInitialThemeAttr must run under the event loop lock")
	case <-time.After(100 * time.Millisecond):
	}

	b.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("applyInitialThemeAttr did not complete after the lock was released")
	}
}

func TestBootstrapIDEsFetchSponsorsFromAPI(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configBody string
		ide        func(*bootstrapHandler) *ide.IDE
	}{
		{
			name: "first run",
			ide:  func(b *bootstrapHandler) *ide.IDE { return b.preIDE },
		},
		{
			name:       "configured",
			configBody: "editor:\n  mode: modal\n",
			ide:        func(b *bootstrapHandler) *ide.IDE { return b.realIDE },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requested := make(chan struct{}, 1)
			api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/sponsors" {
					http.NotFound(w, r)
					return
				}
				select {
				case requested <- struct{}{}:
				default:
				}
				_, _ = w.Write([]byte(`{"sponsor_url":"https://example.com/sponsor","tiers":[]}`))
			})
			b := newBootstrapForTest(t, configFilename, tc.configBody, t.TempDir(), api)
			i := tc.ide(b)
			require.NotNil(t, i)

			b.mu.Lock()
			err := i.DispatchCommand("sponsors")
			b.mu.Unlock()
			require.NoError(t, err)

			select {
			case <-requested:
			case <-time.After(10 * time.Second):
				t.Fatal("sponsors never requested the API server's sponsor list")
			}
		})
	}
}

func TestGUIThemesLiveApplyHook(t *testing.T) {
	const (
		envKey    = "RUNE_TEST_LIVE_APPLY_THEMES_ENV"
		envVal    = "/from/theme/package"
		themeDiff = "gui:\n  themes:\n    livetest:\n" +
			"      foreground: '#112233'\n      background: '#445566'\n"
	)
	livetest := map[string]any{"foreground": "#112233", "background": "#445566"}
	writeYAML := func(gui string) func(t *testing.T, path string) {
		return func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte("editor:\n  mode: modal\n"+gui), 0o644))
		}
	}

	tests := []struct {
		name        string
		filename    string
		initial     string
		writeMerged func(t *testing.T, path string)
		diff        string
		withoutGUI  bool
		wantPaths   [][]string
	}{
		{
			name:        "yaml config",
			filename:    configFilename,
			initial:     "editor:\n  mode: modal\n",
			writeMerged: writeYAML(themeDiff),
			diff:        themeDiff,
			wantPaths:   [][]string{{"gui", "themes"}},
		},
		{
			name:     "starlark config",
			filename: configStarFilename,
			initial:  "config[\"terminal\"][\"initial_reservoir\"] = 2\n",
			writeMerged: func(t *testing.T, path string) {
				require.NoError(t, starlarkconfig.WriteManagedConfigFileAtomic(path, map[string]any{
					"gui": map[string]any{"themes": map[string]any{"livetest": livetest}},
				}))
			},
			diff:      themeDiff,
			wantPaths: [][]string{{"gui", "themes"}},
		},
		{
			name:        "no GUI attached",
			filename:    configFilename,
			initial:     "editor:\n  mode: modal\n",
			writeMerged: writeYAML(themeDiff),
			diff:        themeDiff,
			withoutGUI:  true,
		},
		{
			name:     "env and themes",
			filename: configFilename,
			initial:  "editor:\n  mode: modal\n",
			writeMerged: writeYAML("gui:\n  env:\n    " + envKey + ": " + envVal + "\n" +
				"  themes:\n    livetest:\n      foreground: '#112233'\n      background: '#445566'\n"),
			diff: "gui:\n  env:\n    " + envKey + ": " + envVal + "\n" +
				"  themes:\n    livetest:\n      foreground: '#112233'\n      background: '#445566'\n",
			wantPaths: [][]string{{"gui", "env"}, {"gui", "themes"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envKey, "")
			original := tcell.GetColorValues()
			t.Cleanup(func() { tcell.SetColorValues(original) })

			b := newConfiguredBootstrapForEnvTest(t, tt.filename, tt.initial, t.TempDir())
			var startupThemes []string
			if !tt.withoutGUI {
				guiCfg, ok, err := getGUIConfig(b.config())
				require.NoError(t, err)
				require.True(t, ok)
				g, err := gui.New(b, gui.WithColorThemes("",
					getGUIColorThemes(b.browser(), guiCfg)))
				require.NoError(t, err)
				t.Cleanup(func() { _ = g.Close() })
				b.attachGUI(g, false)
				startupThemes = g.Themes()
				require.NotEmpty(t, startupThemes, "the built-in themes are available at startup")
			}
			tt.writeMerged(t, b.configPath)

			result, err := b.packageConfigMergeHook(mergeEvent(t, tt.diff))
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.wantPaths, result.LivePaths)
			if tt.withoutGUI {
				return
			}

			require.Eventually(t, func() bool {
				b.mu.Lock()
				defer b.mu.Unlock()
				return slices.Contains(b.g.Themes(), "livetest")
			}, 10*time.Second, 10*time.Millisecond,
				"a theme merged by a package install must be available without a restart")
			b.mu.Lock()
			defer b.mu.Unlock()
			assert.Subset(t, b.g.Themes(), startupThemes,
				"the reloaded set keeps the themes available at startup")
			assert.Empty(t, b.g.Theme(), "installing a theme must not switch the active one")
			_, err = b.g.SetTheme("livetest")
			require.NoError(t, err)
		})
	}
}

// reloadHUD runs the reload that runectl screenshot asks for, and waits
// for the tick that applies it.
func reloadHUD(t *testing.T, b *bootstrapHandler) {
	t.Helper()
	b.reloadHUDAndScreenshot(b.realIDE)
	applied := make(chan struct{})
	require.True(t, b.scheduleNextTick(func() { close(applied) }))
	select {
	case <-applied:
	case <-time.After(10 * time.Second):
		t.Fatal("the HUD reload was not applied")
	}
}

func TestReloadHUDAppliesQuickMenu(t *testing.T) {
	const (
		none   = "gui:\n  quick_menu: []\n"
		native = "gui:\n  quick_menu:\n" +
			"    - {symbol: plus, title: Open Project, command: workspaceopen}\n"
	)
	sprite := func(uri string) string {
		return "gui:\n  quick_menu:\n" +
			"    - {title: Open Project, command: workspaceopen, sprite: {idle: " + uri + "}}\n"
	}
	const spriteA, spriteB = "file:///nonexistent/a.png", "file:///nonexistent/b.png"
	nativeInset, nativeButtons := 0, 0
	if glassbar.Supported() {
		nativeInset, nativeButtons = quickMenuColumnCells, 1
	}
	tests := []struct {
		name          string
		before, after string
		hide          bool
		wantDrawn     bool
		wantIdle      string
		wantInset     int
		wantNative    int
		wantSounds    quickmenu.Sounds
	}{
		{name: "sprites replace the native bar", before: native, after: sprite(spriteA),
			wantDrawn: true, wantIdle: spriteA, wantInset: quickMenuColumnCells},
		{name: "the native bar replaces sprites", before: sprite(spriteA), after: native,
			wantInset: nativeInset, wantNative: nativeButtons},
		{name: "new sprites", before: sprite(spriteA), after: sprite(spriteB),
			wantDrawn: true, wantIdle: spriteB, wantInset: quickMenuColumnCells},
		{name: "a new menu reserves its column", before: none, after: sprite(spriteA),
			wantDrawn: true, wantIdle: spriteA, wantInset: quickMenuColumnCells},
		{name: "a removed menu gives its column back", before: sprite(spriteA), after: none},
		{name: "a hidden menu stays hidden", before: sprite(spriteA), after: sprite(spriteB),
			hide: true, wantDrawn: true, wantIdle: spriteB},
		{name: "new sounds", before: sprite(spriteA),
			after:     sprite(spriteA) + "  quick_menu_sounds: {hover: info, click: success}\n",
			wantDrawn: true, wantIdle: spriteA, wantInset: quickMenuColumnCells,
			wantSounds: quickmenu.Sounds{Hover: sounds.Info, Click: sounds.Success}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newConfiguredBootstrapForEnvTest(t, configFilename,
				"editor:\n  mode: modal\n"+tt.before, t.TempDir())
			b.mu.Lock()
			b.Resize(80, 24)
			if tt.hide {
				b.setQuickMenuVisible(false)
			}
			before := b.quickMenuBar
			b.mu.Unlock()

			require.NoError(t, os.WriteFile(b.configPath,
				[]byte("editor:\n  mode: modal\n"+tt.after), 0o644))
			reloadHUD(t, b)

			b.mu.Lock()
			defer b.mu.Unlock()
			if tt.wantDrawn {
				require.NotNil(t, b.quickMenuBar, "Rune must draw the bar")
				assert.NotSame(t, before, b.quickMenuBar,
					"the bar must be rebuilt to draw sprites overwritten in place")
				require.Len(t, b.quickMenu, 1)
				assert.Equal(t, tt.wantIdle, b.quickMenu[0].Sprite.Idle)
			} else {
				assert.Nil(t, b.quickMenuBar)
			}
			assert.Equal(t, tt.wantInset, b.realIDE.RightInset())
			assert.Len(t, b.quickMenuButtons(), tt.wantNative)
			assert.Equal(t, tt.hide, b.quickMenuOff)
			assert.Equal(t, tt.wantSounds, b.quickMenuSounds)
		})
	}
}

func TestReadQuickMenuImagesReadsOverwrittenSprites(t *testing.T) {
	ctx := context.Background()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	path := filepath.Join(dir, "open.png")
	uri := "file://" + path
	menu := []ide.QuickMenuButton{{Title: "Open Project", Command: []string{"workspaceopen"},
		Sprite: ide.QuickMenuSprite{Idle: uri, Hover: uri, Pressed: uri}}}
	storage := storagestub.NewInMemoryService()
	writePNG := func(c color.Color) {
		img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		img.Set(0, 0, c)
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	}
	pixel := func(images map[string]image.Image) color.Color {
		require.Contains(t, images, uri)
		return color.NRGBAModel.Convert(images[uri].At(0, 0))
	}

	writePNG(color.White)
	assert.Equal(t, color.NRGBAModel.Convert(color.White),
		pixel(readQuickMenuImages(ctx, menu, storage)))

	red := color.NRGBA{R: 255, A: 255}
	writePNG(red)
	assert.Equal(t, red, pixel(readQuickMenuImages(ctx, menu, storage)),
		"a sprite overwritten in place")

	require.NoError(t, os.Remove(path))
	assert.Empty(t, readQuickMenuImages(ctx, menu, storage))
	assert.Empty(t, readQuickMenuImages(ctx, menu, nil))
}

func TestQuickMenuCommandWorksOnceAReloadAddsSprites(t *testing.T) {
	b := newConfiguredBootstrapForEnvTest(t, configFilename,
		"editor:\n  mode: modal\ngui:\n  quick_menu: []\n", t.TempDir())
	require.NoError(t, b.subscribeQuickMenuCommand(b.realIDE))
	dispatch := func(args ...string) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.realIDE.DispatchCommand(cmdQuickMenu, args...)
	}
	require.ErrorContains(t, dispatch("off"), "no quick menu buttons are configured")

	require.NoError(t, os.WriteFile(b.configPath, []byte("editor:\n  mode: modal\n"+
		"gui:\n  quick_menu:\n    - {title: Open Project, command: workspaceopen, "+
		"sprite: {idle: file:///nonexistent/open.png}}\n"), 0o644))
	reloadHUD(t, b)
	require.NoError(t, dispatch("off"))
	b.mu.Lock()
	defer b.mu.Unlock()
	assert.False(t, b.quickMenuVisible())
	assert.Zero(t, b.realIDE.RightInset())
}

func TestThemeReloadCommandAppliesHUD(t *testing.T) {
	b := newConfiguredBootstrapForEnvTest(t, configFilename,
		"editor:\n  mode: modal\ngui:\n  quick_menu: []\n", t.TempDir())
	require.NoError(t, b.subscribeThemeReloadCommand(b.realIDE))
	const sprite = "file:///nonexistent/open.png"
	require.NoError(t, os.WriteFile(b.configPath, []byte("editor:\n  mode: modal\n"+
		"gui:\n  quick_menu:\n    - {title: Open Project, command: workspaceopen, "+
		"sprite: {idle: "+sprite+"}}\n"), 0o644))

	b.mu.Lock()
	require.NoError(t, b.realIDE.DispatchCommand(cmdThemeReload))
	b.mu.Unlock()

	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.quickMenu) == 1 && b.quickMenu[0].Sprite.Idle == sprite
	}, 10*time.Second, 10*time.Millisecond, "themereload must apply the config's HUD")
	b.mu.Lock()
	defer b.mu.Unlock()
	assert.Equal(t, quickMenuColumnCells, b.realIDE.RightInset())
}

func TestReloadHUDAppliesChangedVolumeAndTheme(t *testing.T) {
	original := tcell.GetColorValues()
	t.Cleanup(func() { tcell.SetColorValues(original) })
	b := newConfiguredBootstrapForEnvTest(t, configFilename,
		"editor:\n  mode: modal\n", t.TempDir())
	g, err := gui.New(b, gui.WithColorThemes("", nil))
	require.NoError(t, err)
	t.Cleanup(func() { _ = g.Close() })
	b.attachGUI(g, false)

	steps := []struct {
		name          string
		volume, theme string
		pick          func(t *testing.T)
		wantVolume    float64
		wantTheme     string
	}{
		{name: "the config's are applied", volume: "0.5", theme: "livea",
			wantVolume: 0.5, wantTheme: "livea"},
		{name: "unchanged ones keep what the user picked", volume: "0.5", theme: "livea",
			pick: func(t *testing.T) {
				b.soundOutput.SetVolume(0.2)
				_, err := b.g.SetTheme("")
				require.NoError(t, err)
			},
			wantVolume: 0.2},
		{name: "changed ones are applied", volume: "0.7", theme: "liveb",
			wantVolume: 0.7, wantTheme: "liveb"},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(b.configPath, []byte("editor:\n  mode: modal\n"+
				"gui:\n  sound_volume: "+step.volume+"\n  default_theme: "+step.theme+"\n"+
				"  themes:\n"+
				"    livea: {foreground: '#112233', background: '#445566'}\n"+
				"    liveb: {foreground: '#778899', background: '#aabbcc'}\n"), 0o644))
			if step.pick != nil {
				b.mu.Lock()
				step.pick(t)
				b.mu.Unlock()
			}
			reloadHUD(t, b)

			b.mu.Lock()
			defer b.mu.Unlock()
			assert.Equal(t, step.wantVolume, b.soundOutput.Volume())
			assert.Equal(t, step.wantTheme, b.g.Theme())
			assert.Subset(t, b.g.Themes(), []string{"livea", "liveb"},
				"the reload must offer the themes of the config")
		})
	}
}

func TestReloadHUDAppliesAppIcon(t *testing.T) {
	dir := t.TempDir()
	iconPath := filepath.Join(dir, "icon.png")
	var icon bytes.Buffer
	require.NoError(t, png.Encode(&icon, image.NewNRGBA(image.Rect(0, 0, 2, 2))))
	require.NoError(t, os.WriteFile(iconPath, icon.Bytes(), 0o644))
	const base = "editor:\n  mode: modal\n"
	b := newConfiguredBootstrapForEnvTest(t, configFilename, base, t.TempDir())
	g, err := gui.New(b)
	require.NoError(t, err)
	t.Cleanup(func() { _ = g.Close() })
	b.attachGUI(g, false)

	steps := []struct {
		name     string
		config   string
		wantIcon bool
	}{
		{name: "an image is the icon", config: base + "gui:\n  app_icon: " + iconPath + "\n",
			wantIcon: true},
		{name: "a config that does not parse keeps the icon", config: "gui: [",
			wantIcon: true},
		{name: "an image that does not load restores the default",
			config: base + "gui:\n  app_icon: " + filepath.Join(dir, "missing.png") + "\n"},
		{name: "the image again", config: base + "gui:\n  app_icon: " + iconPath + "\n",
			wantIcon: true},
		{name: "no key restores the default", config: base},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(b.configPath, []byte(step.config), 0o644))
			reloadHUD(t, b)

			b.mu.Lock()
			defer b.mu.Unlock()
			assert.Equal(t, step.wantIcon, b.g.AppIcon() != nil)
		})
	}
}

func TestApplyHUDIgnoresTheGUIConfigOfAnOlderHUD(t *testing.T) {
	ctx := context.Background()
	b := newConfiguredBootstrapForEnvTest(t, configFilename,
		"editor:\n  mode: modal\n", t.TempDir())
	load := func(volume string) *ide.HUD {
		require.NoError(t, os.WriteFile(b.configPath, []byte("editor:\n  mode: modal\n"+
			"gui:\n  sound_volume: "+volume+"\n"), 0o644))
		hud, err := b.realIDE.LoadHUD(ctx)
		require.NoError(t, err)
		return hud
	}
	older := load("0.3")
	newer := load("0.6")

	b.mu.Lock()
	defer b.mu.Unlock()
	b.applyHUD(b.realIDE, newer, guiAssets{})
	b.applyHUD(b.realIDE, older, guiAssets{})
	assert.Equal(t, 0.6, b.soundOutput.Volume())
}

func TestQueueScreenshotWaitsForSignalledExtensions(t *testing.T) {
	tests := []struct {
		name      string
		signalled int
		wantWait  []time.Duration
	}{
		{name: "no extension told", signalled: 0},
		{name: "extensions told", signalled: 2, wantWait: []time.Duration{extensionReloadSettle}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var waited []time.Duration
			var pending func()
			var queued int
			b := &bootstrapHandler{
				publishEvent: func(ev term.Event) bool {
					require.Equal(t, term.EventInterrupt, ev.Type)
					queued++
					return true
				},
				afterFunc: func(d time.Duration, f func()) {
					waited = append(waited, d)
					pending = f
				},
			}
			b.queueScreenshot(tt.signalled)
			assert.Equal(t, tt.wantWait, waited)
			if tt.wantWait == nil {
				assert.Equal(t, 1, queued, "queued on the next tick")
				return
			}
			assert.Zero(t, queued, "not before the extensions had time to redraw")
			require.NotNil(t, pending)
			pending()
			assert.Equal(t, 1, queued)
		})
	}
}
