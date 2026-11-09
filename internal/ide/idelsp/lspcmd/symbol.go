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

package lspcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/locationpicker"
)

// matchPosition converts a resolved symbol's term coordinates into an LSP
// position (0-based line and character).
func matchPosition(m syntaxapi.Match) semanticapi.Position {
	return semanticapi.Position{
		Line:      uint32(m.Pos.Y),
		Character: uint32(m.Pos.X),
	}
}

func resolveSymbol(
	ctx context.Context, parser syntaxapi.Parser, name string,
	progress syntaxapi.Progress,
) ([]syntaxapi.Match, error) {
	it, err := parser.ResolveSymbol(ctx, name, progress)
	if err != nil {
		if errors.Is(err, syntaxapi.ErrNoDot) {
			return nil, errUnqualifiedSymbol(name)
		}
		return nil, err
	}
	matches, err := iterator.ToSlice(ctx, it)
	if err != nil {
		if errors.Is(err, syntaxapi.ErrNoDot) {
			return nil, errUnqualifiedSymbol(name)
		}
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no symbols found for %q", name)
	}
	return matches, nil
}

// errUnqualifiedSymbol explains that a symbol name must be qualified with
// its package or module, since resolution keys off the qualifier separator.
func errUnqualifiedSymbol(name string) error {
	return fmt.Errorf(
		"%q is not qualified; prefix it with its package or module", name)
}

// executeFunc is the blocking fetch behind an LSP location command. It is
// called off the event loop and must schedule any UI work onto the loop
// itself via the handler's scheduleNextTick.
type executeFunc func(
	ctx context.Context, uri workspaceapi.URI, pos semanticapi.Position,
) error

// executeResolved adapts a handler's blocking execute func to the
// resolveCommandSymbol onResolve contract: it runs the fetch inline on the
// resolver goroutine and reports failures as error notifications.
func executeResolved(
	name string,
	rootURI workspaceapi.URI,
	notify browserapi.Notifications,
	scheduleNextTick func(func()) bool,
	execute executeFunc,
) func(m syntaxapi.Match, done func()) {
	return func(m syntaxapi.Match, done func()) {
		defer done()
		wsURI, err := LspToURI(rootURI, m.URI)
		if err == nil {
			err = execute(context.Background(), wsURI, matchPosition(m))
		}
		if err != nil {
			scheduleNextTick(func() {
				_, _ = notify.Notify(browserapi.LevelError, "%s: %s", name, err)
			})
		}
	}
}

// executeAtCursor runs the blocking fetch for the cursor position off the
// event loop, tracking completion and failures through notifications. It
// must be called on the event loop and returns immediately.
func executeAtCursor(
	name string,
	notify browserapi.Notifications,
	scheduleNextTick func(func()) bool,
	cmd textapi.Command,
	execute executeFunc,
) {
	id, _ := notify.Notify(browserapi.LevelInfo, "%s…", name)
	_ = notify.UpdateNotificationProgress(id, "", 0, 1)
	go debug.CapturePanicReport(func() {
		err := execute(context.Background(), cmd.URI, CoordToPos(cmd.Cursor.Content))
		scheduleNextTick(func() {
			_ = notify.UpdateNotificationProgress(id, "", 1, 1)
			if err != nil {
				_, _ = notify.Notify(browserapi.LevelError, "%s: %s", name, err)
			}
		})
	})
}

