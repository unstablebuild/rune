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

// Package regexsearch implements the searchregex command: a search of the
// workspace's files for a regular expression, as grep does, picked from a
// floating list whose preview shows each matching line in its file.
package regexsearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/pflag"
	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/codesearch"
	"unstable.build/rune/internal/handler/finder"
	"unstable.build/rune/internal/handler/locationsearch"
)

// Name is the name of the command.
const Name = "searchregex"

const synopsis = "[-i] [-F] [-r <dir>]... [-n <limit>] [--] <pattern...>"

// defaultLimit is how many matching lines a search without a limit lists.
const defaultLimit = 5000

// Manual returns the manual of the command.
func Manual() textapi.CommandManual {
	return textapi.CommandManual{
		Name: Name,
		Summary: "Search the workspace's files for a regular expression and pick a matching " +
			"line from a floating list, whose preview shows it in its file. A trigram index of " +
			"the workspace finds the files that may match without reading the others. `-i` " +
			"ignores case, `-F` searches for the pattern as plain text, `-r` limits the " +
			"search to a folder (repeatable) and `-n` sets how many lines to list " +
			fmt.Sprintf("(%d by default). ", defaultLimit) +
			"Quote a pattern with a backslash or with several spaces in a row.",
		Synopsis: synopsis,
	}
}

var errUsage = errors.New("usage: " + Name + " " + synopsis)

// Searcher searches the workspace; *codesearch.Index is one.
type Searcher interface {
	RegexpSearch(ctx context.Context, req searchapi.RegexpRequest) (iterator.Iterator[searchapi.RegexpResult], error)
	Status() codesearch.Status
}

// Config is what the command works with. Every field is required.
type Config struct {
	// Root is the workspace searched.
	Root     workspaceapi.URI
	Searcher Searcher
	// Clients are the picker's.
	Clients          finder.Clients
	Parser           syntaxapi.Parser
	ScheduleNextTick func(func()) bool
}

// Handler implements the command as a text.CommandHandler.
type Handler struct {
	cfg Config
}

// New returns the command's handler.
func New(cfg Config) *Handler {
	return &Handler{cfg: cfg}
}

// HandleCommand floats the picker of the lines that match the pattern of
// cmd. It returns right away: the search fills the picker in the
// background.
func (h *Handler) HandleCommand(_ context.Context, cmd textapi.Command) error {
	q, err := parseArgs(cmd.Args, h.cfg.Root)
	if err != nil {
		return err
	}
	return h.open(q)
}

// flagNames are the flags Complete offers.
var flagNames = []string{"--ignore-case", "--fixed-strings", "--root", "--limit"}

// Complete offers the flags.
func (h *Handler) Complete(_ context.Context, cmd textapi.Command) (
	blueiterator.Iterator[string], string, error,
) {
	n := len(cmd.Args)
	if n == 0 || !strings.HasPrefix(cmd.Args[n-1], "-") {
		return blueiterator.FromSlice[string](nil), "", nil
	}
	var names []string
	for _, f := range flagNames {
		if strings.HasPrefix(f, cmd.Args[n-1]) {
			names = append(names, f)
		}
	}
	return blueiterator.FromSlice(names), "", nil
}

// parseArgs reads the command's flags; the rest of its args, joined by
// spaces, are the pattern. Roots may be absolute paths in root.
func parseArgs(args []string, root workspaceapi.URI) (searchapi.RegexpRequest, error) {
	fs := pflag.NewFlagSet(Name, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	ignoreCase := fs.BoolP("ignore-case", "i", false, "ignore case")
	fixed := fs.BoolP("fixed-strings", "F", false, "search for the pattern as plain text")
	roots := fs.StringArrayP("root", "r", nil, "search only in this folder")
	limit := fs.IntP("limit", "n", 0, "list at most this many lines")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return searchapi.RegexpRequest{}, errUsage
		}
		return searchapi.RegexpRequest{}, fmt.Errorf("%s: %w", Name, err)
	}
	if *limit < 0 {
		return searchapi.RegexpRequest{}, fmt.Errorf("%s: --limit must not be negative", Name)
	}
	pattern := strings.Join(fs.Args(), " ")
	if pattern == "" {
		return searchapi.RegexpRequest{}, errUsage
	}
	if *fixed {
		pattern = regexp.QuoteMeta(pattern)
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return searchapi.RegexpRequest{}, fmt.Errorf("%s: %w", Name, err)
	}
	for i, r := range *roots {
		if rel, err := filepath.Rel(root.Path(), r); filepath.IsAbs(r) && err == nil {
			(*roots)[i] = rel
		}
	}
	if *limit == 0 {
		*limit = defaultLimit
	}
	return searchapi.RegexpRequest{Pattern: pattern, IgnoreCase: *ignoreCase, Roots: *roots, Limit: *limit}, nil
}

// open floats the picker of the results of q, which it searches in the
// background. It runs on the event loop.
func (h *Handler) open(q searchapi.RegexpRequest) error {
	win, err := h.cfg.Clients.WindowManager.Focus()
	if err != nil {
		return err
	}
	r := newResults(q, time.Now)
	cfg := locationsearch.DefaultConfig()
	cfg.Source = h.source(r, q)
	cfg.Annotate = r.annotate
	cfg.Label = "REGEXP"
	picker, err := locationsearch.New(context.Background(), h.cfg.Clients, win, cfg,
		h.cfg.Parser, h.cfg.ScheduleNextTick, "", nil)
	if err != nil {
		return err
	}
	if _, err := h.cfg.Clients.WindowManager.Floating(picker, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	}); err != nil {
		_ = picker.Close()
		return err
	}
	return nil
}

// source runs q and lists its matches as they are found, recording them
// in r. Closing the picker cancels ctx.
func (h *Handler) source(r *results, q searchapi.RegexpRequest) func(context.Context) (iterator.Iterator[string], error) {
	return func(ctx context.Context) (iterator.Iterator[string], error) {
		// The picker draws the status of the search, which the end of
		// the search would otherwise not redraw.
		interrupt := func() { _ = h.cfg.Clients.Interrupter.Interrupt(context.Background()) }
		it, err := h.cfg.Searcher.RegexpSearch(ctx, q)
		if err != nil {
			if ctx.Err() == nil {
				r.fail(err)
			}
			interrupt()
			return iterator.FromSlice[string](nil), nil
		}
		return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
			m, ok := it.Next(ctx)
			if ok {
				return r.add(m), true, nil
			}
			switch err := it.Err(); {
			case ctx.Err() != nil:
			case err != nil:
				r.fail(err)
			default:
				r.finish(h.note())
			}
			interrupt()
			return "", false, nil
		}, it.Close), nil
	}
}

// note says why the search may have read every file, if it may have.
func (h *Handler) note() string {
	st := h.cfg.Searcher.Status()
	switch {
	case st.Ready:
		return ""
	case st.Building:
		return "still indexing"
	case st.Err != "":
		return "indexing failed: " + st.Err
	}
	return ""
}
