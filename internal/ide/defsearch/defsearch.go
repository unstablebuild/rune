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

// Package defsearch implements the searchfunc, searchvar and searchtype
// commands: a search of the workspace's definitions, picked from a
// floating list whose preview shows each definition in its file.
package defsearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag"
	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/handler/finder"
	"unstable.build/rune/internal/handler/locationsearch"
	"unstable.build/rune/internal/ide/syntax/symboldb"
)

// Names of the commands.
const (
	FuncName = "searchfunc"
	VarName  = "searchvar"
	TypeName = "searchtype"
)

const synopsis = "[-l <lang>]... [-r <dir>]... [-n <limit>]"

// command is what tells the commands apart.
type command struct {
	name  string
	what  string
	label string
	kinds []symboldb.Kind
}

var commands = []command{
	{name: FuncName, what: "functions and methods", label: "FUNCTIONS",
		kinds: []symboldb.Kind{symboldb.KindFunction, symboldb.KindMethod}},
	{name: VarName, what: "variables", label: "VARIABLES", kinds: []symboldb.Kind{symboldb.KindVar}},
	{name: TypeName, what: "types", label: "TYPES", kinds: []symboldb.Kind{symboldb.KindType}},
}

// Manuals returns the manuals of the commands.
func Manuals() []textapi.CommandManual {
	out := make([]textapi.CommandManual, 0, len(commands))
	for _, c := range commands {
		out = append(out, textapi.CommandManual{
			Name: c.name,
			Summary: "Pick one of the " + c.what + " defined in the workspace from a floating " +
				"list, whose preview shows it in its file. The workspace's symbol index, " +
				"kept up to date as files change, lists them at once. `-l` " +
				"limits the search to a language (repeatable), `-r` to a folder " +
				"(repeatable) and `-n` sets how many definitions to list.",
			Synopsis: synopsis,
		})
	}
	return out
}

// Searcher searches the definitions of the workspace; *symboldb.Parser is
// one.
type Searcher interface {
	Definitions(ctx context.Context, req symboldb.DefinitionQuery) (iterator.Iterator[symboldb.Definition], error)
	Status() symboldb.Status
}

// Config is what the commands work with. Every field is required.
type Config struct {
	// Root is the workspace searched.
	Root     workspaceapi.URI
	Searcher Searcher
	// Clients are the picker's.
	Clients          finder.Clients
	Parser           syntaxapi.Parser
	ScheduleNextTick func(func()) bool
}

// Handler implements one of the commands as a text.CommandHandler.
type Handler struct {
	cmd command
	cfg Config
}

// New returns the handler of the command name, one of FuncName, VarName
// and TypeName. It panics on any other name.
func New(name string, cfg Config) *Handler {
	for _, c := range commands {
		if c.name == name {
			return &Handler{cmd: c, cfg: cfg}
		}
	}
	panic("defsearch: unknown command " + name)
}

// HandleCommand floats the picker of the definitions cmd asks for. It
// returns right away: the search fills the picker in the background.
func (h *Handler) HandleCommand(_ context.Context, cmd textapi.Command) error {
	req, err := h.parseArgs(cmd.Args)
	if err != nil {
		return err
	}
	return h.open(req)
}

// flagNames are the flags Complete offers.
var flagNames = []string{"--lang", "--root", "--limit"}

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

func (h *Handler) usage() error {
	return errors.New("usage: " + h.cmd.name + " " + synopsis)
}

// parseArgs reads the command's flags. Roots may be absolute paths in the
// workspace.
func (h *Handler) parseArgs(args []string) (symboldb.DefinitionQuery, error) {
	fs := pflag.NewFlagSet(h.cmd.name, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	langs := fs.StringArrayP("lang", "l", nil, "search only files of this language")
	roots := fs.StringArrayP("root", "r", nil, "search only in this folder")
	limit := fs.IntP("limit", "n", 0, "list at most this many definitions")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return symboldb.DefinitionQuery{}, h.usage()
		}
		return symboldb.DefinitionQuery{}, fmt.Errorf("%s: %w", h.cmd.name, err)
	}
	if fs.NArg() > 0 {
		return symboldb.DefinitionQuery{}, h.usage()
	}
	if *limit < 0 {
		return symboldb.DefinitionQuery{}, fmt.Errorf("%s: --limit must not be negative", h.cmd.name)
	}
	for i, r := range *roots {
		if rel, err := filepath.Rel(h.cfg.Root.Path(), r); filepath.IsAbs(r) && err == nil {
			(*roots)[i] = filepath.ToSlash(rel)
		}
	}
	return symboldb.DefinitionQuery{Kinds: h.cmd.kinds, Langs: *langs, Roots: *roots, Limit: *limit}, nil
}

// open floats the picker of the results of req, which it searches in the
// background. It runs on the event loop.
func (h *Handler) open(req symboldb.DefinitionQuery) error {
	win, err := h.cfg.Clients.WindowManager.Focus()
	if err != nil {
		return err
	}
	r := newResults(req, time.Now)
	cfg := locationsearch.DefaultConfig()
	cfg.Source = h.source(r, req)
	cfg.Annotate = r.annotate
	cfg.Label = h.cmd.label
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

// source runs req and lists its definitions as they are read, recording
// them in r. Closing the picker cancels ctx.
func (h *Handler) source(r *results, req symboldb.DefinitionQuery) func(context.Context) (iterator.Iterator[string], error) {
	return func(ctx context.Context) (iterator.Iterator[string], error) {
		// The picker draws the status of the search, which the end of
		// the search would otherwise not redraw.
		interrupt := func() { _ = h.cfg.Clients.Interrupter.Interrupt(context.Background()) }
		it, err := h.cfg.Searcher.Definitions(ctx, req)
		if err != nil {
			if ctx.Err() == nil {
				r.fail(err)
			}
			interrupt()
			return iterator.FromSlice[string](nil), nil
		}
		return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
			for {
				d, ok := it.Next(ctx)
				if !ok {
					break
				}
				if e, added := r.add(d); added {
					return e, true, nil
				}
			}
			switch err := it.Err(); {
			case ctx.Err() != nil:
			case err != nil:
				r.fail(err)
			default:
				r.finish(note(h.cfg.Searcher.Status()))
			}
			interrupt()
			return "", false, nil
		}, it.Close), nil
	}
}

// note says why the search may have missed definitions, if it may have.
func note(st symboldb.Status) string {
	switch {
	case st.Err != "":
		return "indexing failed: " + st.Err
	case st.Loaded && st.Scanned:
		return ""
	case st.Pending == 1:
		return "still indexing · 1 file left"
	case st.Pending > 1:
		return fmt.Sprintf("still indexing · %d files left", st.Pending)
	}
	return "still indexing"
}
