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

package text

import (
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/workspace"
)

func (c *Component) openAreYouSurePrompt(file workspaceapi.URI) {
	const (
		yesOpt = "    Yes    "
		noOpt  = "    No    "
	)

	msg := fmt.Sprintf(`File %s
has been updated since the last back up was created.
Are you sure you want to recover it
and lose all the new updates?`, file)

	c.Prompt(msg, []string{yesOpt, noOpt},
		[]term.KeyComb{{Ch: 'Y'}, {Ch: 'N'}},
		handler.FuncPromptHandler(
			func(i int, opt string) {

				var h browserapi.Handler
				var err error

				switch opt {
				case yesOpt:
					var swapDir, swapFile workspaceapi.URI
					swapDir, err = c.getSwapDir(file)
					if err == nil {
						swapFile, err = workspace.DefaultSwapFile(swapDir, file)
						if err == nil {
							h, err = c.openFileTab(file, swapFile, false, true)
						}
					}
				case noOpt:
				}
				if h != nil {
					err = c.comp.Focus().SetContent(h)
				}
				if err != nil {
					c.log(log.ErrorLevel, "recovery prompt: %v", err)
					_, _ = c.Notify(browserapi.LevelError, "%v", err)
					return
				}
			},
			func() error { return nil }))
}

func (c *Component) openRecoveryPrompt(file workspaceapi.URI) {
	const (
		recoverOpt  = "   Recover   "
		readOnlyOpt = "   Open rdonly   "
		editOpt     = "   Force Edit   "
		skipOpt     = "   Skip   "
	)

	msg := fmt.Sprintf("File %s is already open by another process "+
		"or an edit session for this file crashed.", file)

	// Pick the window where the recovered file should ultimately land.
	// Prefer the window currently in focus, but only if it is a regular
	// tab window. If the focused window is a floating prompt or a
	// non-tab tiled handler (e.g. a fuzzy-search/finder split, see
	// RUNE-139), the recovered file would otherwise be installed into a
	// transient handler's window. In that case, fall back to a sibling
	// tile via Shiftable() so the file ends up in the previous (real)
	// tab window.
	invokeWindow := c.comp.Focus()
	_, focusIsTab := c.comp.FocusTab()
	if invokeWindow.IsFloating() || !focusIsTab {
		nextWindow, ok := c.comp.Shiftable()
		// this should always be true, since the last tile can never be
		// closed and Shiftable/ShiftFocus only return tiled windows.
		if ok {
			invokeWindow = nextWindow
		}
	}

	c.Prompt(msg, []string{recoverOpt, readOnlyOpt, editOpt, skipOpt},
		[]term.KeyComb{{Ch: 'R'}, {Ch: 'O'}, {Ch: 'E'}, {Ch: 'S'}},
		handler.FuncPromptHandler(
			func(i int, opt string) {
				var h browserapi.Handler
				var err error

				switch opt {
				case recoverOpt:
					var swapDir, swapFile workspaceapi.URI
					swapDir, err = c.getSwapDir(file)
					if err == nil {
						swapFile, err = workspace.DefaultSwapFile(swapDir, file)
						if err == nil {
							h, err = c.recoverOpenFileTab(file, swapFile, false)
							if err == workspaceapi.ErrStaleData {
								c.openAreYouSurePrompt(file)
								return
							}
						}
					}
				case readOnlyOpt:
					h, err = c.OpenFileTab(file, true)
				case editOpt:
					var swapDir, swapFile workspaceapi.URI
					swapDir, err = c.getSwapDir(file)
					if err == nil {
						swapFile, err = workspace.DefaultSwapFile(swapDir, file)
						if err == nil {
							err = c.workspace.Remove(swapFile.Path())
							if err == nil {
								h, err = c.OpenFileTab(file, false)
							}
						}
					}
				case skipOpt:
				}
				if h != nil {
					if invokeWindow.Closed() {
						invokeWindow = c.comp.Focus()
					}
					err = invokeWindow.SetContent(h)
				}
				if err != nil {
					c.log(log.ErrorLevel, "recovery prompt: %v", err)
					_, _ = c.Notify(browserapi.LevelError, "%v", err)
				}
				// Without this, the finder split lingers visible until the user types a key.
				if perr := c.PublishEvent(term.Event{Type: term.EventNone}); perr != nil {
					c.log(log.DebugLevel, "recovery prompt: publish wakeup: %v", perr)
				}
			},
			func() error {
				if perr := c.PublishEvent(term.Event{Type: term.EventNone}); perr != nil {
					c.log(log.DebugLevel, "recovery prompt: publish wakeup: %v", perr)
				}
				return nil
			}))
}
