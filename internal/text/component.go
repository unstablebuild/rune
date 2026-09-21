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

package text

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/debug"
	thandler "unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/handler/command"
	hmarkdown "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/ide/idelsp/languages"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/text/cmdenv"
	"unstable.build/rune/internal/text/streamload"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/walkdir"
)

var _ tui.Component = (*Component)(nil)
var _ browser.Browser = (*Component)(nil)
var _ browser.DragTarget = (*Component)(nil)
var _ Editor = (*Component)(nil)

// Workspace abstracts the workspace functionality needed for a Component.
type Workspace interface {
	workspace.Loader
	walkdir.Reader
	schemeapi.Executor
	Open(string) (workspaceapi.File, error)
}

// Component is an implementation of browser.Browser for file editing.
// It also satisfies tui.Component, and text.Editor.
type Component struct {
	ctx             context.Context
	cancelCtx       func()
	comp            browser.Component
	workspace       Workspace
	ed              Editor
	config          Config
	focus           thandler.Window
	edSubscribers   map[textapi.EventType][]EventHandler
	cmdSubscribers  map[string]commandAll
	replSubscribers map[string]replCommandAll
	editors         map[string]Handler
	fileRegistry    FileCommandRegistry
	streamingLoads  sync.WaitGroup
}

// NewComponent allocates storage for a new Component and initializes it.
func NewComponent(ed Editor, w Workspace, config Config) (
	c *Component, err error,
) {
	c = new(Component)
	err = c.Init(ed, w, config)
	if err != nil {
		return
	}
	return
}

func (c *Component) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "text.Component").Logf(level, msg, args...)
}

func (c *Component) resetTabProperties(file workspaceapi.URI) {
	c.comp.ResetTabNameAndAttrs(file)
}

func (c *Component) setDirtyFileAttr(file workspaceapi.URI, buf *cell.Buffer, lastFlush int) {
	v := buf.Version()
	c.log(log.TraceLevel, "edited, snapshot is %d, buffer version is %d", lastFlush, v)
	if v == lastFlush {
		c.resetTabProperties(file)
		return
	}
	if _, defName, ok := c.comp.TabName(file); ok {
		tabname := fmt.Sprintf("%s*", defName)
		c.comp.SetTabNameAndAttrs(file, tabname, c.config.DirtyTabAttr)
	}
}

func (c *Component) getSwapDir(file workspaceapi.URI) (workspaceapi.URI, error) {
	return workspace.SwapDirectory(c.config.SwapDirectory, file)
}

// fileExists reports whether file is present on the backing
// workspace. A Stat error (including not-exist) is treated as
// absent.
func (c *Component) fileExists(file workspaceapi.URI) bool {
	_, err := c.workspace.Stat(file.Path())
	return err == nil
}

func (c *Component) newFileBuffer(
	file, recSwapFile workspaceapi.URI, buf *cell.Buffer,
	readOnly, forceRecover bool,
) (handler Handler, ret *editorFlusherCloser, err error) {
	fc, recover, err := c.loadFileBuffer(
		file, recSwapFile, buf, readOnly, forceRecover)
	if err != nil {
		return nil, nil, err
	}
	return c.buildEditorHandler(file, buf, fc, readOnly, recover)
}

// loadFileBuffer performs only the disk I/O half of newFileBuffer:
// it populates buf from the workspace and returns the resulting
// FlusherCloser plus a recover flag derived from recSwapFile.
//
// The split exists so the streaming-open code path can run the I/O
// off the host event loop and then schedule the editor-construction
// half (buildEditorHandler) back through ScheduleNextTick. Editor
// construction synchronously publishes Open/Focus events to
// subscribers (history tracker, idecursor, LSP, rune-agent, UI
// bars, ...) that read window-manager and other event-loop-owned
// state, so it must not run on the worker goroutine.
func (c *Component) loadFileBuffer(
	file, recSwapFile workspaceapi.URI, buf *cell.Buffer,
	readOnly, forceRecover bool,
) (workspace.FlusherCloser, bool, error) {
	recover := recSwapFile != (workspaceapi.URI{})
	var (
		fc  workspace.FlusherCloser
		err error
	)
	if recover {
		fc, err = c.workspace.Recover(file, recSwapFile, buf, forceRecover)
	} else {
		var swapDir workspaceapi.URI
		swapDir, err = c.getSwapDir(file)
		if err == nil {
			fc, err = c.workspace.Load(file, buf, swapDir, readOnly)
		}
	}
	if err != nil {
		return nil, recover, err
	}
	return fc, recover, nil
}

func (c *Component) buildEditorHandler(
	file workspaceapi.URI, buf *cell.Buffer, fc workspace.FlusherCloser,
	readOnly, recover bool,
) (handler Handler, ret *editorFlusherCloser, err error) {
	interrupter := browser.EventPublisherInterrupter(c)
	locs := syntax.FuncLocationSetter(func(ll textapi.LocationList) {
		if handler == nil {
			return
		}
		handler.SetLocationList(textapi.LocationPriorityInfo, "syntax", ll)
	})

	var tree *syntax.Tree
	if c.config.MaxSyntaxParseSize == 0 || buf.Size() <= c.config.MaxSyntaxParseSize {
		// install tree in Buffer first so editor can use its
		// capabilities while initializing
		tree = syntax.WithTree(c.ctx, c.config, interrupter,
			c.config.PkgManager, locs, file, buf, fc, c.workspace, c.config.Syntax)
		fc = tree
	}

	handler, err = c.ed.Edit(WithBars(c.ctx, BarOptions{}), file, buf, readOnly, recover)
	if err != nil {
		return nil, nil, err
	}

	var commands []textapi.CommandManual
	if tree != nil {
		var cmdHandler syntax.CommandHandler
		commands, cmdHandler = syntax.Commands(handler, tree)
		for _, cmd := range commands {
			err := c.fileRegistry.SubscribeCommandForFile(file, cmd, cmdHandler)
			if err != nil {
				return nil, nil, err
			}
		}
	}

	efc := &editorFlusherCloser{
		parent:    c,
		fc:        fc,
		uri:       file,
		buf:       buf,
		lastFlush: buf.Version(),
		h:         handler,
		commands:  commands,
	}

	// no need to unsubscribe upon Close since the assumption
	// is that a Component always outlives a cell.Buffer
	buf.Subscribe(efc)

	return handler, efc, nil
}

// Init initializes this Component with the given editor and Options.
// It returns an error if an initial filepath was given through WithFilePath option
// and the file failed to be opened.
func (c *Component) Init(
	ed Editor, w Workspace, config Config,
) error {
	if config.ScheduleNextTick == nil {
		panic("text.Component: config.ScheduleNextTick must not be nil")
	}
	c.config = config
	c.ctx, c.cancelCtx = context.WithCancel(context.Background())

	c.comp.Init(c.config.Config)
	c.comp.SetInterrupter(browser.EventPublisherInterrupter(c))
	c.comp.Subscribe((*handlerWindowSubscriber)(c))
	c.fileRegistry = newFileCommandRegistryFromComponent(c)

	c.ed = ed
	c.workspace = w
	c.edSubscribers = make(map[textapi.EventType][]EventHandler)
	c.cmdSubscribers = make(map[string]commandAll)
	c.replSubscribers = make(map[string]replCommandAll)
	c.editors = make(map[string]Handler)

	// validate that config aliases are not recursive
	return ValidateCommandAliases(c.config.CommandAliases)
}

