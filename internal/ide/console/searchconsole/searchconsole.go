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

// Package searchconsole exposes the `search` REPL command, which manages
// the search indexes of a workspace: what each holds, rebuilding it and
// deleting it. The command prompt's search commands query them.
package searchconsole

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/codesearch"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/ide/syntax/symboldb"
	"unstable.build/rune/internal/llm/llmsearch"
)

// CommandName is the top-level REPL command exposed by this console.
const CommandName = "search"

// The indexes the console manages.
const (
	semanticIndex = "semantic"
	symbolsIndex  = "symbols"
	regexIndex    = "regex"
)

// The actions the console takes on an index.
const (
	actionReindex = "reindex"
	actionStatus  = "status"
	actionRemove  = "remove"
)

// actionManuals returns the actions of index, whose status shows what.
func actionManuals(index, what string) []textapi.CommandManual {
	return []textapi.CommandManual{
		{Name: actionReindex, Summary: "Rebuild the " + index + " index of this workspace from scratch."},
		{Name: actionStatus, Summary: "Show " + what + "."},
		{
			Name: actionRemove,
			Summary: "Delete the " + index + " index of this workspace. It is built again the next " +
				"time the workspace opens, unless `search." + index + "_enabled` is off.",
		},
	}
}

var commandManual = textapi.CommandManual{
	Name: CommandName,
	Summary: "Manage this workspace's search indexes: see what one holds, rebuild it or delete it. " +
		"The command prompt searches with them.",
	Synopsis: "(semantic|symbols|regex) (reindex|status|remove)",
	Commands: []textapi.CommandManual{
		{
			Name: semanticIndex,
			Summary: "The index `searchsemantic` ranks code with, by meaning and by words. Meaning " +
				"search needs llama-server and the local embedding model, which `searchsemantic` " +
				"offers to install.",
			Synopsis: "(reindex|status|remove)",
			Commands: actionManuals(semanticIndex,
				"the files indexed by state, the vectors and the model they were made with, and what the scan left out"),
		},
		{
			Name: symbolsIndex,
			Summary: "The symbol index, read with each language's grammar: the functions, " +
				"methods, variables and types `searchfunc`, `searchvar` and `searchtype` list, " +
				"and the qualified names `lsp` commands and the agent look up.",
			Synopsis: "(reindex|status|remove)",
			Commands: actionManuals(symbolsIndex,
				"the files and symbols indexed, the grammars they were read with, and the files without symbols"),
		},
		{
			Name:     regexIndex,
			Summary:  "The trigram index `searchregex` narrows down the files it reads with.",
			Synopsis: "(reindex|status|remove)",
			Commands: actionManuals(regexIndex,
				"the files indexed and how many changed since the index was built"),
		},
	},
}

const examples = "```\n" +
	"search symbols status     # files, symbols, grammars and files without symbols\n" +
	"search semantic reindex   # rebuild the semantic index from scratch\n" +
	"search regex remove       # delete the regex index of this workspace\n" +
	"```\n"

// Manual returns the REPL command manual.
func Manual() textapi.CommandManual { return commandManual }

var (
	errNoSemanticIndex = errors.New("this workspace has no semantic index: " +
		"search.semantic_enabled is off, the index was removed, or it failed to open")
	errNoSymbolsIndex = errors.New("this workspace has no symbols index: search.symbols_enabled is off")
	errNoRegexIndex   = errors.New("this workspace has no regex index: search.regex_enabled is off")
)

// removedError says index was removed and when it comes back.
func removedError(index string) error {
	return fmt.Errorf("the %s index of this workspace was removed: it is built again when the workspace opens", index)
}

// Config configures a Handler. Every field but Regex and Symbols is
// required.
type Config struct {
	// Search holds the semantic indexes of the workspaces; the console
	// manages Root's.
	Search *llmsearch.Service
	// Root is the workspace the console runs in.
	Root workspaceapi.URI
	// Regex is Root's regex index; nil when Root keeps none.
	Regex RegexIndex
	// Symbols is Root's symbols index; nil when Root keeps none.
	Symbols SymbolsIndex
}

// RegexIndex is the trigram index of a workspace; *codesearch.Index is
// one.
type RegexIndex interface {
	Status() codesearch.Status
	Reindex() error
	Remove() error
}

// SymbolsIndex is the symbols index of a workspace; *symboldb.Parser is
// one.
type SymbolsIndex interface {
	Status() symboldb.Status
	Reindex(ctx context.Context, prog symboldb.Progress) error
	Remove(ctx context.Context) error
}

// Handler is the dispatcher for the `search` command.
type Handler struct {
	search  *llmsearch.Service
	root    workspaceapi.URI
	regex   RegexIndex
	symbols SymbolsIndex
}

var _ textapi.REPLHandler = (*Handler)(nil)

