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

package workspacerpc

import (
	"context"
	"errors"
	"io"
	"time"

	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	gomock "go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspaceapitest"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func doSetupClientServerTest(
	t *testing.T, s *Server,
) (conn *grpc.ClientConn, closeFn func()) {
	lis, err := net.Listen("tcp", ":0")
	require.NoError(t, err)

	grpcServer := grpc.NewServer()
	workspacerpc.RegisterFilesServer(grpcServer, s)
	workspacerpc.RegisterExecutorServer(grpcServer, s)
	workspacerpc.RegisterTerminalServer(grpcServer, s)
	workspacerpc.RegisterSchemeServer(grpcServer, s)

	go grpcServer.Serve(lis)

	conn, err = grpc.Dial(lis.Addr().String(), grpc.WithInsecure())
	require.NoError(t, err)

	closeFn = func() {
		grpcServer.Stop()
		lis.Close()
	}
	return
}

func setupClientServerTest(
	t *testing.T, s *Server,
) (*workspacerpc.Client, func()) {
	conn, closeFn := doSetupClientServerTest(t, s)
	client := workspacerpc.NewClient(context.Background(), conn)
	return client, func() {
		client.Close()
		closeFn()
	}
}

func setupClientServerUnitTest(
	t *testing.T, authorizer CommandAuthorizer,
) (*workspacerpc.Client, *Server, *workspaceapitest.MockFile, func()) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := workspaceapitest.NewMockFile(ctrl)
	mockExecutor := workspacetest.NewMockWorkspace(ctrl)
	if authorizer == nil {
		authorizer = CommandAuthorizerFunc(func(context.Context, workspaceapi.Cmd) error {
			return nil
		})
	}
	server := NewServer(mockExecutor, authorizer)
	client, cleanup := setupClientServerTest(t, server)
	return client, server, mock, cleanup
}

func expectCommand(t *testing.T, s *Server, pid int) {
	s.s.(*workspacetest.MockWorkspace).EXPECT().
		StartCommand(gomock.Any(), gomock.Any()).
		Return(workspaceapi.Pid(pid), nil)
}

