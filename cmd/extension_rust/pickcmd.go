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
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/locationpicker"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// pickEntry is one row of a location-list command's result: where <enter>
// jumps, and how the row reads.
type pickEntry struct {
	loc     semanticapi.Location
	display string
}

// pickResult builds the entries a location-list command offers from the
// server result.
type pickResult func(ctx context.Context, cmd textapi.Command) ([]pickEntry, error)

// pickDeps are the client capabilities a location-list command needs to
// show its picker and open the chosen entry.
type pickDeps struct {
	editor    textapi.Editor
	wm        browserapi.WindowManager
	opener    browserapi.ResourceOpener
	notify    browserapi.Notifications
	fs        workspaceapi.FileSystem
	parser    syntaxapi.Parser
	interrupt term.Interrupter
}

// listPickCmd requests a list of code locations from rust-analyzer and
// offers them in a floating picker with a preview pane, jumping to the
// selection on <enter>.
type listPickCmd struct {
	pickDeps
	produce  pickResult
	emptyMsg string
}

var _ textapi.CommandHandler = (*listPickCmd)(nil)

func (c *listPickCmd) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	win, err := c.wm.Focus()
	if err != nil {
		return err
	}
	entries, err := c.produce(ctx, cmd)
	if err != nil {
		return err
	}
	return c.present(ctx, win, cmd.URI, entries, c.emptyMsg)
}

func (c *listPickCmd) Complete(_ context.Context, _ string, _ []string) (
	iterator.Iterator[string], error,
) {
	return iterator.Empty[string](), nil
}

// present reports emptyMsg when there is nothing to show, jumps straight
// to a lone entry, and otherwise floats a picker. win is the window a
// jump lands in, captured by the caller before any floating window
// opened. base is any URI of the workspace, used to rebase the
// server's file:// locations.
func (d pickDeps) present(
	ctx context.Context, win browserapi.Window, base workspaceapi.URI,
	entries []pickEntry, emptyMsg string,
) error {
	switch len(entries) {
	case 0:
		_, _ = d.notify.Notify(browserapi.LevelInfo, "%s", emptyMsg)
		return nil
	case 1:
		return openLocation(d.editor, d.wm, d.opener, win, base, entries[0].loc)
	}
	// The picker only reports the chosen index; the jump runs on this
	// goroutine so no IDE request is issued from the window event stream.
	ch := make(chan int, 1)
	interrupt := d.interrupt
	if interrupt == nil {
		interrupt = term.NopInterrupter()
	}
	view := &pickerView{ch: ch, interrupt: interrupt}
	picker := locationpicker.New(
		d.pickerEntries(base, entries), d.fs, view.tick, d.parser,
		locationpicker.DefaultConfig(), nil)
	picker.SetOnSelect(func(idx int) { ch <- idx })
	view.Picker = picker
	if _, err := d.wm.Floating(view, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	}); err != nil {
		return fmt.Errorf("show locations: %w", err)
	}
	select {
	case idx := <-ch:
		if idx < 0 || idx >= len(entries) {
			return nil
		}
		return openLocation(d.editor, d.wm, d.opener, win, base, entries[idx].loc)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pickerEntries rebases the server locations onto the workspace scheme,
// keeping the result index-aligned with entries so a selection maps
// back. A location that fails to rebase keeps its display; only its
// preview and jump degrade.
func (d pickDeps) pickerEntries(
	base workspaceapi.URI, entries []pickEntry,
) []locationpicker.Entry {
	out := make([]locationpicker.Entry, len(entries))
	for i, e := range entries {
		out[i].Display = e.display
		uri, err := lspcmd.LspToURI(base, e.loc.URI)
		if err != nil {
			continue
		}
		out[i].URI = uri
		out[i].Range = e.loc.Range
	}
	return out
}

// relPath renders a server file URI as a workspace-relative path for
// display, falling back to the absolute path.
func (d pickDeps) relPath(base workspaceapi.URI, lspURI string) string {
	uri, err := lspcmd.LspToURI(base, lspURI)
	if err != nil {
		return trimFileURI(lspURI)
	}
	root, err := d.fs.URI(".")
	if err != nil {
		return uri.Path()
	}
	return workspaceapi.RelPath(root, uri)
}

// pickerView adapts a location picker to the blocking selection model
// above: Close reports a cancel so the waiting command goroutine
// unblocks when the picker is dismissed without a choice. Its mutex
// stands in for the event loop the extension does not have — handler
// callbacks arrive on the window stream goroutine while the picker's
// asynchronous highlight pass lands via tick — so both run under the
// same lock.
type pickerView struct {
	mu sync.Mutex
	*locationpicker.Picker
	ch        chan<- int
	interrupt term.Interrupter
}

var _ browserapi.Floating = (*pickerView)(nil)

// tick runs deferred highlight work under the view lock, then wakes the
// IDE event loop: the extension has no loop of its own, so without the
// interrupt the preview highlights would render only on the next input
// event.
func (v *pickerView) tick(fn func()) bool {
	v.mu.Lock()
	fn()
	v.mu.Unlock()
	_ = v.interrupt.Interrupt(context.Background())
	return true
}

func (v *pickerView) Handle(ev term.Event) (exit, handled bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.Picker.Handle(ev)
}

func (v *pickerView) Draw(w term.Writer) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.Picker.Draw(w)
}