func (c *Component) tryDispatchEventFocus(win thandler.Window) {
	content := win.Content()
	t, ok := content.(*browser.Tab)
	if !ok {
		return
	}
	res, ok := t.Handler().(Handler)
	if !ok {
		return
	}
	cursor := res.CursorAtScroll()
	(*Component)(c).DispatchEvent(textapi.Event{
		Type:     textapi.EventTypeFocus,
		URI:      t.URI(),
		Resource: res,
		Start:    c.getContentDimensions(win),
		From:     cursor,
	})
}

func (c *Component) getContentDimensions(win thandler.Window) term.Coordinates {
	width := win.Width()
	height := win.Height()
	if c.config.WindowManagerConfig.Frame {
		width = max(0, width-2)
		height = max(0, height-2)
	}
	return term.Coordinates{X: width, Y: height}
}

func (c *Component) tryDispatchEvent(win thandler.Window, evType textapi.EventType) {
	content := win.Content()
	t, ok := content.(*browser.Tab)
	if !ok {
		return
	}
	res, ok := t.Handler().(Handler)
	if !ok {
		return
	}
	(*Component)(c).DispatchEvent(textapi.Event{
		Type:     evType,
		URI:      t.URI(),
		Resource: res,
	})
}

type compTabSubscriber struct {
	parent *Component
	window browser.Window
}

func (s *compTabSubscriber) OnFocus(t *browser.Tab) {
	res, ok := t.Handler().(Handler)
	if !ok {
		return
	}
	w, ok := t.Window()
	if !ok {
		// this should never happen, given that we just received OnFocus
		return
	}
	s.window = w
	if ok, err := w.Focus(); err != nil || !ok {
		// tab has been assigned to window but window
		// is not main window
		return
	}
	dimensions := s.parent.getContentDimensions(s.parent.focus)
	cursor := res.CursorAtScroll()
	s.parent.DispatchEvent(textapi.Event{
		Type:     textapi.EventTypeFocus,
		URI:      t.URI(),
		Resource: res,
		Start:    dimensions,
		From:     cursor,
	})
}

func (s *compTabSubscriber) OnFree(t *browser.Tab) {
	res, ok := t.Handler().(Handler)
	if !ok {
		return
	}
	if s.window == nil {
		// should not happen, if OnFree is called
		// OnFocus should have been called before
		return
	}
	if ok, err := s.window.Focus(); err != nil || !ok {
		return
	}
	s.parent.DispatchEvent(textapi.Event{
		Type:     textapi.EventTypeUnfocus,
		URI:      t.URI(),
		Resource: res,
	})
}

// OpenFileTab opens the file at filename path, as a new browser tab.
// It's up to the caller to use the returned browserapi.Handler and switch
// any of the active windows to use it.
//
// If recoveryFilename is not empty, then the file will be recovered from the
// contents of recoveryFilename.
func (c *Component) OpenFileTab(file workspaceapi.URI, readOnly bool) (
	browserapi.Handler, error,
) {
	if file == (workspaceapi.URI{}) {
		return nil, errors.New("empty URI")
	}
	return c.openFileTab(file, workspaceapi.URI{}, readOnly, false)
}

// Reload reloads the content of the tab at the given window. If the content
// is not a tab, then this method returns an error. Reload is asynchronous;
// see workspace.FlusherCloser for the channel contract.
func (c *Component) Reload(ctx context.Context, win browser.Window) (<-chan error, error) {
	content, err := win.Content()
	if err != nil {
		return nil, fmt.Errorf("get window content: %w", err)
	}
	t, ok := content.(*browser.Tab)
	if !ok {
		return nil, textapi.ErrInvalidReload
	}

	return c.ReloadTab(ctx, t)
}

// ReloadTab reloads the given handler, if it is a tab,
// and if it can be reloaded. Asynchronous; see workspace.FlusherCloser.
func (c *Component) ReloadTab(ctx context.Context, h browserapi.Handler) (<-chan error, error) {
	t, ok := h.(*browser.Tab)
	if !ok {
		return nil, textapi.ErrInvalidReload
	}

	if t.Closer() == nil {
		return nil, textapi.ErrInvalidReload
	}

	fc, ok := t.Closer().(workspace.FlusherCloser)
	if !ok {
		return nil, textapi.ErrInvalidReload
	}
	return fc.Reload(ctx)
}

// RemoveTab removes the given handler, if it is a tab,
// and if it can be removed. A handler that is a tab but has already
// been removed is treated as success.
func (c *Component) RemoveTab(h browserapi.Handler) error {
	if _, ok := h.(*browser.Tab); !ok {
		return errors.New("content is not a tab")
	}
	c.comp.RemoveTab(h)
	return nil
}

// Overwrite overwrites the content of the tab at the given window
// asynchronously. If the content is not a tab, then this method
// returns an error. See workspace.FlusherCloser for the channel
// contract.
func (c *Component) Overwrite(ctx context.Context, win browser.Window) (<-chan error, error) {
	content, err := win.Content()
	if err != nil {
		return nil, fmt.Errorf("get window content: %w", err)
	}
	t, ok := content.(*browser.Tab)
	if !ok {
		return nil, textapi.ErrInvalidReload
	}

	return c.OverwriteTab(ctx, t)
}

// OverwriteTab overwrites the given handler from persistence, if it
// is a tab, and if it can be overwritten. Asynchronous.
func (c *Component) OverwriteTab(ctx context.Context, h browserapi.Handler) (<-chan error, error) {
	t, ok := h.(*browser.Tab)
	if !ok {
		return nil, textapi.ErrInvalidOverwrite
	}

	if t.Closer() == nil {
		return nil, textapi.ErrInvalidOverwrite
	}

	fc, ok := t.Closer().(workspace.FlusherCloser)
	if !ok {
		return nil, textapi.ErrInvalidOverwrite
	}

	return fc.ForceFlush(ctx)
}

func (c *Component) recoverOpenFileTab(
	file workspaceapi.URI, recoveryFilename workspaceapi.URI, readOnly bool,
) (browserapi.Handler, error) {
	if file == (workspaceapi.URI{}) || recoveryFilename == (workspaceapi.URI{}) {
		return nil, errors.New("empty URI")
	}
	return c.openFileTab(file, recoveryFilename, readOnly, false)
}

