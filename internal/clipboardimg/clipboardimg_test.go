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
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The test replaces the contents of the user's clipboard, so it only runs on
// request.
func TestSystemHasImage(t *testing.T) {
	if os.Getenv("RUNE_E2E_OVERWRITE_CLIPBOARD") == "" {
		t.Skip("set RUNE_E2E_OVERWRITE_CLIPBOARD=1 to let the test overwrite the clipboard")
	}
	png := filepath.Join(t.TempDir(), "pixel.png")
	require.NoError(t, exec.Command("sips", "-s", "format", "png",
		"/System/Library/CoreServices/DefaultDesktop.heic", "--out", png).Run())

	tests := []struct {
		name string
		set  []string
		want bool
	}{
		{name: "image", want: true, set: []string{"osascript", "-e",
			`set the clipboard to (read (POSIX file "` + png + `") as «class PNGf»)`}},
		{name: "text", want: false, set: []string{"osascript", "-e",
			`set the clipboard to "hello"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, exec.Command(tc.set[0], tc.set[1:]...).Run())
			require.Equal(t, tc.want, System{}.HasImage())
		})
	}
}