// New returns a Handler configured with cfg. It panics if a required
// field is missing.
func New(cfg Config) *Handler {
	if cfg.Search == nil || cfg.Root.String() == "" {
		panic("searchconsole: Config needs a search service and a root")
	}
	return &Handler{search: cfg.Search, root: cfg.Root, regex: cfg.Regex, symbols: cfg.Symbols}
}

// HandleCommand satisfies repl.CommandHandler.
func (h *Handler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	args := cmd.Args
	if len(args) == 0 || args[0] == "help" {
		return h.Help(ctx, nil)
	}
	index, ok := findSubcommand(commandManual, args[0])
	if !ok {
		return nil, fmt.Errorf("unknown index %q: want semantic, symbols or regex", args[0])
	}
	if len(args) == 1 {
		return h.Help(ctx, args)
	}
	if _, ok := findSubcommand(index, args[1]); !ok || len(args) > 2 {
		return nil, fmt.Errorf("usage: %s %s %s", CommandName, index.Name, index.Synopsis)
	}
	switch index.Name {
	case semanticIndex:
		return h.semantic(ctx, args[1])
	case symbolsIndex:
		return h.symbolsAction(ctx, args[1], pw)
	default:
		return h.regexAction(args[1])
	}
}

// Complete satisfies repl.CommandHandler.
func (h *Handler) Complete(
	_ context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	man := commandManual
	for _, arg := range args[:max(len(args)-1, 0)] {
		sub, ok := findSubcommand(man, arg)
		if !ok {
			return iterator.Empty[string](), nil
		}
		man = sub
	}
	var prefix string
	if len(args) > 0 {
		prefix = args[len(args)-1]
	}
	var names []string
	for _, c := range man.Commands {
		if strings.HasPrefix(c.Name, prefix) {
			names = append(names, c.Name)
		}
	}
	return iterator.FromSlice(names), nil
}

// Help satisfies textapi.REPLHandler. It renders the manual of the index
// or action args name, and the whole command's otherwise.
func (h *Handler) Help(
	_ context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	man, path := commandManual, CommandName
	for _, arg := range args {
		sub, ok := findSubcommand(man, arg)
		if !ok {
			break
		}
		man, path = sub, path+" "+sub.Name
	}
	man.Name = path
	out := usageMarkdown(man)
	if path == CommandName {
		out += "\n### Examples\n\n" + examples
	}
	return markdownOutput(out), nil
}

func findSubcommand(man textapi.CommandManual, name string) (textapi.CommandManual, bool) {
	for _, c := range man.Commands {
		if c.Name == name {
			return c, true
		}
	}
	return textapi.CommandManual{}, false
}

func (h *Handler) semantic(ctx context.Context, action string) (iterator.Iterator[component.Responsive], error) {
	idx, ok := h.search.Index(h.root)
	if !ok {
		return nil, errNoSemanticIndex
	}
	switch action {
	case actionReindex:
		if err := idx.Reindex(); err != nil {
			return nil, fmt.Errorf("search semantic reindex: %w", err)
		}
		return markdownOutput("Rebuilding the semantic index of this workspace"), nil
	case actionRemove:
		if err := idx.Remove(ctx); err != nil {
			return nil, fmt.Errorf("search semantic remove: %w", err)
		}
		return markdownOutput(removedMarkdown(semanticIndex,
			"`searchsemantic` has no index to search")), nil
	}
	var b strings.Builder
	writeSemanticStatus(&b, idx.Status())
	return markdownOutput(b.String()), nil
}

func (h *Handler) symbolsAction(
	ctx context.Context, action string, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if h.symbols == nil {
		return nil, errNoSymbolsIndex
	}
	st := h.symbols.Status()
	if action == actionStatus {
		var b strings.Builder
		writeSymbolsStatus(&b, st)
		return markdownOutput(b.String()), nil
	}
	if st.Removed {
		return nil, removedError(symbolsIndex)
	}
	if action == actionRemove {
		if err := h.symbols.Remove(ctx); err != nil {
			return nil, fmt.Errorf("search symbols remove: %w", err)
		}
		return markdownOutput(removedMarkdown(symbolsIndex,
			"`searchfunc`, `searchvar` and `searchtype` parse the files they search, "+
				"and lookups by name scan the workspace")), nil
	}
	if pw == nil {
		pw = repl.NopProgressWriter()
	}
	err := h.symbols.Reindex(ctx, symboldb.REPLProgress(pw))
	if errors.Is(err, symboldb.ErrRemoved) {
		return nil, removedError(symbolsIndex)
	}
	if err != nil {
		return nil, fmt.Errorf("search symbols reindex: %w", err)
	}
	st = h.symbols.Status()
	return markdownOutput(fmt.Sprintf("Rebuilt the symbols index: %d %s in %d %s",
		st.Records, plural(st.Records, "symbol", "symbols"), st.Files, plural(st.Files, "file", "files"))), nil
}