func (c *Component) openFileTab(
	file workspaceapi.URI, recoveryFilename workspaceapi.URI,
	readOnly, forceRecover bool,
) (browserapi.Handler, error) {
	t, ok := c.comp.Tab(file)
	if ok {
		return t, nil
	}

	userRequestedView := readOnly

	if c.ed.IsExternal() {
		// The external editor is the source of truth for existing
		// files, so the mirror buffer stays read-only to keep the
		// dirty-tab path, flusher, and recovery prompt out of its
		// way. A file that does not exist yet has no content to
		// mirror; forcing the mirror read-only would make
		// workspace.Load refuse to create the empty buffer, so let
		// the new file open writable and let the editor materialize
		// it on disk.
		readOnly = c.fileExists(file)
	}

	if userRequestedView && !c.ed.IsExternal() {
		viewHandler, viewCloser, viewOK, viewErr := c.loadView(file)
		if viewErr != nil {
			return nil, viewErr
		}
		if viewOK {
			return c.newViewTab(file, viewHandler, viewCloser), nil
		}
	}
	if c.config.StreamingOpen {
		return c.openFileTabStreaming(file, recoveryFilename, readOnly, forceRecover)
	}
	return c.openFileTabSync(file, recoveryFilename, readOnly, forceRecover)
}

func (c *Component) openFileTabSync(
	file workspaceapi.URI, recoveryFilename workspaceapi.URI,
	readOnly, forceRecover bool,
) (browserapi.Handler, error) {
	buf := cell.NewBuffer()
	handler, fc, err := c.newFileBuffer(file, recoveryFilename, buf, readOnly, forceRecover)
	if err != nil {
		return nil, err
	}
	t := c.newTab(file, c.iconFor(file), fileTabName(file), handler, fc)
	return t, nil
}

func (c *Component) newViewTab(
	file workspaceapi.URI,
	h browserapi.Handler,
	closer workspace.FlusherCloser,
) *browser.Tab {
	return c.newTab(file, c.iconFor(file), fileTabName(file), h, closer)
}

// fileTabName returns the tab label for a file URI. It always uses the
// basename of the URI path so that files opened from a remote workspace
// (e.g. ssh://) show the same short name as local files instead of the
// full URI, which URI.Name returns for remote schemes.
func fileTabName(file workspaceapi.URI) string {
	return filepath.Base(file.Path())
}

// iconFor returns the configured icon for the given file path.
func (c *Component) iconFor(file workspaceapi.URI) rune {
	ext := filepath.Ext(file.Name())
	if icon, ok := c.config.Icons.Extensions[ext]; ok {
		return icon
	}
	return c.config.Icons.Default
}

// WaitStreamingLoads waits for in-flight asynchronous files being loaded.
// It should be used for testing only.
func (c *Component) WaitStreamingLoads() {
	c.streamingLoads.Wait()
}

func (c *Component) openFileTabStreaming(
	file, recoveryFilename workspaceapi.URI, readOnly, forceRecover bool,
) (browserapi.Handler, error) {
	sh, err := streamload.New(c.workspace, file, streamload.Config{})
	if err != nil {
		if !readOnly && errors.Is(err, os.ErrNotExist) {
			return c.openFileTabSync(file, recoveryFilename, readOnly, forceRecover)
		}
		return nil, fmt.Errorf("stream load file: %w", err)
	}
	icon := c.iconFor(file)
	def := newDeferHandler(sh)
	streamingTab := c.newTab(file, icon, fileTabName(file), def, nil)
	recovering := recoveryFilename != (workspaceapi.URI{})
	loading := make(chan struct{})
	c.streamingLoads.Add(2)
	go debug.CapturePanicReport(func() {
		defer c.streamingLoads.Done()
		c.animateTabLoading(file, loading)
	})
	go debug.CapturePanicReport(func() {
		defer c.streamingLoads.Done()
		defer close(loading)

		buf := cell.NewBuffer()
		fc, _, loadErr := c.loadFileBuffer(
			file, recoveryFilename, buf, readOnly, forceRecover)

		c.config.ScheduleNextTick(func() {
			var (
				realHandler Handler
				efc         *editorFlusherCloser
				buildErr    error
			)
			if loadErr == nil {
				realHandler, efc, buildErr = c.buildEditorHandler(
					file, buf, fc, readOnly, recovering)
			}
			openErr := loadErr
			if openErr == nil {
				openErr = buildErr
			}
			c.streamingBufferLoaded(
				streamingTab, sh, def, file, readOnly, recovering,
				realHandler, efc, openErr)
		})
	})

	return streamingTab, nil
}

func (c *Component) streamingBufferLoaded(
	streamingTab *browser.Tab, sh *streamload.Handler, def *deferHandler,
	file workspaceapi.URI, readOnly, recovering bool,
	realHandler Handler, efc *editorFlusherCloser, loadErr error,
) {
	defer sh.Close() //nolint:errcheck
	switch {
	case c.streamingTabAbandoned(streamingTab, file):
	case loadErr != nil:
		_ = c.comp.RemoveTab(streamingTab)
		switch {
		case isAlreadyOpenErr(loadErr):
			_, handled, routeErr := c.config.OpenRouter.RouteOpen(file, readOnly)
			switch {
			case handled && routeErr != nil:
				_, _ = c.Notify(browserapi.LevelError, "%v", routeErr)
			case !handled:
				c.openRecoveryPrompt(file)
			}
		case recovering && loadErr == workspaceapi.ErrStaleData:
			c.openAreYouSurePrompt(file)
		default:
			_, _ = c.Notify(browserapi.LevelError, "open %s: %v", file, loadErr)
		}
	default:
		c.streamingTabSwapHandler(streamingTab, sh, def, file, realHandler, efc)
		return
	}
	if efc != nil {
		_ = efc.Close()
	}
	if realHandler != nil {
		_ = realHandler.Close()
	}
}

func (c *Component) streamingTabSwapHandler(
	streamingTab *browser.Tab, sh *streamload.Handler, def *deferHandler,
	file workspaceapi.URI,
	realHandler Handler, efc *editorFlusherCloser,
) {
	offset := sh.SeekOffset()
	width, height := sh.Dimensions()
	def.Swap(realHandler)
	if err := streamingTab.SetHandler(realHandler, efc); err != nil {
		_, _ = c.Notify(browserapi.LevelError,
			"swap streaming handler for %s: %v", file, err)
	}
	if width > 0 && height > 0 {
		realHandler.Resize(width, height)
	}
	applyOffset(realHandler, offset)
}

// streamingTabAbandoned reports whether the user has closed the
// streaming tab between OpenFileTab returning and the swap callback
// running on the event loop.
func (c *Component) streamingTabAbandoned(
	streamingTab *browser.Tab, file workspaceapi.URI,
) bool {
	current, ok := c.comp.Tab(file)
	return !ok || current != streamingTab
}

// isAlreadyOpenErr identifies the family of errors that should
// trigger the recovery prompt: another process holds the swap file
// or the file is owned by a different workspace.
func isAlreadyOpenErr(err error) bool {
	return err == workspaceapi.ErrFileAlreadyOpen ||
		errors.Is(err, workspace.ErrOpenInOtherWorkspace)
}

