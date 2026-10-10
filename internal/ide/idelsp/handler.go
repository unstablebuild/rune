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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/html"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace"
)

var errCouldNotSchedule = errors.New(
	"could not schedule operation",
)

// WindowManager provides floating window management for
// LSP callback prompts.
type WindowManager interface {
	Floating(h browserapi.Floating, cfg browserapi.FloatingConfig) (browserapi.Window, error)
	CloseWindow(browserapi.Window) error
}

// Editor provides text editing capabilities needed by
// LSP callbacks.
type Editor interface {
	Editor(resource workspaceapi.URI) (textapi.Handler, error)
	SetLocationList(textapi.Handler, textapi.LocationPriority, string, textapi.LocationList) error
	SetCursor(textapi.Handler, term.Coordinates) error
	CellEditor(textapi.Handler) textapi.CellEditor
}

// FileSystem is the workspace file system that language servers edit.
// Its case rule decides whether two URIs from servers name the same
// document.
type FileSystem interface {
	schemeapi.FileSystem
	PathCaseSensitive() bool
}

// CallbackHandlerConfig holds optional dependencies for
// CallbackHandler.
type CallbackHandlerConfig struct {
	Config           config.Config
	Interrupter      term.Interrupter
	ScheduleNextTick func(fn func()) bool
	Icons            IconSet
}

// IconKey identifies a configurable CallbackHandler icon.
type IconKey string

const (
	// IconDiagnosticError is the icon for LSP error diagnostics.
	IconDiagnosticError IconKey = "error"
	// IconDiagnosticWarning is the icon for LSP warning diagnostics.
	IconDiagnosticWarning IconKey = "warning"
	// IconDiagnosticInformation is the icon for LSP information diagnostics.
	IconDiagnosticInformation IconKey = "information"
	// IconDiagnosticHint is the icon for LSP hint diagnostics.
	IconDiagnosticHint IconKey = "hint"
	// IconCompilerInline is the icon for compiler inlining diagnostics.
	IconCompilerInline IconKey = "inline"
	// IconCompilerEscape is the icon for compiler escape diagnostics.
	IconCompilerEscape IconKey = "escape"
	// IconCompilerBounds is the icon for compiler bounds diagnostics.
	IconCompilerBounds IconKey = "bounds"
	// IconCompilerNilcheck is the icon for compiler nilcheck diagnostics.
	IconCompilerNilcheck IconKey = "nilcheck"
	// IconCompilerDefault is the icon for unclassified compiler diagnostics.
	IconCompilerDefault IconKey = "compiler"
)

// IconSet configures the icons used by CallbackHandler.
type IconSet map[IconKey]string

// CallbackHandler implements semanticapi.LSPCallback by
// wiring LSP server-to-client callbacks to the Rune IDE.
type CallbackHandler struct {
	notifications    browserapi.Notifications
	windowManager    WindowManager
	resourceOpener   browserapi.ResourceOpener
	editor           Editor
	fileSystem       FileSystem
	rootURI          string
	config           config.Config
	interrupter      term.Interrupter
	scheduleNextTick func(fn func()) bool
	icons            IconSet
	log              *slog.Logger

	mu       sync.Mutex
	progress map[string]string
	// fileVersions and diagnostics are keyed by uriKey, so a server
	// naming a file with different case than the editor still finds it.
	fileVersions map[string]*fileVersionState
	versionCond  *sync.Cond
	diagnostics  map[string]*uriDiagnostics
}

// uriDiagnostics caches the latest diagnostics of one document.
type uriDiagnostics struct {
	// uri is the first spelling published for the document, so callers
	// of Diagnostics see a URI a server actually used.
	uri string
	// byServer is keyed by the publishing server so that several
	// backends sharing one language id (e.g. ty + ruff) accumulate
	// independently instead of overwriting each other.
	byServer map[string][]semanticapi.Diagnostic
}

// fileVersionState tracks the latest sent and processed
// document versions for a single URI.
type fileVersionState struct {
	sent               int32
	processed          int32
	pendingUnversioned bool
	awaitActive        bool
	awaitAfter         int32
}

// DefaultIconSet returns the default LSP callback icons.
func DefaultIconSet() IconSet {
	return IconSet{
		IconDiagnosticError:       "✖",
		IconDiagnosticWarning:     "▲",
		IconDiagnosticInformation: "◉",
		IconDiagnosticHint:        "󰌵",
		IconCompilerInline:        "󰁔",
		IconCompilerEscape:        "󰁝",
		IconCompilerBounds:        "󰅪",
		IconCompilerNilcheck:      "∅",
		IconCompilerDefault:       "⚙",
	}
}

func iconSetWithDefaults(icons IconSet) IconSet {
	ret := DefaultIconSet()
	maps.Copy(ret, icons)
	return ret
}

var _ Callback = (*CallbackHandler)(nil)

