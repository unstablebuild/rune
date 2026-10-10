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
}

// SchemeManager abstracts the ability to register new URI schemes.
type SchemeManager interface {
	RegisterScheme(string, schemeapi.SchemeFunc) error
	UnregisterScheme(string) error
}

// WorkspaceManager abstracts the ability to register schemes and workspaces.
type WorkspaceManager interface {
	SchemeManager
	AddWorkspace(context.Context, workspaceapi.URI) (Workspace, error)
	Workspace(workspaceapi.URI) (Workspace, bool, error)
	// IncrementReference bumps the reference count of the workspace
	// registered under uri. A workspace whose reference count has
	// ever been incremented is owned by its callers and will be
	// closed when DecrementReference brings the count back to zero.
	// Workspaces that are never incremented (e.g. the IDE-owned
	// active workspace) are not subject to refcount teardown.
	IncrementReference(workspaceapi.URI)
	// DecrementReference decrements the reference count of the
	// workspace registered under uri and closes the workspace when
	// the count reaches zero. Decrementing a uri that was never
	// incremented is a no-op.
	DecrementReference(workspaceapi.URI) error
	// RemoveWorkspace unregisters uri and returns the underlying
	// Workspace so the caller can close it off the event loop. The
	// manager's maps are only ever touched on the event loop.
	RemoveWorkspace(workspaceapi.URI) (Workspace, bool)
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
