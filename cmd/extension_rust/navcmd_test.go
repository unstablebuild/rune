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
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// recordingOpener returns a fixed handler and records that it was asked
// to open, standing in for the browser's resource opener whose returned
// token is the only handler SetWindowContent can accept.
type recordingOpener struct {
	h      browserapi.Handler
	opened []workspaceapi.URI
}

func (o *recordingOpener) Open(uri workspaceapi.URI) (browserapi.Handler, error) {
	o.opened = append(o.opened, uri)
	return o.h, nil
}

// handleEditor is a fakeEditor whose Editor returns a distinct symbolic
// handle, mirroring the production textrpc client where the editor
// handle is not a browser content token.
type handleEditor struct {
	fakeEditor
	h textapi.Handler
}

func (e *handleEditor) Editor(workspaceapi.URI) (textapi.Handler, error) { return e.h, nil }

func TestOpenLocationSetsOpenerHandlerAsContent(t *testing.T) {
	opened := &stubResource{}
	editorHandle := &stubResource{}
	opener := &recordingOpener{h: opened}
	editor := &handleEditor{h: editorHandle}
	wm := &fakeWM{}

	err := openLocation(editor, wm, opener, fakeWindow{id: editorWinID}, newTestURI(t),
		semanticapi.Location{URI: "file:///ws/src/lib.rs"})
	require.NoError(t, err)
	win, content := wm.lastContent()
	require.NotNil(t, content, "the jump must set the window content")
	require.Same(t, opened, content,
		"window content must be the opener's handler, not the editor handle")
	require.Equal(t, uint64(editorWinID), win.WindowID(),
		"the jump must land in the window it was given")
}