// Open opens the given file in a new browser tab. If file is already
// open by another session or the last edit session crashed, it
// will create a prompt for the user to decide what to do.
func (c *Component) Open(file workspaceapi.URI) (browserapi.Handler, error) {
	h, err := c.OpenFileTab(file, false)
	if err != nil &&
		(err == workspaceapi.ErrFileAlreadyOpen || errors.Is(err, workspace.ErrOpenInOtherWorkspace)) {
		routed, handled, routeErr := c.config.OpenRouter.RouteOpen(file, false)
		if handled || routeErr != nil {
			return routed, routeErr
		}
		c.openRecoveryPrompt(file)
	}
	return h, err
}

// OpenReadOnly is like Open but opens the file in read-only mode (cannot be saved).
func (c *Component) OpenReadOnly(file workspaceapi.URI) (browserapi.Handler, error) {
	h, err := c.OpenFileTab(file, true)
	if err != nil &&
		(err == workspaceapi.ErrFileAlreadyOpen || errors.Is(err, workspace.ErrOpenInOtherWorkspace)) {
		routed, handled, routeErr := c.config.OpenRouter.RouteOpen(file, true)
		if handled || routeErr != nil {
			return routed, routeErr
		}
		c.openRecoveryPrompt(file)
	}
	return h, err
}

// ReadFile injects the contents of the desired file and writes it under the cursor.
func (c *Component) ReadFile(file workspaceapi.URI, h Handler) error {
	if ed, ok := h.(wrapEditor); ok {
		h = ed.Handler
	}

	// dump the file contents into a buffer we can read from
	buf := cell.NewBuffer()
	f, err := c.workspace.Open(file.Path())
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.ReadAll(io.TeeReader(f, buf))
	if err != nil {
		return err
	}
	view := workspace.NewUnixFileView(buf.View())
	if !view.EndsWithEOL() {
		buf.WriteString("\n")
	}
	buf.WithView(view)

	// get the current cursor position to insert to, which will be the line below
	coords := h.CursorAtScroll()
	coords.X = 0
	coords.Y += 1

	// inject the file contents surrounded by newlines to emulate vim's behaviour
	// we don't prefix with \n because we already moved the cursor at a point that
	// follows a \n (coords.X = 0; coords.Y += 1).
	cellEditor := h.CellEditor()
	_, _, _ = cellEditor.Edit(
		context.Background(), coords, coords, fmt.Sprintf("%s\n", buf),
	)
	return nil
}

// ErrHandlerNotFound is returned by Editor when no open editor
// handler exists for the requested resource.
var ErrHandlerNotFound = errors.New("handler not found")

// Editor satisfies Editor interface.
func (c *Component) Editor(resource workspaceapi.URI) (Handler, error) {
	for _, tab := range c.comp.Tabs() {
		if tab.URI().String() == resource.String() {
			h, ok := tab.Handler().(Handler)
			if !ok {
				continue
			}
			return h, nil
		}
	}
	// ensure that non-tab handlers returned by Handler
	// can also be returned with Editor.
	for _, ed := range c.editors {
		if ed.Resource().String() == resource.String() {
			return ed, nil
		}
	}
	return nil, ErrHandlerNotFound
}

// CommandKeyBinding returns a command that was mapped to the given key
// combination and true or an empty string and false if there was
// no command mapped to the given key.
func (c *Component) CommandKeyBinding(key term.KeyComb) ([][]string, bool) {
	cmd, ok := c.config.CommandKeyBindings[key]
	c.log(log.TraceLevel, "command key binding for %#v: %s", key, cmd)
	return cmd, ok
}

// CompleteCommand calls the command's completer with the given args and returns
// an interator with the possible argument completions.
func (c *Component) CompleteCommand(ctx context.Context, cmd textapi.Command) (
	iterator.Iterator[string], string, error,
) {
	if a, ok := c.config.CommandAliases[cmd.Name]; ok {
		if len(a.Completers) == 0 {
			return iterator.FromSlice[string](nil), "", nil
		}
		args := append([]string{cmd.Name}, cmd.Args...)
		if len(a.Completers) == 1 {
			factory := a.Completers[0]
			if factory == nil {
				return iterator.FromSlice[string](nil), "", nil
			}
			return factory(c).Complete(ctx, args)
		}
		completers := make([]command.Completer, 0, len(a.Completers))
		for _, factory := range a.Completers {
			if factory == nil {
				continue
			}
			completers = append(completers, factory(c))
		}
		return command.MultiCompleter(completers...).Complete(ctx, args)
	}

	man, ok := c.cmdSubscribers[cmd.Name]
	if !ok {
		c.log(log.DebugLevel, "complete command %q: no subscribers", cmd.Name)
		return iterator.FromSlice[string](nil), "", nil
	}

	return man.handler.Complete(ctx, cmd)
}

// EnvSource returns the EnvSource configured for this Component.
// Returns nil when none was wired (callers chain to os.Getenv).
func (c *Component) EnvSource() cmdenv.Source {
	return c.config.EnvSource
}

// expansionContext is the per-dispatch snapshot of editor state used
// to resolve $VAR references in alias bodies and dispatched argv. A
// single context is captured at the top of DispatchCommand and shared
// across every alias target so that two targets in the same alias
// chain see identical FILE/LINE/SELECTION values, even if a target's
// own side effects would have changed them.
type expansionContext struct {
	file         workspaceapi.URI
	line, column int
	selection    string
	word         string
	lang         string
}

func (c *Component) captureExpansionContext() expansionContext {
	content, _ := c.comp.Focus().Content()
	tab, ok := content.(*browser.Tab)
	if !ok {
		return expansionContext{}
	}
	ctx := expansionContext{file: tab.URI()}
	if lang, err := languages.LanguageForFile(filepath.Base(ctx.file.Path())); err == nil {
		ctx.lang = lang
	}
	if sel, ok := tab.Selection(); ok {
		ctx.selection = sel
	}
	h, ok := tab.Handler().(Handler)
	if !ok {
		return ctx
	}
	pos := h.CursorAtScroll()
	ctx.line = pos.Y + 1
	ctx.column = pos.X + 1
	ctx.word = wordAtCursor(h.CellView(), pos)
	return ctx
}