// NewCallbackHandler creates a new CallbackHandler.
func NewCallbackHandler(
	notifications browserapi.Notifications,
	windowManager WindowManager,
	resourceOpener browserapi.ResourceOpener,
	editor Editor,
	fileSystem FileSystem,
	rootURI string,
	cfg CallbackHandlerConfig,
) *CallbackHandler {
	interrupter := cfg.Interrupter
	if interrupter == nil {
		interrupter = term.NopInterrupter()
	}
	sched := cfg.ScheduleNextTick
	if sched == nil {
		sched = func(fn func()) bool {
			fn()
			return true
		}
	}
	h := &CallbackHandler{
		notifications:    notifications,
		windowManager:    windowManager,
		resourceOpener:   resourceOpener,
		editor:           editor,
		fileSystem:       fileSystem,
		rootURI:          rootURI,
		config:           cfg.Config,
		interrupter:      interrupter,
		scheduleNextTick: sched,
		icons:            iconSetWithDefaults(cfg.Icons),
		log:              slog.With("struct", "idelsp.CallbackHandler", "workspace", rootURI),
		progress:         make(map[string]string),
		fileVersions:     make(map[string]*fileVersionState),
		diagnostics:      make(map[string]*uriDiagnostics),
	}
	h.versionCond = sync.NewCond(&h.mu)
	return h
}

func (h *CallbackHandler) uriKey(uri string) string {
	if h.fileSystem.PathCaseSensitive() {
		return uri
	}
	u, err := workspaceapi.ParseURI(uri)
	if err != nil {
		return uri
	}
	return workspace.URIKey(u, false)
}

// ShowMessage displays a message notification.
func (h *CallbackHandler) ShowMessage(
	ctx context.Context,
	params semanticapi.ShowMessageParams,
) error {
	level := messageTypeToNotificationLevel(params.Type)
	msg := params.Message
	md, ok := metadataFromContext(ctx)
	if ok && md.ServerName != "" {
		msg = md.ServerName + ": " + msg
	}
	ok = h.scheduleNextTick(func() {
		h.notifications.Notify(level, msg) //nolint:errcheck
	})
	if !ok {
		return errCouldNotSchedule
	}
	return nil
}

// LogMessage logs a message at the appropriate slog level.
func (h *CallbackHandler) LogMessage(
	ctx context.Context, params semanticapi.LogMessageParams,
) error {
	level := messageTypeToSlogLevel(params.Type)
	h.log.Log(ctx, level, params.Message)
	return nil
}

// PublishDiagnostics publishes diagnostics for a document.
func (h *CallbackHandler) PublishDiagnostics(
	ctx context.Context,
	params semanticapi.PublishDiagnosticsParams,
) error {
	// Signal that the server has processed this document version.
	h.fileDidProcess(params.URI, params.Version)

	uri, err := workspaceapi.ParseURI(params.URI)
	if err != nil {
		return fmt.Errorf("parse URI: %w", err)
	}
	key := h.uriKey(params.URI)

	// serverID combines the publishing server name with its root URI
	// so two servers sharing a name (e.g. "ty") rooted at different
	// project roots cannot overwrite each other's diagnostics.
	serverID := ""
	if md, ok := metadataFromContext(ctx); ok {
		serverID = md.ServerName + "\x00" + md.RootURI
	}

	// Centrally cache the latest diagnostics for this URI so the
	// `lsp diagnostics` command can list them across all files,
	// regardless of whether the file is currently open in an editor.
	// The cache is keyed by publishing server so multiple backends
	// for one language id accumulate instead of overwriting.
	h.mu.Lock()
	entry, found := h.diagnostics[key]
	if len(params.Diagnostics) == 0 {
		if found {
			delete(entry.byServer, serverID)
			if len(entry.byServer) == 0 {
				delete(h.diagnostics, key)
			}
		}
	} else {
		stored := make([]semanticapi.Diagnostic, len(params.Diagnostics))
		copy(stored, params.Diagnostics)
		if !found {
			entry = &uriDiagnostics{
				uri:      params.URI,
				byServer: make(map[string][]semanticapi.Diagnostic),
			}
			h.diagnostics[key] = entry
		}
		entry.byServer[serverID] = stored
	}
	// Merge every server's diagnostics for this URI into a single
	// location list. Keying the cache by server keeps each server's
	// set independently overridable, while the merged emission means
	// diagnostics from several backends (e.g. ty + ruff) coexist
	// under one "lsp-diagnostics" source so navigation aliases treat
	// them as a single list.
	merged := make([]semanticapi.Diagnostic, 0)
	if entry != nil {
		for _, diags := range entry.byServer {
			merged = append(merged, diags...)
		}
	}
	h.mu.Unlock()

	locs := make([]textapi.Location, 0, len(merged))
	highest := textapi.LocationPriorityInfo
	for _, diag := range merged {
		p := diagnosticSeverityToLocationPriority(diag.Severity)
		if p > highest {
			highest = p
		}

		msg := diag.Message
		attr, icon := diagnosticSeverityToAttr(diag.Severity, h.icons)
		if (diag.Source == "compiler" || diag.Source == "optimizer details") &&
			diag.Severity != semanticapi.DiagnosticSeverityError &&
			diag.Severity != semanticapi.DiagnosticSeverityWarning {
			icon, msg, attr = classifyCompilerDiagnostic(diag.Message, h.icons)
		}

		locs = append(locs, textapi.Location{
			From: term.Coordinates{
				X: int(diag.Range.Start.Character),
				Y: int(diag.Range.Start.Line),
			},
			To: term.Coordinates{
				X: int(diag.Range.End.Character),
				Y: int(diag.Range.End.Line),
			},
			Message: msg,
			Attr:    attr,
			Icon:    icon,
		})
	}
	ll := textapi.LocationSlice(locs)

	ok := h.scheduleNextTick(func() {
		eh, err := h.editor.Editor(uri)
		if err != nil {
			// Servers publish diagnostics for files that are not
			// open in any editor; the central cache above already
			// serves those, so a missing handler is expected and
			// not worth logging.
			if !errors.Is(err, text.ErrHandlerNotFound) {
				h.log.Warn("editor for diagnostics", "uri", uri.Name(), "err", err)
			}
			return
		}
		err = h.editor.SetLocationList(eh, highest, "lsp-diagnostics", ll)
		if err != nil {
			h.log.Warn("set diagnostics", "err", err)
		}
	})
	if !ok {
		return errCouldNotSchedule
	}
	return nil
}

