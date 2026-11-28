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
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

func newSearchCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search the workspace's files through its indexes",
	}
	cmd.AddCommand(
		newSearchRegexpCmd(a),
		newSearchSemanticCmd(a),
	)
	return cmd
}

func newSearchRegexpCmd(a *app) *cobra.Command {
	var (
		format     string
		ignoreCase bool
		literal    bool
		roots      []string
		include    []string
		limit      int
		maxCount   int
	)

	cmd := &cobra.Command{
		Use:   "regexp <pattern>",
		Short: "Search the workspace's files for lines matching a regular expression",
		Long: `Search the workspace's files for lines matching a Go regular expression,
answered from the index the workspace keeps of them, and print each line
as grep -n does:

  runectl search regexp 'func \w+Retry'
  internal/retry/backoff.go:42:func parseRetry(spec string) {

^ and $ match at the start and end of each line. Files the workspace
hides from search (hidden, ignored or excluded) are skipped, unless
--root names them or a folder that holds them, as in ripgrep:

  runectl search regexp -r .github runs-on

-F takes table, json, or a Go template over Path, Line, Text and Spans,
the [start, end) byte offsets of the matches in Text.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			if limit < 0 {
				return fmt.Errorf("--limit: must not be negative, got %d", limit)
			}
			if maxCount < 0 {
				return fmt.Errorf("--max-count: must not be negative, got %d", maxCount)
			}
			pattern := args[0]
			if literal {
				pattern = regexp.QuoteMeta(pattern)
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			it, err := w.Search(cmd.Context()).RegexpSearch(cmd.Context(),
				searchapi.RegexpRequest{
					Pattern:    pattern,
					IgnoreCase: ignoreCase,
					Roots:      roots,
					Include:    include,
					Limit:      limit,
					MaxPerFile: maxCount,
				})
			if err != nil {
				return err
			}
			return printRegexpResults(cmd.Context(), format, it)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().BoolVarP(
		&ignoreCase, "ignore-case", "i", false,
		"Match case-insensitively",
	)
	cmd.Flags().BoolVarP(
		&literal, "literal", "Q", false,
		"Match the pattern as literal text, not a regular expression",
	)
	cmd.Flags().StringArrayVarP(
		&roots, "root", "r", nil,
		"Folder or file to search, workspace-relative or absolute inside "+
			"the workspace (repeatable); default the whole workspace",
	)
	cmd.Flags().StringArrayVarP(
		&include, "include", "g", nil,
		"Search only files whose name matches this glob, such as '*.go' "+
			"(repeatable)",
	)
	cmd.Flags().IntVarP(
		&limit, "limit", "n", 0,
		"Maximum number of lines; 0 means no limit",
	)
	cmd.Flags().IntVarP(
		&maxCount, "max-count", "m", 0,
		"Maximum number of lines per file; 0 means no limit",
	)
	return cmd
}

type flatRegexpResult struct {
	Path  string   `json:"path"`
	Line  int      `json:"line"`
	Text  string   `json:"text"`
	Spans [][2]int `json:"spans"`
}

// printRegexpResults prints the lines grep-style when format is empty,
// streaming them as they arrive, and every field otherwise.
func printRegexpResults(
	ctx context.Context, format string,
	results iterator.Iterator[searchapi.RegexpResult],
) error {
	it := iterator.Map(results, func(r searchapi.RegexpResult) flatRegexpResult {
		return flatRegexpResult(r)
	})
	defer func() { _ = it.Close() }()
	if format == "" {
		for r, ok := it.Next(ctx); ok; r, ok = it.Next(ctx) {
			fmt.Printf("%s:%d:%s\n", r.Path, r.Line, r.Text)
		}
		return it.Err()
	}
	return printIterator(ctx, format, it, []string{"Path", "Line", "Text", "Spans"})
}

// searchAlias is the host's alias for the model that answers semantic
// searches: unset, it is the configured local embedding model.
const searchAlias = "search"

// searchModel turns a --model value into the model SemanticSearch is
// called with. A value without '/' is an alias the host resolves;
// anything else must be provider/model.
func searchModel(arg string) (llmapi.ModelEntry, error) {
	if !strings.Contains(arg, "/") {
		if arg == "" {
			return llmapi.ModelEntry{}, errors.New("--model: must not be empty")
		}
		return llmapi.ModelEntry{Name: arg}, nil
	}
	provider, name, err := parseProviderModel(arg)
	if err != nil {
		return llmapi.ModelEntry{}, fmt.Errorf("--model: %w", err)
	}
	return llmapi.ModelEntry{Provider: provider, Name: name}, nil
}

func newSearchSemanticCmd(a *app) *cobra.Command {
	var (
		format     string
		model      string
		roots      []string
		limit      int
		meaning    bool
		prefixLast bool
	)

	cmd := &cobra.Command{
		Use:   "semantic <query...>",
		Short: "Search the workspace's code by meaning and keywords",
		Long: `Search the workspace's code by meaning and keywords, best match first.

Every argument is part of the query, so it needs no quoting:

  runectl search semantic where is the retry backoff computed

A query shaped like an identifier, such as CodeLookup.definition, points
at where that symbol is defined; a leading "code:" is stripped. Put the
query after -- when it starts with a dash.

Results are ranked by meaning with the local embedding model behind the
"search" alias. Until that model is downloaded, or with --meaning=false,
they are ranked by keywords alone and z is 0.

Each result is printed as grep prints a match, path:line:, followed by
its score, its z (how far it stands out by meaning), and the line it
opens at:

  retry/backoff.go:42:   3.14  2.50  func parseRetryBackoff(spec string) {

A notebook cell or a file matched by its name opens at line 1 and shows
its excerpt instead. -F takes a Go template over the fields of -F json;
.Text is the line as written, so this prints exactly what grep -n would:

  runectl search semantic -F '{{.Path}}:{{.Line}}:{{.Text}}' retry backoff

While the workspace is still being indexed, or the model cannot rank by
meaning, results may be missing or worse; why is printed to stderr.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			query := strings.Join(args, " ")
			if strings.TrimSpace(query) == "" {
				return errors.New("empty query")
			}
			if limit < 0 {
				return fmt.Errorf("--limit: must not be negative, got %d", limit)
			}
			entry, err := searchModel(model)
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			it, status, err := w.Search(cmd.Context()).SemanticSearch(cmd.Context(),
				searchapi.SemanticRequest{
					Query:      query,
					Model:      entry,
					Roots:      roots,
					Limit:      limit,
					Meaning:    meaning,
					PrefixLast: prefixLast,
				})
			if err != nil {
				return err
			}
			if status.Message != "" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "runectl: %s\n", status.Message)
			}
			return printSemanticResults(cmd.Context(), format, it)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().StringVarP(
		&model, "model", "m", searchAlias,
		"Model that encodes the query: an alias, or provider/model of a "+
			"local embedding model",
	)
	cmd.Flags().StringArrayVarP(
		&roots, "root", "r", nil,
		"Workspace-relative folder to search in (repeatable); "+
			"default the whole workspace",
	)
	cmd.Flags().IntVarP(
		&limit, "limit", "n", 0,
		"Maximum number of results; 0 means 20",
	)
	cmd.Flags().BoolVar(
		&meaning, "meaning", true,
		"Rank by meaning as well as keywords; --meaning=false ranks by "+
			"keywords alone",
	)
	cmd.Flags().BoolVar(
		&prefixLast, "prefix-last", false,
		"Also match the last query word as a prefix, as while typing it",
	)
	return cmd
}

