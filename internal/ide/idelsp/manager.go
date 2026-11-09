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

package idelsp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/retry"
	"unstable.build/rune/internal/debug"
)

// ErrNoServer is returned when no language server is
// available for the requested file.
var ErrNoServer = errors.New("no language server")

// serverKey identifies an initialized language server by both its
// language id and the root URI it is rooted at. Keying servers by
// {languageID, rootURI} lets several servers of the same language
// coexist for distinct project roots inside one workspace (e.g. a
// workspace-root Python project plus a nested one).
type serverKey struct {
	languageID string
	rootURI    string
}

// serverID identifies one backend of a serverKey, a slot that outlives
// the restarts of its process.
type serverID struct {
	key  serverKey
	name string
}

// initSpec is what Initialize was asked to bring up for a serverKey,
// kept so the server can be brought up again on request.
type initSpec struct {
	cfg        langConfig
	alternates map[string]string
	params     semanticapi.InitializeParams
}

// ServerState is the lifecycle state of a language server.
type ServerState string

const (
	// ServerStarting means the process is being spawned and initialized.
	ServerStarting ServerState = "starting"
	// ServerRunning means the server completed the initialize handshake.
	ServerRunning ServerState = "running"
	// ServerRestarting means the process exited unexpectedly and the
	// server is being brought back up.
	ServerRestarting ServerState = "restarting"
	// ServerFailed means the server could not be brought up.
	ServerFailed ServerState = "failed"
	// ServerStopped means the server was stopped on request, or its
	// process exited cleanly on its own and was not restarted.
	ServerStopped ServerState = "stopped"
)

// ServerStatus is a snapshot of one language server: a backend serving
// a language at a project root, which outlives the restarts of its
// process.
type ServerStatus struct {
	// Language is the language id the server was initialized for.
	Language string
	// Name identifies the backend. It is the language id unless the
	// language is served by several backends, where it is the base name
	// of the backend's command.
	Name    string
	RootURI string
	// Path, Args and Env are what the server process runs with.
	Path  string
	Args  []string
	Env   []string
	State ServerState
	// Pid is the current or last process of the server on the
	// workspace host, 0 if none was started. A failed or stopped server
	// keeps its last pid so the output of that process can be found.
	Pid workspaceapi.Pid
	// Started is when the current or last process was started.
	Started time.Time
	// Restarts counts how many times the server was brought up again
	// since it was initialized.
	Restarts int
	// LastError is the most recent failure: why the server could not
	// start, or why its process last exited. It is nil if none.
	LastError error
	// Params is the initialize request sent to the server, as JSON.
	Params json.RawMessage
	// Result is the initialize response of the server, as JSON. It is
	// nil until the handshake of the current process completes.
	Result json.RawMessage
}

// errExitedCleanly is the LastError of a server whose process exited
// with status 0 without being asked to.
var errExitedCleanly = errors.New("process exited with status 0")

// rootContains reports whether the file URI fileURI lives inside the
// root URI rootURI. Both are file:// URIs as produced by convertURI.
// A root contains itself.
func rootContains(rootURI, fileURI string) bool {
	if rootURI == fileURI {
		return true
	}
	prefix := rootURI
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return strings.HasPrefix(fileURI, prefix)
}

// Callback extends the SDK's LSPCallback with methods for tracking
// document version changes and waiting for the server to process them.
// This allows pull-diagnostics to wait until the server has processed
// a recently sent didChange before issuing the request.
type Callback interface {
	semanticapi.LSPCallback
	// FileDidChange reports that a file changed and how the next
	// WaitFileProcessed should wait for the server to reconcile it.
	// version is the editor's document version, open reports whether
	// the file is open in the editor, and oob reports whether the
	// change is out-of-band (a file watcher event or a
	// workspace/didChangeWatchedFiles triggered by a tool such as
	// apply_patch) rather than a versioned editor edit. An
	// out-of-band change to an open file makes the wait block for a
	// strictly newer version (using version as the floor), while one
	// to a closed file waits for the next diagnostics push.
	FileDidChange(uri string, version int32, open, oob bool)
	// InvalidateAllPending marks every URI tracked by the callback
	// as having a pending unversioned change, so that the next
	// WaitFileProcessed call for any of those URIs blocks until a
	// fresh publishDiagnostics push arrives. This is used when an
	// out-of-band workspace event (e.g. a file deletion) can
	// invalidate diagnostics for unrelated files in the same
	// package, and we want to avoid returning a stale snapshot from
	// the LSP cache.
	InvalidateAllPending()
	WaitFileProcessed(ctx context.Context, uri string) error
}

// Config provides optional configuration for a Manager.
type Config struct {
	Callback           Callback
	MaxRetries         uint
	InitializeTimeout  time.Duration
	CloseTimeout       time.Duration
	EventHandleTimeout time.Duration
	NoInitializeServer bool
	WorkDoneProgress   bool
	// PullDiagnosticsDebounce is the quiet period a document must stay
	// idle before the pull-diagnostics bridge issues a
	// textDocument/diagnostic request for it.
	PullDiagnosticsDebounce time.Duration
	// ScheduleNextTick hops onto the host event loop. When nil,
	// notifications fire directly from background goroutines, which
	// races workspaceManagerHandler.focus reads in notis.inFocus.
	ScheduleNextTick func(func()) bool
	// Parser enables a tree-sitter fallback for Definition,
	// References and WorkspaceSymbol when no language server is
	// available for a file's language.
	Parser syntaxapi.Parser
	// IndexedSymbols reports that Parser is backed by a symbol
	// index, making workspace-wide symbol enumeration cheap enough
	// for WorkspaceSymbol and unqualified lookups.
	IndexedSymbols bool
}

// Manager is a multi-language LSP server manager.
// It implements semanticapi.LSP and textapi.EventHandler.
type Manager struct {
	cfg           Config
	evs           chan textapi.Event
	mu            sync.Mutex
	tokenSeq      int64
	rootURI       string
	fileSystem    schemeapi.FileSystem
	executor      schemeapi.Executor
	notifications browserapi.Notifications
	pkgManager    PkgManager
	callback      Callback
	maxRetries    uint
	servers       map[serverKey]server
	specs         map[serverKey]initSpec
	statuses      map[serverID]ServerStatus
	files         map[string]*file
	pendingOpens  map[string]textapi.Event
	pullTimers    map[string]*time.Timer
	fallback      lspFallback
	ctx           context.Context
	cancel        context.CancelFunc
	log           *slog.Logger
}

