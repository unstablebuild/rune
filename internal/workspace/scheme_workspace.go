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

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
)

// simple Scheme-backed Workspace implementation.
type schemeWorkspace struct {
	w workspaceapi.URI
	schemeapi.Scheme
	// scheduleNextTick is forwarded into newFile/newFileRecover so
	// reload's buffer mutations run on the host event loop. See
	// workspace.file for details.
	scheduleNextTick func(func()) bool
}

// OnDisconnect forwards [RemoteScheme.OnDisconnect] when the embedded scheme
// supports it. Returns nil for local schemes, which means callers
// must check for nil before selecting on the returned channel.
func (w *schemeWorkspace) OnDisconnect() <-chan struct{} {
	if rs, ok := w.Scheme.(RemoteScheme); ok {
		return rs.OnDisconnect()
	}
	return nil
}

// WaitConnected forwards [RemoteScheme.WaitConnected] when the
// embedded scheme is remote. Local schemes are always "connected".
func (w *schemeWorkspace) WaitConnected(ctx context.Context) error {
	if rs, ok := w.Scheme.(RemoteScheme); ok {
		return rs.WaitConnected(ctx)
	}
	return nil
}

// InstallDataDir forwards [InstallDataDirProvider.InstallDataDir] when the
// embedded scheme supports it; otherwise the host cannot report its
// install root and callers must fall back to their own guess.
func (w *schemeWorkspace) InstallDataDir(ctx context.Context) (string, error) {
	if p, ok := w.Scheme.(InstallDataDirProvider); ok {
		return p.InstallDataDir(ctx)
	}
	return "", errors.ErrUnsupported
}

// NewSchemeWorkspace wraps a schemeapi.Scheme and implements a workspace.Loader,
// effectively converting a schemeapi.Scheme into a workspace.Workspace.
//
// scheduleNextTick is forwarded into the loaded file's reload
// goroutine so cell.Buffer mutations always run on the host event
// loop. It must not be nil.
func NewSchemeWorkspace(
	w workspaceapi.URI, p schemeapi.Scheme,
	scheduleNextTick func(func()) bool,
) Workspace {
	ret := new(schemeWorkspace)
	ret.Init(w, p, scheduleNextTick)
	return ret
}

func (w *schemeWorkspace) Init(
	uri workspaceapi.URI, p schemeapi.Scheme,
	scheduleNextTick func(func()) bool,
) {
	w.w = uri
	w.Scheme = p
	w.scheduleNextTick = scheduleNextTick
}

func (w *schemeWorkspace) Recover(
	uri, swapURI workspaceapi.URI, buf *cell.Buffer, force bool,
) (ret FlusherCloser, err error) {
	// force cleanup and expansion of URI paths
	// but first check if it's from this workspace
	is, err := CanWorkspaceURI(w, uri)
	if err != nil {
		err = fmt.Errorf("CanWorkspaceURI: %s", err)
		return
	}
	if !is {
		err = fmt.Errorf("invalid URI %q for workspace with URI %q", uri, w.w)
		return
	}
	is, err = CanWorkspaceURI(w, swapURI)
	if err != nil {
		err = fmt.Errorf("CanWorkspaceURI: %s", err)
		return
	}
	if !is {
		err = fmt.Errorf("invalid URI %q for workspace with URI %q", swapURI, w.w)
		return
	}

	// turn into relative if possible
	path := workspaceapi.RelPath(w.w, uri)
	swapPath := workspaceapi.RelPath(w.w, swapURI)

	ret, err = newFileRecover(w.Scheme, path, swapPath, buf, force, w.scheduleNextTick)
	return
}

func (w *schemeWorkspace) Load(
	uri workspaceapi.URI, buf *cell.Buffer, swapDir workspaceapi.URI, readOnly bool,
) (ret FlusherCloser, err error) {
	// force cleanup and expansion of URI paths
	// but first check if it's from this workspace
	is, err := CanWorkspaceURI(w, uri)
	if err != nil {
		err = fmt.Errorf("CanWorkspaceURI: %s", err)
		return
	}
	if !is {
		err = fmt.Errorf("invalid file URI %q for workspace with URI %q", uri, w.w)
		return
	}
	is, err = CanWorkspaceURI(w, swapDir)
	if err != nil {
		err = fmt.Errorf("CanWorkspaceURI: %s", err)
		return
	}
	if !is {
		err = fmt.Errorf("invalid file URI %q for workspace with URI %q", swapDir, w.w)
		return
	}

	// The entry name derives from the absolute path, and has to match
	// what the recovery prompts derive through DefaultSwapFile.
	swapFile, err := DefaultSwapFile(swapDir, uri)
	if err != nil {
		return
	}

	// turn into relative if possible
	path := workspaceapi.RelPath(w.w, uri)
	swapFilePath := workspaceapi.RelPath(w.w, swapFile)
	swapDirPath := sharedSwapDir(workspaceapi.RelPath(w.w, swapDir), path)

	ret, err = newFile(
		w.Scheme, path, buf, swapDirPath, swapFilePath, readOnly, w.scheduleNextTick)
	if err == os.ErrNotExist {
		if readOnly {
			err = errors.New("cannot open file that doesn't exist in read-only")
		} else {
			err = errors.New("directory structure does not support creating file")
		}
	}
	return
}