// Diagnostics returns a snapshot of the latest diagnostics
// received via PublishDiagnostics, keyed by document URI.
// The returned map and its slices are owned by the caller and
// safe to mutate.
func (h *CallbackHandler) Diagnostics() map[string][]semanticapi.Diagnostic {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string][]semanticapi.Diagnostic, len(h.diagnostics))
	for _, entry := range h.diagnostics {
		var merged []semanticapi.Diagnostic
		for _, diags := range entry.byServer {
			merged = append(merged, diags...)
		}
		out[entry.uri] = merged
	}
	return out
}

// Progress handles $/progress notifications.
func (h *CallbackHandler) Progress(
	_ context.Context, params semanticapi.ProgressParams,
) error {
	var kind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(params.Value, &kind); err != nil {
		return fmt.Errorf("unmarshal progress kind: %w", err)
	}

	key := progressTokenKey(params.Token)

	switch kind.Kind {
	case "begin":
		var begin struct {
			Title   string `json:"title"`
			Message string `json:"message,omitempty"`
		}
		err := json.Unmarshal(params.Value, &begin)
		if err != nil {
			return fmt.Errorf("unmarshal progress begin: %w", err)
		}
		if progressTitleIgnored(begin.Title) {
			h.mu.Lock()
			delete(h.progress, key)
			h.mu.Unlock()
			return nil
		}
		msg := begin.Title
		if begin.Message != "" {
			msg = begin.Title + ": " + begin.Message
		}
		ok := h.scheduleNextTick(func() {
			id, err := h.notifications.Notify(browserapi.LevelInfo, msg)
			if err != nil {
				h.log.Warn("progress begin notify", "err", err)
				return
			}
			h.mu.Lock()
			h.progress[key] = id
			h.mu.Unlock()
		})
		if !ok {
			return errCouldNotSchedule
		}

	case "report":
		var report struct {
			Message    string `json:"message,omitempty"`
			Percentage *int64 `json:"percentage,omitempty"`
		}
		err := json.Unmarshal(params.Value, &report)
		if err != nil {
			return fmt.Errorf("unmarshal progress report: %w", err)
		}
		h.mu.Lock()
		id, ok := h.progress[key]
		h.mu.Unlock()
		if !ok {
			return nil
		}
		var pct int64
		if report.Percentage != nil {
			pct = *report.Percentage
		}
		scheduled := h.scheduleNextTick(func() {
			_ = h.notifications.UpdateNotificationProgress(id, report.Message, pct, 100)
		})
		if !scheduled {
			return errCouldNotSchedule
		}

	case "end":
		h.mu.Lock()
		id, ok := h.progress[key]
		delete(h.progress, key)
		h.mu.Unlock()
		if !ok {
			return nil
		}
		scheduled := h.scheduleNextTick(func() {
			_ = h.notifications.UpdateNotificationProgress(id, "", 100, 100)
		})
		if !scheduled {
			return errCouldNotSchedule
		}
	}
	return nil
}

// LogTrace logs a trace message at debug level.
func (h *CallbackHandler) LogTrace(ctx context.Context, params semanticapi.LogTraceParams) error {
	h.log.Log(ctx, slog.LevelDebug, params.Message, "verbose", params.Verbose)
	return nil
}