func (c *Component) resolveBuiltin(
	ctx expansionContext, name string,
) (string, bool) {
	switch name {
	case "FILE":
		return ctx.file.Path(), true
	case "FILE_URI":
		if ctx.file == (workspaceapi.URI{}) {
			return "", true
		}
		return ctx.file.String(), true
	case "FILE_DIR":
		if ctx.file.Path() == "" {
			return "", true
		}
		return filepath.Dir(ctx.file.Path()), true
	case "FILE_BASENAME":
		if ctx.file.Path() == "" {
			return "", true
		}
		return filepath.Base(ctx.file.Path()), true
	case "FILE_STEM":
		if ctx.file.Path() == "" {
			return "", true
		}
		base := filepath.Base(ctx.file.Path())
		return strings.TrimSuffix(base, filepath.Ext(base)), true
	case "FILE_EXT":
		if ctx.file.Path() == "" {
			return "", true
		}
		return strings.TrimPrefix(filepath.Ext(ctx.file.Path()), "."), true
	case "FILE_REL":
		return c.fileRel(ctx), true
	case "LINE":
		if ctx.line == 0 {
			return "", true
		}
		return strconv.Itoa(ctx.line), true
	case "COLUMN":
		if ctx.column == 0 {
			return "", true
		}
		return strconv.Itoa(ctx.column), true
	case "SELECTION":
		return ctx.selection, true
	case "WORD":
		return ctx.word, true
	case "LANG":
		return ctx.lang, true
	}
	return "", false
}

func (c *Component) fileRel(ctx expansionContext) string {
	if ctx.file == (workspaceapi.URI{}) {
		return ""
	}
	user := c.config.EnvSource
	if user == nil {
		return ""
	}
	wsRaw, ok := user("WORKSPACE_URI")
	if !ok || wsRaw == "" {
		return ""
	}
	wsURI, err := workspaceapi.ParseURI(wsRaw)
	if err != nil {
		return ""
	}
	return workspaceapi.RelPath(wsURI, ctx.file)
}

func (c *Component) dispatchedEnvSource(ctx expansionContext) cmdenv.Source {
	user := c.config.EnvSource
	return func(name string) (string, bool) {
		if v, ok := c.resolveBuiltin(ctx, name); ok {
			return v, true
		}
		if user != nil {
			return user(name)
		}
		return "", false
	}
}

// DispatchEnv returns the cmdenv.Source that DispatchCommand uses to
// expand args. Includes $FILE/$WORD/$WORKSPACE_* derived from the
// currently focused window plus the user-configured EnvSource.
// External alias dispatchers consume it to perform per-step
// expansion outside the component.
func (c *Component) DispatchEnv() cmdenv.Source {
	return func(key string) (value string, ok bool) {
		return c.dispatchedEnvSource(c.captureExpansionContext())(key)
	}
}

// CommandAliases returns the alias table configured on this
// Component. The Component itself does not act on aliases (resolution
// is performed by ide-level dispatchers); the accessor exists so
// those dispatchers and test harnesses can read what was configured.
func (c *Component) CommandAliases() map[string]CommandAlias {
	return c.config.CommandAliases
}

func wordAtCursor(view cell.View, pos term.Coordinates) string {
	if view == nil {
		return ""
	}
	rows := view.RawCells()
	if pos.Y < 0 || pos.Y >= len(rows) {
		return ""
	}
	row := rows[pos.Y]
	if pos.X < 0 || pos.X >= len(row) {
		return ""
	}
	if !isWordRune(row[pos.X].Ch) {
		return ""
	}
	start := pos.X
	for start > 0 && isWordRune(row[start-1].Ch) {
		start--
	}
	end := pos.X
	for end < len(row) && isWordRune(row[end].Ch) {
		end++
	}
	var b strings.Builder
	b.Grow(end - start)
	for i := start; i < end; i++ {
		b.WriteRune(row[i].Ch)
	}
	return b.String()
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// DispatchCommand dispatches a EventTypeCommand with cmd to subscribers
// subscribed via SubscribeEvents.
func (c *Component) DispatchCommand(
	ctx context.Context, cmd textapi.Command,
) (handled bool, err error) {
	if cmd.Window == nil {
		panic("invalid command: missing Window from which command was invoked")
	}

	man, ok := c.cmdSubscribers[cmd.Name]
	if !ok {
		if fb, ok := c.config.CommandFallbacks[cmd.Name]; ok {
			c.log(log.DebugLevel, "Dispatching command %q: running fallback", cmd.Name)
			fb.ShowFallbackPrompt(ctx, cmd.Name, cmd.Args...)
			return true, nil
		}
		c.log(log.DebugLevel, "Dispatching command %q: no subscribers", cmd.Name)
		return false, nil
	}
	c.log(log.DebugLevel, "Dispatching command %q with args %v", cmd.Name, cmd.Args)
	err = man.handler.HandleCommand(ctx, cmd)
	if err != nil {
		return true, err
	}
	return true, nil
}

func (c *Component) dispatchFlush(file workspaceapi.URI, h Handler) error {
	content := c.getContent(h)

	// clear dirty/flushed attributes
	c.resetTabProperties(file)

	ev := textapi.Event{
		Type:     textapi.EventTypeFlush,
		URI:      file,
		Resource: h,
		Content:  content,
	}
	c.DispatchEvent(ev)
	return nil
}

// DispatchEvent dispatches the given event to subscribers.
func (c *Component) DispatchEvent(ev textapi.Event) (handled bool) {
	subs, ok := c.edSubscribers[ev.Type]
	if !ok || len(subs) == 0 {
		return
	}

	c.edSubscribers[ev.Type] = make([]EventHandler, 0, len(subs))
	for _, h := range subs {
		exit := h.Handle(context.Background(), ev)
		if !exit {
			c.edSubscribers[ev.Type] = append(c.edSubscribers[ev.Type], h)
		}
	}

	handled = true
	return
}

// Notify satisfies browser.Browser.
func (c *Component) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return c.config.Notify(level, "%s", fmt.Sprintf(msg, args...))
}

// NotifyOnce satisfies browser.Browser.
func (c *Component) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return c.config.NotifyOnce(level, msg, args...)
}

// UpdateNotificationProgress satisfies browser.Browser.
func (c *Component) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return c.config.UpdateNotificationProgress(id, message, progress, total)
}

// Split satisfies browser.WindowManager.
func (c *Component) Split(
	o browserapi.Orientation, win browser.Window, h browserapi.Handler,
) (browser.Window, error) {
	w, ok := c.comp.Split(o, win, h)
	if !ok {
		return nil, textapi.ErrInvalidSplit
	}
	return w, nil
}

// IterateWindows satisfies browser.WindowManager.
func (c *Component) IterateWindows(fn func(browser.Window)) {
	c.comp.IterateWindows(fn)
}

// SplitRoot satisfies browser.WindowManager.
func (c *Component) SplitRoot(
	alignment component.Alignment, h browserapi.Handler,
) (browser.Window, error) {
	w, ok := c.comp.SplitRoot(alignment, h)
	if !ok {
		return nil, textapi.ErrInvalidSplit
	}
	return w, nil
}

// Bar creates a new status bar with h's component and delegates handling of mouse events to h.
func (c *Component) Bar(cfg browserapi.BarConfig, h tui.Handler) error {
	if cfg.Size <= 0 {
		return fmt.Errorf("invalid bar size: %d", cfg.Size)
	}
	c.comp.Bar(cfg, h)
	return nil
}

