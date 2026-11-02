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

// Package searchconsole exposes the `search` REPL command, which searches
// a workspace's code and inspects its search index.
package searchconsole

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/llm/llmsearch"
)

// CommandName is the top-level REPL command exposed by this console.
const CommandName = "search"

const (
	searchLimit = 20
	// weakRatio is the share of the top score below which hits are listed
	// as weaker matches, as DigUp does.
	weakRatio = 0.5
)

var commandManual = textapi.CommandManual{
	Name: CommandName,
	Summary: "Search this workspace's code by meaning and by words. Keyword " +
		"search works at once; meaning search needs llama-server and the " +
		"local embedding model, which `searchsemantic` offers to install. " +
		"The `search` model alias picks the model.",
	Synopsis: "(<query...>|status|reindex)",
	Commands: []textapi.CommandManual{
		{
			Name: "status",
			Summary: "Show what the search index holds: files by state, " +
				"vectors and the model they were made with, and what the " +
				"scan left out.",
		},
		{Name: "reindex", Summary: "Rebuild this workspace's search index from scratch."},
	},
}

const examples = "```\n" +
	"search where is the retry backoff  # rank files by meaning and words\n" +
	"search CodeLookup.definition       # jump to where a symbol is defined\n" +
	"search status                      # files, vectors, what was left out\n" +
	"search reindex                     # rebuild the index from scratch\n" +
	"```\n"

// Manual returns the REPL command manual.
func Manual() textapi.CommandManual { return commandManual }

var errNoSearchIndex = errors.New(
	"this workspace has no search index: search.semantic_enabled is off, or the index failed to open")

// SemanticSearcher answers the console's queries. searchapi.Searcher
// satisfies it.
type SemanticSearcher interface {
	SemanticSearch(ctx context.Context, req searchapi.SemanticRequest) (
		iterator.Iterator[searchapi.SemanticResult], error)
}

// Config configures a Handler. Every field is required.
type Config struct {
	// Searcher answers queries over Root. An error wrapping
	// llmsearch.ErrNoIndex means Root has no index.
	Searcher SemanticSearcher
	// Search holds the workspace search indexes; `status` and `reindex`
	// operate on Root's.
	Search *llmsearch.Service
	// Root is the workspace the console runs in.
	Root workspaceapi.URI
}

// Handler is the dispatcher for the `search` command.
type Handler struct {
	searcher SemanticSearcher
	search   *llmsearch.Service
	root     workspaceapi.URI
}

var _ textapi.REPLHandler = (*Handler)(nil)

// New returns a Handler configured with cfg. It panics if a field is
// missing.
func New(cfg Config) *Handler {
	if cfg.Searcher == nil || cfg.Search == nil || cfg.Root.String() == "" {
		panic("searchconsole: Config needs a searcher, a search service and a root")
	}
	return &Handler{searcher: cfg.Searcher, search: cfg.Search, root: cfg.Root}
}

// HandleCommand satisfies repl.CommandHandler. Anything but a lone
// subcommand is a query.
func (h *Handler) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return h.Help(ctx, nil)
	}
	if len(cmd.Args) == 1 {
		switch cmd.Args[0] {
		case "help":
			return h.Help(ctx, nil)
		case "reindex":
			return h.reindex()
		case "status":
			return h.status()
		}
	}
	return h.query(ctx, strings.Join(cmd.Args, " "))
}

// Complete satisfies repl.CommandHandler.
func (h *Handler) Complete(
	_ context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	if len(args) > 1 {
		return iterator.Empty[string](), nil
	}
	var prefix string
	if len(args) == 1 {
		prefix = args[0]
	}
	var names []string
	for _, c := range commandManual.Commands {
		if strings.HasPrefix(c.Name, prefix) {
			names = append(names, c.Name)
		}
	}
	return iterator.FromSlice(names), nil
}

// Help satisfies textapi.REPLHandler.
func (h *Handler) Help(
	_ context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) > 0 {
		for _, c := range commandManual.Commands {
			if c.Name == args[0] {
				c.Name = CommandName + " " + c.Name
				return markdownOutput(usageMarkdown(c)), nil
			}
		}
	}
	return markdownOutput(usageMarkdown(commandManual) + "\n### Examples\n\n" + examples), nil
}

func (h *Handler) query(ctx context.Context, q string) (iterator.Iterator[component.Responsive], error) {
	it, err := h.searcher.SemanticSearch(ctx,
		searchapi.SemanticRequest{Query: q, Limit: searchLimit, Meaning: true})
	if errors.Is(err, llmsearch.ErrNoIndex) {
		return nil, errNoSearchIndex
	}
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	hits, err := iterator.ToSlice(ctx, it)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if len(hits) == 0 {
		return markdownOutput(fmt.Sprintf("Nothing matches %s", codeSpan(q))), nil
	}
	strong := llmsearch.StrongCount(hits, weakRatio)
	var b strings.Builder
	fmt.Fprintf(&b, "## Search %s\n\n", codeSpan(q))
	for i, hit := range hits {
		if i == strong {
			b.WriteString("\n### Weaker matches\n\n")
		}
		writeHit(&b, hit)
	}
	return markdownOutput(b.String()), nil
}

