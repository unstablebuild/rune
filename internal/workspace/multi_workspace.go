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
	"fmt"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
)

var _ Workspace = (multi)(multi{})

// Multi wraps a Workspace to provide oob Recover and Load requests to other workspaces/schemes
// whether initialized or not.
func Multi(
	ctx context.Context, m WorkspaceManager, def Workspace, uri workspaceapi.URI,
) Workspace {
	return newMulti(ctx, m, uri, def)
}

type multi struct {
	parentCtx context.Context
	defURI    workspaceapi.URI
	Workspace
	manager WorkspaceManager
}

func newMulti(
	ctx context.Context, manager WorkspaceManager,
	defURI workspaceapi.URI, def Workspace,
) *multi {
	return &multi{parentCtx: ctx, Workspace: def, defURI: defURI, manager: manager}
}

// Load runs LoaderFor, so like it Load must be called on the event loop.
func (m multi) Load(
	file workspaceapi.URI, buf *cell.Buffer, swapDir workspaceapi.URI, readOnly bool,
) (FlusherCloser, error) {
	return m.loadThrough(file, func(l FileLoader) (FlusherCloser, error) {
		return l.Load(buf, swapDir, readOnly)
	})
}

// Recover runs LoaderFor, so like it Recover must be called on the event
// loop.
func (m multi) Recover(
	file, swapFilePath workspaceapi.URI, buf *cell.Buffer, force bool,
) (FlusherCloser, error) {
	return m.loadThrough(file, func(l FileLoader) (FlusherCloser, error) {
		return l.Recover(swapFilePath, buf, force)
	})
}

func (m multi) loadThrough(
	file workspaceapi.URI, load func(FileLoader) (FlusherCloser, error),
) (FlusherCloser, error) {
	l, err := m.LoaderFor(file)
	if err != nil {
		return nil, err
	}
	fc, err := load(l)
	if err != nil {
		if relErr := l.Release(); relErr != nil {
			log.Warnf("release workspace of %s: %v", file, relErr)
		}
		return nil, err
	}
	return fc, nil
}

// LoaderFor loads a file outside the default workspace through the
// workspace of the file's directory, unless another workspace that
// manager reports owns the file: it then returns ErrOpenInOtherWorkspace.
func (m multi) LoaderFor(file workspaceapi.URI) (FileLoader, error) {
	is, err := IsWorkspaceURI(m.Workspace, file)
	if err != nil {
		return nil, fmt.Errorf("workspaceapi.URI: %s", err)
	}
	if is {
		return m.Workspace.LoaderFor(file)
	}
	_, ok, err := m.manager.Workspace(file)
	if err != nil {
		return nil, err
	}
	if ok {
		return nil, ErrOpenInOtherWorkspace
	}
	dir := workspaceapi.Dir(file)
	w, err := m.manager.AcquireWorkspace(m.parentCtx, dir)
	if err != nil {
		return nil, err
	}
	return extraneousLoader{w: w, manager: m.manager, dir: dir, file: file}, nil
}

// extraneousLoader loads file through w, the workspace of dir, and holds
// a reference to w that the FlusherCloser of a successful load takes over.
type extraneousLoader struct {
	w         Workspace
	manager   WorkspaceManager
	dir, file workspaceapi.URI
}

func (l extraneousLoader) Load(
	buf *cell.Buffer, swapDir workspaceapi.URI, readOnly bool,
) (FlusherCloser, error) {
	fc, err := l.w.Load(l.file, buf, swapDir, readOnly)
	if err != nil {
		return nil, err
	}
	return &extraneousCloser{FlusherCloser: fc, manager: l.manager, dir: l.dir}, nil
}

func (l extraneousLoader) Recover(
	swapFilePath workspaceapi.URI, buf *cell.Buffer, force bool,
) (FlusherCloser, error) {
	fc, err := l.w.Recover(l.file, swapFilePath, buf, force)
	if err != nil {
		return nil, err
	}
	return &extraneousCloser{FlusherCloser: fc, manager: l.manager, dir: l.dir}, nil
}

func (l extraneousLoader) Release() error {
	return releaseWorkspace(l.manager, l.dir)
}

// releaseWorkspace releases a reference to the workspace registered under
// uri, and closes the workspace if it was the last.
func releaseWorkspace(manager WorkspaceManager, uri workspaceapi.URI) error {
	w, ok := manager.ReleaseWorkspace(uri)
	if !ok {
		return nil
	}
	return w.Close()
}

// extraneousCloser wraps a per-file FlusherCloser so its Close
// also releases the reference to the workspace of the file's
// directory. Close is idempotent: a repeated call returns the cached
// error without releasing twice, so a tab that runs Close from both
// its normal teardown and a shutdown path cannot under-count the
// workspace.
type extraneousCloser struct {
	FlusherCloser
	manager  WorkspaceManager
	dir      workspaceapi.URI
	closeOne sync.Once
	closeErr error
}

func (e *extraneousCloser) Close() error {
	e.closeOne.Do(func() {
		e.closeErr = e.FlusherCloser.Close()
		if relErr := releaseWorkspace(e.manager, e.dir); relErr != nil && e.closeErr == nil {
			e.closeErr = relErr
		}
	})
	return e.closeErr
}

// OnDisconnect forwards to the wrapped workspace's RemoteScheme
// when present. The embedded Workspace's method set does not
// include OnDisconnect (RemoteScheme is an optional interface), so
// without this explicit forwarder a multi value would not satisfy
// RemoteScheme even when the underlying workspace does — and
// callers like vtereservoir.New that type-assert against
// RemoteScheme to install transport-drop watchers would silently
// skip installation, leaving dead VTEs in the pool after SSH
// reconnects.
func (m multi) OnDisconnect() <-chan struct{} {
	if rs, ok := m.Workspace.(RemoteScheme); ok {
		return rs.OnDisconnect()
	}
	return nil
}

// WaitConnected forwards [RemoteScheme.WaitConnected] when the
// wrapped workspace is remote. Local workspaces are always
// "connected".
func (m multi) WaitConnected(ctx context.Context) error {
	if rs, ok := m.Workspace.(RemoteScheme); ok {
		return rs.WaitConnected(ctx)
	}
	return nil
}