// PublishEvent sends an event to the main event loop which
// forces Handle to be called on the tui.Handler in focus.
func (c *Component) PublishEvent(ev term.Event) error {
	ok := c.config.EventPublisher(ev)
	if !ok {
		return errors.New("event stream not ready")
	}
	return nil
}

// Focus returns the current window in focus. It satisfies browser.Browser.
func (c *Component) Focus() (browser.Window, error) {
	return c.comp.Focus(), nil
}

// FocusTab returns the Tab corresponding to the current window in focus,
// or false if the current window in focus is not drawing a Tab.
func (c *Component) FocusTab() (*browser.Tab, bool) {
	return c.comp.FocusTab()
}

// SetFocus sets the window in focus and returns the previous window in focus.
// It satisfies browser.Browser.
func (c *Component) SetFocus(win browser.Window) (browser.Window, error) {
	return c.comp.SetFocus(win), nil
}

// SetWindowWidth sets the width of the given window.
func (c *Component) SetWindowWidth(win browser.Window, width int) bool {
	return c.comp.SetWindowWidth(win, width)
}

// Edit edits the resource with name and buffer
// with the underlying Editor in a new browser buffer.
//
// NOTE: Returned Handler has no EventTypeFocus support.
//
// Deprecated: Use OpenFileTab instead.
func (c *Component) Edit(
	ctx context.Context,
	file workspaceapi.URI, buf *cell.Buffer, readOnly, recover bool,
) (Handler, error) {
	editor, err := c.ed.Edit(ctx, file, buf, readOnly, recover)
	if err != nil {
		return nil, err
	}
	editor = wrapEditor{parent: c, Handler: editor}
	c.editors[file.String()] = editor
	return editor, nil
}

// Flush flushes the contents of the tab at the given window,
// asynchronously. See workspace.FlusherCloser for the channel
// contract.
func (c *Component) Flush(ctx context.Context, win browser.Window) (<-chan error, error) {
	content, err := win.Content()
	if err != nil {
		return nil, fmt.Errorf("get window content: %w", err)
	}
	return c.FlushTab(ctx, content)
}

// ForceFlush flushes the contents of the tab at the given window
// asynchronously, overriding read-only mode, and ignoring stale data
// errors and others. See workspace.FlusherCloser for the channel
// contract.
func (c *Component) ForceFlush(ctx context.Context, win browser.Window) (<-chan error, error) {
	content, err := win.Content()
	if err != nil {
		return nil, fmt.Errorf("get window content: %w", err)
	}
	return c.ForceFlushTab(ctx, content)
}

// FlushTab flushes the contents of the given handler, if it is a
// tab, asynchronously.
func (c *Component) FlushTab(ctx context.Context, h browserapi.Handler) (<-chan error, error) {
	t, ok := h.(*browser.Tab)
	if !ok || t.Closer() == nil {
		return nil, textapi.ErrInvalidSave
	}

	fc, ok := t.Closer().(workspace.FlusherCloser)
	if !ok {
		return nil, textapi.ErrInvalidSave
	}

	return fc.Flush(ctx)
}

// ForceFlushTab force-flushes the contents of the given handler, if
// it is a tab, asynchronously, overriding read-only mode, and
// ignoring stale data errors and others.
func (c *Component) ForceFlushTab(ctx context.Context, h browserapi.Handler) (<-chan error, error) {
	t, ok := h.(*browser.Tab)
	if !ok || t.Closer() == nil {
		return nil, textapi.ErrInvalidSave
	}

	fc, ok := t.Closer().(workspace.FlusherCloser)
	if !ok {
		return nil, textapi.ErrInvalidSave
	}

	return fc.ForceFlush(ctx)
}

// LastFlush returns the last time this handler was flushed, if it is a valid
// tab that can be flushed. If the tab has not yet been flushed, then
// the returned time will be a zero time.Time value.
func (c *Component) LastFlush(h browserapi.Handler) (time.Time, error) {
	t, ok := h.(*browser.Tab)
	if !ok || t.Closer() == nil {
		return time.Time{}, errors.New("content is not a tab")
	}

	fc, ok := t.Closer().(workspace.FlusherCloser)
	if !ok {
		return time.Time{}, errors.New("content is not a tab that can be flushed")
	}

	return fc.LastFlush(), nil
}

// Settled returns nil when the given handler has no save or reload whose
// result is still pending, and otherwise a channel that is closed once
// every pending result has been handed to the scheduler. Callers that must
// not act on the half-applied state of a file (its saved timestamp, its
// dirty marker) wait on it and then schedule their retry, which runs behind
// those results.
func (c *Component) Settled(h browserapi.Handler) <-chan struct{} {
	t, ok := h.(*browser.Tab)
	if !ok {
		return nil
	}
	efc, ok := t.Closer().(*editorFlusherCloser)
	if !ok {
		return nil
	}
	return efc.settled
}

func (c *Component) getContent(h Handler) string {
	cells := h.CellView().RawCells()
	return term.CellsToString(cells)
}

func (c *Component) dispatchOpenUponSubscribe(h EventHandler) bool {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, tab := range c.comp.Tabs() {
		resHandler, ok := tab.Handler().(Handler)
		if !ok {
			// tab handler does not implement Handler
			continue
		}
		str := c.getContent(resHandler)
		exit := h.Handle(ctx, textapi.Event{
			Type:     textapi.EventTypeOpen,
			URI:      tab.URI(),
			Resource: resHandler,
			Content:  str,
		})
		if exit {
			return true
		}
	}
	return false
}

func (c *Component) dispatchFocusUponSubscribe(h EventHandler) bool {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t, ok := c.comp.FocusTab()
	if ok {
		dimensions := c.getContentDimensions(c.focus)
		resHandler, ok := t.Handler().(Handler)
		if ok {
			ev := textapi.Event{
				Type:     textapi.EventTypeFocus,
				URI:      t.URI(),
				Resource: resHandler,
				Start:    dimensions,
				From:     resHandler.CursorAtScroll(),
			}
			return h.Handle(ctx, ev)
		}
	}
	return false
}

// SubscribeEvents subscribes h to editor events of type ev.
// If ev is of type EventTypeOpen, an event will be dispatched for
// every Tab currently open.
func (c *Component) SubscribeEvents(evs []textapi.EventType, h EventHandler) error {
	c.log(log.TraceLevel, "subscribe sub=%p: evs=%v", h, evs)
	// iterate to dispatch immediate events
	for _, ev := range evs {
		switch ev {
		case textapi.EventTypeOpen:
			exit := c.dispatchOpenUponSubscribe(h)
			if exit {
				c.log(log.DebugLevel, "not subscribe sub=%p: open: exit=true", h)
				return nil
			}
		case textapi.EventTypeFocus:
			exit := c.dispatchFocusUponSubscribe(h)
			if exit {
				c.log(log.DebugLevel, "not subscribe sub=%p: focus: exit=true", h)
				return nil
			}
		}
	}

	// iterate again so if handler exited for any event, we have returned
	// and we do not subscribe it
	var delegated []textapi.EventType
	for _, ev := range evs {
		switch ev {
		// delegate certain event dispatching to underlying editor.
		case textapi.EventTypeOpen, textapi.EventTypeEdit,
			textapi.EventTypeScroll, textapi.EventTypeCursor,
			textapi.EventTypeSelection, textapi.EventTypeHidden,
			textapi.EventTypeVisible:
			delegated = append(delegated, ev)
		default:
			if _, ok := c.edSubscribers[ev]; !ok {
				c.edSubscribers[ev] = make([]EventHandler, 0, 1)
			}
			c.edSubscribers[ev] = append(c.edSubscribers[ev], h)
		}
	}

	return c.ed.SubscribeEvents(delegated, h)
}