func (v *pickerView) Resize(w, h int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.Picker.Resize(w, h)
}

func (v *pickerView) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.Picker.Cursor()
}

func (v *pickerView) Selection() (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.Picker.Selection()
}

func (v *pickerView) Dimensions() (int, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.Picker.Dimensions()
}

func (v *pickerView) Close() error {
	select {
	case v.ch <- -1:
	default:
	}
	return nil
}

// diagnosticsPick pulls the diagnostics for the current file, listing
// each as "severity[code] line:col message".
func diagnosticsPick(lsp semanticapi.LSP) pickResult {
	return func(ctx context.Context, cmd textapi.Command) ([]pickEntry, error) {
		if err := requireFile(cmd); err != nil {
			return nil, err
		}
		doc := docParams(cmd)
		report, err := lsp.Diagnostic(ctx, semanticapi.DocumentDiagnosticParams{
			TextDocument: doc,
		})
		if err != nil {
			return nil, fmt.Errorf("document diagnostics: %w", err)
		}
		entries := make([]pickEntry, 0, len(report.Items))
		for _, d := range report.Items {
			entries = append(entries, pickEntry{
				loc: semanticapi.Location{URI: doc.URI, Range: d.Range},
				display: fmt.Sprintf("%s%s %d:%d %s",
					diagnosticSeverityLabel(d.Severity), diagnosticCode(d),
					d.Range.Start.Line+1, d.Range.Start.Character+1,
					flattenMessage(d.Message)),
			})
		}
		return entries, nil
	}
}

func diagnosticSeverityLabel(s semanticapi.DiagnosticSeverity) string {
	switch s {
	case semanticapi.DiagnosticSeverityError:
		return "error"
	case semanticapi.DiagnosticSeverityWarning:
		return "warning"
	case semanticapi.DiagnosticSeverityInformation:
		return "info"
	case semanticapi.DiagnosticSeverityHint:
		return "hint"
	default:
		return "diagnostic"
	}
}

func diagnosticCode(d semanticapi.Diagnostic) string {
	if d.CodeIsInt {
		return fmt.Sprintf("[%d]", d.CodeInt)
	}
	if d.Code != "" {
		return "[" + d.Code + "]"
	}
	return ""
}

// runnableItem is the subset of rust-analyzer's Runnable a location list
// needs: the label to show and the location to jump to.
type runnableItem struct {
	Label    string                    `json:"label"`
	Location *semanticapi.LocationLink `json:"location"`
}

