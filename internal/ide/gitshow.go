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
	"fmt"
	"io"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace"
)

const (
	gitshowContextLines = 3
	// The ' ', '+' or '-' column UnifiedBuffer writes ahead of every
	// content line, which highlight columns shift back by.
	gitshowGutter = 1
	// A diff's ideal width is its single longest line, so without a cap
	// one stray long line stretches the popup across the screen.
	gitshowMaxWidth     = 120
	gitshowLocationList = "gitshowhunk"
)

// Opening a popup publishes an EventTypeOpen that nothing ever
// retracts, so the namespace has to be a scheme the workspace manager
// can still resolve on a reload.
const gitshowBase = workspace.MemoryScheme + ":///gitshow"

func gitshowBaseURI() workspaceapi.URI {
	uri, err := workspaceapi.ParseURI(gitshowBase)
	if err != nil {
		panic("gitshow: malformed base URI: " + err.Error())
	}
	return uri
}

// gitshowLocations maps each hunk onto the block of rows it occupies.
// The counter rides a location Message because the editors own the
// status bar for their mode indicator and overwrite anything else put
// there. Spanning the whole block is free: with no Attr, DrawLocations
// tints nothing and only column zero of each row gets indexed.
func gitshowLocations(
	hunks []vctrl.UnifiedHunk, anchors []vctrl.HunkAnchor, label string,
) []textapi.Location {
	locs := make([]textapi.Location, 0, len(hunks))
	for i, h := range hunks {
		locs = append(locs, textapi.Location{
			From: term.Coordinates{Y: anchors[i].Row},
			To:   term.Coordinates{Y: anchors[i].Row + len(h.Lines)},
			Message: fmt.Sprintf("hunk %d/%d · %s",
				i+1, len(hunks), label),
		})
	}
	return locs
}

func readFile(ws workspace.Workspace, uri workspaceapi.URI) (string, error) {
	f, err := ws.Open(uri.Path())
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck
	content, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func gitshowLabel(
	ctx context.Context, svc vctrl.Service, uri workspaceapi.URI,
) string {
	sha, err := svc.CurrentCommit(ctx, uri)
	if err != nil || sha == "" {
		return "vs HEAD"
	}
	return "vs HEAD " + sha[:min(len(sha), 7)]
}

// gitshowSeek returns the index of the hunk covering the new-file line,
// or the last one when the cursor sits past every change.
func gitshowSeek(hunks []vctrl.UnifiedHunk, line int) int {
	for i, h := range hunks {
		if h.NewStart+h.NewLines > line {
			return i
		}
	}
	return len(hunks) - 1
}

// gitshowKeyHandler makes the popup closable. Esc only closes for a
// modeless editor, since a modal one needs it to leave insert mode, and
// neither key fires while the editor runs its own search prompt.
func gitshowKeyHandler(edh text.Handler, modal bool) tui.Handler {
	return handler.Wrap(edh, func(ev term.Event) (exit, handled bool) {
		if ev.Type == term.EventKey && !edh.IsSearchMode() {
			closing := ev.Ch == 'w' && ev.Mod == term.ModCtrl
			closing = closing || (ev.Key == term.KeyEsc && !modal)
			if closing {
				return true, true
			}
		}
		return edh.Handle(ev)
	})
}

// gitshowService reports the diff on screen, so the status bar's
// +added / −deleted describe what the user is reading, while the branch
// name still resolves against the real file's repository.
type gitshowService struct {
	vctrl.Service
	file workspaceapi.URI
	diff vctrl.FileDiff
}

func (g gitshowService) Diff(
	context.Context, workspaceapi.URI,
) (vctrl.FileDiff, error) {
	return g.diff, nil
}

func (g gitshowService) ShortRef(
	ctx context.Context, _ workspaceapi.URI,
) (string, error) {
	return g.Service.ShortRef(ctx, g.file)
}