// UnsubscribeEvents unsubscribes sub from all events.
func (c *Component) UnsubscribeEvents(sub EventHandler) (ret bool, err error) {
	c.log(log.TraceLevel, "unsubscribe sub=%p", sub)
	final := make(map[textapi.EventType][]EventHandler)
	for ev, subs := range c.edSubscribers {
		final[ev] = make([]EventHandler, 0, len(subs))
		for _, s := range subs {
			if s != sub {
				final[ev] = append(final[ev], s)
			} else {
				ret = true
			}
		}
	}
	c.edSubscribers = final
	var ed bool
	ed, err = c.ed.UnsubscribeEvents(sub)
	if err != nil {
		return
	}
	return ret || ed, nil
}

// IsExternal delegates to the underlying Editor.
func (c *Component) IsExternal() bool { return c.ed.IsExternal() }

// Commands returns a list of commands registered via SubscribeCommand.
func (c *Component) Commands() (ret []command.Manual) {
	ret = make([]command.Manual, len(c.cmdSubscribers))
	var i int
	for _, cmd := range c.cmdSubscribers {
		ret[i] = cmd.man
		i++
	}
	return ret
}

// SubscribeCommand installs cm as a command handler of cmd or returns
// an error if there's already a CommandHandler installed for this cmd.
func (c *Component) SubscribeCommand(cmd textapi.CommandManual, cm CommandHandler) error {
	if _, ok := c.cmdSubscribers[cmd.Name]; ok {
		return errors.New("command already registered")
	}

	c.cmdSubscribers[cmd.Name] = commandAll{
		man:     apiManualToManual(cmd),
		handler: cm,
	}
	return nil
}

// RegisterREPLCommand registers a REPL command to be dispatched to a
// textapi.REPLHandler.
func (c *Component) RegisterREPLCommand(
	cmd textapi.CommandManual, h textapi.REPLHandler,
) error {
	if _, ok := c.replSubscribers[cmd.Name]; ok {
		return errors.New("command already registered")
	}
	c.replSubscribers[cmd.Name] = replCommandAll{man: cmd, handler: h}
	return nil
}

// UnregisterREPLCommand un-registers a REPL command.
func (c *Component) UnregisterREPLCommand(cmd string) error {
	if _, ok := c.replSubscribers[cmd]; !ok {
		return ErrCommandNotRegistered
	}
	delete(c.replSubscribers, cmd)
	return nil
}

// REPLCommand returns the REPL handler registered for cmd.
func (c *Component) REPLCommand(cmd string) (textapi.REPLHandler, bool) {
	h, ok := c.replSubscribers[cmd]
	return h.handler, ok
}

// REPLCommands returns registered REPL command manuals.
func (c *Component) REPLCommands() (ret []textapi.CommandManual) {
	ret = make([]textapi.CommandManual, 0, len(c.replSubscribers))
	for _, cmd := range c.replSubscribers {
		ret = append(ret, cmd.man)
	}
	return ret
}

// UnsubscribeCommand un-registers command.
func (c *Component) UnsubscribeCommand(cmd string) error {
	if _, ok := c.cmdSubscribers[cmd]; !ok {
		return ErrCommandNotRegistered
	}
	delete(c.cmdSubscribers, cmd)
	return nil
}

// Browser returns this Component's underlying browser.Component.
func (c *Component) Browser() *browser.Component {
	return &c.comp
}

// Resize satisfies tui.Component.
func (c *Component) Resize(width, height int) {
	c.comp.Resize(width, height)
	if c.focus != (thandler.Window{}) {
		c.tryDispatchEventFocus(c.focus)
	}
}

// Draw satisfies tui.Component.
func (c *Component) Draw(w term.Writer) {
	c.comp.Draw(w)
}

// Handle delegates events to the underlying browser.Component.
func (c *Component) Handle(ev term.Event) (exit, handled bool) {
	return c.comp.Handle(ev)
}

// DragHover satisfies browser.DragTarget.
func (c *Component) DragHover(pos term.Coordinates) bool {
	return c.comp.DragHover(pos)
}

// DragCancel satisfies browser.DragTarget.
func (c *Component) DragCancel() {
	c.comp.DragCancel()
}

// DragDrop satisfies browser.DragTarget.
func (c *Component) DragDrop(pos term.Coordinates, paths []string) bool {
	return c.comp.DragDrop(pos, paths)
}

// SetDim sets whether next call to draw should use
// non-focus window diming feature.
func (c *Component) SetDim(to bool) bool {
	return c.comp.SetDim(to)
}

// WindowManagerPosition returns the offset from the top left corner
// where the underlying window manager starts.
func (c *Component) WindowManagerPosition() term.Coordinates {
	return c.comp.WindowManagerPosition()
}

// WindowManagerSize returns the size of the window manager.
func (c *Component) WindowManagerSize() (width, height int) {
	return c.comp.WindowManagerSize()
}

// SetRightInset changes the width of the reserved right column and
// relays the editor out around it.
func (c *Component) SetRightInset(cells int) {
	c.comp.SetRightInset(cells)
}

// Floating satisfies browser.WindowManager.
func (c *Component) Floating(
	h browser.Floating, cfg browserapi.FloatingConfig,
) (browser.Window, error) {
	return c.comp.Floating(h, cfg), nil
}

// Tab satisfies browser.WindowManager.
func (c *Component) Tab(
	resource workspaceapi.URI, icon rune, name string, h browserapi.Handler,
) (browserapi.Handler, error) {
	if _, ok := h.(Handler); ok {
		panic("handler must not be a text.Handler. " +
			"Use OpenFileTab if attempting to create a tab with the return value of Edit")
	}

	t, ok := c.comp.Tab(resource)
	if ok {
		return t, nil
	}

	t = c.newTab(resource, icon, name, h, nil)
	return t, nil
}

// Resource returns an open resource or false if resource with uri is not open.
func (c *Component) Resource(uri workspaceapi.URI) (browserapi.Handler, bool) {
	return c.comp.Tab(uri)
}