// tokenFor returns existing if non-nil, or generates a new
// integer ProgressToken when WorkDoneProgress is enabled.
func (m *Manager) tokenFor(
	existing *semanticapi.ProgressToken,
) *semanticapi.ProgressToken {
	if existing != nil || !m.cfg.WorkDoneProgress {
		return existing
	}
	id := int(atomic.AddInt64(&m.tokenSeq, 1))
	return &semanticapi.ProgressToken{
		IntegerValue: id, IsInteger: true,
	}
}

var (
	_ semanticapi.LSP      = (*Manager)(nil)
	_ textapi.EventHandler = (*Manager)(nil)
)

// New creates a new Manager with the given dependencies and configuration.
func New(
	uri workspaceapi.URI, fileSystem schemeapi.FileSystem,
	executor schemeapi.Executor,
	pkgManager PkgManager, notifications browserapi.Notifications,
	opener browserapi.ResourceOpener,
	cfg Config,
) *Manager {
	const eventsBufferSize = 5

	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.InitializeTimeout == 0 {
		cfg.InitializeTimeout = 3 * time.Second
	}
	if cfg.CloseTimeout == 0 {
		cfg.CloseTimeout = 3 * time.Second
	}
	if cfg.EventHandleTimeout == 0 {
		cfg.EventHandleTimeout = 1 * time.Second
	}
	if cfg.PullDiagnosticsDebounce == 0 {
		cfg.PullDiagnosticsDebounce = defaultPullDiagnosticsDebounce
	}
	ctx, cancel := context.WithCancel(context.Background())
	ret := &Manager{
		cfg:           cfg,
		log:           slog.With("struct", "idelsp.Manager", "workspace", convertURI(uri)),
		rootURI:       convertURI(uri),
		fileSystem:    fileSystem,
		executor:      executor,
		pkgManager:    pkgManager,
		notifications: notifications,
		callback:      cfg.Callback,
		maxRetries:    cfg.MaxRetries,
		servers:       make(map[serverKey]server),
		specs:         make(map[serverKey]initSpec),
		statuses:      make(map[serverID]ServerStatus),
		files:         make(map[string]*file),
		pendingOpens:  make(map[string]textapi.Event),
		pullTimers:    make(map[string]*time.Timer),
		ctx:           ctx,
		cancel:        cancel,
		evs:           make(chan textapi.Event, eventsBufferSize),
	}
	go debug.CapturePanicReport(func() {
		ret.handleEvs()
	})
	// Decorate the callback so server-driven workspace refresh requests
	// reach the Manager (see refreshingCallback). A nil callback stays
	// nil: such Managers never start servers that could send refreshes.
	if cfg.Callback != nil {
		ret.callback = &refreshingCallback{Callback: cfg.Callback, m: ret}
	}
	ret.fallback = noFallback{}
	if cfg.Parser != nil {
		ret.fallback = newSyntaxFallback(
			cfg.Parser, ret.readFileContent, cfg.IndexedSymbols,
		)
	}
	return ret
}

