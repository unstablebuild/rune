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

package finder

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/search"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/workspace/walkdir"
)

const (
	readerBufferSize  = 64 * 1024
	defaultMaxHistory = 2000
)

// Permissions returns the permissions required by this extension.
func Permissions() []extensionapi.Permission {
	return []extensionapi.Permission{
		extensionapi.Permission(extensionapi.PermissionBrowserResourceOpener),
		extensionapi.Permission(extensionapi.PermissionInterrupt),
		extensionapi.Permission(extensionapi.PermissionNotifications),
		extensionapi.Permission(extensionapi.PermissionBrowserWindowManager),
		extensionapi.PermissionStorage,
		extensionapi.Permission(extensionapi.PermissionEditor),
		extensionapi.Permission(extensionapi.PermissionFileSystem),
		extensionapi.Permission(extensionapi.PermissionExecute),
	}
}

// Clients contains the workspace clients required by New.
type Clients struct {
	Storage        storageapi.Service
	ResourceOpener browserapi.ResourceOpener
	WindowManager  browserapi.WindowManager
	Interrupter    term.Interrupter
	Notifications  browserapi.Notifications
	Editor         textapi.Editor
	FileSystem     workspaceapi.FileSystem
	Executor       workspaceapi.Executor
	// IgnoreMatcher excludes paths from the workspace traversal performed by
	// the native (non-ripgrep) fuzzy search backend. May be nil, in which
	// case no exclusions are applied.
	IgnoreMatcher walkdir.Filter
}

// RedispatchHandler is a browser handler that can handle repeated command
// invocations while its split window is already open.
type RedispatchHandler interface {
	browserapi.Handler
	Redispatch(context.Context, textapi.Command) error
}

// Options are optional behaviors for NewWithOptions; the zero value
// preserves the default file-finder behavior.
type Options struct {
	// OnSelect replaces getResource + the resource opener for results that
	// do not identify files, e.g. agent conversations.
	OnSelect func(item string)
	// InitialQuery seeds the search input before the data scan starts.
	InitialQuery string
}

// New returns a tui handler that uses native extensionv2 workspace clients.
func New(
	ctx context.Context, clients Clients, invokeWindow browserapi.Window, cfg config.Config,
	historyKey term.KeyComb, historyDocumentID string, command string,
	fallback func(workspaceapi.FileSystem, context.Context) (iterator.Iterator[string], error),
	getResource func(exec workspaceapi.FileSystem, line string) (workspaceapi.URI, term.Coordinates, bool),
) (RedispatchHandler, error) {
	return NewWithOptions(ctx, clients, invokeWindow, cfg, historyKey,
		historyDocumentID, command, fallback, getResource, Options{})
}

// NewWithOptions is like New but accepts additional behavior overrides.
func NewWithOptions(
	ctx context.Context, clients Clients, invokeWindow browserapi.Window, cfg config.Config,
	historyKey term.KeyComb, historyDocumentID string, command string,
	fallback func(workspaceapi.FileSystem, context.Context) (iterator.Iterator[string], error),
	getResource func(exec workspaceapi.FileSystem, line string) (workspaceapi.URI, term.Coordinates, bool),
	opts Options,
) (RedispatchHandler, error) {
	maxHistory, err := cfg.GetInt("history")
	if err != nil {
		if err != config.ErrNotFound {
			log.Errorf("failed to load 'history' from config: %v", err)
		}
		maxHistory = defaultMaxHistory
	} else {
		log.Tracef("loaded 'history' from config: %v", maxHistory)
	}

	listCfg := buildListConfig(cfg, clients.Interrupter)
	return newWithListConfig(ctx, clients, invokeWindow,
		historyKey, historyDocumentID, command, maxHistory, listCfg,
		fallback, getResource, opts)
}

// NewWithListConfig is like New but accepts a pre-built
// search.ListConfig so callers can opt into SyncSearch and other
// deterministic settings without going through config.Config.
func NewWithListConfig(
	ctx context.Context, clients Clients, invokeWindow browserapi.Window,
	historyKey term.KeyComb, historyDocumentID string, command string,
	maxHistory int, listCfg search.ListConfig,
	fallback func(workspaceapi.FileSystem, context.Context) (iterator.Iterator[string], error),
	getResource func(exec workspaceapi.FileSystem, line string) (workspaceapi.URI, term.Coordinates, bool),
	opts ...Options,
) (RedispatchHandler, error) {
	var opt Options
	if len(opts) > 0 {
		opt = opts[0]
	}
	return newWithListConfig(ctx, clients, invokeWindow,
		historyKey, historyDocumentID, command, maxHistory, listCfg,
		fallback, getResource, opt)
}