func TestClientServer(t *testing.T) {
	ctx := context.Background()

	tsuite := []struct {
		description string
		do          func(*testing.T, *workspaceapitest.MockFile, *workspacerpc.Client, *Server)
	}{
		{"Command happy path", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				StartCommand(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
					assert.Equal(t, "six", cmd.Path)
					assert.Equal(t, []string{"arg1"}, cmd.Args)
					return workspaceapi.Pid(1), nil
				})
			pid, err := c.StartCommand(ctx, workspaceapi.Cmd{
				Path: "six",
				Args: []string{"arg1"},
			})
			require.NoError(t, err)
			assert.Equal(t, workspaceapi.Pid(1), pid)
		}},
		{"Command context cancel is propagated", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			var cmdCtx context.Context
			var mu sync.Mutex
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				StartCommand(gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
					cmdCtx = ctx
					mu.Unlock()
					return workspaceapi.Pid(1), nil
				})

			mu.Lock()
			ctx, cancel := context.WithCancel(ctx)
			pid, err := c.StartCommand(ctx, workspaceapi.Cmd{
				Path: "six",
			})
			require.NoError(t, err)
			assert.Equal(t, workspaceapi.Pid(1), pid)

			mu.Lock()
			cancel()
			waitCtx, cancelWait := context.WithTimeout(
				context.Background(), 2*time.Second)
			defer cancelWait()

			select {
			case <-waitCtx.Done():
				t.Logf("failed to kill process in time")
				t.Fail()
			case <-cmdCtx.Done():
			}
		}},
		{"Command Dir is passed from client to server", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				StartCommand(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
					assert.Equal(t, "six", cmd.Path)
					assert.Equal(t, []string{"arg1"}, cmd.Args)
					assert.Equal(t, "/tmp", cmd.Dir)
					return workspaceapi.Pid(1), nil
				})
			pid, err := c.StartCommand(ctx, workspaceapi.Cmd{
				Path: "six",
				Args: []string{"arg1"},
				Dir:  "/tmp",
			})
			require.NoError(t, err)
			assert.Equal(t, workspaceapi.Pid(1), pid)
		}},
		{"Command error", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				StartCommand(gomock.Any(), gomock.Any()).
				Return(workspaceapi.Pid(0), errors.New("boom"))

			_, err := c.StartCommand(ctx, workspaceapi.Cmd{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "boom")
		}},
		{"Signal happy path", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			expectCommand(t, s, 99)
			pid, err := c.StartCommand(ctx, workspaceapi.Cmd{
				Path: "six",
				Args: []string{"arg1"},
			})
			require.NoError(t, err)

			s.s.(*workspacetest.MockWorkspace).EXPECT().
				Signal(gomock.Eq(workspaceapi.Pid(99)), gomock.Eq(syscall.SIGTERM)).
				Return(nil)

			err = c.Signal(pid, syscall.SIGTERM)
			assert.NoError(t, err)
		}},
		{"Signal error", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			expectCommand(t, s, 99)
			pid, err := c.StartCommand(ctx, workspaceapi.Cmd{
				Path: "six",
				Args: []string{"arg1"},
			})
			require.NoError(t, err)

			s.s.(*workspacetest.MockWorkspace).EXPECT().
				Signal(gomock.Any(), gomock.Any()).
				Return(errors.New("boom"))

			err = c.Signal(pid, syscall.SIGKILL)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "boom")
		}},
		{"URI happy path", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			uri, err := workspaceapi.ParseURI("ssh://user@my_host:8080/tmp/hello/world.go")
			require.NoError(t, err)

			s.s.(*workspacetest.MockWorkspace).EXPECT().
				URI(gomock.Eq("/tmp/hello_world.go")).Return(uri, nil)

			actualUri, err := c.URI("/tmp/hello_world.go")
			assert.NoError(t, err)
			assert.Equal(t, uri.String(), actualUri.String())
		}},
		{"URI error", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				URI(gomock.Any()).Return(workspaceapi.URI{}, errors.New("boom"))

			_, err := c.URI("/tmp/hello_world.go")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "boom")
		}},
		{"Remove happy path", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				Remove(gomock.Eq("/tmp/hello_world.go")).
				Return(nil)

			err := c.Remove("/tmp/hello_world.go")
			assert.NoError(t, err)
		}},
		{"Remove error", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				Remove(gomock.Eq("/tmp/hello_world.go")).
				Return(errors.New("pow"))

			err := c.Remove("/tmp/hello_world.go")
			require.NotNil(t, err)
			assert.True(t, strings.Contains(err.Error(), "pow"))
		}},
		{"Open happy path", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				OpenFile(gomock.Eq("/tmp/hello_world.go"), gomock.Eq(os.O_RDWR|os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_SYNC|os.O_TRUNC), gomock.Eq(os.FileMode(0666))).
				Return(testFile{}, nil)

			f, err := c.OpenFile("/tmp/hello_world.go", os.O_RDWR|os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_SYNC|os.O_TRUNC, 0666)
			assert.Nil(t, err)
			assert.NotNil(t, f)
		}},
		{"Open error", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				OpenFile(gomock.Eq("/tmp/hello_world.go"), gomock.Eq(os.O_RDONLY), gomock.Eq(os.FileMode(2))).
				Return(nil, errors.New("pow"))

			f, err := c.OpenFile("/tmp/hello_world.go", os.O_RDONLY, 2)
			require.NotNil(t, err)
			assert.True(t, strings.Contains(err.Error(), "pow"))
			assert.Nil(t, f)
		}},
		{"NewPty happy path", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			ctrl := gomock.NewController(t)
			mockFile := workspaceapitest.NewMockFile(ctrl)
			mockFile.EXPECT().Name().Return("bla").AnyTimes()
			mockFile.EXPECT().Fd().Return(uintptr(99)).AnyTimes()
			slaveMockFile := workspaceapitest.NewMockFile(ctrl)
			slaveMockFile.EXPECT().Name().Return("blo").AnyTimes()
			slaveMockFile.EXPECT().Fd().Return(uintptr(199)).AnyTimes()
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				NewPty(gomock.Any()).
				Return(workspaceapi.Pty{Master: mockFile, Slave: slaveMockFile}, nil)

			pty, err := c.NewPty(ctx)
			require.NoError(t, err)

			mockFileForRead := workspaceapitest.NewMockFile(ctrl)
			mockFileForRead.EXPECT().Name().Return("bla").AnyTimes()
			mockFileForRead.EXPECT().Fd().Return(uintptr(99)).AnyTimes()
			mockFileForRead.EXPECT().Close().Return(nil).
				AnyTimes()
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				NewFile(gomock.Any(), gomock.Any()).
				Return(mockFileForRead).AnyTimes()
			mockFileForRead.EXPECT().Read(gomock.Any()).DoAndReturn(func(b []byte) (n int, err error) {
				b[0] = []byte("a")[0]
				return 1, io.EOF
			})
			b, err := io.ReadAll(pty.Master)
			require.NoError(t, err)
			assert.Equal(t, "a", string(b))
			assert.Equal(t, uintptr(99), pty.Master.Fd())
		}},
		{"NewPty error", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				NewPty(gomock.Any()).
				Return(workspaceapi.Pty{}, errors.New("bummer"))

			_, err := c.NewPty(ctx)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "bummer")
		}},
		{"SetPtySize happy path", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			ctrl := gomock.NewController(t)
			mockFile := workspaceapitest.NewMockFile(ctrl)
			mockFile.EXPECT().Name().Return("bla").AnyTimes()
			mockFile.EXPECT().Fd().Return(uintptr(1)).AnyTimes()
			slaveMockFile := workspaceapitest.NewMockFile(ctrl)
			slaveMockFile.EXPECT().Name().Return("blo").AnyTimes()
			slaveMockFile.EXPECT().Fd().Return(uintptr(2)).AnyTimes()
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				NewPty(gomock.Any()).
				Return(workspaceapi.Pty{Slave: slaveMockFile, Master: mockFile}, nil)
			pty, err := c.NewPty(ctx)
			require.NoError(t, err)

			s.s.(*workspacetest.MockWorkspace).EXPECT().
				NewFile(gomock.Any(), gomock.Any()).
				Return(mockFile).AnyTimes()

			mockFile.EXPECT().Close().Return(nil).
				AnyTimes( /* Close runs in runtime.Finalizer */ )

			want := workspaceapi.PtySize{Columns: 1, Rows: 1, PixelWidth: 10, PixelHeight: 20}
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				SetPtySize(gomock.Any(), gomock.Eq(want)).
				DoAndReturn(func(pty workspaceapi.Pty, size workspaceapi.PtySize) error {
					assert.Equal(t, want, size, "the pixel size travels with the cells")
					// must be exact instance returned by underlying Scheme
					// or else certain implementations might fail
					assert.Equal(t, mockFile, pty.Master)
					return nil
				})
			err = c.SetPtySize(pty, want)
			assert.NoError(t, err)
		}},
		{"SetPtySize error", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			ctrl := gomock.NewController(t)
			mockFile := workspaceapitest.NewMockFile(ctrl)
			mockFile.EXPECT().Name().Return("bla").AnyTimes()
			mockFile.EXPECT().Fd().Return(uintptr(1)).AnyTimes()
			slaveMockFile := workspaceapitest.NewMockFile(ctrl)
			slaveMockFile.EXPECT().Name().Return("blo").AnyTimes()
			slaveMockFile.EXPECT().Fd().Return(uintptr(11)).AnyTimes()
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				NewPty(gomock.Any()).
				Return(workspaceapi.Pty{Slave: slaveMockFile, Master: mockFile}, nil)
			pty, err := c.NewPty(ctx)
			require.NoError(t, err)

			s.s.(*workspacetest.MockWorkspace).EXPECT().
				NewFile(gomock.Any(), gomock.Any()).
				Return(mockFile).AnyTimes()

			s.s.(*workspacetest.MockWorkspace).EXPECT().
				SetPtySize(gomock.Any(), gomock.Eq(workspaceapi.PtySize{Columns: 1, Rows: 1})).
				Return(errors.New("bummer"))
			err = c.SetPtySize(pty, workspaceapi.PtySize{Columns: 1, Rows: 1})
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "bummer")
		}},
		{"Remove error", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				Remove(gomock.Eq("/tmp/hello_world.go")).
				Return(errors.New("pow"))

			err := c.Remove("/tmp/hello_world.go")
			require.NotNil(t, err)
			assert.True(t, strings.Contains(err.Error(), "pow"))
		}},
		{"ReadDir happy path", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				ReadDir(gomock.Any()).
				DoAndReturn(func(root string) ([]os.DirEntry, error) {
					return []os.DirEntry{dirEntry{name: "a"}}, nil
				})
			dirs, err := c.ReadDir("")
			require.NoError(t, err)
			require.Len(t, dirs, 1)
			assert.Equal(t, "a", dirs[0].Name())
		}},
		{"ReadDir error", func(t *testing.T, mock *workspaceapitest.MockFile, c *workspacerpc.Client, s *Server) {
			s.s.(*workspacetest.MockWorkspace).EXPECT().
				ReadDir(gomock.Any()).
				DoAndReturn(func(string) ([]os.DirEntry, error) {
					return nil, errors.New("boom")
				})
			l, err := c.ReadDir("")
			require.Error(t, err)
			assert.Nil(t, l)
			assert.Contains(t, err.Error(), "boom")
		}},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.description, func(t *testing.T) {
			client, server, mock, cleanup := setupClientServerUnitTest(t, nil)
			defer cleanup()
			tcase.do(t, mock, client, server)
		})
	}
}

