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

package extension

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/workspace"
	tworkspacerpc "unstable.build/rune/internal/workspace/workspacerpc"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestWorkspaceResourcesUsesStartCommandAuthorizer(t *testing.T) {
	t.Parallel()

	scheme := &testWorkspace{NopScheme: &workspacetest.NopScheme{}}
	scheme.StartCommandFunc = func(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error) {
		return 1, nil
	}

	calls := 0
	resources := WorkspaceResources(scheme, tworkspacerpc.CommandAuthorizerFunc(
		func(ctx context.Context, cmd workspaceapi.Cmd) error {
			calls++
			assert.Equal(t, "echo", cmd.Path)
			assert.Equal(t, []string{"hello"}, cmd.Args)
			return nil
		}))

	lis, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	grpcServer := grpc.NewServer()
	closer, err := resources[extensionapi.PermissionExecute].Register(grpcServer, new(sync.Mutex))
	require.NoError(t, err)
	defer closer.Close()
	go grpcServer.Serve(lis)
	defer grpcServer.Stop()

	conn, err := grpc.Dial(lis.Addr().String(), grpc.WithInsecure())
	require.NoError(t, err)
	defer conn.Close()
	client := workspacerpc.NewClient(context.Background(), conn)
	defer client.Close()

	pid, err := client.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "echo",
		Args: []string{"hello"},
	})
	require.NoError(t, err)
	require.NotZero(t, pid)
	assert.Equal(t, 1, calls)
}

type testWorkspace struct {
	*workspacetest.NopScheme
}

func (t *testWorkspace) Load(
	file workspaceapi.URI, buf *cell.Buffer, swapDir workspaceapi.URI, readOnly bool,
) (workspace.FlusherCloser, error) {
	return testFlusherCloser{}, nil
}

func (t *testWorkspace) Recover(
	file, swapFilePath workspaceapi.URI, buf *cell.Buffer, force bool,
) (workspace.FlusherCloser, error) {
	return testFlusherCloser{}, nil
}

func (t *testWorkspace) LoaderFor(file workspaceapi.URI) (workspace.FileLoader, error) {
	return workspace.FileLoaderOf(t, file), nil
}

var _ workspace.Workspace = (*testWorkspace)(nil)

type testFlusherCloser struct{}

func (testFlusherCloser) Flush(context.Context) (<-chan error, error) {
	return testFlusherCloserDone(), nil
}
func (testFlusherCloser) LastFlush() time.Time { return time.Time{} }
func (testFlusherCloser) ForceFlush(context.Context) (<-chan error, error) {
	return testFlusherCloserDone(), nil
}
func (testFlusherCloser) Reload(context.Context) (<-chan error, error) {
	return testFlusherCloserDone(), nil
}
func (testFlusherCloser) Close() error { return nil }

func testFlusherCloserDone() <-chan error {
	ch := make(chan error, 1)
	ch <- nil
	close(ch)
	return ch
}

var _ io.Closer = testFlusherCloser{}