// Close shuts down all active language servers.
func (m *Manager) Close() error {
	defer m.cancel()
	m.cancelAllPullDiagnostics()
	m.mu.Lock()
	servers := make([]server, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.mu.Unlock()

	var errs []error
	ctx, cancel := context.WithTimeout(
		context.Background(), m.cfg.CloseTimeout,
	)
	defer cancel()
	for _, s := range servers {
		if err := s.stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Handle implements textapi.EventHandler.
func (m *Manager) Handle(
	_ context.Context, ev textapi.Event,
) bool {
	m.log.Debug("received event", "type", ev.Type, "file", ev.URI)
	select {
	case m.evs <- ev:
		return false
	case <-m.ctx.Done():
		return true
	}
}

func (m *Manager) handleEvs() {
	// ensure Handle doesn't block if this goroutine panics
	// and no goroutine is listening on m.evs.
	defer m.cancel()
	for {
		select {
		case <-m.ctx.Done():
			return
		case ev := <-m.evs:
			err := m.handle(ev)
			if err != nil {
				m.log.Error("handle event", "error", err)
			}
		}
	}
}

func (m *Manager) handle(ev textapi.Event) error {
	uri := convertURI(ev.URI)

	ctx, cancel := context.WithTimeout(context.Background(),
		m.cfg.EventHandleTimeout)
	defer cancel()

	switch ev.Type {
	case textapi.EventTypeOpen:
		m.log.Debug("process open", "file", uri, "size", len(ev.Content))
		srv, err := m.ensureServer(ctx, ev.URI)
		if err != nil {
			if m.cfg.NoInitializeServer {
				m.mu.Lock()
				m.pendingOpens[uri] = ev
				m.mu.Unlock()
				m.log.Debug("tracked pending open for server init", "file", uri)
				return nil
			}
			return err
		}
		// ensureServer may have consumed most of the
		// EventHandleTimeout while starting the server (it
		// uses its own InitializeTimeout internally). Reset
		// the deadline so the didOpen notify gets a fresh
		// timeout.
		cancel()
		openCtx, openCancel := context.WithTimeout(
			context.Background(), m.cfg.EventHandleTimeout)
		defer openCancel()
		f, err := m.ensureFile(ev.URI, ev.Content, srv.key())
		if err != nil {
			return err
		}
		err = srv.notify(openCtx, "textDocument/didOpen",
			semanticapi.DidOpenTextDocumentParams{
				TextDocument: semanticapi.TextDocumentItem{
					URI:        uri,
					LanguageID: f.languageID,
					Version:    f.version,
					Text:       f.content,
				},
			})
		if err == nil {
			m.schedulePullDiagnostics(uri)
		}
		return err

	case textapi.EventTypeClose:
		m.cancelPullDiagnostics(uri)
		srv, err := m.serverForURI(uri)
		if err != nil {
			if m.cfg.NoInitializeServer {
				m.mu.Lock()
				delete(m.pendingOpens, uri)
				delete(m.files, uri)
				m.mu.Unlock()
				return nil
			}
			return err
		}
		notifyErr := srv.notify(ctx, "textDocument/didClose",
			semanticapi.DidCloseTextDocumentParams{
				TextDocument: semanticapi.TextDocumentIdentifier{
					URI: uri,
				},
			})
		// Drop the cached entry regardless of notify outcome: once
		// we send didClose (or fail trying), the server side is no
		// longer guaranteed to track this URI and our cached copy
		// would only leak memory until process exit.
		m.mu.Lock()
		delete(m.files, uri)
		m.mu.Unlock()
		return notifyErr

	case textapi.EventTypeEdit:
		srv, err := m.serverForURI(uri)
		if err != nil {
			if m.cfg.NoInitializeServer && m.hasPendingOpen(uri) {
				return nil
			}
			return err
		}
		uriStr := convertURI(ev.URI)
		f, ok := m.getFile(uriStr)
		if !ok {
			return errors.New("received edit event for non-open file")
		}
		m.mu.Lock()
		m.files[f.docID.URI].version++
		version := m.files[f.docID.URI].version
		m.mu.Unlock()
		err = srv.notify(ctx, "textDocument/didChange",
			semanticapi.DidChangeTextDocumentParams{
				TextDocument: semanticapi.VersionedTextDocumentIdentifier{
					Version: version,
					URI:     uri,
				},
				ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
					{
						Range: &semanticapi.Range{
							Start: semanticapi.Position{
								Line:      uint32(ev.Start.Y),
								Character: uint32(ev.Start.X),
							},
							End: semanticapi.Position{
								Line:      uint32(ev.End.Y),
								Character: uint32(ev.End.X),
							},
						},
						Text: ev.Content,
					},
				},
			})
		if err == nil {
			m.callback.FileDidChange(uri, version, true, false)
			m.schedulePullDiagnostics(uri)
		}
		return err

	case textapi.EventTypeFlush:
		srv, err := m.serverForURI(uri)
		if err != nil {
			if m.cfg.NoInitializeServer && m.refreshPendingOpen(uri, ev.Content) {
				return nil
			}
			return err
		}
		f, err := m.ensureFile(ev.URI, ev.Content, srv.key())
		if err != nil {
			return err
		}
		err = srv.notify(ctx, "textDocument/didSave",
			semanticapi.DidSaveTextDocumentParams{
				TextDocument: semanticapi.TextDocumentIdentifier{
					URI: uri,
				},
				Text: f.content,
			})
		if err == nil {
			m.schedulePullDiagnostics(uri)
		}
		return err

	case textapi.EventTypeCreate:
		m.fileDidChangeOOB(uri)
		return m.broadcastNotify(ctx,
			workspaceapi.URI{}, "workspace/didChangeWatchedFiles",
			semanticapi.DidChangeWatchedFilesParams{
				Changes: []semanticapi.FileEvent{
					{
						URI:  uri,
						Type: semanticapi.FileChangeTypeCreated,
					},
				},
			})

	case textapi.EventTypeChange:
		m.fileDidChangeOOB(uri)
		return m.broadcastNotify(ctx,
			workspaceapi.URI{}, "workspace/didChangeWatchedFiles",
			semanticapi.DidChangeWatchedFilesParams{
				Changes: []semanticapi.FileEvent{
					{
						URI:  uri,
						Type: semanticapi.FileChangeTypeChanged,
					},
				},
			})

	case textapi.EventTypeRemove:
		m.fileDidChangeOOB(uri)
		return m.broadcastNotify(ctx,
			workspaceapi.URI{}, "workspace/didChangeWatchedFiles",
			semanticapi.DidChangeWatchedFilesParams{
				Changes: []semanticapi.FileEvent{
					{
						URI:  uri,
						Type: semanticapi.FileChangeTypeDeleted,
					},
				},
			})

	case textapi.EventTypeRename:
		// textapi.EventTypeRename doesn't contain the old/new path mapping
		// so we simply tell the LSP server that the file has changed
		// in which case it will try to read it and succeed (rename target)
		// or fail (rename source), and apply the right changes internally.
		m.fileDidChangeOOB(uri)
		return m.broadcastNotify(ctx,
			workspaceapi.URI{}, "workspace/didChangeWatchedFiles",
			semanticapi.DidChangeWatchedFilesParams{
				Changes: []semanticapi.FileEvent{
					{
						URI:  uri,
						Type: semanticapi.FileChangeTypeChanged,
					},
				},
			})
	default:
		return fmt.Errorf("extraneous event %v", ev.Type)
	}
}

// Pending-open snapshots are refreshed only from events that carry the
// full buffer content (open, flush). Edit deltas are deliberately
// swallowed without replaying them: doing so would replicate the
// editor's buffer semantics here, and any divergence would corrupt the
// didOpen base the server pins once it initializes.
func (m *Manager) hasPendingOpen(uri string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.pendingOpens[uri]
	return ok
}

func (m *Manager) refreshPendingOpen(uri, content string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending, ok := m.pendingOpens[uri]
	if !ok {
		return false
	}
	pending.Content = content
	m.pendingOpens[uri] = pending
	return true
}

func (m *Manager) fileDidChangeOOB(uri string) {
	f, open := m.getFile(uri)
	var version int32
	if open {
		version = f.version
	}
	m.callback.FileDidChange(uri, version, open, true)
}

const (
	// defaultPullDiagnosticsDebounce is the quiet period the pull
	// bridge waits for before requesting diagnostics. rust-analyzer
	// recomputes the whole crate per pull, so a request per keystroke
	// would be prohibitively expensive.
	defaultPullDiagnosticsDebounce = 200 * time.Millisecond
	// pullDiagnosticsTimeout bounds a single bridged pull so a wedged
	// server cannot leak the request goroutine.
	pullDiagnosticsTimeout = 30 * time.Second
)

// schedulePullDiagnostics arms (or re-arms) the per-URI debounce timer
// that drives the pull-diagnostics bridge. rust-analyzer stopped
// computing native semantic diagnostics on the push path once build
// scripts and proc macros are enabled, regardless of whether the buffer
// is saved. Flycheck output is a different diagnostic set from a
// different tool and does not substitute for it, so for pull-capable
// servers the only way to get native semantic diagnostics is to pull
// them and feed the reports back into the push pipeline.
func (m *Manager) schedulePullDiagnostics(uri string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return
	}
	if t, ok := m.pullTimers[uri]; ok {
		t.Stop()
	}
	m.pullTimers[uri] = time.AfterFunc(m.cfg.PullDiagnosticsDebounce, func() {
		debug.CapturePanicReport(func() { m.pullDiagnostics(uri) })
	})
}

// cancelPullDiagnostics drops a pending debounce timer, so a pull is
// never issued for a document the server no longer tracks.
func (m *Manager) cancelPullDiagnostics(uri string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.pullTimers[uri]; ok {
		t.Stop()
		delete(m.pullTimers, uri)
	}
}

func (m *Manager) cancelAllPullDiagnostics() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for uri, t := range m.pullTimers {
		t.Stop()
		delete(m.pullTimers, uri)
	}
}