func TestServerStartCommandAuthorizer(t *testing.T) {
	t.Run("receives command and allows", func(t *testing.T) {
		calls := 0
		client, server, _, cleanup := setupClientServerUnitTest(t, CommandAuthorizerFunc(
			func(ctx context.Context, cmd workspaceapi.Cmd) error {
				calls++
				assert.Equal(t, "six", cmd.Path)
				assert.Equal(t, []string{"arg1"}, cmd.Args)
				assert.Equal(t, "/tmp", cmd.Dir)
				return nil
			}))
		defer cleanup()

		server.s.(*workspacetest.MockWorkspace).EXPECT().
			StartCommand(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
				assert.Equal(t, "six", cmd.Path)
				return workspaceapi.Pid(1), nil
			})

		pid, err := client.StartCommand(context.Background(), workspaceapi.Cmd{
			Path: "six",
			Args: []string{"arg1"},
			Dir:  "/tmp",
		})
		require.NoError(t, err)
		assert.Equal(t, workspaceapi.Pid(1), pid)
		assert.Equal(t, 1, calls)
	})

	t.Run("denies before start", func(t *testing.T) {
		client, _, _, cleanup := setupClientServerUnitTest(t, CommandAuthorizerFunc(
			func(ctx context.Context, cmd workspaceapi.Cmd) error {
				assert.Equal(t, "six", cmd.Path)
				return errors.New("denied")
			}))
		defer cleanup()

		_, err := client.StartCommand(context.Background(), workspaceapi.Cmd{Path: "six"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "denied")
	})
}