// writeHit renders hit as a list item: where it is, how it scored, and
// its excerpt.
func writeHit(b *strings.Builder, hit searchapi.SemanticResult) {
	place := hit.Path
	switch hit.Segment {
	case "lines":
		line := hit.Focus
		if line == 0 {
			line = hit.Loc
		}
		place = fmt.Sprintf("%s:%d", hit.Path, line)
	case "cells":
		place = fmt.Sprintf("%s cell %d", hit.Path, hit.Loc)
	}
	fmt.Fprintf(b, "- %s (score %.2f", codeSpan(place), hit.Score)
	if hit.Cosine != 0 {
		fmt.Fprintf(b, ", z %.2f", hit.Z)
	}
	b.WriteString(")")
	if excerpt := strings.NewReplacer("«", "", "»", "").Replace(hit.Excerpt); excerpt != "" {
		b.WriteString(" ")
		b.WriteString(codeSpan(excerpt))
	}
	b.WriteString("\n")
}

// codeSpan renders s as inline code, fenced by more backticks than any
// run of them in s.
func codeSpan(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	fence := strings.Repeat("`", longest+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}

func (h *Handler) index() (*llmsearch.Index, error) {
	idx, ok := h.search.Index(h.root)
	if !ok {
		return nil, errNoSearchIndex
	}
	return idx, nil
}

func (h *Handler) reindex() (iterator.Iterator[component.Responsive], error) {
	idx, err := h.index()
	if err != nil {
		return nil, err
	}
	if err := idx.Reindex(); err != nil {
		return nil, fmt.Errorf("search reindex: %w", err)
	}
	return markdownOutput("Rebuilding the search index of this workspace"), nil
}

func (h *Handler) status() (iterator.Iterator[component.Responsive], error) {
	idx, err := h.index()
	if err != nil {
		return nil, err
	}
	st := idx.Status()
	if !st.Loaded {
		return markdownOutput("The search index is loading"), nil
	}
	var b strings.Builder
	b.WriteString("## Search Index\n\n")
	fmt.Fprintf(&b, "- **Files:** %d indexed, %d pending, %d skipped, %d failed\n",
		st.Files["done"], st.Files["pending"], st.Files["skipped"], st.Files["failed"])
	fmt.Fprintf(&b, "- **Stretches of code:** %d\n", st.Segments)
	if st.VectorSource == "" {
		fmt.Fprintf(&b, "- **Vectors:** none, keyword search only\n")
	} else {
		fmt.Fprintf(&b, "- **Vectors:** %d, made with `%s`\n", st.Vectors, st.VectorSource)
	}
	if st.Fingerprint != "" {
		fmt.Fprintf(&b, "- **Fingerprint:** `%s`\n", st.Fingerprint)
	}
	encoder := st.Encoder
	if encoder == "" {
		encoder = "not used yet"
	}
	fmt.Fprintf(&b, "- **Query encoder:** %s\n", encoder)
	if !st.Scanned {
		b.WriteString("- **Scan:** in progress\n")
	}
	if st.Stalled != "" {
		fmt.Fprintf(&b, "- **Stalled:** %s\n", st.Stalled)
	}
	writeCounts(&b, "Files left out", st.Skipped)
	writeCounts(&b, "Folders left out", st.SkippedFolders)
	return markdownOutput(b.String()), nil
}

func writeCounts(b *strings.Builder, title string, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	fmt.Fprintf(b, "\n### %s\n\n", title)
	for _, reason := range slices.Sorted(maps.Keys(counts)) {
		fmt.Fprintf(b, "- %s: %d\n", reason, counts[reason])
	}
}

func usageMarkdown(man textapi.CommandManual) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## `%s`\n\n", man.Name)
	if man.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", man.Summary)
	}
	if man.Synopsis != "" {
		fmt.Fprintf(&b, "**Usage:** `%s %s`\n\n", man.Name, man.Synopsis)
	}
	if len(man.Commands) > 0 {
		b.WriteString("### Subcommands\n\n")
		for _, c := range man.Commands {
			invocation := c.Name
			if c.Synopsis != "" {
				invocation = c.Name + " " + c.Synopsis
			}
			fmt.Fprintf(&b, "- `%s`\n  %s\n", invocation, c.Summary)
		}
	}
	return b.String()
}

func markdownOutput(content string) iterator.Iterator[component.Responsive] {
	md, err := markdown.New(content)
	if err != nil {
		r := component.NewResponsiveString(content, component.StringResponsiveConfig{})
		return iterator.FromSlice([]component.Responsive{r})
	}
	return iterator.FromSlice([]component.Responsive{md})
}