// HandleNotification receives server→client JSON-RPC notifications
// that are not modeled by a dedicated callback method, such as
// rust-analyzer's experimental/serverStatus and experimental test
// notifications. The default host has no behavior for these; it logs
// them at debug and returns nil so unrecognized methods do not error.
func (h *CallbackHandler) HandleNotification(
	ctx context.Context, method string, params json.RawMessage,
) error {
	h.log.Log(ctx, slog.LevelDebug, "unhandled lsp notification",
		"method", method, "params", string(params))
	return nil
}

// ShowDocument requests the client to display a document.
func (h *CallbackHandler) ShowDocument(
	ctx context.Context, params semanticapi.ShowDocumentParams,
) (semanticapi.ShowDocumentResult, error) {
	if strings.HasPrefix(params.URI, "http://") || strings.HasPrefix(params.URI, "https://") {
		return h.showHTTPDocument(params.URI)
	}

	uri, err := workspaceapi.ParseURI(params.URI)
	if err != nil {
		return semanticapi.ShowDocumentResult{Success: false}, fmt.Errorf("parse URI: %w", err)
	}
	prefix := "LSP"
	md, ok := metadataFromContext(ctx)
	if ok && md.ServerName != "" {
		prefix = md.ServerName
	}
	sel := params.Selection
	resultCh := make(chan semanticapi.ShowDocumentResult, 1)
	errCh := make(chan error, 1)
	ok = h.scheduleNextTick(func() {
		if _, err := h.resourceOpener.Open(uri); err != nil {
			errCh <- fmt.Errorf("open resource: %w", err)
			return
		}
		//nolint:errcheck
		h.notifications.Notify(browserapi.LevelInfo, "%s: see %s", prefix, uri.Name())
		if sel != nil {
			eh, err := h.editor.Editor(uri)
			if err == nil {
				cursor := term.Coordinates{X: int(sel.Start.Character), Y: int(sel.Start.Line)}
				_ = h.editor.SetCursor(eh, cursor)
			}
		}
		resultCh <- semanticapi.ShowDocumentResult{Success: true}
	})
	if !ok {
		return semanticapi.ShowDocumentResult{}, errCouldNotSchedule
	}

	select {
	case result := <-resultCh:
		return result, nil
	case err := <-errCh:
		return semanticapi.ShowDocumentResult{Success: false}, err
	case <-ctx.Done():
		return semanticapi.ShowDocumentResult{Success: false}, ctx.Err()
	}
}

func (h *CallbackHandler) showHTTPDocument(rawURL string) (
	semanticapi.ShowDocumentResult, error,
) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return semanticapi.ShowDocumentResult{Success: false}, fmt.Errorf("parse URL: %w", err)
	}
	resultCh := make(chan semanticapi.ShowDocumentResult, 1)
	errCh := make(chan error, 1)
	handler := html.New(h.interrupter, parsed)
	ok := h.scheduleNextTick(func() {
		_, err := h.windowManager.Floating(handler, browserapi.FloatingConfig{
			Alignment: component.AlignmentCentered,
		})
		if err != nil {
			errCh <- fmt.Errorf("show floating: %w", err)
			return
		}
		resultCh <- semanticapi.ShowDocumentResult{Success: true}
	})
	if !ok {
		return semanticapi.ShowDocumentResult{}, errCouldNotSchedule
	}

	select {
	case result := <-resultCh:
		return result, nil
	case err := <-errCh:
		return semanticapi.ShowDocumentResult{Success: false}, err
	}
}