// pullDiagnostics issues one textDocument/diagnostic request and
// republishes the report through the callback. It deliberately bypasses
// Manager.Diagnostic: that entry point waits for diagnostics to settle,
// and the publishes it would wait on are the very ones this bridge
// produces.
func (m *Manager) pullDiagnostics(uri string) {
	if _, open := m.fileVersion(uri); !open {
		return
	}
	srv, err := m.serverForURI(uri)
	if err != nil || !srv.supportsPullDiagnostics() {
		return
	}

	ctx, cancel := context.WithTimeout(m.ctx, pullDiagnosticsTimeout)
	defer cancel()
	report, err := srv.pullDiagnostics(ctx, semanticapi.DocumentDiagnosticParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		m.log.Debug("pull diagnostics", "file", uri, "error", err)
		return
	}
	// An unchanged report carries no items; publishing it would clear
	// the diagnostics the previous full report established.
	if report.Kind != "full" {
		return
	}

	// Re-read the version after the round trip: edits made while the
	// pull was in flight must not be reported as processed by this
	// publish, and a document closed meanwhile must not be published at
	// all.
	version, open := m.fileVersion(uri)
	if !open {
		return
	}

	// The ":pull" suffix gives the bridge its own slot in the callback's
	// per-server diagnostics cache, so bridged reports merge with the
	// server's own pushes (flycheck) instead of overwriting them.
	ctx = ContextWithMetadata(ctx, Metadata{
		ServerName: srv.name() + ":pull",
		RootURI:    srv.key().rootURI,
	})
	err = m.callback.PublishDiagnostics(ctx, semanticapi.PublishDiagnosticsParams{
		URI:         uri,
		Version:     version,
		Diagnostics: report.Items,
	})
	if err != nil {
		m.log.Warn("publish pulled diagnostics", "file", uri, "error", err)
	}
}

func (m *Manager) fileVersion(uri string) (int32, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[uri]
	if !ok {
		return 0, false
	}
	return f.version, true
}

// RefreshDiagnostics re-pulls every open file owned by a pull-capable
// server. It is driven by workspace/diagnostic/refresh through the
// Manager's callback decorator: rust-analyzer sends the request when
// flycheck finishes, which is exactly when its cached reports become
// stale.
func (m *Manager) RefreshDiagnostics(_ context.Context) error {
	m.mu.Lock()
	uris := make([]string, 0, len(m.files))
	for uri := range m.files {
		uris = append(uris, uri)
	}
	m.mu.Unlock()
	for _, uri := range uris {
		m.schedulePullDiagnostics(uri)
	}
	return nil
}

// refreshingCallback decorates the configured Callback with the
// Manager's reaction to workspace refresh requests. The Manager
// installs the callback into every server it starts, so decorating it
// here lets server-driven refreshes reach the Manager without the
// callback implementation needing a reference back to it.
type refreshingCallback struct {
	Callback
	m *Manager
}

// DiagnosticRefresh schedules pulls for the Manager's open files, then
// forwards the request to the decorated callback.
func (c *refreshingCallback) DiagnosticRefresh(ctx context.Context) error {
	if err := c.m.RefreshDiagnostics(ctx); err != nil {
		return err
	}
	return c.Callback.DiagnosticRefresh(ctx)
}

func (m *Manager) getFile(uriStr string) (*file, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[uriStr]
	return f, ok
}

// readFileContent reads a workspace file from the filesystem and
// normalizes its trailing newline. It does not touch m.files.
func (m *Manager) readFileContent(path string) (string, error) {
	f, err := m.fileSystem.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for reading: %w", err)
	}
	defer f.Close() // nolint:errcheck
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("read workspace file: %w", err)
	}
	content := string(data)
	if len(content) == 0 || content[len(content)-1] != '\n' {
		// LSP servers expect a trailing EOL.
		content += "\n"
	}
	return content, nil
}

// withEnsuredOpen runs fn against the server owning uri, guaranteeing
// the document is open on that server for the duration of the call.
// Servers such as ty reject position-based requests (definition,
// hover, ...) for documents they do not track. When uri is already
// open in the editor (present in m.files) we run fn directly.
// Otherwise we transiently didOpen the file, run fn, then didClose,
// without caching the document in m.files.
func (m *Manager) withEnsuredOpen(
	ctx context.Context, uri string, fn func(srv server) error,
) error {
	srv, err := m.serverForURI(uri)
	if err != nil {
		return err
	}
	if _, open := m.getFile(uri); open {
		return fn(srv)
	}

	lang, err := languageForFilename(uri)
	if err != nil {
		return err
	}
	content, err := m.readFileContent(uriToPath(uri))
	if err != nil {
		return err
	}
	if err := srv.notify(ctx, "textDocument/didOpen",
		semanticapi.DidOpenTextDocumentParams{
			TextDocument: semanticapi.TextDocumentItem{
				URI:        uri,
				LanguageID: lang.documentID,
				Version:    firstFileVersion,
				Text:       content,
			},
		}); err != nil {
		return err
	}
	// A concurrent editor open could also open this file, in which
	// case both paths issue didOpen/didClose. That is harmless, so we
	// avoid holding m.mu across the LSP round trip.
	defer func() {
		if cerr := srv.notify(ctx, "textDocument/didClose",
			semanticapi.DidCloseTextDocumentParams{
				TextDocument: semanticapi.TextDocumentIdentifier{
					URI: uri,
				},
			}); cerr != nil {
			m.log.Debug("transient didClose", "error", cerr, "file", uri)
		}
	}()
	return fn(srv)
}

func (m *Manager) ensureFile(
	uri workspaceapi.URI,
	content string, key serverKey,
) (*file, error) {
	uriStr := convertURI(uri)
	if key.languageID == "" || uri == (workspaceapi.URI{}) {
		panic("empty params for ensuring available file")
	}
	f, ok := m.getFile(uriStr)
	if ok {
		if content != "" {
			m.files[uriStr].content = content
			m.files[uriStr].version++
		}
		return f, nil
	}
	lang, err := languageForFile(uri)
	if err != nil {
		return nil, err
	}
	if content == "" {
		read, err := m.readFileContent(uri.Path())
		if err != nil {
			return nil, err
		}
		content = read
	} else if len(content) == 0 || content[len(content)-1] != '\n' {
		// editor trims last EOL but LSP servers expect it
		content += "\n"
	}
	f = newFile(uri, content, lang.documentID, key)
	m.mu.Lock()
	m.files[uriStr] = f
	m.mu.Unlock()

	return f, nil
}

func (m *Manager) ensureServer(
	_ context.Context, filename workspaceapi.URI,
) (server, error) {
	lang, err := languageForFile(filename)
	if err != nil {
		return nil, err
	}

	if srv, err := m.serverForURI(convertURI(filename)); err == nil {
		return srv, nil
	}

	if m.cfg.NoInitializeServer {
		return nil, fmt.Errorf("server not initialized and auto-initialize config is false")
	}

	ctx, cancel := context.WithTimeout(m.ctx, m.cfg.InitializeTimeout)
	defer cancel()
	key := serverKey{languageID: lang.id, rootURI: m.rootURI}
	spec := initSpec{cfg: lang, params: autoInitParams(m.rootURI)}
	m.mu.Lock()
	m.specs[key] = spec
	m.mu.Unlock()
	return m.bringUp(ctx, key, spec)
}

