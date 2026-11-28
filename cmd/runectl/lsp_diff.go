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
	"fmt"
	"log/slog"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

const (
	ansiReset = "\033[0m"
	ansiRed   = "\033[31m"
	ansiGreen = "\033[32m"
	ansiCyan  = "\033[36m"
	ansiBold  = "\033[1m"
)

// fileDiff holds the before/after text for a single file.
type fileDiff struct {
	URI     string
	OldText string
	NewText string
}

// uriToPath strips the file:// scheme from a URI for display purposes.
// It handles both file:///path (RFC 8089) and file://host/path forms.
func uriToPath(uri string) string {
	if strings.HasPrefix(uri, "file:///") {
		return uri[len("file://"):]
	}
	return strings.TrimPrefix(uri, "file://")
}

// formatUnifiedDiff produces a unified diff string for a single file.
// When color is true, ANSI escape codes are applied to the output.
func formatUnifiedDiff(uri, oldText, newText string, color bool) string {
	slog.Debug("formatUnifiedDiff", "uri", uri,
		"old", oldText, "new", newText, "color", color)
	if oldText == newText {
		slog.Debug("formatUnifiedDiff: no changes")
		return ""
	}

	dmp := diffmatchpatch.New()
	a, b, lines := dmp.DiffLinesToRunes(oldText, newText)
	diffs := dmp.DiffMainRunes(a, b, false)
	diffs = dmp.DiffCharsToLines(diffs, lines)
	path := uriToPath(uri)
	aPath := "a" + path
	bPath := "b" + path
	if !strings.HasPrefix(path, "/") {
		aPath = "a/" + path
		bPath = "b/" + path
	}
	var sb strings.Builder

	// File headers.
	if color {
		fmt.Fprintf(&sb, "%s%s--- %s%s\n",
			ansiBold, ansiRed, aPath, ansiReset)
		fmt.Fprintf(&sb, "%s%s+++ %s%s\n",
			ansiBold, ansiGreen, bPath, ansiReset)
	} else {
		fmt.Fprintf(&sb, "--- %s\n", aPath)
		fmt.Fprintf(&sb, "+++ %s\n", bPath)
	}

	// Build line-level hunks with context.
	var allLines []diffLine
	for _, d := range diffs {
		dl := splitDiffLines(d.Text)
		for _, l := range dl {
			allLines = append(allLines, diffLine{
				op: d.Type, text: l,
			})
		}
	}

	const contextLines = 3
	hunks := buildHunks(allLines, contextLines)
	for _, h := range hunks {
		writeHunk(&sb, h, color)
	}

	return sb.String()
}

type diffLine struct {
	op   diffmatchpatch.Operation
	text string
}

// splitDiffLines splits text into individual lines,
// preserving trailing newlines as part of each line.
func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	// SplitAfter may produce an empty trailing element
	// when the text ends with \n.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type hunk struct {
	oldStart int
	oldCount int
	newStart int
	newCount int
	lines    []hunkLine
}

type hunkLine struct {
	op   diffmatchpatch.Operation
	text string
}

// buildHunks groups diff lines into hunks with the given number of context lines.
func buildHunks(lines []diffLine, ctx int) []hunk {
	if len(lines) == 0 {
		return nil
	}

	// Find ranges of changed lines.
	type changeRange struct{ start, end int }
	var changes []changeRange
	for i, l := range lines {
		if l.op != diffmatchpatch.DiffEqual {
			if len(changes) > 0 &&
				i-changes[len(changes)-1].end <= 2*ctx {
				changes[len(changes)-1].end = i + 1
			} else {
				changes = append(changes,
					changeRange{i, i + 1})
			}
		}
	}

	if len(changes) == 0 {
		return nil
	}

	var hunks []hunk
	for _, cr := range changes {
		start := cr.start - ctx
		if start < 0 {
			start = 0
		}
		end := cr.end + ctx
		if end > len(lines) {
			end = len(lines)
		}

		var h hunk
		oldLine := 1
		newLine := 1

		// Count lines before the hunk to get starting
		// line numbers.
		for i := 0; i < start; i++ {
			switch lines[i].op {
			case diffmatchpatch.DiffEqual:
				oldLine++
				newLine++
			case diffmatchpatch.DiffDelete:
				oldLine++
			case diffmatchpatch.DiffInsert:
				newLine++
			}
		}

		h.oldStart = oldLine
		h.newStart = newLine

		for i := start; i < end; i++ {
			h.lines = append(h.lines, hunkLine{
				op:   lines[i].op,
				text: lines[i].text,
			})
			switch lines[i].op {
			case diffmatchpatch.DiffEqual:
				h.oldCount++
				h.newCount++
			case diffmatchpatch.DiffDelete:
				h.oldCount++
			case diffmatchpatch.DiffInsert:
				h.newCount++
			}
		}

		hunks = append(hunks, h)
	}

	return hunks
}

// writeHunk writes a single hunk to the builder.
func writeHunk(sb *strings.Builder, h hunk, color bool) {
	// Hunk header.
	header := fmt.Sprintf("@@ -%d,%d +%d,%d @@",
		h.oldStart, h.oldCount, h.newStart, h.newCount)
	if color {
		fmt.Fprintf(sb, "%s%s%s\n", ansiCyan, header, ansiReset)
	} else {
		fmt.Fprintln(sb, header)
	}

	for _, l := range h.lines {
		text := strings.TrimRight(l.text, "\n")
		switch l.op {
		case diffmatchpatch.DiffEqual:
			fmt.Fprintf(sb, " %s\n", text)
		case diffmatchpatch.DiffDelete:
			if color {
				fmt.Fprintf(sb, "%s-%s%s\n",
					ansiRed, text, ansiReset)
			} else {
				fmt.Fprintf(sb, "-%s\n", text)
			}
		case diffmatchpatch.DiffInsert:
			if color {
				fmt.Fprintf(sb, "%s+%s%s\n",
					ansiGreen, text, ansiReset)
			} else {
				fmt.Fprintf(sb, "+%s\n", text)
			}
		}
	}
}

// formatFileDiffs produces a unified diff string for multiple files.
func formatFileDiffs(diffs []fileDiff, color bool) string {
	var sb strings.Builder
	for _, d := range diffs {
		sb.WriteString(formatUnifiedDiff(d.URI, d.OldText, d.NewText, color))
	}
	return sb.String()
}