// resolveCommandSymbol resolves the symbol named by cmd.Args and hands the
// match to onResolve. onResolve is invoked off the event loop and may block
// on I/O; it must schedule any UI work onto the loop itself and call done
// exactly once. done is safe to call from any goroutine. When cmd carries
// no args, resolveCommandSymbol reports (true, nil) and the caller executes
// its cursor-position path instead.
func resolveCommandSymbol(
	_ context.Context, cmd *textapi.Command,
	rootURI workspaceapi.URI,
	wm browserapi.WindowManager,
	fs workspaceapi.FileSystem,
	notify browserapi.Notifications,
	scheduleNextTick func(func()) bool,
	parser syntaxapi.Parser,
	onResolve func(m syntaxapi.Match, done func()),
) (proceed bool, err error) {
	if len(cmd.Args) == 0 {
		if cmd.Resource == nil {
			return false, fmt.Errorf("no file open; pass a symbol name or open a file first")
		}
		return true, nil
	}
	name := strings.Join(cmd.Args, " ")

	id, _ := notify.Notify(browserapi.LevelInfo, "Resolving %s…", name)
	_ = notify.UpdateNotificationProgress(id, "", 0, 4)
	done := func() {
		scheduleNextTick(func() {
			_ = notify.UpdateNotificationProgress(id, "", 4, 4)
		})
	}

	go debug.CapturePanicReport(func() {
		progress := syntaxapi.ProgressFunc(func(msg string, found int, step, total int64) {
			if found > 0 {
				msg = fmt.Sprintf("%s (%d found)", msg, found)
			}
			scheduleNextTick(func() {
				_ = notify.UpdateNotificationProgress(id, msg, step, total)
			})
		})
		matches, err := resolveSymbol(context.Background(), parser, name, progress)
		if err != nil {
			done()
			scheduleNextTick(func() {
				_, _ = notify.Notify(browserapi.LevelError, "%s", err)
			})
			return
		}
		if len(matches) == 1 {
			onResolve(matches[0], done)
			return
		}
		scheduleNextTick(func() {
			err := showSymbolPicker(rootURI, matches, wm, fs, scheduleNextTick, parser, onResolve, done)
			if err != nil {
				done()
				_, _ = notify.Notify(browserapi.LevelError, "%s", err)
			}
		})
	})

	return false, nil
}

// showSymbolPicker floats a picker over the resolved matches. It runs on
// the event loop; the selection handoff spawns a goroutine because onPick
// follows the onResolve contract and may block.
func showSymbolPicker(
	rootURI workspaceapi.URI,
	matches []syntaxapi.Match,
	wm browserapi.WindowManager,
	fs workspaceapi.FileSystem,
	scheduleNextTick func(func()) bool,
	parser syntaxapi.Parser,
	onPick func(m syntaxapi.Match, done func()),
	done func(),
) error {
	entries := make([]locationEntry, len(matches))
	for i, m := range matches {
		entries[i] = locationEntry{
			uri:     m.URI,
			rng:     semanticapi.Range{Start: matchPosition(m), End: matchPosition(m)},
			display: m.Display,
		}
	}
	pickerEntries := make([]locationpicker.Entry, len(entries))
	for i, e := range entries {
		uri, err := LspToURI(rootURI, e.uri)
		if err != nil {
			return err
		}
		pickerEntries[i] = locationpicker.Entry{URI: uri, Range: e.rng, Display: e.display}
	}
	picker := locationpicker.New(
		pickerEntries, fs, scheduleNextTick, parser,
		locationpicker.DefaultConfig(), nil,
	)
	picker.SetOnSelect(func(idx int) {
		go debug.CapturePanicReport(func() {
			onPick(matches[idx], done)
		})
	})
	_, err := wm.Floating(picker, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	return err
}

func completeReferencedSymbol(
	ctx context.Context, parser syntaxapi.Parser,
) (iterator.Iterator[string], error) {
	it, err := parser.ListReferencedSymbols(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
		for {
			s, ok := it.Next(ctx)
			if !ok {
				return "", false, it.Err()
			}
			if seen[s] {
				continue
			}
			seen[s] = true
			return s, true, nil
		}
	}, it.Close), nil
}

func normalizeMethodName(name string) string {
	if after, ok := strings.CutPrefix(name, "(*"); ok {
		if i := strings.Index(after, ")."); i != -1 {
			return after[:i] + "." + after[i+2:]
		}
	}
	if after, ok := strings.CutPrefix(name, "("); ok {
		if i := strings.Index(after, ")."); i != -1 {
			return after[:i] + "." + after[i+2:]
		}
	}
	return name
}