func newWithListConfig(
	ctx context.Context, clients Clients, invokeWindow browserapi.Window,
	historyKey term.KeyComb, historyDocumentID string, command string,
	maxHistory int, listCfg search.ListConfig,
	fallback func(workspaceapi.FileSystem, context.Context) (iterator.Iterator[string], error),
	getResource func(exec workspaceapi.FileSystem, line string) (workspaceapi.URI, term.Coordinates, bool),
	opts Options,
) (RedispatchHandler, error) {
	h := &fuzzyFinderHandler{
		s:                    clients.Storage,
		f:                    clients.ResourceOpener,
		wm:                   clients.WindowManager,
		p:                    clients.Interrupter,
		m:                    clients.Notifications,
		ed:                   clients.Editor,
		fs:                   clients.FileSystem,
		executor:             clients.Executor,
		ignoreMatcher:        clients.IgnoreMatcher,
		invokeWindow:         invokeWindow,
		historyKey:           historyKey,
		getResource:          getResource,
		onSelect:             opts.OnSelect,
		cmdStr:               command,
		useWorkspaceFallback: command == "",
		workspaceFallback:    fallback,
		waitChan:             make(chan error),
		scanDone:             make(chan struct{}),
	}
	if (h.f == nil && h.onSelect == nil) || h.p == nil {
		return nil, errors.New("extension is missing critical permissions")
	}
	if h.s != nil {
		h.history.Init(h.s, historyDocumentID, maxHistory)
		if err := h.history.Load(); err != nil {
			return nil, err
		}
	}

	h.ctx, h.cancelCtx = context.WithCancel(context.Background())

	h.list.Init(listCfg)
	if opts.InitialQuery != "" {
		h.list.Buffer().WriteString(opts.InitialQuery)
	}
	ed, _ := standard.Editor(standard.WithWrap(true)).
		Edit(h.ctx, workspaceapi.RandomURI("memory"), h.list.Buffer(), false, false)
	h.listHandler = search.Handler(&h.list, ed, func(item string) {
		searchQuery := h.list.Buffer().String()
		if h.onSelect != nil {
			h.onSelect(item)
		} else {
			h.openResource(searchQuery, item)
		}
		h.addSearchHistory(searchQuery)
	})

	var defCell term.Cell
	if listCfg.ElementAttr != nil {
		defCell.Bg = listCfg.ElementAttr.Bg
		defCell.Fg = listCfg.ElementAttr.Fg
	}
	h.background = component.WithBackground(h.listHandler, defCell)

	log.Debugf("useWorkspaceFallback set to %v", h.useWorkspaceFallback)

	go debug.CapturePanicReport(func() {
		h.scanData()
	})

	return h, nil
}

type fuzzyFinderHandler struct {
	s                    storageapi.Service
	f                    browserapi.ResourceOpener
	wm                   browserapi.WindowManager
	p                    term.Interrupter
	m                    browserapi.Notifications
	ed                   textapi.Editor
	fs                   workspaceapi.FileSystem
	executor             workspaceapi.Executor
	ignoreMatcher        walkdir.Filter
	invokeWindow         browserapi.Window
	historyKey           term.KeyComb
	mu                   sync.Mutex
	cmdStr               string
	getResource          func(workspaceapi.FileSystem, string) (workspaceapi.URI, term.Coordinates, bool)
	onSelect             func(item string)
	workspaceFallback    func(workspaceapi.FileSystem, context.Context) (iterator.Iterator[string], error)
	pid                  workspaceapi.Pid
	ctx                  context.Context
	cancelCtx            func()
	waitChan             chan error
	height               int
	list                 search.List
	background           tui.Component
	listHandler          tui.Handler
	killed               bool
	useWorkspaceFallback bool
	cancelScan           func()

	history search.History

	// scanDone is closed after the initial scanData goroutine finishes.
	// Used by tests to wait for the workspace scan to complete.
	scanDone chan struct{}
}

