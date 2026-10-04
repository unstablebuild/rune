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

package font

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unstable.build/rune/internal/term/gui/font/builtinfont"
)

// stubEmojiPaths pins the candidate list so the emoji resolver runs
// deterministically regardless of the host's installed fonts.
func stubEmojiPaths(paths ...string) func() []string {
	return func() []string { return paths }
}

func TestEmojiFacePrefersSystemFont(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SystemColorEmoji.ttf")
	require.NoError(t, os.WriteFile(path, builtinfont.EmojiTTF, 0o600))

	m := &Manager{emojiPaths: stubEmojiPaths(path)}
	face, source := m.EmojiFace()

	require.NotNil(t, face)
	assert.Equal(t, "system: "+path, source)
	assert.True(t, face.Has([]rune{'😀'}))
}

func TestEmojiFaceFallsBackToBundled(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "NotThere.ttf")
	m := &Manager{emojiPaths: stubEmojiPaths(missing)}
	face, source := m.EmojiFace()

	require.NotNil(t, face)
	assert.Equal(t, "bundled: Noto Color Emoji", source)
	assert.True(t, face.Has([]rune{'😀'}))
}

func TestEmojiFaceSkipsUnusableSystemFont(t *testing.T) {
	// A non-font file makes NewFaceFromFile reject the candidate.
	bad := filepath.Join(t.TempDir(), "notafont.ttf")
	require.NoError(t, os.WriteFile(bad, []byte("not a font"), 0o600))

	m := &Manager{emojiPaths: stubEmojiPaths(bad)}
	face, source := m.EmojiFace()

	require.NotNil(t, face)
	assert.Equal(t, "bundled: Noto Color Emoji", source)
}

func TestEmojiFaceResolvesOnce(t *testing.T) {
	m := &Manager{emojiPaths: stubEmojiPaths()}
	face1, source1 := m.EmojiFace()
	face2, source2 := m.EmojiFace()

	assert.Same(t, face1, face2, "face is cached across calls")
	assert.Equal(t, source1, source2)
}
