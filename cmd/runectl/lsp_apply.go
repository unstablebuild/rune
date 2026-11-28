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
	"log/slog"
	"sort"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// fileEdit groups text edits for a single file URI.
type fileEdit struct {
	URI   string
	Edits []semanticapi.TextEdit
}

// textEditsToFileEdits wraps a flat slice of TextEdits into a single
// fileEdit.
func textEditsToFileEdits(uri string, edits []semanticapi.TextEdit) []fileEdit {
	if len(edits) == 0 {
		return nil
	}
	return []fileEdit{{URI: uri, Edits: edits}}
}

// workspaceEditToFileEdits converts a WorkspaceEdit into a slice of
// fileEdit, one per affected URI.
func workspaceEditToFileEdits(edit *semanticapi.WorkspaceEdit) []fileEdit {
	if edit == nil {
		return nil
	}
	var out []fileEdit
	for uri, edits := range edit.Changes {
		if len(edits) > 0 {
			out = append(out, fileEdit{
				URI:   uri,
				Edits: edits,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].URI < out[j].URI
	})
	return out
}

// applyEditsToString applies a set of TextEdits to the given content
// string and returns the result. Edits are applied in reverse position
// order to preserve offsets.
func applyEditsToString(content string, edits []semanticapi.TextEdit) string {
	lines := strings.SplitAfter(content, "\n")
	slog.Debug("applyEditsToString", "content_len", len(content),
		"num_lines", len(lines), "num_edits", len(edits))

	// Sort edits in reverse order so earlier offsets
	// are not invalidated.
	sorted := make([]semanticapi.TextEdit, len(edits))
	copy(sorted, edits)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i].Range.Start, sorted[j].Range.Start
		if a.Line != b.Line {
			return a.Line > b.Line
		}
		return a.Character > b.Character
	})

	for i, e := range sorted {
		slog.Debug("applying edit", "index", i,
			"start_line", e.Range.Start.Line, "start_char", e.Range.Start.Character,
			"end_line", e.Range.End.Line, "end_char", e.Range.End.Character,
			"new_text", e.NewText, "new_text_len", len(e.NewText))
		lines = applyOneEdit(lines, e)
	}
	result := strings.Join(lines, "")
	slog.Debug("applyEditsToString result", "result_len", len(result))
	return result
}

func applyOneEdit(lines []string, e semanticapi.TextEdit) []string {
	startLine := int(e.Range.Start.Line)
	startChar := int(e.Range.Start.Character)
	endLine := int(e.Range.End.Line)
	endChar := int(e.Range.End.Character)

	// Clamp to bounds.
	if startLine >= len(lines) {
		slog.Debug("applyOneEdit: clamping startLine", "from", startLine, "to", len(lines)-1)
		startLine = len(lines) - 1
	}
	if endLine >= len(lines) {
		slog.Debug("applyOneEdit: clamping endLine", "from", endLine, "to", len(lines)-1)
		endLine = len(lines) - 1
	}

	prefix := ""
	if startChar <= len(lines[startLine]) {
		prefix = lines[startLine][:startChar]
	} else {
		slog.Debug("applyOneEdit: startChar beyond line", "startChar", startChar,
			"line_len", len(lines[startLine]), "line_content", lines[startLine])
	}
	suffix := ""
	if endChar <= len(lines[endLine]) {
		suffix = lines[endLine][endChar:]
	} else {
		slog.Debug("applyOneEdit: endChar beyond line", "endChar", endChar,
			"line_len", len(lines[endLine]), "line_content", lines[endLine])
	}

	replacement := prefix + e.NewText + suffix
	newLines := strings.SplitAfter(replacement, "\n")

	slog.Debug("applyOneEdit", "startLine", startLine, "startChar", startChar,
		"endLine", endLine, "endChar", endChar, "prefix", prefix, "suffix", suffix,
		"replacement_len", len(replacement), "new_lines_count", len(newLines))

	result := make([]string, 0, len(lines)-(endLine-startLine+1)+len(newLines))
	result = append(result, lines[:startLine]...)
	result = append(result, newLines...)
	if endLine+1 < len(lines) {
		result = append(result, lines[endLine+1:]...)
	}
	return result
}

// applyFileEdits applies the given file edits through the editor API.
func (a *app) applyFileEdits(ctx context.Context, edits []fileEdit) error {
	for _, fe := range edits {
		h, err := getEditorHandler(ctx, a, fe.URI)
		if err != nil {
			return err
		}
		w, err := a.getWorkspace()
		if err != nil {
			return err
		}
		editor := w.Editor(ctx).CellEditor(h)
		// Sort in reverse position order.
		sorted := make([]semanticapi.TextEdit, len(fe.Edits))
		copy(sorted, fe.Edits)
		sort.Slice(sorted, func(i, j int) bool {
			a, b := sorted[i].Range.Start, sorted[j].Range.Start
			if a.Line != b.Line {
				return a.Line > b.Line
			}
			return a.Character > b.Character
		})
		for _, e := range sorted {
			start := term.Coordinates{
				X: int(e.Range.Start.Character),
				Y: int(e.Range.Start.Line),
			}
			end := term.Coordinates{
				X: int(e.Range.End.Character),
				Y: int(e.Range.End.Line),
			}
			_, _, _, err = editor.Edit(ctx, start, end, e.NewText)
			if err != nil {
				return fmt.Errorf("edit %s: %w", fe.URI, err)
			}
		}
	}
	return nil
}

// getFileContent returns the current text content of a file identified by its URI.
func (a *app) getFileContent(ctx context.Context, uri string) (string, error) {
	slog.Debug("getFileContent", "uri", uri)
	h, err := getEditorHandler(ctx, a, uri)
	if err != nil {
		return "", err
	}
	w, err := a.getWorkspace()
	if err != nil {
		return "", err
	}
	cells, err := w.Editor(ctx).CellView(h).RawCells()
	if err != nil {
		return "", err
	}
	content := term.CellsToString(cells)
	slog.Debug("getFileContent result", "uri", uri, "content_len", len(content))
	return content, nil
}

// dryRunFileEdits prints a unified diff of the edits without applying them.
func (a *app) dryRunFileEdits(ctx context.Context, edits []fileEdit, color bool) error {
	slog.Debug("dryRunFileEdits", "num_files", len(edits), "color", color)
	var diffs []fileDiff
	for _, fe := range edits {
		slog.Debug("dryRunFileEdits: processing", "uri", fe.URI, "edits", fe.Edits)
		content, err := a.getFileContent(ctx, fe.URI)
		if err != nil {
			return err
		}
		newContent := applyEditsToString(content, fe.Edits)
		diffs = append(diffs, fileDiff{
			URI:     fe.URI,
			OldText: content,
			NewText: newContent,
		})
	}
	out := formatFileDiffs(diffs, color)
	if out == "" {
		fmt.Println("no changes")
		return nil
	}
	fmt.Print(out)
	return nil
}

// handleEdits is the main entry point for all commands that return edits.
// When dryRun is true it prints a unified diff; otherwise it applies the edits.
func (a *app) handleEdits(
	ctx context.Context, edits []fileEdit,
	dryRun, noColor bool,
) error {
	if len(edits) == 0 {
		fmt.Println("no edits")
		return nil
	}
	if dryRun {
		return a.dryRunFileEdits(ctx, edits, !noColor)
	}
	return a.applyFileEdits(ctx, edits)
}
