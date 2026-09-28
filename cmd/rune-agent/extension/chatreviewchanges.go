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

package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"

	"unstable.build/rune/cmd/rune-agent/agent/agentools/applypatch"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/vctrl"
)

const applyPatchToolName = "apply_patch"

// Outcomes an apply_patch invocation can be labeled with. Failed calls
// changed nothing on disk, so they are left out of the review entirely.
const (
	outcomeApplied  = "applied"
	outcomeFailed   = "failed"
	outcomeNoResult = "no result"
	outcomeUnknown  = "unknown"
)

// errNoAppliedChanges is returned by reviewChanges when the transcript
// holds no apply_patch invocation that reached the workspace.
var errNoAppliedChanges = errors.New(
	"this conversation has no applied changes to review")

// errNoChangesRemain is returned when the conversation did patch the
// workspace but the workspace no longer shows any of it.
var errNoChangesRemain = errors.New(
	"none of the changes this conversation applied are in the workspace")

// reviewPatchArgs mirrors the persisted apply_patch argument shape.
// The tool's own struct is unexported in agentools, and the review
// only ever reads the patch payload back out of the transcript.
type reviewPatchArgs struct {
	Patch string `json:"patch"`
}

// diffGutter is the width of the "+", "-" or " " column vctrl.DiffBuffer
// writes ahead of every content line. Source handed to the syntax parser
// has it stripped, and highlight columns are shifted back by it.
const diffGutter = 1

// defaultHunkContextLines is how many surrounding source lines are
// pulled in around each hunk when review_context_lines is unset.
// Patches usually carry a single line of context, which is too little
// to judge a change against.
const defaultHunkContextLines = 3

// currentSource returns the workspace file at path as it stands now.
// Hunks record only the lines the model touched, so the surrounding
// code has to come from disk.
type currentSource func(path string) ([]string, bool)

// workspaceView compares the patches a conversation applied against the
// workspace as it stands now. It pads hunks with the code surrounding
// them today and flags the changes the workspace no longer shows, so a
// patch that was later reverted does not read as if it still holds. A
// zero value checks nothing, which is what callers with no workspace to
// read from want.
type workspaceView struct {
	src     currentSource
	context int
}

// presence is how one fragment of a patch compares to the workspace.
//
// It corroborates the transcript, it does not explain it: a fragment
// can be missing because the change was reverted, but just as well
// because later work rewrote the same lines. Nothing here consults
// version control.
type presence int

const (
	// presenceUnchecked means no workspace was available to compare.
	presenceUnchecked presence = iota
	// presenceOnce means found exactly once, so it can anchor context.
	presenceOnce
	// presenceRepeated means found, but in more than one place.
	presenceRepeated
	// presenceMissing means the workspace does not show the fragment.
	presenceMissing
)

func (p presence) found() bool {
	return p == presenceOnce || p == presenceRepeated
}

// resolveReviewContextLines reads review_context_lines, the number of
// workspace lines shown around each hunk in a changes review. A
// negative width is clamped rather than rejected: zero already means
// "show the hunk exactly as the model wrote it".
func resolveReviewContextLines(pconfig config.Config) int {
	v, err := pconfig.GetInt("review_context_lines")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'review_context_lines' from config", "error", err)
		}
		return defaultHunkContextLines
	}
	return max(0, v)
}

// workspaceSource reads patch paths, which are workspace relative,
// against the agent's working directory. Results are memoised because
// one conversation commonly patches the same file many times.
func workspaceSource(
	fs workspaceapi.FileSystem, cwd workspaceapi.URI,
) currentSource {
	cache := make(map[string][]string)
	return func(path string) ([]string, bool) {
		if lines, ok := cache[path]; ok {
			return lines, lines != nil
		}
		resolved, err := workspaceapi.ExpandPathWithURI(path, cwd)
		if err != nil {
			resolved = path
		}
		data, err := readWorkspaceFile(fs, resolved)
		if err != nil {
			cache[path] = nil
			return nil, false
		}
		lines := splitSourceLines(string(data))
		cache[path] = lines
		return lines, true
	}
}

