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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
)

func TestDeleteClipboard(t *testing.T) {
	buf := cell.NewBuffer()
	clip := clipboard.NewInMemory()
	var scroll component.Scroll
	scroll.Init(buf)
	c := NewCursor(&scroll, nil)
	WithCopyDelete("", clip, c, buf)

	content := "my whatever"
	buf.InsertString(term.Coordinates{}, content)
	c.SelectLine()
	c.DeleteSelection()

	data, err := clip.Paste("")
	require.NoError(t, err)
	assert.Equal(t, clipboard.Data{Text: content, Metadata: LineSelection}, data)

	// test that undo inserts do not get copied to clipboard
	buf.Undo()
	buf.Undo()

	data, err = clip.Paste("")
	require.NoError(t, err)
	assert.Equal(t, clipboard.Data{Text: content, Metadata: LineSelection}, data)
}

func TestDeleteClipboardReloadDoesNotCopy(t *testing.T) {
	buf := cell.NewBuffer()
	clip := clipboard.NewInMemory()
	var scroll component.Scroll
	scroll.Init(buf)
	c := NewCursor(&scroll, nil)
	WithCopyDelete("", clip, c, buf)

	clip.Copy("", clipboard.Data{Text: "user clipboard"})
	buf.InsertString(term.Coordinates{}, "the entire file contents")

	buf.ReloadContents(context.Background(), "reloaded from disk")

	require.Equal(t, "reloaded from disk", buf.String())
	data, err := clip.Paste("")
	require.NoError(t, err)
	assert.Equal(t, "user clipboard", data.Text,
		"reload must not overwrite the clipboard with the replaced contents")
}