func TestServerChrootErrorPropagation(t *testing.T) {
	ctx := context.Background()
	root := "unavailable-root"
	chrootErr := errors.New("chroot failed")
	tests := []struct {
		name string
		call func(*Server) error
	}{
		{"Read", func(s *Server) error {
			_, err := s.Read(ctx, &workspacerpc.ReadRequest{Root: root})
			return err
		}},
		{"ReadAt", func(s *Server) error {
			_, err := s.ReadAt(ctx, &workspacerpc.ReadRequest{Root: root})
			return err
		}},
		{"Write", func(s *Server) error {
			_, err := s.Write(ctx, &workspacerpc.WriteRequest{Root: root})
			return err
		}},
		{"Close", func(s *Server) error {
			_, err := s.Close(ctx, &workspacerpc.CloseFileRequest{Root: root})
			return err
		}},
		{"Sync", func(s *Server) error {
			_, err := s.Sync(ctx, &workspacerpc.SyncRequest{Root: root})
			return err
		}},
		{"Truncate", func(s *Server) error {
			_, err := s.Truncate(ctx, &workspacerpc.TruncateRequest{Root: root})
			return err
		}},
		{"Seek", func(s *Server) error {
			_, err := s.Seek(ctx, &workspacerpc.SeekRequest{Root: root})
			return err
		}},
		{"Stat", func(s *Server) error {
			_, err := s.Stat(ctx, &workspacerpc.StatRequest{Root: root})
			return err
		}},
		{"Watch", func(s *Server) error {
			return s.Watch(&workspacerpc.WatchRequest{
				Root: root,
				Path: "path",
				Events: []workspacerpc.Event{
					workspacerpc.Event_Write,
				},
			}, testWatchServer{ctx: ctx})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			scheme := workspacetest.NewMockWorkspace(ctrl)
			scheme.EXPECT().Chroot(root).Return(nil, chrootErr)
			server := NewServer(scheme, CommandAuthorizerFunc(
				func(context.Context, workspaceapi.Cmd) error { return nil }))

			err := test.call(server)

			require.ErrorIs(t, err, chrootErr)
		})
	}
}