// splitSourceLines splits file content into lines, dropping the empty
// tail a trailing newline produces so expansion does not render a
// phantom blank line at the end of a file.
func splitSourceLines(content string) []string {
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// changesReview is the aggregate diff of a conversation together with
// the rows introducing each invocation, which the renderer emphasises,
// and the source fragments it can syntax highlight.
type changesReview struct {
	diffs   []diffmatchpatch.Diff
	headers []reviewHeader
	// snippets are gutter-stripped source fragments handed to the
	// syntax parser.
	snippets []reviewSnippet
}

type reviewHeader struct {
	row  int
	text string
}

// reviewSnippet is a run of source lines parsed as one fragment. rows
// maps each snippet line back to the buffer row it was rendered into,
// since a hunk's lines are contiguous in the fragment but not
// necessarily in the buffer.
type reviewSnippet struct {
	uri  workspaceapi.URI
	text string
	rows []int
}

// diffBuilder accumulates line-oriented diff entries, coalescing runs of
// the same operation so the rendered buffer keeps contiguous insertions
// and deletions in a single styled block.
type diffBuilder struct {
	diffs []diffmatchpatch.Diff
	rows  int
}

type reviewInvocation struct {
	outcome    string
	ops        []applypatch.FileOp
	diagnostic string
}

func (b *diffBuilder) line(op diffmatchpatch.Operation, text string) {
	text += "\n"
	b.rows++
	if n := len(b.diffs); n > 0 && b.diffs[n-1].Type == op {
		b.diffs[n-1].Text += text
		return
	}
	b.diffs = append(b.diffs, diffmatchpatch.Diff{Type: op, Text: text})
}

// reviewChanges aggregates the net apply_patch changes persisted in msgs
// into a single ordered diff, then corroborates them against the current
// workspace.
func reviewChanges(
	msgs []llmapi.Message, view workspaceView,
) (changesReview, error) {
	results := make(map[string]string)
	for _, msg := range msgs {
		if msg.Role == llmapi.RoleTool && msg.ToolCallID != "" {
			results[msg.ToolCallID] = msg.Content
		}
	}

	var invocations []reviewInvocation
	for _, msg := range msgs {
		if msg.Role != llmapi.RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.Function.Name != applyPatchToolName {
				continue
			}
			result, ok := results[call.ID]
			outcome := patchOutcome(result, ok)
			if outcome == outcomeFailed {
				continue
			}
			ops, diagnostic := parseReviewOps(call.Function.Arguments)
			invocations = append(invocations, reviewInvocation{
				outcome: outcome, ops: ops, diagnostic: diagnostic,
			})
		}
	}
	if len(invocations) == 0 {
		return changesReview{}, errNoAppliedChanges
	}

	cancelInverseHunks(invocations, view)
	var (
		b      diffBuilder
		review changesReview
		shown  int
	)
	for _, invocation := range invocations {
		ops := invocation.ops
		if invocation.diagnostic == "" {
			ops = view.surviving(ops)
		}
		if len(ops) == 0 && invocation.diagnostic == "" {
			continue
		}
		shown++
		if shown > 1 {
			b.line(diffmatchpatch.DiffEqual, "")
		}
		header := fmt.Sprintf("apply_patch #%d [%s]", shown, invocation.outcome)
		review.headers = append(review.headers,
			reviewHeader{row: b.rows, text: header})
		b.line(diffmatchpatch.DiffEqual, header)
		if invocation.diagnostic != "" {
			b.line(diffmatchpatch.DiffEqual, invocation.diagnostic)
			continue
		}
		review.snippets = append(review.snippets,
			appendPatchDiff(&b, ops)...)
	}
	if shown == 0 {
		return changesReview{}, errNoChangesRemain
	}
	review.diffs = b.diffs
	return review, nil
}

// reviewBuffer renders the aggregate, emphasises each invocation header
// so the boundaries between calls stay visible while scrolling, and
// syntax highlights the source lines when a parser is available.
//
// Attributes are written straight into the cell matrix rather than
// through buffer edits: the buffer is handed to an editor afterwards,
// and styling must not land on its undo stack.
func reviewBuffer(
	ctx context.Context, review changesReview, parser syntaxapi.Parser,
) *cell.Buffer {
	buf := vctrl.DiffBuffer(review.diffs)
	cells := buf.RawCells()
	for _, h := range review.headers {
		vctrl.BoldRow(cells, h.row, len([]rune(h.text)))
	}
	snippets := make([]vctrl.Snippet, 0, len(review.snippets))
	for _, s := range review.snippets {
		snippets = append(snippets,
			vctrl.Snippet{URI: s.uri, Text: s.text, Rows: s.rows})
	}
	vctrl.HighlightSnippets(ctx, cells, snippets, parser, diffGutter)
	return buf
}

