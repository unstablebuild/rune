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
	"errors"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
)

func TestSystemClipboardDelegatesWhenAvailable(t *testing.T) {
	backing := clipboard.NewInMemory()
	clip := newSystemClipboard(backing, nil)

	require.NoError(t, clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "x"}))
	data, err := clip.Paste(clipboard.DefaultRegisterID)
	require.NoError(t, err)
	require.Equal(t, "x", data.Text)
}

func TestSystemClipboardOpenErrorStillFunctionsViaMemory(t *testing.T) {
	clip := newSystemClipboard(nil, errors.New("no provider"))

	err := clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "x"})
	require.EqualError(t, err, "system clipboard: no provider",
		"a failed open must surface through Copy with a user-friendly message")

	data, err := clip.Paste(clipboard.DefaultRegisterID)
	require.EqualError(t, err, "system clipboard: no provider",
		"a failed open must surface through Paste with a user-friendly message")
	require.Equal(t, "x", data.Text,
		"the in-memory fallback must still service copy/paste within Rune")
}

func TestSystemClipboardUnsupportedErrorSuggestsPackages(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("package-install hint is only appended on Linux")
	}
	clip := newSystemClipboard(nil, errors.New("system clipboard unsupported"))

	want := "system clipboard: system clipboard unsupported; " +
		"install one of the following clipboard utilities: " +
		"xclip, xsel, or wl-clipboard (Wayland)"

	err := clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "x"})
	require.EqualError(t, err, want,
		"an unsupported clipboard must tell the user which packages to install")

	_, err = clip.Paste(clipboard.DefaultRegisterID)
	require.EqualError(t, err, want,
		"an unsupported clipboard must tell the user which packages to install")
}

func TestSystemClipboardUnsupportedErrorNoHintOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("hint is expected on Linux")
	}
	clip := newSystemClipboard(nil, errors.New("system clipboard unsupported"))

	err := clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "x"})
	require.EqualError(t, err, "system clipboard: system clipboard unsupported",
		"non-Linux platforms must not get Linux package advice")
}

func TestSystemClipboardCopyPasteErrorStillFunctionsViaMemory(t *testing.T) {
	sysErr := errors.New("write failed")
	clip := newSystemClipboard(failingRegister{err: sysErr}, nil)

	err := clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "x"})
	require.EqualError(t, err, "system clipboard: write failed",
		"a failed system Copy must surface a user-friendly error")

	data, err := clip.Paste(clipboard.DefaultRegisterID)
	require.EqualError(t, err, "system clipboard: write failed",
		"a failed system Paste must surface a user-friendly error")
	require.Equal(t, "x", data.Text,
		"the in-memory fallback must still service copy/paste within Rune")
}

type failingRegister struct {
	err error
}

func (f failingRegister) Copy(string, clipboard.Data) error { return f.err }

func (f failingRegister) Paste(string) (clipboard.Data, error) {
	return clipboard.Data{}, f.err
}

// recordingRegister records every register ID written to the underlying OS
// clipboard so tests can assert what is forwarded to the system layer.
type recordingRegister struct {
	clipboard.Register
	copied []string
}

func (r *recordingRegister) Copy(registerID string, data clipboard.Data) error {
	r.copied = append(r.copied, registerID)
	return r.Register.Copy(registerID, data)
}

func TestSystemClipboardOnlyDefaultRegisterReachesOS(t *testing.T) {
	sys := &recordingRegister{Register: clipboard.NewInMemory()}
	clip := newSystemClipboard(sys, nil)

	require.NoError(t, clip.Copy(".", clipboard.Data{Text: "typed text"}))
	require.Empty(t, sys.copied,
		"a non-default register write must not reach the OS clipboard")

	require.NoError(t, clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "yanked"}))
	require.Equal(t, []string{clipboard.DefaultRegisterID}, sys.copied,
		"only the default register write may reach the OS clipboard")
}