func (h *fuzzyFinderHandler) execCommand(ctx context.Context, command string) (
	*os.File, *os.File, func(), workspaceapi.Pid, error,
) {
	shell := os.Getenv("SHELL")
	if len(shell) == 0 {
		shell = "sh"
	}
	return h.execCommandWith(ctx, shell, command)
}

// Watch satisfies workspaceapi.Watcher which is employed
// to wait for the underlying command to execute.
func (h *fuzzyFinderHandler) WatchProcess() chan error {
	return h.waitChan
}

// ScanWaiter is implemented by the handler returned from New /
// NewWithListConfig. It exposes a hook that closes once the initial
// scan goroutine completes; tests may type-assert to this interface
// to deterministically wait before driving the handler.
type ScanWaiter interface {
	ScanDone() <-chan struct{}
	// DrainList blocks until any in-flight list consumer goroutine has
	// exited. Combined with a closed ScanDone() this guarantees the
	// list's contents are stable.
	DrainList()
}

// ScanDone returns a channel that is closed when the initial scan
// goroutine completes. See ScanWaiter.
func (h *fuzzyFinderHandler) ScanDone() <-chan struct{} {
	return h.scanDone
}

// DrainList re-invokes the underlying list's Push to force the prior
// consumer goroutine to exit, then closes the new channel so the
// freshly started consumer also returns. After DrainList returns the
// list is guaranteed to be in a stable state.
func (h *fuzzyFinderHandler) DrainList() {
	ch := h.list.Push(context.Background())
	close(ch)
}

func (h *fuzzyFinderHandler) execCommandWith(
	ctx context.Context, shell string, commandStr string,
) (*os.File, *os.File, func(), workspaceapi.Pid, error) {
	cmd := workspaceapi.Cmd{
		Path:    shell,
		Args:    []string{"-c", commandStr},
		Watcher: h,
	}
	stderr, stdout, closeWrites, err := h.setPipes(&cmd)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	pid, err := h.executor.Start(ctx, cmd)
	if err != nil {
		closeWrites()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, nil, nil, 0, fmt.Errorf("failed to create command: %w", err)
	}
	return stderr, stdout, closeWrites, pid, nil
}

func (h *fuzzyFinderHandler) setPipes(
	cmd *workspaceapi.Cmd,
) (stderr, stdout *os.File, closeWrites func(), err error) {
	stdout, stdoutWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stderr, stderrWrite, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutWrite.Close()
		return nil, nil, nil, err
	}
	cmd.Stdout = stdoutWrite
	cmd.Stderr = stderrWrite
	// The write ends must be closed once the child exits: the child
	// only holds duplicates, so EOF never reaches the readers while
	// our copies stay open.
	closeWrites = func() {
		_ = stdoutWrite.Close()
		_ = stderrWrite.Close()
	}
	return stderr, stdout, closeWrites, nil
}

func (h *fuzzyFinderHandler) readCommand(ctx context.Context, datachan chan<- []byte, src io.Reader, cancelScan func()) {
	defer cancelScan()
	defer close(datachan)
	reader := bufio.NewReaderSize(src, readerBufferSize)
	for {
		data, err := reader.ReadBytes('\n')
		if len(data) > 0 {
			select {
			case datachan <- data[:len(data)-1]:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				log.Error(err)
			}
			break
		}
	}
}

func (h *fuzzyFinderHandler) addSearchHistory(searchQuery string) {
	if h.s == nil {
		// Storage not granted; ignoring history feature
		return
	}

	if searchQuery == "" {
		log.Trace("skipping persisting of empty query")
		return
	}

	err := h.history.Add(searchQuery)
	if err != nil {
		log.Errorf("error adding search history: %v", err)
	} else {
		log.Tracef("added %q to query history", searchQuery)
	}
}

func (h *fuzzyFinderHandler) open(resource workspaceapi.URI) (browserapi.Handler, error) {
	return h.f.Open(resource)
}

func (h *fuzzyFinderHandler) setContent(
	resource workspaceapi.URI, b browserapi.Handler, pos term.Coordinates,
) error {
	err := h.wm.SetWindowContent(h.invokeWindow, b)
	if err != nil && !errors.Is(err, browserapi.ErrTabNotFree) {
		return err
	}

	hed, err := h.ed.Editor(resource)
	if err != nil {
		return err
	}

	return h.ed.SetCursor(hed, pos)
}

