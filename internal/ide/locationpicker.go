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

package ide

import (
	"context"
	"errors"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"unstable.build/rune/internal/handler/finder"
	"unstable.build/rune/internal/handler/locationsearch"
	"unstable.build/rune/internal/text/cmdenv"
)

// locationpicker runs the given program via $SHELL -c and presents
// each stdout line as an entry in a floating fuzzy-search picker
// with a syntax-highlighted preview pane on top. Lines must follow
// the `path[:line[:col]]` convention; selecting an entry opens the
// file in the previously focused window at the parsed coordinates.
func (e *ex) locationpicker(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("expected at least one argument")
	}
	// args reach this handler already unquoted by the command prompt
	// (and stripped of any alias-substituted quoting via
	// regroupAndUnquote in text/component.go). Re-shell-quote each
	// token so that joining them with spaces and handing the result
	// to `$SHELL -c` recovers the original argument boundaries even
	// when individual values contain whitespace, single quotes,
	// regex metacharacters, or shell metacharacters. Without this
	// round-trip, an alias body like
	//   "locationpicker git grep -n --column $1"
	// invoked as `:gitgrep 'foo bar'` would reach the shell as
	// `git grep -n --column foo bar`, splitting the user's
	// single-token query into two grep arguments.
	cmdStr := strings.Join(cmdenv.QuoteArgsForShellFields(args), " ")

	prevWin := e.invokeWindow()
	apibrowser := newBrowserAdapter(e.Browser())
	clients := finder.Clients{
		Storage:        e.storage,
		ResourceOpener: apibrowser,
		WindowManager:  apibrowser,
		Interrupter:    apibrowser,
		Notifications:  apibrowser,
		// Wrap the text.Component-backed Editor (rather than the
		// user-supplied text.Editor like vi.Editor()) so that
		// finder.setContent can resolve the just-opened tab's
		// text.Handler via Editor(uri) and dispatch SetCursor to
		// it. The user-level text.Editor's Editor(uri) is "not
		// supported", which silently broke cursor dispatch on
		// :gitgrep.
		Editor:     newEditorAdapter(&e.comp),
		FileSystem: e.workspace,
		Executor:   workspaceExecutorAdapter{e: e.executor},
	}

	cfg := locationsearch.DefaultConfig()
	cfg.HistoryDocumentID = "locationpicker.history"
	h, err := locationsearch.New(
		ctx, clients, prevWin,
		cfg,
		e.parser,
		e.emulatorConfig.ScheduleNextTick,
		cmdStr,
		nil,
	)
	if err != nil {
		return err
	}

	_, err = e.Browser().Floating(h, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if err != nil {
		_ = h.Close()
	}
	return err
}