// patchOutcome labels an invocation from the tool result the agent
// persisted. Tool messages carry no error flag, so the label is derived
// from the content apply_patch writes on each path.
func patchOutcome(result string, found bool) string {
	if !found {
		return outcomeNoResult
	}
	trimmed := strings.TrimSpace(result)
	switch {
	case strings.HasPrefix(trimmed, "error:"),
		strings.Contains(trimmed, "operations; errors:"):
		return outcomeFailed
	case strings.HasSuffix(trimmed, "operations successfully"):
		return outcomeApplied
	}
	return outcomeUnknown
}

// parseReviewOps returns one persisted patch or a diagnostic to render
// in its place. Parsing is separate from workspace corroboration because
// inverse changes must cancel across the whole conversation first.
func parseReviewOps(arguments string) ([]applypatch.FileOp, string) {
	var args reviewPatchArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, fmt.Sprintf("(invalid arguments: %v)", err)
	}
	patch, err := applypatch.Parse(args.Patch)
	if err != nil {
		return nil, fmt.Sprintf("(invalid patch: %v)", err)
	}
	return patch.Ops, ""
}

type hunkRef struct {
	invocation int
	op         int
	hunk       int
}

// cancelInverseHunks removes old→new/new→old pairs before disk
// corroboration. A unique shared workspace anchor distinguishes the
// same edit from identical text changed elsewhere in the file.
func cancelInverseHunks(invocations []reviewInvocation, view workspaceView) {
	byPath := make(map[string][]hunkRef)
	for i := range invocations {
		for j := range invocations[i].ops {
			op := &invocations[i].ops[j]
			if op.Type != applypatch.OpUpdate || op.MoveTo != "" {
				continue
			}
			for k, hunk := range op.Hunks {
				refs := byPath[op.Path]
				for n := len(refs) - 1; n >= 0; n-- {
					ref := refs[n]
					prior := invocations[ref.invocation].ops[ref.op].Hunks[ref.hunk]
					if !inverseHunks(view, op.Path, prior, hunk) {
						continue
					}
					invocations[ref.invocation].ops[ref.op].Hunks[ref.hunk].Lines = nil
					op.Hunks[k].Lines = nil
					byPath[op.Path] = slices.Delete(refs, n, n+1)
					goto nextHunk
				}
				byPath[op.Path] = append(refs,
					hunkRef{invocation: i, op: j, hunk: k})
			nextHunk:
			}
		}
	}
	for i := range invocations {
		ops := invocations[i].ops[:0]
		for _, op := range invocations[i].ops {
			if op.Type == applypatch.OpUpdate {
				hunks := op.Hunks[:0]
				for _, hunk := range op.Hunks {
					if len(hunk.Lines) > 0 {
						hunks = append(hunks, hunk)
					}
				}
				op.Hunks = hunks
				if len(hunks) == 0 {
					continue
				}
			}
			ops = append(ops, op)
		}
		invocations[i].ops = ops
	}
}

func inverseHunks(view workspaceView, path string, a, b applypatch.Hunk) bool {
	return shareUniqueContextAnchor(view, path, a, b) &&
		slices.Equal(changedLines(a.Lines, applypatch.LineRemove),
			changedLines(b.Lines, applypatch.LineAdd)) &&
		slices.Equal(changedLines(a.Lines, applypatch.LineAdd),
			changedLines(b.Lines, applypatch.LineRemove))
}

func changedLines(lines []applypatch.Line, kind applypatch.LineKind) []string {
	var out []string
	for _, line := range lines {
		if line.Kind == kind {
			out = append(out, line.Content)
		}
	}
	return out
}

func shareUniqueContextAnchor(
	view workspaceView, path string, a, b applypatch.Hunk,
) bool {
	anchors := make(map[string]struct{})
	for _, line := range a.Lines {
		if line.Kind == applypatch.LineContext && strings.TrimSpace(line.Content) != "" {
			anchors[line.Content] = struct{}{}
		}
	}
	for _, line := range b.Lines {
		if line.Kind != applypatch.LineContext || strings.TrimSpace(line.Content) == "" {
			continue
		}
		if _, ok := anchors[line.Content]; !ok {
			continue
		}
		if _, p := view.find(path, []string{line.Content}); p == presenceOnce {
			return true
		}
	}
	return false
}

