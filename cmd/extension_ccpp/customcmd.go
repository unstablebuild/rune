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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"google.golang.org/grpc/status"
	mdcomp "unstable.build/rune/internal/component/markdown"
	mdhandler "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

var errNoFile = errors.New("no file open; open a C or C++ file first")

// switchSourceHeaderCmd opens the header of a source file or the source
// of a header, as clangd pairs them.
type switchSourceHeaderCmd struct {
	d actionDeps
}

var _ textapi.CommandHandler = (*switchSourceHeaderCmd)(nil)

func (c *switchSourceHeaderCmd) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if cmd.Resource == nil {
		return errNoFile
	}
	target, err := execRequest[string](ctx, c.d.lsp, "textDocument/switchSourceHeader",
		lspcmd.TextDocID(cmd.URI))
	if err != nil {
		return err
	}
	if target == "" {
		_, _ = c.d.notify.Notify(browserapi.LevelInfo,
			"No source or header pairs with %s; clangd pairs files by name, so a.h goes with a.c or a.cpp",
			cmd.URI.Name())
		return nil
	}
	uri, err := lspcmd.LspToURI(cmd.URI, target)
	if err != nil {
		return err
	}
	win, err := c.d.wm.Focus()
	if err != nil {
		return err
	}
	h, err := c.d.opener.Open(uri)
	if err != nil {
		return fmt.Errorf("open %s: %w", uri.Name(), err)
	}
	// The window content must be the opener's handler: the browser
	// recognizes only its own tokens.
	if err := c.d.wm.SetWindowContent(win, h); err != nil && !errors.Is(err, browserapi.ErrTabNotFree) {
		return err
	}
	return nil
}

func (c *switchSourceHeaderCmd) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// astParams is the params of clangd's textDocument/ast request.
type astParams struct {
	TextDocument semanticapi.TextDocumentIdentifier `json:"textDocument"`
	Range        *semanticapi.Range                 `json:"range,omitempty"`
}

// astNode is a node of the clang AST as clangd reports it.
type astNode struct {
	Role     string             `json:"role"`
	Kind     string             `json:"kind"`
	Detail   string             `json:"detail,omitempty"`
	Arcana   string             `json:"arcana,omitempty"`
	Range    *semanticapi.Range `json:"range,omitempty"`
	Children []astNode          `json:"children,omitempty"`
}

// astCmd shows the clang AST of the selection, or of the smallest node
// at the cursor.
type astCmd struct {
	d actionDeps
}

var _ textapi.CommandHandler = (*astCmd)(nil)

func (c *astCmd) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if cmd.Resource == nil {
		return errNoFile
	}
	rng := selectionOrCursor(c.d, cmd)
	node, err := execRequest[*astNode](ctx, c.d.lsp, "textDocument/ast",
		astParams{TextDocument: lspcmd.TextDocID(cmd.URI), Range: &rng})
	if err != nil {
		return err
	}
	if node == nil {
		_, _ = c.d.notify.Notify(browserapi.LevelInfo,
			"No AST node covers the selection; clangd reports none while the file has a parse error")
		return nil
	}
	var b strings.Builder
	renderAST(&b, *node, 0)
	return showMarkdown(c.d.wm, fencedCode(b.String(), ""))
}

func (c *astCmd) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// renderAST writes node and its children as indented `kind detail
// [range]` lines, ranges in 1-based line:column form.
func renderAST(b *strings.Builder, node astNode, depth int) {
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteString(node.Kind)
	if node.Detail != "" {
		b.WriteByte(' ')
		b.WriteString(node.Detail)
	}
	if node.Range != nil {
		fmt.Fprintf(b, " [%d:%d-%d:%d]",
			node.Range.Start.Line+1, node.Range.Start.Character+1,
			node.Range.End.Line+1, node.Range.End.Character+1)
	}
	b.WriteByte('\n')
	for _, child := range node.Children {
		renderAST(b, child, depth+1)
	}
}

// symbolDetails is an entry of clangd's textDocument/symbolInfo answer.
type symbolDetails struct {
	Name          string `json:"name"`
	ContainerName string `json:"containerName"`
	USR           string `json:"usr"`
	ID            string `json:"id,omitempty"`
}

// symbolInfoCmd shows how clangd identifies the symbol at the cursor.
type symbolInfoCmd struct {
	d actionDeps
}

var _ textapi.CommandHandler = (*symbolInfoCmd)(nil)

func (c *symbolInfoCmd) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if cmd.Resource == nil {
		return errNoFile
	}
	params := semanticapi.TextDocumentPositionParams{
		TextDocument: lspcmd.TextDocID(cmd.URI),
		Position:     lspcmd.CoordToPos(cmd.Cursor.Content),
	}
	syms, err := execRequest[[]symbolDetails](ctx, c.d.lsp, "textDocument/symbolInfo", params)
	if err != nil {
		return err
	}
	if len(syms) == 0 {
		_, _ = c.d.notify.Notify(browserapi.LevelInfo, "No symbol at the cursor")
		return nil
	}
	var b strings.Builder
	for i, s := range syms {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "- **Name:** `%s`\n", s.Name)
		if s.ContainerName != "" {
			fmt.Fprintf(&b, "- **Container:** `%s`\n", s.ContainerName)
		}
		fmt.Fprintf(&b, "- **USR:** `%s`\n", s.USR)
		if s.ID != "" {
			fmt.Fprintf(&b, "- **ID:** `%s`\n", s.ID)
		}
	}
	return showMarkdown(c.d.wm, b.String())
}

