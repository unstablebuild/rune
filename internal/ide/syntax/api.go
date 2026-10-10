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

// Package syntax defines the syntax trees that file tabs install into their
// buffers: their configuration, their dependencies and the state they report.
// It does not link a parser, so editors that never parse files depend on it
// and still build without cgo; package treesitter implements it.
package syntax

import (
	"context"
	"os"
	"strings"

	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/workspace"
)

// Tree is a syntax tree installed into a file tab's buffer. It wraps the
// tab's FlusherCloser, so closing the tree also closes the wrapped one.
type Tree interface {
	workspace.FlusherCloser
	// Commands returns the file-level commands the tree provides for the tab
	// edited by handler, along with the CommandHandler that serves them.
	Commands(handler Handler) ([]textapi.CommandManual, CommandHandler)
}

// NewTreeFunc installs a Tree into buf for the file tab of uri. The returned
// tree wraps fc and replaces it as the tab's FlusherCloser. It is called
// before the tab's editor is created, so a tree that installs itself as buf's
// view is visible to the editor while the editor initializes.
type NewTreeFunc func(
	ctx context.Context, n browserapi.Notifications, interrupter term.Interrupter,
	pkg PkgManager, loc LocationSetter,
	uri workspaceapi.URI, buf *cell.Buffer, fc workspace.FlusherCloser,
	opener Opener, config Config,
) Tree

// Handler is the subset of text.Handler that a Tree's commands drive.
type Handler interface {
	SetCursorAtScroll(term.Coordinates) bool
}

// CommandHandler serves the commands of a Tree. It satisfies
// text.CommandHandler.
type CommandHandler interface {
	HandleCommand(context.Context, textapi.Command) error
	Complete(ctx context.Context, cmd textapi.Command) (
		blueiterator.Iterator[string], string, error,
	)
}

// Opener opens the custom query files a Tree is asked to run.
type Opener interface {
	OpenFile(path string, flag int, perm os.FileMode) (workspaceapi.File, error)
}

// State is the state of a Tree, as reported to the status bar.
type State struct {
	Progress    float64
	ParserError string
	LangID      string
	Closed      bool
	Highlights  bool
	Folds       bool
	Indents     bool
}

// Config configures a tree parser.
type Config struct {
	// CaptureNamesAttributes maps the capture names
	// of a highlights.scm tree-sitter file, into term.Attributes.
	CaptureNamesAttributes map[string]term.Attributes

	// ScheduleNextTick schedules an arbitrary function to be run
	// in the next event-loop tick.
	ScheduleNextTick func(func()) bool

	// ReparseOnErrors forces tree to re-parse the entire file
	// if there are failures to keep tree sitter's tree and the file contents
	// in sync.
	ReparseOnErrors bool

	// StrictErrors enables showing when the parser reports an error
	// but incremental parsing overall didn't fail.
	StrictErrors bool

	// Autoindent enables or disables indentation features. In practice,
	// if disabled, IndentationAt always returns 0, false.
	Autoindent bool
}

// PkgManager abstracts a subset of idepkg.Manager for a tree parser.
type PkgManager interface {
	LibDir(ctx context.Context, pkgID string) (iterator.Iterator[string], error)
}

// LocationSetter abstracts the ability to visualize locations. Implementations
// must replace a list during SetLocationList and must not retain its backing
// storage after a subsequent call returns.
type LocationSetter interface {
	SetLocationList(textapi.LocationList)
}

// FuncLocationSetter returns a LocationSetter that uses the given fn to
// set locations.
func FuncLocationSetter(fn func(textapi.LocationList)) LocationSetter {
	return fnLocationList{fn: fn}
}

// DefaultConfig returns a sane configuration for a tree parser.
func DefaultConfig() Config {
	return Config{
		CaptureNamesAttributes: defaultCaptureNamesAttributes,
		ScheduleNextTick:       func(cb func()) bool { cb(); return true },
		ReparseOnErrors:        true,
		StrictErrors:           false,
		Autoindent:             true,
	}
}

// CaptureNameAttributes resolves a tree-sitter capture name, falling back to
// progressively shorter dotted prefixes so that refined captures such as
// "keyword.function" inherit the attributes configured for "keyword". At each
// prefix, captureNamesAttributes takes precedence over the defaults.
func CaptureNameAttributes(
	captureNamesAttributes map[string]term.Attributes, name string,
) term.Attributes {
	for {
		if attr, ok := captureNamesAttributes[name]; ok {
			return attr
		}
		if attr, ok := defaultCaptureNamesAttributes[name]; ok {
			return attr
		}
		idx := strings.LastIndexByte(name, '.')
		if idx < 0 {
			return term.Attributes{}
		}
		name = name[:idx]
	}
}

var defaultCaptureNamesAttributes = map[string]term.Attributes{
	"function":         {},
	"function.builtin": {Fg: term.ColorYellow},
	"function.method":  {},
	"type":             {},
	"property":         {},
	"variable":         {},
	"operator":         {},
	"keyword":          {Fg: term.ColorYellow},
	"string":           {Fg: term.ColorFuchsia},
	"escape":           {},
	"number":           {Fg: term.ColorRed},
	"constant.builtin": {},
	"comment":          {Fg: term.ColorBlue},

	// Markup captures are emitted by common tree-sitter queries for markdown,
	// markdown_inline, djot, rst, latex, typst, vimdoc, gitcommit, pod, and
	// other prose/markup-like languages.
	"markup.heading":       {Fg: term.ColorYellow},
	"markup.raw":           {Fg: term.ColorFuchsia},
	"markup.raw.delimiter": {Fg: term.ColorYellow},
	"markup.link":          {Fg: term.ColorYellow},
	"markup.link.url":      {Fg: term.ColorFuchsia},
	"markup.link.label":    {Fg: term.ColorYellow},
	"markup.link.text":     {Fg: term.ColorYellow},
	"markup.list":          {Fg: term.ColorYellow},
	"markup.quote":         {Fg: term.ColorBlue},
	"markup.strong":        {Fg: term.ColorYellow},
	"markup.italic":        {Fg: term.ColorYellow},
	"markup.strikethrough": {Fg: term.ColorBlue},
	"markup.underline":     {Fg: term.ColorYellow},
	"markup.math":          {Fg: term.ColorFuchsia},

	// Legacy markdown captures used by older upstream queries.
	"text.title":     {Fg: term.ColorYellow},
	"text.literal":   {Fg: term.ColorFuchsia},
	"text.uri":       {Fg: term.ColorFuchsia},
	"text.reference": {Fg: term.ColorYellow},
	"text.emphasis":  {Fg: term.ColorYellow},
	"text.strong":    {Fg: term.ColorYellow},
}

type fnLocationList struct {
	fn func(textapi.LocationList)
}

func (f fnLocationList) SetLocationList(list textapi.LocationList) {
	f.fn(list)
}