func TestServerReadHonorsRequestedSize(t *testing.T) {
	t.Parallel()
	const requested = 2 << 20
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	scheme := workspacetest.NewMockWorkspace(ctrl)
	file := workspaceapitest.NewMockFile(ctrl)
	scheme.EXPECT().NewFile(uintptr(1), "large").Return(file)
	file.EXPECT().Read(gomock.Any()).DoAndReturn(func(buf []byte) (int, error) {
		assert.Len(t, buf, requested)
		copy(buf, "chunk")
		return len("chunk"), nil
	})
	server := NewServer(scheme, CommandAuthorizerFunc(
		func(context.Context, workspaceapi.Cmd) error { return nil }))

	resp, err := server.Read(ctx, &workspacerpc.ReadRequest{
		Fd:       1,
		Filename: "large",
		N:        requested,
	})

	require.NoError(t, err)
	assert.Equal(t, []byte("chunk"), resp.GetData())
	assert.EqualValues(t, len("chunk"), resp.GetN())
	assert.False(t, resp.GetIsEof())
}

func TestServerReadRejectsNegativeSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(*Server, *workspacerpc.ReadRequest) error
	}{
		{"Read", func(s *Server, req *workspacerpc.ReadRequest) error {
			_, err := s.Read(context.Background(), req)
			return err
		}},
		{"ReadAt", func(s *Server, req *workspacerpc.ReadRequest) error {
			_, err := s.ReadAt(context.Background(), req)
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			scheme := workspacetest.NewMockWorkspace(ctrl)
			file := workspaceapitest.NewMockFile(ctrl)
			scheme.EXPECT().NewFile(uintptr(1), "negative").Return(file)
			server := NewServer(scheme, CommandAuthorizerFunc(
				func(context.Context, workspaceapi.Cmd) error { return nil }))

			err := test.call(server, &workspacerpc.ReadRequest{
				Fd:       1,
				Filename: "negative",
				N:        -1,
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid read size")
		})
	}
}

// A scheme call that blocks (e.g. a slow remote Stat) must not prevent
// unrelated requests from being served: schemes are goroutine safe and
// the server must not serialize calls into them.
func TestServerDoesNotSerializeSchemeCalls(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	scheme := workspacetest.NewMockWorkspace(ctrl)

	statEntered := make(chan struct{})
	release := make(chan struct{})
	scheme.EXPECT().Stat("slow").DoAndReturn(func(string) (os.FileInfo, error) {
		close(statEntered)
		<-release
		return nil, os.ErrNotExist
	})
	uri, err := workspaceapi.ParseURI("file:///tmp/fast")
	require.NoError(t, err)
	scheme.EXPECT().URI("fast").Return(uri, nil)

	server := NewServer(scheme, CommandAuthorizerFunc(
		func(context.Context, workspaceapi.Cmd) error { return nil }))

	statDone := make(chan error, 1)
	go func() {
		_, err := server.Stat(ctx, &workspacerpc.StatRequest{Filename: "slow"})
		statDone <- err
	}()
	<-statEntered

	uriDone := make(chan error, 1)
	go func() {
		_, err := server.URI(ctx, &workspacerpc.URIRequest{Path: "fast"})
		uriDone <- err
	}()

	select {
	case err := <-uriDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("URI blocked behind an in-flight Stat: server serializes scheme calls")
	}

	close(release)
	require.NoError(t, <-statDone)
}

func TestServerWatchpointLifecycle(t *testing.T) {
	t.Run("error does not register watchpoint", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		scheme := workspacetest.NewMockWorkspace(ctrl)
		watchErr := errors.New("watch failed")
		scheme.EXPECT().Watch("path", gomock.Any(), gomock.Any()).
			Return(0, watchErr)
		server := NewServer(scheme, CommandAuthorizerFunc(
			func(context.Context, workspaceapi.Cmd) error { return nil }))

		err := server.Watch(&workspacerpc.WatchRequest{
			Path:   "path",
			Events: []workspacerpc.Event{workspacerpc.Event_Write},
		}, testWatchServer{ctx: context.Background()})

		require.ErrorIs(t, err, watchErr)
		server.mu.Lock()
		assert.Empty(t, server.watchpoints)
		server.mu.Unlock()
	})

	t.Run("watchpoint removed when stream ends", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		scheme := workspacetest.NewMockWorkspace(ctrl)
		scheme.EXPECT().Watch("path", gomock.Any(), gomock.Any()).
			Return(7, nil)
		scheme.EXPECT().StopWatch(7).Return(nil)
		server := NewServer(scheme, CommandAuthorizerFunc(
			func(context.Context, workspaceapi.Cmd) error { return nil }))

		ctx, cancel := context.WithCancel(context.Background())
		watchDone := make(chan error, 1)
		go func() {
			watchDone <- server.Watch(&workspacerpc.WatchRequest{
				Path:   "path",
				Events: []workspacerpc.Event{workspacerpc.Event_Write},
			}, testWatchServer{ctx: ctx})
		}()

		require.Eventually(t, func() bool {
			server.mu.Lock()
			defer server.mu.Unlock()
			return len(server.watchpoints) == 1
		}, 5*time.Second, time.Millisecond)

		cancel()
		require.ErrorIs(t, <-watchDone, context.Canceled)
		server.mu.Lock()
		assert.Empty(t, server.watchpoints)
		server.mu.Unlock()
	})

	t.Run("streams whose schemes hand out the same id do not clobber each other", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		scheme := workspacetest.NewMockWorkspace(ctrl)
		// A chroot is a fresh scheme with its own id counter, so two
		// streams rooted in different directories both see id 2.
		for _, root := range []string{"a", "b"} {
			chroot := workspacetest.NewMockWorkspace(ctrl)
			chroot.EXPECT().Watch("path", gomock.Any(), gomock.Any()).Return(2, nil)
			chroot.EXPECT().StopWatch(2).Return(nil)
			scheme.EXPECT().Chroot(root).Return(chroot, nil)
		}
		server := NewServer(scheme, CommandAuthorizerFunc(
			func(context.Context, workspaceapi.Cmd) error { return nil }))

		type stream struct {
			cancel context.CancelFunc
			done   chan error
			id     chan int64
		}
		start := func(root string) stream {
			ctx, cancel := context.WithCancel(context.Background())
			st := stream{cancel: cancel, done: make(chan error, 1), id: make(chan int64, 1)}
			go func() {
				st.done <- server.Watch(&workspacerpc.WatchRequest{
					Path:   "path",
					Root:   root,
					Events: []workspacerpc.Event{workspacerpc.Event_Write},
				}, testWatchServer{ctx: ctx, ids: st.id})
			}()
			return st
		}
		a := start("a")
		idA := <-a.id
		b := start("b")
		idB := <-b.id
		assert.NotEqual(t, idA, idB, "server must hand out unique ids across chroots")

		a.cancel()
		require.ErrorIs(t, <-a.done, context.Canceled)

		_, err := server.StopWatch(context.Background(), &workspacerpc.StopWatchRequest{Id: idB})
		require.NoError(t, err, "stopping the second stream after the first ended")
		require.ErrorIs(t, <-b.done, context.Canceled)
		server.mu.Lock()
		assert.Empty(t, server.watchpoints)
		server.mu.Unlock()
	})
}