func (c *symbolInfoCmd) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// memoryTree is clangd's $/memoryUsage answer: every node carries its
// own size under _self and the size of its subtree under _total, beside
// its named children.
type memoryTree struct {
	Self     int64
	Total    int64
	Children map[string]memoryTree
}

func (t *memoryTree) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.Children = map[string]memoryTree{}
	for name, value := range raw {
		switch name {
		case "_self":
			if err := json.Unmarshal(value, &t.Self); err != nil {
				return err
			}
		case "_total":
			if err := json.Unmarshal(value, &t.Total); err != nil {
				return err
			}
		default:
			var child memoryTree
			if err := json.Unmarshal(value, &child); err != nil {
				return err
			}
			t.Children[name] = child
		}
	}
	return nil
}

// memoryUsageCmd shows what clangd holds in memory, largest subtree
// first.
type memoryUsageCmd struct {
	d actionDeps
}

var _ textapi.CommandHandler = (*memoryUsageCmd)(nil)

func (c *memoryUsageCmd) HandleCommand(ctx context.Context, _ textapi.Command) error {
	tree, err := execRequest[*memoryTree](ctx, c.d.lsp, "$/memoryUsage", nil)
	if err != nil {
		return err
	}
	if tree == nil {
		_, _ = c.d.notify.Notify(browserapi.LevelInfo, "clangd is not running")
		return nil
	}
	var b strings.Builder
	renderMemory(&b, "clangd", *tree, 0)
	return showMarkdown(c.d.wm, fencedCode(b.String(), ""))
}

func (c *memoryUsageCmd) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// renderMemory writes the tree as indented `name total (self)` lines,
// children ordered by total descending.
func renderMemory(b *strings.Builder, name string, t memoryTree, depth int) {
	fmt.Fprintf(b, "%s%s %s", strings.Repeat("  ", depth), name, humanBytes(t.Total))
	if t.Self > 0 && t.Self != t.Total {
		fmt.Fprintf(b, " (self %s)", humanBytes(t.Self))
	}
	b.WriteByte('\n')
	names := make([]string, 0, len(t.Children))
	for n := range t.Children {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		ti, tj := t.Children[names[i]].Total, t.Children[names[j]].Total
		if ti != tj {
			return ti > tj
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		renderMemory(b, n, t.Children[n], depth+1)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// selectionOrCursor returns the live selection of cmd's file, clamped to
// its buffer, or the cursor as a collapsed range.
func selectionOrCursor(d actionDeps, cmd textapi.Command) semanticapi.Range {
	if rng, ok := d.sel.Get(cmd.URI); ok {
		return lspcmd.ClampRange(rng, d.editor, cmd.Resource)
	}
	pos := lspcmd.CoordToPos(cmd.Cursor.Content)
	return semanticapi.Range{Start: pos, End: pos}
}

// execRequest sends a clangd request the SDK has no method for and
// decodes its answer into T, leaving T zero for null. Targeting clangd by
// server ID keeps the request from other languages' servers, which would
// fail it.
func execRequest[T any](ctx context.Context, lsp semanticapi.LSP, method string, params any) (T, error) {
	var zero T
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return zero, fmt.Errorf("marshal %s params: %w", method, err)
		}
		raw = data
	}
	result, err := lsp.ExecuteRequest(ctx, semanticapi.ExecuteRequestParams{
		Method:   method,
		Params:   raw,
		ServerID: ccppLanguageID,
	})
	if err != nil {
		return zero, fmt.Errorf("%s: %s", method, lspErrorMessage(err))
	}
	if len(result) == 0 || string(result) == "null" {
		return zero, nil
	}
	var out T
	if err := json.Unmarshal(result, &out); err != nil {
		return zero, fmt.Errorf("decode %s result: %w", method, err)
	}
	return out, nil
}

// lspErrorMessage extracts the server-reported message, which gRPC's
// status rendering ("rpc error: code = Unknown desc = ...") buries.
func lspErrorMessage(err error) string {
	if s, ok := status.FromError(err); ok {
		return s.Message()
	}
	return err.Error()
}

// showMarkdown floats a scrollable, dismiss-on-key markdown view of
// source.
func showMarkdown(wm browserapi.WindowManager, source string) error {
	comp, err := mdcomp.New(source)
	if err != nil {
		return fmt.Errorf("render markdown: %w", err)
	}
	mdh := mdhandler.New(comp)
	span := handler.NewSpan(mdh, component.SpanConfig{
		PadHorizontal:    2,
		ContentAlignment: component.AlignmentCentered,
	})
	view := browserapi.FuncFloatingHandler(span, mdh.Close)
	if _, err := wm.Floating(view, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	}); err != nil {
		return fmt.Errorf("show viewer: %w", err)
	}
	return nil
}

// fencedCode wraps plain server output in a fenced code block tagged
// with lang, so the markdown viewer renders it verbatim. The fence is
// longer than any backtick run in text.
func fencedCode(text, lang string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	fence := strings.Repeat("`", max(longest+1, 3))
	return fence + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + fence
}