// runnablesPick requests the runnables at the cursor. Rune does not run
// cargo targets from this surface (`rust run` does), so selecting one
// navigates to the target it would run.
func runnablesPick(lsp semanticapi.LSP) pickResult {
	return func(ctx context.Context, cmd textapi.Command) ([]pickEntry, error) {
		if err := requireFile(cmd); err != nil {
			return nil, err
		}
		pos := posParams(cmd).Position
		params := struct {
			TextDocument semanticapi.TextDocumentIdentifier `json:"textDocument"`
			Position     *semanticapi.Position              `json:"position,omitempty"`
		}{TextDocument: docParams(cmd), Position: &pos}
		res, err := execRequest[[]runnableItem](ctx, lsp, "experimental/runnables", params)
		if err != nil {
			return nil, err
		}
		return runnableEntries(res), nil
	}
}

// relatedTestsPick requests the tests covering the symbol at the cursor.
func relatedTestsPick(lsp semanticapi.LSP) pickResult {
	return func(ctx context.Context, cmd textapi.Command) ([]pickEntry, error) {
		if err := requireFile(cmd); err != nil {
			return nil, err
		}
		res, err := execRequest[[]struct {
			Runnable runnableItem `json:"runnable"`
		}](ctx, lsp, "rust-analyzer/relatedTests", posParams(cmd))
		if err != nil {
			return nil, err
		}
		items := make([]runnableItem, len(res))
		for i, r := range res {
			items[i] = r.Runnable
		}
		return runnableEntries(items), nil
	}
}

// runnableEntries drops runnables the server did not locate: without a
// location there is nothing for the picker to preview or jump to.
func runnableEntries(items []runnableItem) []pickEntry {
	entries := make([]pickEntry, 0, len(items))
	for _, r := range items {
		if r.Location == nil {
			continue
		}
		entries = append(entries, pickEntry{
			loc: semanticapi.Location{
				URI:   r.Location.TargetURI,
				Range: r.Location.TargetSelectionRange,
			},
			display: r.Label,
		})
	}
	return entries
}

// dependenciesPick requests fetchDependencyList and lists each crate as
// "name version", jumping to the crate manifest.
func dependenciesPick(lsp semanticapi.LSP, fs workspaceapi.FileSystem) pickResult {
	return func(ctx context.Context, _ textapi.Command) ([]pickEntry, error) {
		res, err := execRequest[struct {
			Crates []struct {
				Name    string `json:"name"`
				Version string `json:"version"`
				Path    string `json:"path"`
			} `json:"crates"`
		}](ctx, lsp, "rust-analyzer/fetchDependencyList", struct{}{})
		if err != nil {
			return nil, err
		}
		entries := make([]pickEntry, 0, len(res.Crates))
		for _, c := range res.Crates {
			if c.Path == "" {
				continue
			}
			display := c.Name
			if c.Version != "" {
				display += " " + c.Version
			}
			entries = append(entries, pickEntry{
				loc:     semanticapi.Location{URI: crateFileURI(fs, c.Path)},
				display: display,
			})
		}
		return entries, nil
	}
}

// crateFileURI normalizes fetchDependencyList's crate path — newer
// servers report a file:// URL, older ones a plain path — and points it
// at the crate root directory's manifest when present, since a
// directory can neither be previewed nor opened as a buffer.
func crateFileURI(fs workspaceapi.FileSystem, raw string) string {
	path := strings.TrimPrefix(raw, "file://")
	if info, err := fs.Stat(path); err == nil && info.IsDir() {
		manifest := path + "/Cargo.toml"
		if _, err := fs.Stat(manifest); err == nil {
			path = manifest
		}
	}
	return "file://" + path
}

// flattenMessage collapses a multi-line server message onto the single
// line a picker row renders.
func flattenMessage(msg string) string {
	return strings.Join(strings.Fields(msg), " ")
}