func setupClientServerIntegrationTest(
	t *testing.T, scheme schemeapi.Scheme,
) (*workspacerpc.Client, func()) {
	server := NewServer(scheme, CommandAuthorizerFunc(
		func(context.Context, workspaceapi.Cmd) error { return nil }))
	return setupClientServerTest(t, server)
}

func TestSchemeIntegration(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		workspacetest.TestWorkspaceSchemeFiles(t, func(t *testing.T) schemeapi.Scheme {
			memURI, err := workspaceapi.ParseURI("memory:///tmp")
			require.NoError(t, err)
			scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), memURI)
			require.NoError(t, err)
			client, cleanup := setupClientServerIntegrationTest(t, scheme)
			t.Cleanup(cleanup)
			return client
		})
	})

	t.Run("file", func(t *testing.T) {
		workspacetest.TestWorkspaceSchemeExecutor(t, func(t *testing.T) schemeapi.Scheme {
			dir, err := os.MkdirTemp("", "workspacerpc_suite")
			require.NoError(t, err)

			workspaceURI, err := workspaceapi.ParseURI("file://" + dir)
			require.NoError(t, err)

			fileScheme, err := workspace.NewFileScheme(
				context.Background(), config.NopConfig(), workspaceURI)
			require.NoError(t, err)

			client, cleanup := setupClientServerIntegrationTest(t, fileScheme)
			t.Cleanup(func() {
				_ = os.RemoveAll(dir)
				cleanup()
			})
			return client
		})
	})
}