// bringUp starts the server or servers spec describes for key and
// registers them.
func (m *Manager) bringUp(
	ctx context.Context, key serverKey, spec initSpec,
) (server, error) {
	if len(spec.alternates) == 0 {
		srv, err := m.initializeServer(ctx, spec.cfg, key, spec.params)
		if err != nil {
			return nil, err
		}
		return srv, nil
	}
	srv, err := m.initializeMultiServer(ctx, spec.cfg, key, spec.alternates, spec.params)
	if err != nil {
		return nil, err
	}
	return srv, nil
}

func (m *Manager) initializeServer(
	ctx context.Context, lang langConfig, key serverKey,
	params semanticapi.InitializeParams,
) (*langServer, error) {
	if lang.command == "" {
		return nil, errors.New("language configuration with empty command")
	}
	srv := m.buildChild(ctx, lang, lang.id, key.rootURI, params)
	m.trackStarting(srv)
	if err := srv.start(ctx); err != nil {
		m.trackFailed(srv, err)
		return nil, err
	}
	m.trackRunning(srv, false)

	m.mu.Lock()
	m.servers[key] = srv
	m.mu.Unlock()

	m.sendPendingOpens(key, srv)

	go debug.CapturePanicReport(func() {
		m.watchServer(&lang, srv)
	})
	return srv, nil
}

// buildChild resolves the binary for lang and constructs an
// un-started langServer. serverName is the identity reported to the
// callback handler (and used to key diagnostics by source); for a
// single-server language it is the language id, for a multi-server
// child it is the child's command so each backend's diagnostics
// accumulate independently.
func (m *Manager) buildChild(
	ctx context.Context, lang langConfig, serverName string, rootURI string,
	params semanticapi.InitializeParams,
) *langServer {
	var binPath string
	if filepath.IsAbs(lang.command) {
		binPath = lang.command
	} else {
		var err error
		binPath, err = m.findBinary(ctx, &lang)
		if err != nil {
			m.log.Debug(
				"find lsp executable, falling back to using PATH",
				"executable", lang.command, "error", err,
			)
			binPath = lang.command
		}
	}
	srv := newLangServer(
		m.ctx, lang, binPath, m.executor, rootURI,
		newCallbackAdapter(m.callback, serverName, rootURI),
		params,
	)
	srv.serverName = serverName
	return srv
}

// childName derives the server-name identity for a child backend
// from its command string: the base name of the executable
// (e.g. "ty server" -> "ty", "/usr/bin/ruff" -> "ruff").
func childName(command string) string {
	argv := strings.Fields(command)
	if len(argv) == 0 {
		return command
	}
	return filepath.Base(argv[0])
}

// initializeMultiServer builds and starts a multiLangServer for lang.
// The default child serves any method without an explicit route;
// alternates maps an LSP method to a command string, and one extra
// child is spawned per distinct alternate command. Each child is
// supervised independently so a single crash does not tear down the
// others.
func (m *Manager) initializeMultiServer(
	ctx context.Context, lang langConfig, key serverKey,
	alternates map[string]string, params semanticapi.InitializeParams,
) (*multiLangServer, error) {
	if lang.command == "" {
		return nil, errors.New("language configuration with empty command")
	}

	defaultChild := m.buildChild(ctx, lang, childName(lang.command), key.rootURI, params)
	children := []*langServer{defaultChild}
	routes := make(map[string]int)

	// Dedup alternate commands so two methods sharing one command
	// map to a single child.
	cmdIndex := make(map[string]int)
	for method, cmd := range alternates {
		if cmd == "" || cmd == lang.command {
			continue
		}
		idx, ok := cmdIndex[cmd]
		if !ok {
			argv := strings.Split(cmd, " ")
			childCfg := langConfig{
				id: lang.id, command: argv[0], args: argv[1:], env: lang.env,
			}
			child := m.buildChild(ctx, childCfg, childName(cmd), key.rootURI, params)
			children = append(children, child)
			idx = len(children) - 1
			cmdIndex[cmd] = idx
		}
		routes[method] = idx
	}

	mls := &multiLangServer{cfg: lang, routes: routes}
	mls.children = make([]server, len(children))
	for i, c := range children {
		mls.children[i] = c
	}

	for _, c := range children {
		m.trackStarting(c)
	}
	if err := mls.start(ctx); err != nil {
		for _, c := range children {
			m.trackFailed(c, err)
		}
		return nil, err
	}
	for _, c := range children {
		m.trackRunning(c, false)
	}

	m.mu.Lock()
	m.servers[key] = mls
	m.mu.Unlock()

	// Pending opens are drained once through mls so the notify fans
	// out to every child; draining per child would let the first one
	// consume them and leave the rest rejecting later didChanges.
	m.sendPendingOpens(key, mls)
	for _, child := range children {
		watched := child
		go debug.CapturePanicReport(func() {
			m.watchServer(&lang, watched)
		})
	}
	return mls, nil
}

// installRestarted swaps a freshly restarted child into the
// manager's routing for key. When the root is backed by a
// multiLangServer, only the crashed child is replaced (by pointer
// identity) so the other children keep running; otherwise the
// single registered server is replaced wholesale. It installs nothing
// and reports false when old is no longer registered under key, as
// when the server was stopped or replaced while it was restarting.
func (m *Manager) installRestarted(key serverKey, old, restarted *langServer) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch cur := m.servers[key].(type) {
	case *multiLangServer:
		return cur.replaceChild(old, restarted)
	case *langServer:
		if cur != old {
			return false
		}
		m.servers[key] = restarted
		return true
	}
	return false
}

// sendPendingOpens sends didOpen for files that were opened before this
// server existed. Files whose language matches key.languageID are sent
// when their URI is contained in key.rootURI, or when they live outside
// the workspace root entirely (no root can ever contain them, so the
// first same-language server claims them); the rest stay in
// pendingOpens for a future server init.
func (m *Manager) sendPendingOpens(key serverKey, srv server) {
	m.mu.Lock()
	var opens []textapi.Event
	for uri, ev := range m.pendingOpens {
		cfg, err := languageForFilename(uri)
		if err != nil || cfg.id != key.languageID {
			continue
		}
		if rootContains(m.rootURI, uri) && !rootContains(key.rootURI, uri) {
			continue
		}
		opens = append(opens, ev)
		delete(m.pendingOpens, uri)
	}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(
		context.Background(), m.cfg.EventHandleTimeout,
	)
	defer cancel()

	for _, ev := range opens {
		uri := convertURI(ev.URI)
		f, err := m.ensureFile(ev.URI, ev.Content, key)
		if err != nil {
			m.log.Error("send pending open", "error", err, "file", uri)
			continue
		}
		_ = srv.notify(ctx, "textDocument/didOpen",
			semanticapi.DidOpenTextDocumentParams{
				TextDocument: semanticapi.TextDocumentItem{
					URI:        uri,
					LanguageID: f.languageID,
					Version:    f.version,
					Text:       f.content,
				},
			})
	}
}