// appendPatchDiff renders the operations of one apply_patch invocation
// and returns the source fragments it produced.
func appendPatchDiff(
	b *diffBuilder, ops []applypatch.FileOp,
) []reviewSnippet {
	var snippets []reviewSnippet
	for _, op := range ops {
		uri, hasLang := sourceURI(op.Path)
		switch op.Type {
		case applypatch.OpAdd:
			b.line(diffmatchpatch.DiffEqual, "*** Add File: "+op.Path)
			rows := appendPatchLines(b, op.Lines)
			if hasLang {
				snippets = appendSnippet(snippets,
					uri, op.Lines, rows, applypatch.LineRemove)
			}
		case applypatch.OpDelete:
			// The persisted patch carries no old file body, so a
			// deletion can only be reported, not shown.
			b.line(diffmatchpatch.DiffEqual, "*** Delete File: "+op.Path)
		case applypatch.OpUpdate:
			b.line(diffmatchpatch.DiffEqual, "*** Update File: "+op.Path)
			if op.MoveTo != "" {
				b.line(diffmatchpatch.DiffEqual, "*** Move to: "+op.MoveTo)
			}
			for _, hunk := range op.Hunks {
				header := "@@"
				if hunk.ContextHint != "" {
					header += " " + hunk.ContextHint
				}
				b.line(diffmatchpatch.DiffEqual, header)
				lines := hunk.Lines
				rows := appendPatchLines(b, lines)
				if !hasLang {
					continue
				}
				// The post-image covers context and added lines.
				// Removed lines only read as coherent source next to
				// the context they were deleted from, so they need a
				// parse of the pre-image too.
				snippets = appendSnippet(snippets,
					uri, lines, rows, applypatch.LineRemove)
				if hunkHas(lines, applypatch.LineRemove) {
					snippets = appendSnippet(snippets,
						uri, lines, rows, applypatch.LineAdd)
				}
			}
		}
	}
	return snippets
}

// appendPatchLines renders lines and returns the buffer row each one
// landed on.
func appendPatchLines(b *diffBuilder, lines []applypatch.Line) []int {
	rows := make([]int, 0, len(lines))
	for _, l := range lines {
		rows = append(rows, b.rows)
		switch l.Kind {
		case applypatch.LineAdd:
			b.line(diffmatchpatch.DiffInsert, l.Content)
		case applypatch.LineRemove:
			b.line(diffmatchpatch.DiffDelete, l.Content)
		case applypatch.LineContext:
			b.line(diffmatchpatch.DiffEqual, " "+l.Content)
		}
	}
	return rows
}

// appendSnippet gathers one side of a hunk: every line except those of
// the excluded kind, gutter-free, so tree-sitter sees plain source.
func appendSnippet(
	snippets []reviewSnippet, uri workspaceapi.URI,
	lines []applypatch.Line, rows []int, exclude applypatch.LineKind,
) []reviewSnippet {
	s := reviewSnippet{uri: uri}
	var b strings.Builder
	for i, l := range lines {
		if l.Kind == exclude {
			continue
		}
		if len(s.rows) > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l.Content)
		s.rows = append(s.rows, rows[i])
	}
	if len(s.rows) == 0 {
		return snippets
	}
	s.text = b.String()
	return append(snippets, s)
}

func hunkHas(lines []applypatch.Line, kind applypatch.LineKind) bool {
	for _, l := range lines {
		if l.Kind == kind {
			return true
		}
	}
	return false
}

// find reports how block compares to the workspace file at path, and
// where it sits when it occurs exactly once.
func (w workspaceView) find(path string, block []string) (int, presence) {
	if w.src == nil || len(block) == 0 {
		return 0, presenceUnchecked
	}
	file, ok := w.src(path)
	if !ok {
		return 0, presenceMissing
	}
	at, count := countBlock(file, block)
	switch count {
	case 0:
		return 0, presenceMissing
	case 1:
		return at, presenceOnce
	default:
		return at, presenceRepeated
	}
}

// restored reports whether a file the patch deleted is back in the
// workspace.
func (w workspaceView) restored(path string) bool {
	if w.src == nil {
		return false
	}
	_, ok := w.src(path)
	return ok
}