var _ billy.File = testFile{}

type testFile struct {
}

func (t testFile) Name() string {
	return ""
}

func (t testFile) Stat() (os.FileInfo, error) {
	panic("unimplemented")
}

func (t testFile) Sync() error {
	return nil
}
func (t testFile) Truncate(size int64) error {
	return nil
}

func (t testFile) Seek(x int64, y int) (int64, error) {
	return 0, nil
}

func (t testFile) Read(b []byte) (int, error) {
	return 0, io.EOF
}

func (t testFile) ReadAt(b []byte, offset int64) (int, error) {
	return 0, io.EOF
}

func (t testFile) Write(b []byte) (int, error) {
	return 0, nil
}

func (t testFile) Fd() uintptr {
	return 0
}

func (t testFile) Close() error {
	return nil
}

type dirEntry struct {
	c        *workspacerpc.Client
	name     string
	isDir    bool
	modeType int32
}

type testWatchServer struct {
	ctx context.Context
	ids chan<- int64
}

func (s testWatchServer) Send(msg *workspacerpc.WatchMessage) error {
	if s.ids != nil && msg.GetType() == workspacerpc.WatchMessage_TypeResponse {
		s.ids <- msg.GetResponse().GetId()
	}
	return nil
}

func (s testWatchServer) Context() context.Context {
	return s.ctx
}

func (testWatchServer) SendMsg(any) error {
	return nil
}

func (testWatchServer) RecvMsg(any) error {
	return io.EOF
}

func (testWatchServer) SetHeader(metadata.MD) error {
	return nil
}

func (testWatchServer) SendHeader(metadata.MD) error {
	return nil
}

func (testWatchServer) SetTrailer(metadata.MD) {}

func (e dirEntry) Name() string {
	return e.name
}

func (e dirEntry) IsDir() bool {
	return e.isDir
}

func (e dirEntry) Type() os.FileMode {
	return os.FileMode(e.modeType)
}

func (e dirEntry) Info() (os.FileInfo, error) {
	return e.c.Stat(e.Name())
}
