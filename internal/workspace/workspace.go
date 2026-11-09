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

//revive:disable:exported
package workspace

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/cell"
)

// Workspace binds a Loader and a Scheme together for use in internal
// packages that both need to share an API with external resources
// and use a Loader to load resources into buffers.
type Workspace interface {
	Loader
	schemeapi.Scheme
	InstallDataDirProvider
	// PathCaseSensitive reports whether two paths that differ only in
	// letter case name different files on the workspace host. It is
	// true when the host cannot tell, so callers keep comparing paths
	// exactly. The answer is fixed for the workspace's lifetime.
	PathCaseSensitive() bool
}

// RemoteScheme is implemented by schemes whose underlying transport
// can drop and reconnect (e.g. SSH).
type RemoteScheme interface {
	OnDisconnect() <-chan struct{}
	WaitConnected(ctx context.Context) error
}

// InstallDataDirProvider reports the workspace host's Rune data directory.
type InstallDataDirProvider interface {
	InstallDataDir(ctx context.Context) (string, error)
}

// PackageHost is implemented by a workspace on another machine whose
// connection can carry services other than the workspace's own, such
// as the host's package manager.
type PackageHost interface {
	// HostConn returns the connection to the workspace host. ok is
	// false when the workspace has none, e.g. it is on this machine.
	HostConn() (cc grpc.ClientConnInterface, ok bool)
}

// Loader abstracts the ability to load resource data into a working buffer
// and provide a FlusherCloser to manage flushing data to storage.
type Loader interface {
	Load(file workspaceapi.URI, buf *cell.Buffer, swapDir workspaceapi.URI, readOnly bool) (FlusherCloser, error)
	Recover(file, swapFilePath workspaceapi.URI, buf *cell.Buffer, force bool) (FlusherCloser, error)
	Remove(file string) error
	// LoaderFor returns the FileLoader that loads and recovers file. It
	// must be called on the event loop, because it may take a reference
	// to the workspace file belongs to; the returned FileLoader holds it.
	LoaderFor(file workspaceapi.URI) (FileLoader, error)
}

// FileLoader loads or recovers the file it was returned for by
// [Loader.LoaderFor]. Its Load and Recover may run on any goroutine.
type FileLoader interface {
	Load(buf *cell.Buffer, swapDir workspaceapi.URI, readOnly bool) (FlusherCloser, error)
	Recover(swapFilePath workspaceapi.URI, buf *cell.Buffer, force bool) (FlusherCloser, error)
	// Release releases what LoaderFor took for the file. Call it once, on
	// the event loop, unless Load or Recover succeeded: the FlusherCloser
	// they returned then holds it until it is closed.
	Release() error
}

// FileLoaderOf returns a FileLoader that loads file through l and holds
// nothing, so its Release does nothing.
func FileLoaderOf(l Loader, file workspaceapi.URI) FileLoader {
	return directLoader{l: l, file: file}
}

type directLoader struct {
	l    Loader
	file workspaceapi.URI
}

func (d directLoader) Load(
	buf *cell.Buffer, swapDir workspaceapi.URI, readOnly bool,
) (FlusherCloser, error) {
	return d.l.Load(d.file, buf, swapDir, readOnly)
}

func (d directLoader) Recover(
	swapFilePath workspaceapi.URI, buf *cell.Buffer, force bool,
) (FlusherCloser, error) {
	return d.l.Recover(d.file, swapFilePath, buf, force)
}

func (directLoader) Release() error { return nil }

// SchemeManager abstracts the ability to register new URI schemes.
type SchemeManager interface {
	RegisterScheme(string, schemeapi.SchemeFunc) error
	UnregisterScheme(string) error
}

// WorkspaceManager abstracts the ability to register schemes and workspaces.
type WorkspaceManager interface {
	SchemeManager
	// AcquireWorkspace returns the workspace registered under uri,
	// registering one if there is none, and takes a reference to it.
	// Every caller of the same uri shares the workspace, which stays
	// open until each reference is released with ReleaseWorkspace or
	// the manager closes.
	AcquireWorkspace(context.Context, workspaceapi.URI) (Workspace, error)
	Workspace(workspaceapi.URI) (Workspace, bool, error)
	// ReleaseWorkspace releases a reference taken by AcquireWorkspace.
	// Releasing the last one unregisters uri and returns the workspace
	// and true without closing it: closing it is up to the caller. It
	// returns false while other references remain, and when uri is not
	// registered.
	ReleaseWorkspace(workspaceapi.URI) (Workspace, bool)
}

var ErrOpenInOtherWorkspace = errors.New("file should be opened in another workspace")

// ErrFlushInProgress is returned by Flush, ForceFlush and Reload when a
// previous async flush or reload for the same buffer has not yet
// completed.
var ErrFlushInProgress = errors.New(
	"save or reload already in progress for this buffer")

// ErrNoFlushInProgress is returned by callers that need to cancel an
// in-flight flush/reload when there is nothing pending. It is not
// produced by FlusherCloser itself; it is exported here so that
// higher layers can share a single sentinel.
var ErrNoFlushInProgress = errors.New("no save in progress for this buffer")

// FlusherCloser wraps methods to manipulate a cell.Buffer's persistence.
type FlusherCloser interface {
	Flush(ctx context.Context) (<-chan error, error)
	ForceFlush(ctx context.Context) (<-chan error, error)
	Reload(ctx context.Context) (<-chan error, error)
	LastFlush() time.Time
	io.Closer
}