func (h *fuzzyFinderHandler) notifyError(msg string, args ...any) error {
	// allow browser messenger permission to be denied
	if h.m == nil {
		return nil
	}

	_, err := h.m.Notify(browserapi.LevelError, msg, args...)
	return err
}

func (h *fuzzyFinderHandler) openResource(searchQuery, data string) {
	resource, pos, ok := h.getResource(h.fs, data)
	if !ok {
		_ = h.notifyError("line does not conform to file:location format")
		return
	}
	if resource == (workspaceapi.URI{}) {
		log.Warnf("trying to open a line with a parse error")
		return
	}
	handler, err := h.open(resource)
	if err != nil {
		merr := h.notifyError("Open: %v", err)
		if merr != nil {
			log.Errorf("error setting message: %v", merr)
		}
		log.Errorf("error opening new resource: %v", err)
		return
	}

	err = h.setContent(resource, handler, pos)
	if err != nil {
		log.Errorf("error SetContent: %v", err)
		return
	}
}

func (h *fuzzyFinderHandler) doScanDataViaWorkspaceAPI(
	ctx context.Context, datachan chan<- []byte,
) (err error) {
	defer close(datachan)
	log.Debugf("using workspace API to get resource iterator")

	if h.ignoreMatcher != nil {
		ctx = walkdir.WithContextFilter(ctx, h.ignoreMatcher)
	}
	it, err := h.workspaceFallback(h.fs, ctx)
	if err != nil {
		return fmt.Errorf("workspace API fallback: %v", err)
	}
	defer func() {
		err = errors.Join(err, it.Close())
	}()

	for {
		resource, ok := it.Next(ctx)
		if !ok {
			break
		}
		select {
		case datachan <- []byte(resource):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return it.Err()
}

func (h *fuzzyFinderHandler) scanDataViaWorkspaceAPI(
	ctx context.Context, datachan chan<- []byte, cancelScan func(),
) {
	defer cancelScan()
	err := h.doScanDataViaWorkspaceAPI(ctx, datachan)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Errorf("scan data: %v", err)

		merr := h.notifyError("failed to scan: %v", err)
		if merr != nil {
			log.Errorf("error setting message: %v", merr)
		}
	}
}

func (h *fuzzyFinderHandler) scanData() {
	start := time.Now()
	defer close(h.scanDone)
	defer func() {
		if log.IsLevelEnabled(log.DebugLevel) {
			log.Debugf("Done iterating over data in %s", time.Since(start))
		}
	}()

	ctx := h.ctx
	ctx, cancelScan := context.WithCancel(ctx)

	h.mu.Lock()
	h.cancelScan = cancelScan
	datachan := h.list.Push(ctx)
	h.mu.Unlock()

	if h.useWorkspaceFallback {
		h.scanDataViaWorkspaceAPI(ctx, datachan, cancelScan)
		return
	}

	log.Debugf("using resource list command: %s", h.cmdStr)

	stderr, stdout, closeWrites, exec, err := h.execCommand(ctx, h.cmdStr)
	if err != nil {
		log.Debugf("fallback to scan data via workspace API: %v", err)
		h.scanDataViaWorkspaceAPI(ctx, datachan, cancelScan)
		return
	}

	h.mu.Lock()
	h.pid = exec
	h.mu.Unlock()

	readerDone := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(readerDone)
		h.readCommand(ctx, datachan, stdout, cancelScan)
	})
	go debug.CapturePanicReport(func() {
		data, err := io.ReadAll(stderr)
		if err != nil {
			log.Errorf("failed to read from stderr: %v", err)
		}
		log.Debugf("stderr: %s", string(data))
	})

	log.Debugf("waiting for command to be done")
	err = <-h.waitChan
	log.Debugf("command is done: %v", err)

	// The child exited, but its final output may still be buffered
	// in the pipe. Close our write ends so the reader sees EOF after
	// the tail, then join it: a closed scanDone promises that every
	// line has been handed to the list consumer (see ScanWaiter).
	closeWrites()
	<-readerDone

	h.mu.Lock()
	defer h.mu.Unlock()

	killed := h.killed
	h.pid = 0

	if !killed && err != nil {
		merr := h.notifyError("failed to execute '%s': %v", h.cmdStr, err)
		if merr != nil {
			log.Errorf("error setting message: %v", merr)
		}
	}

}