func (h *Handler) regexAction(action string) (iterator.Iterator[component.Responsive], error) {
	if h.regex == nil {
		return nil, errNoRegexIndex
	}
	st := h.regex.Status()
	if action == actionStatus {
		var b strings.Builder
		writeRegexStatus(&b, st)
		return markdownOutput(b.String()), nil
	}
	if st.Removed {
		return nil, removedError(regexIndex)
	}
	if action == actionRemove {
		if err := h.regex.Remove(); err != nil {
			return nil, fmt.Errorf("search regex remove: %w", err)
		}
		return markdownOutput(removedMarkdown(regexIndex, "`searchregex` reads every file")), nil
	}
	err := h.regex.Reindex()
	if errors.Is(err, codesearch.ErrRemoved) {
		return nil, removedError(regexIndex)
	}
	if err != nil {
		return nil, fmt.Errorf("search regex reindex: %w", err)
	}
	return markdownOutput("Rebuilding the regex index of this workspace"), nil
}

// removedMarkdown says index was removed, what searches do meanwhile, and
// how to keep it off.
func removedMarkdown(index, meanwhile string) string {
	return fmt.Sprintf("Removed the %s index of this workspace. Until the workspace opens again, %s. "+
		"To keep it from being built again, turn off `search.%s_enabled`.", index, meanwhile, index)
}

func writeSemanticStatus(b *strings.Builder, st llmsearch.Status) {
	b.WriteString("## Semantic Index\n\n")
	if !st.Loaded {
		b.WriteString("- **Index:** loading\n")
		return
	}
	fmt.Fprintf(b, "- **Files:** %d indexed, %d pending, %d skipped, %d failed\n",
		st.Files["done"], st.Files["pending"], st.Files["skipped"], st.Files["failed"])
	fmt.Fprintf(b, "- **Stretches of code:** %d\n", st.Segments)
	if st.VectorSource == "" {
		fmt.Fprintf(b, "- **Vectors:** none, keyword search only\n")
	} else {
		fmt.Fprintf(b, "- **Vectors:** %d, made with `%s`\n", st.Vectors, st.VectorSource)
	}
	if st.Fingerprint != "" {
		fmt.Fprintf(b, "- **Fingerprint:** `%s`\n", st.Fingerprint)
	}
	encoder := st.Encoder
	if encoder == "" {
		encoder = "not used yet"
	}
	fmt.Fprintf(b, "- **Query encoder:** %s\n", encoder)
	if !st.Scanned {
		b.WriteString("- **Scan:** in progress\n")
	}
	if st.Stalled != "" {
		fmt.Fprintf(b, "- **Stalled:** %s\n", st.Stalled)
	}
	writeCounts(b, "Files left out", st.Skipped)
	writeCounts(b, "Folders left out", st.SkippedFolders)
}

func writeSymbolsStatus(b *strings.Builder, st symboldb.Status) {
	b.WriteString("## Symbols Index\n\n")
	switch {
	case st.Removed:
		b.WriteString("- **Index:** removed: `searchfunc`, `searchvar` and `searchtype` parse the files " +
			"they search, and lookups by name scan the workspace, until the workspace opens again\n")
		return
	case !st.Loaded:
		b.WriteString("- **Index:** loading\n")
		return
	}
	fmt.Fprintf(b, "- **Files:** %d, %d pending\n", st.Files, st.Pending)
	fmt.Fprintf(b, "- **Symbols:** %d\n", st.Records)
	if !st.Scanned {
		b.WriteString("- **Scan:** in progress\n")
	}
	if st.Err != "" {
		fmt.Fprintf(b, "- **Error:** %s\n", st.Err)
	}
	writeCounts(b, "Files without symbols", st.Skipped)
	langs := slices.Sorted(maps.Keys(st.Grammars))
	var installed, missing []string
	for _, lang := range langs {
		if fp := st.Grammars[lang]; fp != "" {
			installed = append(installed, fmt.Sprintf("- %s: `%s`", lang, fp))
		} else {
			missing = append(missing, lang)
		}
	}
	if len(installed) > 0 {
		fmt.Fprintf(b, "\n### Grammars\n\n%s\n", strings.Join(installed, "\n"))
	}
	if len(missing) > 0 {
		fmt.Fprintf(b, "\n**No grammar installed:** %s\n", strings.Join(missing, ", "))
	}
}

func writeRegexStatus(b *strings.Builder, st codesearch.Status) {
	b.WriteString("## Regex Index\n\n")
	switch {
	case st.Removed:
		b.WriteString("- **Index:** removed: `searchregex` reads every file until the workspace opens again\n")
		return
	case st.Ready:
		fmt.Fprintf(b, "- **Files:** %d indexed, %d changed since the index was built\n", st.Files, st.Changed)
	case !st.Building && st.Err == "":
		b.WriteString("- **Index:** loading\n")
	}
	if st.Building {
		b.WriteString("- **Build:** in progress\n")
	}
	if st.Err != "" {
		fmt.Fprintf(b, "- **Error:** %s\n", st.Err)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