func (m *Manager) findBinary(
	ctx context.Context, lang *langConfig,
) (string, error) {
	files, err := m.pkgManager.LibDir(
		ctx, lang.id,
	)
	if err != nil {
		return "", fmt.Errorf("lib dir: %w", err)
	}
	defer func() { _ = files.Close() }()

	paths, err := iterator.ToSlice(ctx, files)
	if err != nil {
		return "", fmt.Errorf("lib dir iterator: %w", err)
	}

	for _, file := range paths {
		// The path is on the workspace host, which need not be this
		// machine, so it is handed to the executor as listed.
		if filepath.Base(file) == lang.command {
			return file, nil
		}
	}
	return "", fmt.Errorf(
		"%s not found in any package directory",
		lang.command,
	)
}

func (m *Manager) watchServer(
	lang *langConfig, srv *langServer,
) {
	defer func() {
		err := srv.Close()
		if err != nil {
			m.log.Warn("jsonrpc2 connection close", "error", err)
		}
	}()

	ch := srv.watcher
	if ch == nil {
		return
	}

	select {
	case <-m.ctx.Done():
		return
	case err := <-ch:
		if srv.wasStopped() {
			m.log.Debug("server exited after stop", "language", lang.id, "pid", srv.pid)
			return
		}
		if err == nil {
			m.log.Warn("language server exited on its own",
				srv.logAttrs("error", errExitedCleanly)...)
			m.trackStopped(srv, errExitedCleanly)
			return
		}
		m.trackRestarting(srv, fmt.Errorf("process %d exited: %w", srv.pid, err))
	case <-srv.conn.Done():
		// The jsonrpc2 connection died (e.g. read or write error)
		// while the child process is still running. This leaves the
		// editor unable to communicate even though the server looks
		// alive. Kill the orphaned process and fall through into the
		// restart loop.
		if srv.wasStopped() {
			return
		}
		if err := m.executor.Signal(srv.pid, syscall.SIGKILL); err != nil {
			m.log.Warn("kill orphan lsp process",
				"language", lang.id, "pid", srv.pid, "error", err)
		}
		m.trackRestarting(srv, srv.withExitStatus(errors.New("connection to server lost")))
	}

	strategy := retry.CombinedStrategy(
		retry.LimitStrategy(m.maxRetries),
		retry.ExponentialStrategy(500*time.Millisecond, 5*time.Second),
	)

	var attempts int
	var lastErr error
	retryErr := retry.Retry(m.ctx, strategy,
		func(ctx context.Context) (bool, error) {
			if srv.wasStopped() {
				return false, nil
			}
			ctx, cancel := context.WithTimeout(ctx, m.cfg.InitializeTimeout)
			defer cancel()

			attempts++
			m.log.Info("restarting language server", srv.logAttrs("attempt", attempts)...)
			newSrv := newLangServer(
				m.ctx, srv.cfg, srv.binPath, m.executor, srv.rootURI,
				newCallbackAdapter(m.callback, srv.serverName, srv.rootURI),
				srv.params,
			)
			newSrv.serverName = srv.serverName

			if err := newSrv.start(ctx); err != nil {
				lastErr = err
				m.trackRestartFailed(newSrv, err)
				return true, err
			}
			if !m.installRestarted(srv.key(), srv, newSrv) {
				m.log.Info("language server stopped while restarting",
					newSrv.logAttrs()...)
				m.shutdown(m.ctx, newSrv)
				return false, nil
			}
			m.trackRunning(newSrv, true)

			m.reopenFiles(ctx, newSrv.key(), newSrv)
			go debug.CapturePanicReport(func() {
				m.watchServer(lang, newSrv)
			})
			return false, nil
		})

	if retryErr != nil {
		if lastErr == nil {
			lastErr = retryErr
		}
		m.trackFailed(srv, fmt.Errorf("gave up restarting after %d attempts: %w",
			attempts, lastErr))
		m.notify(browserapi.LevelError,
			"LSP server %s failed after %d retries: %s",
			lang.command, m.maxRetries, retryErr,
		)
	}
}

func (m *Manager) reopenFiles(
	ctx context.Context, key serverKey,
	srv *langServer,
) {
	m.reopen(ctx, srv, m.filesOf(key))
}

func (m *Manager) filesOf(key serverKey) []*file {
	m.mu.Lock()
	defer m.mu.Unlock()
	var files []*file
	for _, file := range m.files {
		if file.serverKey == key {
			files = append(files, file)
		}
	}
	return files
}

func (m *Manager) reopen(ctx context.Context, srv server, files []*file) {
	for _, f := range files {
		_ = srv.notify(ctx, "textDocument/didOpen",
			semanticapi.DidOpenTextDocumentParams{
				TextDocument: semanticapi.TextDocumentItem{
					URI:        f.docID.URI,
					LanguageID: f.languageID,
					Version:    f.version,
					Text:       f.content,
				},
			})
	}
}

// serverForURI routes uri to a language server among the already
// initialized servers of its language, in order of preference:
//  1. the server of the most specific initialized root containing the
//     file;
//  2. the same-language server with the broadest root, for files
//     outside every root (dependency sources, GOROOT, module caches),
//     shortest rootURI with a lexicographic tie-break;
//  3. ErrNoServer when no server of the language exists at all.
//
// serverForURI never brings a server up: a file whose owning project
// root has no server stays unrouted until the owning extension
// initializes it (its pending open flushes through sendPendingOpens).
func (m *Manager) serverForURI(uri string) (server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := languageForFilename(uri)
	if err != nil {
		return nil, err
	}
	var best server
	var bestLen int
	for key, srv := range m.servers {
		if key.languageID != cfg.id {
			continue
		}
		if !rootContains(key.rootURI, uri) {
			continue
		}
		if best == nil || len(key.rootURI) > bestLen {
			best = srv
			bestLen = len(key.rootURI)
		}
	}
	if best != nil {
		return best, nil
	}
	var broadest server
	var broadestRoot string
	for key, srv := range m.servers {
		if key.languageID != cfg.id {
			continue
		}
		if broadest == nil || len(key.rootURI) < len(broadestRoot) ||
			(len(key.rootURI) == len(broadestRoot) && key.rootURI < broadestRoot) {
			broadest = srv
			broadestRoot = key.rootURI
		}
	}
	if broadest == nil {
		return nil, fmt.Errorf("%w: server %s not running%s",
			ErrNoServer, cfg.id, m.notRunningReason(cfg.id, uri))
	}
	return broadest, nil
}