// ShowMessageRequest shows a message with action items.
func (h *CallbackHandler) ShowMessageRequest(
	ctx context.Context,
	params semanticapi.ShowMessageRequestParams,
) (*semanticapi.MessageActionItem, error) {
	titles := make([]string, len(params.Actions))
	for i, a := range params.Actions {
		titles[i] = fmt.Sprintf("   %s    ", a.Title)
	}

	ch := make(chan *semanticapi.MessageActionItem, 1)
	prompt := handler.NewPrompt(handler.PromptConfig{
		HighlightAttr: term.Attributes{
			Attrs: term.AttrBold,
			Bg:    term.ColorRed,
		},
		OptionAttr: term.Attributes{
			Attrs: term.AttrBold,
			Bg:    term.ColorGray,
		},
		PromptConfig: component.PromptConfig{
			Message: params.Message,
			Options: titles,
		},
		PromptHandler: handler.FuncPromptHandler(
			func(idx int, _ string) {
				if idx >= 0 && idx < len(params.Actions) {
					ch <- &params.Actions[idx]
				} else {
					ch <- nil
				}
			},
			func() error {
				ch <- nil
				return nil
			},
		),
	})

	winCh := make(chan browserapi.Window, 1)
	errCh := make(chan error, 1)
	ok := h.scheduleNextTick(func() {
		win, err := h.windowManager.Floating(
			prompt, browserapi.FloatingConfig{Alignment: component.AlignmentCentered},
		)
		if err != nil {
			errCh <- fmt.Errorf("show floating: %w", err)
			return
		}
		winCh <- win
	})
	if !ok {
		return nil, errCouldNotSchedule
	}

	var win browserapi.Window
	select {
	case win = <-winCh:
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	defer func() {
		h.scheduleNextTick(func() { //nolint:errcheck
			h.windowManager.CloseWindow(win) //nolint:errcheck
		})
	}()

	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// WorkDoneProgressCreate pre-allocates a progress token.
func (h *CallbackHandler) WorkDoneProgressCreate(
	_ context.Context,
	params semanticapi.WorkDoneProgressCreateParams,
) error {
	key := progressTokenKey(params.Token)
	h.mu.Lock()
	h.progress[key] = ""
	h.mu.Unlock()
	return nil
}

// ApplyEdit applies a workspace edit.
func (h *CallbackHandler) ApplyEdit(
	ctx context.Context,
	params semanticapi.ApplyWorkspaceEditParams,
) (semanticapi.ApplyWorkspaceEditResult, error) {
	edit := params.Edit

	if len(edit.DocumentChanges) > 0 {
		ok := h.scheduleNextTick(func() {
			err := h.applyDocumentChanges(ctx, edit.DocumentChanges)
			if err != nil {
				h.log.Warn("apply document changes", "err", err)
			}
		})
		if !ok {
			return semanticapi.ApplyWorkspaceEditResult{}, errCouldNotSchedule
		}
		return semanticapi.ApplyWorkspaceEditResult{Applied: true}, nil
	}

	if len(edit.Changes) > 0 {
		ok := h.scheduleNextTick(func() {
			err := h.applyChanges(ctx, edit.Changes)
			if err != nil {
				h.log.Warn("apply changes", "err", err)
			}
		})
		if !ok {
			return semanticapi.ApplyWorkspaceEditResult{}, errCouldNotSchedule
		}
		return semanticapi.ApplyWorkspaceEditResult{Applied: true}, nil
	}

	return semanticapi.ApplyWorkspaceEditResult{Applied: true}, nil
}

// WorkspaceFolders returns the workspace folders.
func (h *CallbackHandler) WorkspaceFolders(
	_ context.Context,
) ([]semanticapi.WorkspaceFolder, error) {
	name := filepath.Base(h.rootURI)
	return []semanticapi.WorkspaceFolder{
		{URI: h.rootURI, Name: name},
	}, nil
}

// Configuration fetches configuration from the client.
func (h *CallbackHandler) Configuration(
	_ context.Context,
	params semanticapi.ConfigurationParams,
) ([]json.RawMessage, error) {
	results := make(
		[]json.RawMessage, len(params.Items),
	)
	for i, item := range params.Items {
		results[i] = h.configForItem(item)
	}
	return results, nil
}

// RegisterCapability is a no-op.
func (h *CallbackHandler) RegisterCapability(
	_ context.Context, _ semanticapi.RegistrationParams,
) error {
	return nil
}

// UnregisterCapability is a no-op.
func (h *CallbackHandler) UnregisterCapability(
	_ context.Context, _ semanticapi.UnregistrationParams,
) error {
	return nil
}

// The workspace refresh requests below are no-ops on the handler side.
// Refreshes that require server interaction (re-pulling diagnostics)
// are intercepted by the Manager's callback decorator before the
// callback chain reaches this handler; lenses, tokens and hints are
// re-requested on demand as the editor renders, so the notifications
// need no action here.

// CodeLensRefresh implements semanticapi.LSPCallback.
func (h *CallbackHandler) CodeLensRefresh(
	_ context.Context,
) error {
	return nil
}

// SemanticTokensRefresh implements semanticapi.LSPCallback.
func (h *CallbackHandler) SemanticTokensRefresh(
	_ context.Context,
) error {
	return nil
}

// InlayHintRefresh implements semanticapi.LSPCallback.
func (h *CallbackHandler) InlayHintRefresh(
	_ context.Context,
) error {
	return nil
}

// DiagnosticRefresh implements semanticapi.LSPCallback.
func (h *CallbackHandler) DiagnosticRefresh(
	_ context.Context,
) error {
	return nil
}

// FileDidChange records that a file changed and how the next
// WaitFileProcessed should wait for the LSP server to reconcile it.
func (h *CallbackHandler) FileDidChange(uri string, version int32, open, oob bool) {
	key := h.uriKey(uri)
	h.mu.Lock()
	defer h.mu.Unlock()

	state, ok := h.fileVersions[key]
	if !ok {
		state = &fileVersionState{}
		h.fileVersions[key] = state
	}
	switch {
	case !oob:
		if version > state.sent {
			state.sent = version
		}
	case open:
		state.awaitActive = true
		state.awaitAfter = max(state.processed, state.sent, version)
	default:
		state.pendingUnversioned = true
	}
}

// InvalidateAllPending marks every tracked URI as having an
// unversioned pending change. This is used when an out-of-band
// workspace event (such as a file deletion) can cause the LSP
// server to asynchronously re-typecheck and re-publish
// diagnostics for unrelated files. After this call, the next
// WaitFileProcessed for any tracked URI will block until that
// URI's next publishDiagnostics push arrives (or the fallback
// timeout fires).
func (h *CallbackHandler) InvalidateAllPending() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, state := range h.fileVersions {
		state.pendingUnversioned = true
	}
}

// WaitFileProcessed blocks until the LSP server has processed
// the latest known version for the given URI, or until the
// context is cancelled. A fallback timeout of 5s is applied
// when the caller's context has no deadline, in case the LSP
// server never sends a publishDiagnostics for the file.
func (h *CallbackHandler) WaitFileProcessed(ctx context.Context, uri string) error {
	// Apply a fallback timeout so we never block forever if the
	// server does not send publishDiagnostics for this URI.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}

	key := h.uriKey(uri)
	h.mu.Lock()
	defer h.mu.Unlock()

	state, ok := h.fileVersions[key]
	if !ok {
		return nil
	}

	// Determine what we're waiting for: either a specific
	// version or just the next diagnostics push.
	waitUnversioned := state.pendingUnversioned
	waitAfter := state.awaitActive
	targetVersion := int32(0)
	if waitAfter {
		if state.processed > state.awaitAfter {
			return nil
		}
	} else if !waitUnversioned {
		targetVersion = state.sent
		if state.processed >= targetVersion {
			return nil
		}
	}

	done := make(chan struct{})
	go debug.CapturePanicReport(func() {

		select {
		case <-ctx.Done():
			h.versionCond.Broadcast()
		case <-done:
		}

	})
	defer close(done)

	for {
		if waitAfter {
			if !state.awaitActive {
				return nil
			}
		} else if waitUnversioned {
			if !state.pendingUnversioned {
				return nil
			}
		} else if state.processed >= targetVersion {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		h.versionCond.Wait()
	}
}

func (h *CallbackHandler) fileDidProcess(uri string, version int32) {
	key := h.uriKey(uri)
	h.mu.Lock()
	defer h.mu.Unlock()

	state, ok := h.fileVersions[key]
	if !ok {
		return
	}
	if version > 0 && version > state.processed {
		state.processed = version
	}
	if state.awaitActive {
		// A stale push at or below the awaited version must not
		// release the wait; only a strictly newer processed version
		// proves the server re-typechecked the out-of-band change.
		if state.processed > state.awaitAfter {
			state.awaitActive = false
		}
	} else {
		// Any diagnostics push for an untracked pending URI clears
		// the unversioned flag.
		state.pendingUnversioned = false
	}
	h.versionCond.Broadcast()
}

func progressTokenKey(
	token semanticapi.ProgressToken,
) string {
	if token.IsInteger {
		return fmt.Sprintf("%d", token.IntegerValue)
	}
	return token.StringValue
}

// ignoredProgressTitles are $/progress begin titles that carry no
// information worth a pop-up. rust-analyzer reruns flycheck on every
// change, so its begin/end pair would notify on every edit.
var ignoredProgressTitles = []string{"cargo clippy"}

func progressTitleIgnored(title string) bool {
	for _, ignored := range ignoredProgressTitles {
		if strings.Contains(title, ignored) {
			return true
		}
	}
	return false
}

func messageTypeToNotificationLevel(
	mt semanticapi.MessageType,
) browserapi.NotificationLevel {
	switch mt {
	case semanticapi.MessageTypeError:
		return browserapi.LevelError
	case semanticapi.MessageTypeWarning:
		return browserapi.LevelWarn
	default:
		return browserapi.LevelInfo
	}
}

func messageTypeToSlogLevel(
	mt semanticapi.MessageType,
) slog.Level {
	switch mt {
	case semanticapi.MessageTypeError:
		return slog.LevelError
	case semanticapi.MessageTypeWarning:
		return slog.LevelWarn
	case semanticapi.MessageTypeInfo:
		return slog.LevelInfo
	default:
		return slog.LevelDebug
	}
}

func diagnosticSeverityToLocationPriority(
	s semanticapi.DiagnosticSeverity,
) textapi.LocationPriority {
	switch s {
	case semanticapi.DiagnosticSeverityError:
		return textapi.LocationPriorityError
	case semanticapi.DiagnosticSeverityWarning:
		return textapi.LocationPriorityWarning
	default:
		return textapi.LocationPriorityInfo
	}
}

func diagnosticSeverityToAttr(
	s semanticapi.DiagnosticSeverity,
	icons IconSet,
) (term.Attributes, string) {
	key := diagnosticSeverityIconKey(s)
	switch s {
	case semanticapi.DiagnosticSeverityError:
		return term.Attributes{
			Bg: term.ColorRed,
		}, icons[key]
	case semanticapi.DiagnosticSeverityWarning:
		return term.Attributes{
			Bg: term.ColorYellow,
		}, icons[key]
	case semanticapi.DiagnosticSeverityInformation:
		return term.Attributes{
			Bg: term.ColorBlue,
		}, icons[key]
	default:
		return term.Attributes{
			Bg: term.ColorGray,
		}, icons[key]
	}
}

func diagnosticSeverityIconKey(s semanticapi.DiagnosticSeverity) IconKey {
	switch s {
	case semanticapi.DiagnosticSeverityError:
		return IconDiagnosticError
	case semanticapi.DiagnosticSeverityWarning:
		return IconDiagnosticWarning
	case semanticapi.DiagnosticSeverityInformation:
		return IconDiagnosticInformation
	case semanticapi.DiagnosticSeverityHint:
		return IconDiagnosticHint
	default:
		return ""
	}
}

// classifyCompilerDiagnostic categorizes a compiler optimization
// diagnostic (gc_details) by its message content and returns a
// descriptive icon, a prefixed message, and a category-specific color.
func classifyCompilerDiagnostic(
	msg string,
	icons IconSet,
) (icon, enhanced string, attr term.Attributes) {
	switch {
	case strings.Contains(msg, "inline") || strings.Contains(msg, "inlining"):
		return icons[IconCompilerInline], "Inline: " + msg, term.Attributes{
			Bg: term.GetColor("indigo"),
		}
	case strings.Contains(msg, "escape") ||
		strings.Contains(msg, "heap") ||
		strings.Contains(msg, "leaking"):
		return icons[IconCompilerEscape], "Escape: " + msg, term.Attributes{
			Bg: term.GetColor("darkmagenta"),
		}
	case strings.Contains(msg, "Bounds"):
		return icons[IconCompilerBounds], "Bounds: " + msg, term.Attributes{
			Bg: term.GetColor("rebeccapurple"),
		}
	case strings.Contains(msg, "nilcheck") ||
		strings.Contains(msg, "nil check"):
		return icons[IconCompilerNilcheck], "Nilcheck: " + msg, term.Attributes{
			Bg: term.GetColor("blueviolet"),
		}
	default:
		return icons[IconCompilerDefault], msg, term.Attributes{
			Bg: term.GetColor("darkslateblue"),
		}
	}
}

func (h *CallbackHandler) applyDocumentChanges(
	ctx context.Context,
	changes []semanticapi.DocumentChange,
) error {
	for _, change := range changes {
		switch {
		case change.TextDocumentEdit != nil:
			if err := h.applyTextDocumentEdit(ctx, change.TextDocumentEdit); err != nil {
				return err
			}
		case change.CreateFile != nil:
			if err := h.applyCreateFile(change.CreateFile); err != nil {
				return err
			}
		case change.RenameFile != nil:
			if err := h.applyRenameFile(change.RenameFile); err != nil {
				return err
			}
		case change.DeleteFile != nil:
			if err := h.applyDeleteFile(change.DeleteFile); err != nil {
				return err
			}
		}
	}
	return nil
}

// editorForURI returns the editor handler for the given URI.
// If the editor is not available, it opens the resource via
// the resource opener and retries.
func (h *CallbackHandler) editorForURI(
	uri workspaceapi.URI,
) (textapi.Handler, error) {
	eh, err := h.editor.Editor(uri)
	if err == nil {
		return eh, nil
	}
	if h.resourceOpener == nil {
		return nil, err
	}
	if _, openErr := h.resourceOpener.Open(uri); openErr != nil {
		return nil, err
	}
	return h.editor.Editor(uri)
}

func (h *CallbackHandler) applyTextDocumentEdit(
	ctx context.Context,
	edit *semanticapi.TextDocumentEdit,
) error {
	uri, err := workspaceapi.ParseURI(edit.TextDocument.URI)
	if err != nil {
		return fmt.Errorf("parse URI: %w", err)
	}
	editorHandler, err := h.editorForURI(uri)
	if err != nil {
		return fmt.Errorf("editor for %s: %w", uri.Name(), err)
	}
	return applyTextEdits(ctx, h.editor, editorHandler, edit.Edits)
}

func applyTextEdits(
	ctx context.Context,
	editor Editor,
	editorHandler textapi.Handler,
	edits []semanticapi.TextEdit,
) error {
	sorted := make([]semanticapi.TextEdit, len(edits))
	copy(sorted, edits)
	sort.SliceStable(sorted, func(i, j int) bool {
		a := sorted[i].Range.Start
		b := sorted[j].Range.Start
		if a.Line != b.Line {
			return a.Line > b.Line
		}
		return a.Character > b.Character
	})
	// Per the LSP spec, when multiple inserts share the same
	// position, the array order defines the resulting text order.
	// Since we apply bottom-to-top, same-position inserts must be
	// reversed so the first-in-array insert ends up first in text.
	reverseSameStartEdits(sorted)

	cellEditor := editor.CellEditor(editorHandler)
	for _, edit := range sorted {
		start := term.Coordinates{
			X: int(edit.Range.Start.Character),
			Y: int(edit.Range.Start.Line),
		}
		end := term.Coordinates{
			X: int(edit.Range.End.Character),
			Y: int(edit.Range.End.Line),
		}
		if _, _, _, err := cellEditor.Edit(
			ctx, start, end, edit.NewText,
		); err != nil {
			return fmt.Errorf("cell edit: %w", err)
		}
	}
	return nil
}

// reverseSameStartEdits reverses each contiguous group of edits
// sharing the same start position so that applying them bottom-to-top
// produces the correct array-order text per the LSP spec.
func reverseSameStartEdits(edits []semanticapi.TextEdit) {
	for i := 0; i < len(edits); {
		j := i + 1
		for j < len(edits) &&
			edits[j].Range.Start == edits[i].Range.Start {
			j++
		}
		if j-i > 1 {
			for l, r := i, j-1; l < r; l, r = l+1, r-1 {
				edits[l], edits[r] = edits[r], edits[l]
			}
		}
		i = j
	}
}

func (h *CallbackHandler) applyCreateFile(
	cf *semanticapi.CreateFile,
) error {
	uri, err := workspaceapi.ParseURI(cf.URI)
	if err != nil {
		return fmt.Errorf("parse URI: %w", err)
	}
	path := uri.Path()

	if cf.Options != nil && cf.Options.IgnoreIfExists {
		if _, err := h.fileSystem.Stat(path); err == nil {
			return nil
		}
	}

	flag := os.O_CREATE | os.O_WRONLY
	if cf.Options != nil && cf.Options.Overwrite {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_EXCL
	}

	f, err := h.fileSystem.OpenFile(path, flag, 0644)
	if err != nil {
		return fmt.Errorf("create file %s: %w", path, err)
	}
	return f.Close()
}

func (h *CallbackHandler) applyRenameFile(
	rf *semanticapi.RenameFile,
) error {
	oldURI, err := workspaceapi.ParseURI(rf.OldURI)
	if err != nil {
		return fmt.Errorf("parse old URI: %w", err)
	}
	newURI, err := workspaceapi.ParseURI(rf.NewURI)
	if err != nil {
		return fmt.Errorf("parse new URI: %w", err)
	}

	oldPath := oldURI.Path()
	newPath := newURI.Path()

	if rf.Options != nil && rf.Options.IgnoreIfExists {
		if _, err := h.fileSystem.Stat(newPath); err == nil {
			return nil
		}
	}

	return h.fileSystem.Rename(oldPath, newPath)
}

func (h *CallbackHandler) applyDeleteFile(
	df *semanticapi.DeleteFile,
) error {
	uri, err := workspaceapi.ParseURI(df.URI)
	if err != nil {
		return fmt.Errorf("parse URI: %w", err)
	}
	path := uri.Path()

	if df.Options != nil && df.Options.IgnoreIfNotExists {
		if _, err := h.fileSystem.Stat(path); err != nil {
			return nil
		}
	}

	return h.fileSystem.Remove(path)
}

func (h *CallbackHandler) applyChanges(
	ctx context.Context,
	changes map[string][]semanticapi.TextEdit,
) error {
	for uriStr, edits := range changes {
		uri, err := workspaceapi.ParseURI(uriStr)
		if err != nil {
			return fmt.Errorf("parse URI: %w", err)
		}
		editorHandler, err := h.editorForURI(uri)
		if err != nil {
			return fmt.Errorf("editor for %s: %w", uri.Name(), err)
		}
		if err := applyTextEdits(ctx, h.editor, editorHandler, edits); err != nil {
			return err
		}
	}
	return nil
}

func (h *CallbackHandler) configForItem(
	item semanticapi.ConfigurationItem,
) json.RawMessage {
	if h.config == nil {
		return json.RawMessage("null")
	}
	cfg := h.config
	segments := []string{"lsp", "servers"}
	if item.Section != "" {
		segments = append(segments, item.Section)
	}
	for i, seg := range segments {
		if i == len(segments)-1 {
			m, err := cfg.GetMap(seg)
			if err != nil {
				return json.RawMessage("null")
			}
			data, err := json.Marshal(m)
			if err != nil {
				return json.RawMessage("null")
			}
			return data
		}
		next, err := cfg.GetConfig(seg)
		if err != nil {
			return json.RawMessage("null")
		}
		cfg = next
	}
	return json.RawMessage("null")
}