// surviving keeps the operations the workspace still shows and drops
// the rest, each remaining hunk widened by the source around it. A
// review is a list of changes to look at, and a change the workspace
// has no trace of is not one of them: showing it would bury the changes
// that do still hold under diffs of work that has already been undone.
//
// An update whose every hunk is gone drops out along with its hunks, so
// no file heading is left standing over nothing.
func (w workspaceView) surviving(
	ops []applypatch.FileOp,
) []applypatch.FileOp {
	var out []applypatch.FileOp
	for _, op := range ops {
		switch op.Type {
		case applypatch.OpAdd:
			post := image(op.Lines, applypatch.LineRemove)
			if _, p := w.find(op.Path, post); p == presenceMissing {
				continue
			}
		case applypatch.OpDelete:
			if w.restored(op.Path) {
				continue
			}
		case applypatch.OpUpdate:
			// A move leaves the patched content at the new path, so
			// that is where the workspace has to be consulted.
			path := op.Path
			if op.MoveTo != "" {
				path = op.MoveTo
			}
			hunks := make([]applypatch.Hunk, 0, len(op.Hunks))
			for _, hunk := range op.Hunks {
				lines, p := w.reviewHunk(path, hunk.Lines)
				if p == presenceMissing {
					continue
				}
				hunks = append(hunks, applypatch.Hunk{
					ContextHint: hunk.ContextHint, Lines: lines,
				})
			}
			if len(hunks) == 0 {
				continue
			}
			op.Hunks = hunks
		}
		out = append(out, op)
	}
	return out
}

// reviewHunk pads a hunk with the source that surrounds it in the
// workspace today and reports whether the workspace still shows the
// change. Padding needs an unambiguous anchor, so a hunk that no longer
// appears verbatim, or appears more than once, is left exactly as the
// model wrote it rather than padded with lines that may belong
// somewhere else entirely.
func (w workspaceView) reviewHunk(
	path string, lines []applypatch.Line,
) ([]applypatch.Line, presence) {
	post := image(lines, applypatch.LineRemove)
	at, p := w.find(path, post)
	if !hunkHas(lines, applypatch.LineAdd) {
		// A pure deletion leaves only context behind, and context
		// reads the same whether or not the deletion happened. Such a
		// hunk is corroborated in reverse: finding the removed lines
		// still in place means the deletion is not in effect.
		if _, q := w.find(path, image(lines, applypatch.LineAdd)); q.found() {
			p = presenceMissing
		}
	}
	if p != presenceOnce || w.context <= 0 {
		return lines, p
	}
	file, _ := w.src(path)

	before := file[max(0, at-w.context):at]
	end := at + len(post)
	after := file[end:min(len(file), end+w.context)]

	out := make([]applypatch.Line, 0, len(before)+len(lines)+len(after))
	out = append(out, contextLines(before)...)
	out = append(out, lines...)
	return append(out, contextLines(after)...), p
}

// image is one side of a hunk: every line except those of the excluded
// kind. Excluding removals yields the lines the file should hold after
// the patch, excluding additions the lines it held before.
func image(lines []applypatch.Line, exclude applypatch.LineKind) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l.Kind != exclude {
			out = append(out, l.Content)
		}
	}
	return out
}

func contextLines(content []string) []applypatch.Line {
	out := make([]applypatch.Line, len(content))
	for i, c := range content {
		out[i] = applypatch.Line{Kind: applypatch.LineContext, Content: c}
	}
	return out
}

// countBlock reports where block first occurs in file and how many
// times it occurs. The count separates a fragment that is simply gone
// from one that is present but too repetitive to anchor against.
func countBlock(file, block []string) (first, count int) {
	if len(block) == 0 || len(block) > len(file) {
		return 0, 0
	}
	for i := 0; i+len(block) <= len(file); i++ {
		if !slices.Equal(file[i:i+len(block)], block) {
			continue
		}
		if count == 0 {
			first = i
		}
		count++
	}
	return first, count
}

// sourceURI builds the synthetic URI whose extension tells the parser
// which grammar to use. Only the base name matters; nothing is read
// from disk. It reports false when the path carries no language hint.
func sourceURI(path string) (workspaceapi.URI, bool) {
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		return workspaceapi.URI{}, false
	}
	uri, err := workspaceapi.ParseURI("file:///" + url.PathEscape(base))
	if err != nil {
		return workspaceapi.URI{}, false
	}
	return uri, true
}