// notRunningReason explains, from the recorded lifecycle, why no server
// of languageID serves uri, preferring the server of the most specific
// root containing uri. It returns "" when nothing was recorded. m.mu
// must be held.
func (m *Manager) notRunningReason(languageID, uri string) string {
	var st ServerStatus
	var found, contains bool
	for id, s := range m.statuses {
		if id.key.languageID != languageID {
			continue
		}
		c := rootContains(id.key.rootURI, uri)
		if !found || (c && !contains) ||
			(c == contains && len(id.key.rootURI) > len(st.RootURI)) {
			st, found, contains = s, true, c
		}
	}
	if !found {
		return ""
	}
	switch st.State {
	case ServerFailed:
		return fmt.Sprintf(": last start failed: %v", st.LastError)
	case ServerStopped:
		if st.LastError != nil {
			return fmt.Sprintf(": stopped: %v", st.LastError)
		}
		return ": stopped"
	case ServerStarting:
		return ": still starting"
	}
	return ""
}

func (m *Manager) allServers() []server {
	m.mu.Lock()
	defer m.mu.Unlock()
	servers := make(
		[]server, 0, len(m.servers),
	)
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	return servers
}

// AnyServerRunning reports whether at least one initialized LSP server
// in this Manager is currently alive. Membership in m.servers already
// implies a completed initialize handshake; the isAlive check further
// excludes servers that have stopped but not yet been removed.
func (m *Manager) AnyServerRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		if s.isAlive() {
			return true
		}
	}
	return false
}

// RootURI returns the file URI of the workspace root, which contains
// the root of every server of this Manager.
func (m *Manager) RootURI() string {
	return m.rootURI
}

// Servers returns the status of every language server initialized in
// this Manager, including those that failed to start or were stopped,
// ordered by language, root and name.
func (m *Manager) Servers() []ServerStatus {
	m.mu.Lock()
	ret := make([]ServerStatus, 0, len(m.statuses))
	for _, st := range m.statuses {
		st.Args = slices.Clone(st.Args)
		st.Env = slices.Clone(st.Env)
		ret = append(ret, st)
	}
	m.mu.Unlock()
	slices.SortFunc(ret, func(a, b ServerStatus) int {
		return cmp.Or(
			strings.Compare(a.Language, b.Language),
			strings.Compare(a.RootURI, b.RootURI),
			strings.Compare(a.Name, b.Name),
		)
	})
	return ret
}

// Stop shuts down the language server of languageID rooted at rootURI.
// It stays down until Start or Restart is called, or its language
// extension initializes it again. It returns ErrNoServer if the server
// is not running.
func (m *Manager) Stop(ctx context.Context, languageID, rootURI string) error {
	key := serverKey{languageID: languageID, rootURI: rootURI}
	m.mu.Lock()
	srv, ok := m.servers[key]
	delete(m.servers, key)
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: server %s not running at %s", ErrNoServer, languageID, rootURI)
	}
	m.shutdown(ctx, srv)
	for _, child := range childServers(srv) {
		m.trackStopped(child, nil)
	}
	return nil
}

// Start brings up the language server of languageID rooted at rootURI
// with the parameters it was last initialized with, and reopens the
// files it served. It fails if the server is running, and returns
// ErrNoServer if it was never initialized.
func (m *Manager) Start(ctx context.Context, languageID, rootURI string) error {
	key := serverKey{languageID: languageID, rootURI: rootURI}
	m.mu.Lock()
	_, running := m.servers[key]
	spec, known := m.specs[key]
	m.mu.Unlock()
	if running {
		return fmt.Errorf("language server %s is already running at %s", languageID, rootURI)
	}
	if !known {
		return fmt.Errorf("%w: server %s was never initialized at %s",
			ErrNoServer, languageID, rootURI)
	}
	// Snapshot before bring-up, which opens pending files itself.
	files := m.filesOf(key)
	srv, err := m.bringUp(ctx, key, spec)
	if err != nil {
		return err
	}
	m.reopen(ctx, srv, files)
	return nil
}

// Restart stops the language server of languageID rooted at rootURI if
// it is running, then starts it as Start does.
func (m *Manager) Restart(ctx context.Context, languageID, rootURI string) error {
	key := serverKey{languageID: languageID, rootURI: rootURI}
	m.mu.Lock()
	srv, running := m.servers[key]
	delete(m.servers, key)
	m.mu.Unlock()
	if running {
		m.shutdown(ctx, srv)
	}
	if err := m.Start(ctx, languageID, rootURI); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, st := range m.statuses {
		if id.key == key {
			st.Restarts++
			m.statuses[id] = st
		}
	}
	return nil
}

// shutdown asks srv to shut down, bounded by the close timeout, and
// releases its connection; the process is killed if it does not exit.
func (m *Manager) shutdown(ctx context.Context, srv server) {
	ctx, cancel := context.WithTimeout(ctx, m.cfg.CloseTimeout)
	defer cancel()
	if err := srv.stop(ctx); err != nil {
		m.log.Warn("stop language server", "error", err)
	}
	if err := srv.Close(); err != nil {
		m.log.Warn("close language server", "error", err)
	}
}

// childServers returns the language server processes behind srv.
func childServers(srv server) []*langServer {
	switch s := srv.(type) {
	case *langServer:
		return []*langServer{s}
	case *multiLangServer:
		var ret []*langServer
		for _, c := range s.allChildren() {
			if ls, ok := c.(*langServer); ok {
				ret = append(ret, ls)
			}
		}
		return ret
	}
	return nil
}

// dropStatuses forgets the lifecycle of the servers of key. m.mu must
// be held.
func (m *Manager) dropStatuses(key serverKey) {
	for id := range m.statuses {
		if id.key == key {
			delete(m.statuses, id)
		}
	}
}

func (m *Manager) updateStatus(srv *langServer, update func(*ServerStatus)) {
	id := serverID{key: srv.key(), name: srv.name()}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.statuses[id]
	update(&st)
	m.statuses[id] = st
}

