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

//go:build e2e && darwin && cgo

package clipboardimg

import (
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSystemHasImage(t *testing.T) {
	// It replaces the contents of the user's clipboard, so it only runs on request.
	if os.Getenv("RUNE_E2E_OVERWRITE_CLIPBOARD") == "" {
		t.Skip("set RUNE_E2E_OVERWRITE_CLIPBOARD=1 to let the test overwrite the clipboard")
	}
	pngPath := filepath.Join(t.TempDir(), "pixel.png")
	f, err := os.Create(pngPath)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	require.NoError(t, f.Close())

	tests := []struct {
		name   string
		script string
		want   bool
	}{
		{name: "image", want: true,
			script: `set the clipboard to (read (POSIX file "` + pngPath + `") as «class PNGf»)`},
		{name: "text", want: false, script: `set the clipboard to "hello"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, exec.Command("osascript", "-e", tc.script).Run())
			require.Equal(t, tc.want, System{}.HasImage())
		})
	}
}