type flatSemanticResult struct {
	Path        string  `json:"path"`
	Line        int     `json:"line"`
	Text        string  `json:"text"`
	Kind        string  `json:"kind"`
	Score       float64 `json:"score"`
	Z           float64 `json:"z"`
	Cosine      float64 `json:"cosine"`
	KeywordRank int     `json:"keyword_rank"`
	Segment     string  `json:"segment"`
	Loc         int     `json:"loc"`
	LocEnd      int     `json:"loc_end"`
	Focus       int     `json:"focus"`
	Excerpt     string  `json:"excerpt"`
	Modified    int64   `json:"modified"`
	Size        int64   `json:"size"`
}

func flattenSemanticResult(r searchapi.SemanticResult) flatSemanticResult {
	line := 1
	if r.Segment == "lines" {
		line = cmp.Or(r.Focus, r.Loc, 1)
	}
	return flatSemanticResult{
		Path:        r.Path,
		Line:        line,
		Text:        r.Text,
		Kind:        r.Kind,
		Score:       r.Score,
		Z:           r.Z,
		Cosine:      r.Cosine,
		KeywordRank: r.KeywordRank,
		Segment:     r.Segment,
		Loc:         r.Loc,
		LocEnd:      r.LocEnd,
		Focus:       r.Focus,
		Excerpt:     r.Excerpt,
		Modified:    r.Modified,
		Size:        r.Size,
	}
}

var excerptMarks = strings.NewReplacer("«", "", "»", "")

// shown is what the default output prints of the result: its line
// without the indentation, which the score columns before it would only
// widen; else its excerpt without the keyword marks.
func (r flatSemanticResult) shown() string {
	if text := strings.TrimLeft(r.Text, " \t"); text != "" {
		return text
	}
	excerpt := excerptMarks.Replace(r.Excerpt)
	if r.Segment == "cells" {
		return strings.TrimSpace(fmt.Sprintf("[cell %d] %s", r.Loc, excerpt))
	}
	return excerpt
}

// printSemanticResults prints the results grep-style when format is
// empty, the scores aligned after each path:line:, and every field
// otherwise.
func printSemanticResults(
	ctx context.Context, format string,
	results iterator.Iterator[searchapi.SemanticResult],
) error {
	it := iterator.Map(results, flattenSemanticResult)
	defer func() { _ = it.Close() }()
	if format == "" {
		all, err := iterator.ToSlice(ctx, it)
		if err != nil {
			return err
		}
		where := make([]string, len(all))
		width := 0
		for i, r := range all {
			where[i] = fmt.Sprintf("%s:%d:", r.Path, r.Line)
			width = max(width, utf8.RuneCountInString(where[i]))
		}
		for i, r := range all {
			line := fmt.Sprintf("%-*s %6.2f %5.2f  %s",
				width, where[i], r.Score, r.Z, r.shown())
			fmt.Println(strings.TrimRight(line, " "))
		}
		return nil
	}
	return printIterator(ctx, format, it, []string{
		"Path", "Line", "Text", "Kind", "Segment", "Loc", "LocEnd", "Focus",
		"Score", "Z", "Cosine", "KeywordRank", "Excerpt",
		"Modified", "Size",
	})
}