func buildListConfig(c config.Config, interrupter term.Interrupter) search.ListConfig {
	caseSensitive, err := c.GetBool("case_sensitive")
	if err != nil {
		if err != config.ErrNotFound {
			log.Errorf("failed to load 'case_sensitive' from config: %v", err)
		}
		caseSensitive = true
	}
	algoStr, err := c.GetString("algo")
	if err != nil && err != config.ErrNotFound {
		log.Errorf("failed to load 'algo' from config: %v", err)
	}
	algo := search.FuzzyMatch
	switch algoStr {
	case "equal":
		algo = search.EqualMatch
	case "contains":
		algo = search.ContainsMatch
	}

	cfg := search.ListConfig{
		Algo: algo,
		Interrupter: term.FuncInterrupter(func(ctx context.Context) error {
			err := interrupter.Interrupt(ctx)
			if err != nil {
				log.Errorf("interrupt: %v", err)
			}
			return err
		}),
		CaseSensitive: caseSensitive,
	}

	matchedTextAttr, err := config.GetAttributes(c, "matched_text_attr")
	if err != nil && err != config.ErrNotFound {
		log.Errorf("failed to load 'matched_text_attr' from config: %v", err)
	} else if err == nil {
		log.Tracef("loaded 'matched_text_attr' from config: %v", matchedTextAttr)
		cfg.MatchedTextAttr = &matchedTextAttr
	}

	countAttr, err := config.GetAttributes(c, "count_attr")
	if err != nil && err != config.ErrNotFound {
		log.Errorf("failed to load 'count_attr' from config: %v", err)
	} else if err == nil {
		log.Tracef("loaded 'count_attr' from config: %v", countAttr)
		cfg.CountAttr = &countAttr
	}

	textAttr, err := config.GetAttributes(c, "element_attr")
	if err != nil && err != config.ErrNotFound {
		log.Errorf("failed to load 'element_attr' from config: %v", err)
	} else if err == nil {
		log.Tracef("loaded 'element_attr' from config: %v", textAttr)
		cfg.ElementAttr = &textAttr
	}

	focusAttr, err := config.GetAttributes(c, "focus_element_attr")
	if err != nil && err != config.ErrNotFound {
		log.Errorf("failed to load 'focus_element_attr' from config: %v", err)
	} else if err == nil {
		log.Tracef("loaded 'focus_element_attr' from config: %v", focusAttr)
		cfg.FocusElementAttr = &focusAttr
	}

	return cfg
}

func (h *fuzzyFinderHandler) Resize(width, height int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.height = height
	h.background.Resize(width, height)
}

func (h *fuzzyFinderHandler) Draw(w term.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.background.Draw(w)
}

func (h *fuzzyFinderHandler) writeLastSearchQuery() {
	search := h.history.Next()
	if search == "" {
		log.Debugf("no search queries stored")
		return
	}
	h.list.Buffer().Reset()
	h.list.Buffer().WriteString(search)
}

func (h *fuzzyFinderHandler) Redispatch(ctx context.Context, cmd textapi.Command) error {
	h.writeLastSearchQuery()
	return nil
}

func (h *fuzzyFinderHandler) Handle(ev term.Event) (exit, handled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if ev.Type != term.EventKey {
		return
	}

	comb := ev.KeyComb()
	if comb == h.historyKey {
		h.writeLastSearchQuery()
		handled = true
		return
	}

	if comb.Ch == 'c' && comb.Mod == term.ModCtrl {
		h.killed = true
		if h.cancelScan != nil {
			h.cancelScan()
		}
	}

	exit, handled = h.listHandler.Handle(ev)

	log.Tracef("fuzzyFinderHandler.Handle(%#v): %v", ev, handled)

	return
}

func (h *fuzzyFinderHandler) Selection() (string, bool) {
	return h.listHandler.Selection()
}

func (h *fuzzyFinderHandler) Cursor() (
	pos term.Coordinates, style term.CursorStyle, show bool,
) {
	return h.listHandler.Cursor()
}

func (h *fuzzyFinderHandler) Close() (ret error) {
	h.cancelCtx()

	h.mu.Lock()
	defer h.mu.Unlock()

	log.Tracef("fuzzyFinderHandler.Close(): %#v", h.pid)

	h.killed = true
	if h.cancelScan != nil {
		h.cancelScan()
	}
	if err := h.list.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}
	return ret
}