// IsDirty returns whether the given tab has unflushed changes.
func (c *Component) IsDirty(uri workspaceapi.URI) (dirty bool, ok bool) {
	attrs, ok := c.comp.TabAttrs(uri)
	if !ok {
		return
	}
	dirty = attrs == c.config.DirtyTabAttr
	ok = true
	return
}

// Window returns the window with the given id and true or nil and false if no
// window with the given id could be found in the underlying browser.
func (c *Component) Window(id uint64) (browser.Window, bool) {
	return c.comp.Window(id)
}

// Tabs returns the tabs open in this browser.Component.
func (c *Component) Tabs() []*browser.Tab {
	return c.comp.Tabs()
}

// SetTabName sets the title and attributes of the title of the given tab.
// If the given browserapi.Handler is not a tab, then this method returns an error.
func (c *Component) SetTabName(uri workspaceapi.URI, title string, attr term.Attributes) error {
	ok := c.comp.SetTabDefaultNameAndAttrs(uri, title, attr)
	if !ok {
		return errors.New("set title called on unknown tab")
	}
	_ = c.comp.SetTabNameAndAttrs(uri, title, attr)
	return nil
}

// Prompt creates a new prompt to be drawn as an overlay on the next call to Draw
// and it also takes over event control until user either exits prompt or selects
// an option.
func (c *Component) Prompt(
	message string, options []string,
	bindings []term.KeyComb,
	promptHandler handler.PromptHandler,
) browser.Window {
	return c.comp.Prompt(message, options, bindings, promptHandler)
}

// SubscribeWindow subscribes sub to changes in focus due to changing the window in focus.
func (c *Component) SubscribeWindow(sub thandler.WindowSubscriber) {
	c.comp.Subscribe(sub)
}

// Workspace returns the workspace used by this Component.
func (c *Component) Workspace() Workspace {
	return c.workspace
}

// Close closes all resources associated with this Component.
func (c *Component) Close() error {
	// avoid dispatching close events on flusherCloser callbacks
	c.edSubscribers = make(map[textapi.EventType][]EventHandler)
	c.cancelCtx()

	err := c.comp.Close()
	if err != nil {
		c.log(log.ErrorLevel, "browser.Component.Close error: %v", err)
		return err
	}
	return nil
}

func (c *Component) newTab(
	resource workspaceapi.URI, icon rune, name string,
	h browserapi.Handler, closer workspace.FlusherCloser,
) *browser.Tab {
	t := c.comp.NewTab(resource, icon, name, h, closer)
	t.Subscribe(&compTabSubscriber{parent: c})
	return t
}

func (c *Component) loadView(file workspaceapi.URI) (
	browserapi.Handler,
	workspace.FlusherCloser,
	bool,
	error,
) {
	lang, _ := languages.LanguageForFile(filepath.Base(file.Path()))
	if lang != "markdown" {
		return nil, nil, false, nil
	}

	markdownComponent, modTime, err := c.loadMarkdown(file)
	if err != nil {
		return nil, nil, false, err
	}
	markdownHandler := c.newMarkdownHandler(markdownComponent)
	closer := newMarkdownFlusherCloser(
		c, file, markdownHandler, markdownComponent, modTime,
	)
	return markdownHandler, closer, true, nil
}

func (c *Component) loadMarkdown(
	uri workspaceapi.URI,
) (*markdown.Component, time.Time, error) {
	f, err := c.workspace.OpenFile(uri.Path(), os.O_RDONLY, 0)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("stat file: %w", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read file: %w", err)
	}
	component, err := markdown.NewWithConfig(string(data), c.config.Markdown)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("new markdown component: %w", err)
	}
	return component, info.ModTime(), nil
}

func (c *Component) newMarkdownHandler(component *markdown.Component) *hmarkdown.Handler {
	return hmarkdown.New(component, hmarkdown.WithOnLinkClick(func(link *url.URL) bool {
		if link.Scheme != "http" && link.Scheme != "https" {
			return false
		}
		linkstr := link.String()
		meta := clipboard.Data{Text: linkstr}
		err := c.config.Clipboard.Copy(clipboard.DefaultRegisterID, meta)
		if err != nil {
			_, _ = c.config.Notifications.Notify(browserapi.LevelError,
				"copy URL to clipboard: %v", err)
		} else {
			_, _ = c.config.Notifications.Notify(browserapi.LevelSuccess,
				"copied URL %s to clipboard", linkstr)
		}
		return true
	}))
}

var loadingSpinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

const loadingSpinnerFPS = 10

func (c *Component) animateTabLoading(uri workspaceapi.URI, done <-chan struct{}) {
	ticker := time.NewTicker(time.Second / loadingSpinnerFPS)
	defer ticker.Stop()

	frame := 0
	c.scheduleSpinnerFrame(uri, frame)
	for {
		select {
		case <-done:
			c.scheduleResetTabIcon(uri)
			return
		case <-c.ctx.Done():
			c.scheduleResetTabIcon(uri)
			return
		case <-ticker.C:
			frame++
			c.scheduleSpinnerFrame(uri, frame)
		}
	}
}

func (c *Component) scheduleSpinnerFrame(uri workspaceapi.URI, frame int) {
	ch := loadingSpinnerFrames[frame%len(loadingSpinnerFrames)]
	c.config.ScheduleNextTick(func() {
		select {
		case <-c.ctx.Done():
			return
		default:
		}
		c.comp.SetTabIcon(uri, ch)
		_ = c.PublishEvent(term.Event{Type: term.EventNone})
	})
}

func (c *Component) scheduleResetTabIcon(uri workspaceapi.URI) {
	c.config.ScheduleNextTick(func() {
		select {
		case <-c.ctx.Done():
			return
		default:
		}
		c.comp.ResetTabIcon(uri)
		_ = c.PublishEvent(term.Event{Type: term.EventNone})
	})
}

type commandAll struct {
	handler CommandHandler
	man     command.Manual
}

type replCommandAll struct {
	handler textapi.REPLHandler
	man     textapi.CommandManual
}

type handlerWindowSubscriber = Component

func (c *handlerWindowSubscriber) OnFocus(old, focus thandler.Window) {
	if old != (thandler.Window{}) {
		c.tryDispatchEvent(old, textapi.EventTypeUnfocus)
	}
	c.tryDispatchEventFocus(focus)
	(*Component)(c).focus = focus
}

func apiManualToManual(cmd textapi.CommandManual) command.Manual {
	var subcmds []command.Manual
	if len(cmd.Commands) != 0 {
		subcmds = make([]command.Manual, len(cmd.Commands))
		for i, cmd := range cmd.Commands {
			subcmds[i] = apiManualToManual(cmd)
		}
	}
	return command.Manual{
		Name:     cmd.Name,
		Summary:  cmd.Summary,
		Synopsis: cmd.Synopsis,
		Commands: subcmds,
	}
}

func applyOffset(scr component.Scrollable, offset int) {
	if offset <= 0 || scr == nil {
		return
	}
	for scr.SeekOffset() < offset {
		if !scr.SeekDown() {
			return
		}
	}
}