func (m *Manager) trackStarting(srv *langServer) {
	params, err := json.Marshal(srv.initializeRequest())
	if err != nil {
		m.log.Warn("encode initialize request", srv.logAttrs("error", err)...)
	}
	m.updateStatus(srv, func(st *ServerStatus) {
		st.Language = srv.cfg.id
		st.Name = srv.name()
		st.RootURI = srv.rootURI
		st.Path = srv.binPath
		st.Args = srv.cfg.args
		st.Env = srv.cfg.env
		st.State = ServerStarting
		st.Pid = 0
		st.Started = time.Now()
		st.Params = params
		st.Result = nil
	})
}

// trackRunning records a completed handshake. A fresh bring-up clears
// the last error, while an automatic restart keeps the one that caused
// it.
func (m *Manager) trackRunning(srv *langServer, restarted bool) {
	m.log.Info("language server initialized", srv.logAttrs("restarted", restarted)...)
	m.updateStatus(srv, func(st *ServerStatus) {
		st.State = ServerRunning
		st.Pid = srv.pid
		st.Result = srv.initRaw
		if restarted {
			st.Started = time.Now()
			st.Restarts++
		} else {
			st.LastError = nil
		}
	})
}

func (m *Manager) trackFailed(srv *langServer, err error) {
	m.log.Error("language server failed", srv.logAttrs("error", err)...)
	m.updateStatus(srv, func(st *ServerStatus) {
		st.State = ServerFailed
		st.Pid = srv.pid
		st.LastError = err
	})
}

func (m *Manager) trackRestarting(srv *langServer, err error) {
	m.log.Warn("language server exited unexpectedly, restarting",
		srv.logAttrs("error", err)...)
	m.updateStatus(srv, func(st *ServerStatus) {
		st.State = ServerRestarting
		st.LastError = err
	})
}

// trackRestartFailed records a failed restart attempt of the server
// whose replacement is next; the server stays restarting.
func (m *Manager) trackRestartFailed(next *langServer, err error) {
	m.log.Warn("restart language server", next.logAttrs("error", err)...)
	m.updateStatus(next, func(st *ServerStatus) {
		if next.pid != 0 {
			st.Pid = next.pid
		}
		st.LastError = err
	})
}

// trackStopped records that srv is down without a restart. err is why,
// if it was not stopped on request.
func (m *Manager) trackStopped(srv *langServer, err error) {
	m.log.Info("language server stopped", srv.logAttrs()...)
	m.updateStatus(srv, func(st *ServerStatus) {
		st.State = ServerStopped
		if err != nil {
			st.LastError = err
		}
	})
}

// notify delivers a notification, on the host event loop when one is
// configured.
func (m *Manager) notify(level browserapi.NotificationLevel, msg string, args ...any) {
	m.deliver(func(n browserapi.Notifications) { _, _ = n.Notify(level, msg, args...) })
}

// notifyOnce is notify for messages that may repeat, such as the same
// start failure on every retry by a language extension.
func (m *Manager) notifyOnce(level browserapi.NotificationLevel, msg string, args ...any) {
	m.deliver(func(n browserapi.Notifications) { _, _ = n.NotifyOnce(level, msg, args...) })
}

func (m *Manager) deliver(fn func(browserapi.Notifications)) {
	if m.notifications == nil {
		return
	}
	if m.cfg.ScheduleNextTick == nil {
		fn(m.notifications)
		return
	}
	m.cfg.ScheduleNextTick(func() { fn(m.notifications) })
}

func (m *Manager) broadcastNotify(
	ctx context.Context, uri workspaceapi.URI, method string, params any,
) (ret error) {
	if uri == (workspaceapi.URI{}) {
		for _, srv := range m.allServers() {
			if err := srv.notify(ctx, method, params); err != nil {
				ret = errors.Join(ret, err)
			}
		}
		return
	}

	uriStr := convertURI(uri)

	// Tier 1: watcher-based routing — servers that registered
	// file watchers matching this URI. This is not supported for now.

	// Tier 2: root-aware routing — deliver to the single
	// most-specific initialized root that contains this file so a
	// file event is not fanned out to unrelated roots of the same
	// language.
	if srv, err := m.serverForURI(uriStr); err == nil {
		return srv.notify(ctx, method, params)
	}

	// Tier 3: broadcast to all running servers.
	m.log.Debug("broadcasting to all servers", "method", method, "uri", uriStr)
	for _, srv := range m.allServers() {
		if err := srv.notify(ctx, method, params); err != nil {
			ret = errors.Join(ret, err)
		}
	}
	return ret
}

func autoInitParams(rootURI string) semanticapi.InitializeParams {
	capabilities := map[string]any{
		"textDocument": map[string]any{
			"implementation": map[string]any{
				"linkSupport": true,
			},
			"completion":     map[string]any{},
			"hover":          map[string]any{},
			"signatureHelp":  map[string]any{},
			"definition":     map[string]any{},
			"references":     map[string]any{},
			"documentSymbol": map[string]any{},
			"formatting":     map[string]any{},
			// Servers like zls only push diagnostics when the client
			// advertises publishDiagnostics support.
			"publishDiagnostics": map[string]any{},
			"rename": map[string]any{
				"prepareSupport": true,
			},
			"codeAction": map[string]any{},
			"foldingRange": map[string]any{
				"lineFoldingOnly": false,
			},
			"selectionRange":    map[string]any{},
			"documentHighlight": map[string]any{},
			"callHierarchy":     map[string]any{},
			"codeLens":          map[string]any{},
			"inlayHint":         map[string]any{},
			"semanticTokens": map[string]any{
				"requests": map[string]any{
					"full":  true,
					"range": true,
				},
				"tokenTypes": []string{
					"namespace", "type", "class",
					"enum", "interface", "struct",
					"typeParameter", "parameter",
					"variable", "property",
					"enumMember", "event",
					"function", "method", "macro",
					"keyword", "modifier",
					"comment", "string", "number",
					"regexp", "operator",
					"decorator", "label",
				},
				"tokenModifiers": []string{
					"declaration", "definition",
					"readonly", "static",
					"deprecated", "abstract",
					"async", "modification",
					"documentation",
					"defaultLibrary",
				},
				"formats": []string{"relative"},
			},
		},
		"workspace": map[string]any{
			"symbol":      map[string]any{},
			"diagnostics": map[string]any{},
		},
		"window": map[string]any{
			"workDoneProgress": true,
			"showDocument": map[string]any{
				"support": true,
			},
		},
	}
	capabilitiesData, err := json.Marshal(capabilities)
	if err != nil {
		panic("marshal capabilities")
	}
	return semanticapi.InitializeParams{
		RootURI:      rootURI,
		Capabilities: json.RawMessage(capabilitiesData),
	}
}
