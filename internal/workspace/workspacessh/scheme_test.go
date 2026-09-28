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

package workspacessh

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestNewScheme(t *testing.T) {
	tsuite := []struct {
		desc         string
		workspaceURI string
		expectedURI  string
		expectedErr  string
	}{
		{"no port no user workspace absolute", "ssh://ernest.photography", "", ""},
		{"port no user workspace absolute", "ssh://ernest.photography:4222", "", ""},
		{"port user workspace absolute", "ssh://ernie@ernest.photography:4222", "", ""},
		{"port user workspace absolute slash", "ssh://ernie@ernest.photography:4222/", "", ""},
		{"no port user workspace absolute slash", "ssh://ernie@ernest.photography/", "", ""},
		{"no port user workspace relative slash", "ssh://ernie@ernest.photography/~/src",
			"ssh://ernie@ernest.photography/home/ernie/src", ""},
		{"no host returns error", "ssh:///tmp", "", "could not parse ssh workspaceapi.URI: ssh scheme with empty host is invalid"},
		{"different scheme returns error", "file:///tmp", "", "invalid non-ssh scheme"},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			workspaceURI, err := workspaceapi.ParseURI(tcase.workspaceURI)
			require.NoError(t, err)

			// sut
			ch := make(chan string, 1) // when uri is a file URI it gets called twice
			s, err := newTestScheme(config.NopConfig(), workspaceURI,
				func(ctx context.Context, uri workspaceapi.URI, closeHook func(error)) (schemeapi.Scheme, error) {
					go func() { ch <- uri.String() }()
					return workspacetest.NewNopScheme("test")(
						ctx, config.NopConfig(), uri)
				})
			if tcase.expectedErr != "" {
				assert.EqualError(t, err, tcase.expectedErr)
			} else {
				assert.NoError(t, err)
				expectedURI := tcase.expectedURI
				if expectedURI == "" {
					expectedURI = workspaceURI.String()
				}
				actualURI := <-ch
				assert.Equal(t, expectedURI, actualURI)
				assert.NoError(t, s.Close())
			}
		})
	}
}

func TestURI(t *testing.T) {
	tsuite := []struct {
		desc         string
		workspaceURI string
		inPath       string
		expectedOut  string
		expectedErr  string
	}{
		{"no port no user workspace absolute", "ssh://ernest.photography", "/",
			"ssh://ernest.photography/", ""},
		{"no port no user workspace home relative", "ssh://ernest.photography", "/~/",
			"ssh://ernest.photography/home/git", ""}, // newTestScheme sets git as default user
		{"port no user workspace home relative", "ssh://ernest.photography:455", "/~/",
			"ssh://ernest.photography:455/home/git", ""},
		{"port no user workspace absolute", "ssh://ernest.photography:455", "/tmp/var",
			"ssh://ernest.photography:455/tmp/var", ""},
		{"port user workspace absolute", "ssh://ernie@ernest.photography:455", "/tmp/var",
			"ssh://ernie@ernest.photography:455/tmp/var", ""},
		{"port user workspace home relative", "ssh://ernie@ernest.photography:455", "~/src/blue",
			"ssh://ernie@ernest.photography:455/home/ernie/src/blue", ""},
		{"relative common path implicit home folder", "ssh://ernest.photography/home/git/src/blue", "~/src/blue/fireplace.txt",
			"ssh://ernest.photography/home/git/src/blue/fireplace.txt", ""},
		{"relative common path explicit home folder", "ssh://ernest.photography/home/git/src/blue", "/home/git/src/blue/fireplace.txt",
			"ssh://ernest.photography/home/git/src/blue/fireplace.txt", ""},
		{"relative upwards workspace tree", "ssh://ernest.photography/home/git/src/blue", "../../",
			"ssh://ernest.photography/home/git", ""},
		{"relative upwards workspace tree ending slash", "ssh://ernest.photography/home/git/src/blue/", "../../",
			"ssh://ernest.photography/home/git", ""},
		{"relative upwards workspace tree ./ path", "ssh://ernest.photography/home/git/src/blue", "./../../file.txt",
			"ssh://ernest.photography/home/git/file.txt", ""},
		{"dollar in name is literal", "ssh://ernest.photography/home/git/src", "routes/$RUNE_TEST_UNSET_VAR/f",
			"ssh://ernest.photography/home/git/src/routes/$RUNE_TEST_UNSET_VAR/f", ""},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			workspaceURI, err := workspaceapi.ParseURI(tcase.workspaceURI)
			require.NoError(t, err)

			s := newNopScheme(t, workspaceURI)
			defer s.Close()

			// sut
			actualOut, actualErr := s.URI(tcase.inPath)
			if tcase.expectedErr != "" {
				assert.Error(t, actualErr)
				assert.Nil(t, actualOut)
			} else {
				assert.NoError(t, actualErr)

				expectedURI, err := workspaceapi.ParseURI(tcase.expectedOut)
				require.NoError(t, err)
				assert.Equal(t, expectedURI.String(), actualOut.String())
			}

		})
	}
}

// TestConnectSchemeUsesRune asserts that the workspace-scheme bootstrap
// looks for the `rune` binary on the remote — the same name that
// `cmd/rune` accepts via its `--workspace-server / -x` flag (see
// cmd/rune/main.go). A previous version of the code looked for an
// obsolete binary called `six`, which made every real connection fail
// with "six executable was not found on remote" even when authentication
// succeeded. The bug went undetected because the docker e2e matrix
// short-circuits via TestAuthDial and never reaches connectScheme.
func TestConnectSchemeUsesRune(t *testing.T) {
	rec := &recordingRemote{}

	s := new(scheme)
	s.ctx, s.cancelCtx = context.WithCancel(context.Background())
	defer s.cancelCtx()
	s.remoteFn = func(context.Context, sshConfig, workspaceapi.URI) (remote, error) {
		return rec, nil
	}
	s.getUser = func() (*user.User, error) {
		return &user.User{Username: "test", HomeDir: "/home/test"}, nil
	}
	s.ui = errorUI{}
	uri, err := workspaceapi.ParseURI("ssh://test@example.com/tmp")
	require.NoError(t, err)
	s.user, s.homedir, s.hostPort, s.basePath, err = parseWorkspaceURI(uri, s.getUser)
	require.NoError(t, err)

	closeHook := func(error) {}
	scheme, err := s.connectScheme(context.Background(), uri, closeHook)
	require.NoError(t, err)
	if scheme != nil {
		_ = scheme.Close()
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.NotEmpty(t, rec.commands, "expected connectScheme to issue commands")

	// First command is `which <bin>` — that's the canonical signal.
	first := rec.commands[0]
	assert.Equal(t, remotePathEnv+" which", first.Path,
		"first command should be `which <remote bin>` with the "+
			"~/.local/bin PATH injection; got %+v", first)
	require.NotEmpty(t, first.Args)
	assert.Equal(t, "rune", first.Args[0],
		"connectScheme must look for the `rune` binary on the remote "+
			"(matches cmd/rune --workspace-server). Got %q.",
		first.Args[0])

	// The actual workspace-server invocation should also use `rune`.
	var sawServer bool
	for _, c := range rec.commands {
		if c.Path == remotePathEnv+" rune" {
			sawServer = true
			assert.Contains(t, c.Args, "-x",
				"rune workspace server should be started with -x; got %+v", c)
			break
		}
	}
	assert.True(t, sawServer,
		"expected at least one command to invoke `rune` with the "+
			"~/.local/bin PATH injection; saw %+v", rec.commands)
}

// TestConnectSchemeSkipPreflight asserts that when
// sshConfig.skipPreflight is true, connectScheme does NOT issue the
// `which rune` and `ls <path>` pre-flight probes. Each probe opens a
// fresh SSH session channel, so skipping them is the user-visible
// escape hatch on servers with a tight MaxSessions budget (manual
// scenario workspace/workspacessh/manual_test/12_max_sessions_one.sh).
func TestConnectSchemeSkipPreflight(t *testing.T) {
	rec := &recordingRemote{}

	s := new(scheme)
	s.ctx, s.cancelCtx = context.WithCancel(context.Background())
	defer s.cancelCtx()
	s.cfg.skipPreflight = true
	s.remoteFn = func(context.Context, sshConfig, workspaceapi.URI) (remote, error) {
		return rec, nil
	}
	s.getUser = func() (*user.User, error) {
		return &user.User{Username: "test", HomeDir: "/home/test"}, nil
	}
	s.ui = errorUI{}
	uri, err := workspaceapi.ParseURI("ssh://test@example.com/tmp")
	require.NoError(t, err)
	s.user, s.homedir, s.hostPort, s.basePath, err = parseWorkspaceURI(uri, s.getUser)
	require.NoError(t, err)

	closeHook := func(error) {}
	scheme, err := s.connectScheme(context.Background(), uri, closeHook)
	require.NoError(t, err)
	if scheme != nil {
		_ = scheme.Close()
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.commands, 1,
		"skip_preflight must avoid the `which` and `ls` probes; only "+
			"the rune workspace-server invocation should be issued. "+
			"Got %+v", rec.commands)
	assert.Equal(t, remotePathEnv+" rune", rec.commands[0].Path,
		"the only command issued must be the rune workspace server, "+
			"launched with the ~/.local/bin PATH injection so the "+
			"supported install location works even without preflight")
	assert.Contains(t, rec.commands[0].Args, "-x",
		"rune workspace server should be started with -x; got %+v",
		rec.commands[0])
}

func TestRunAndWaitHonorsAttemptContext(t *testing.T) {
	schemeCtx, cancelScheme := context.WithCancel(context.Background())
	defer cancelScheme()
	s := &scheme{ctx: schemeCtx}
	remote := newBlockingRemote()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		_, _, err := s.runAndWait(ctx, remote, "ls", "/tmp")
		result <- err
	})

	select {
	case <-remote.started:
	case <-time.After(time.Second):
		t.Fatal("preflight command did not start")
	}
	cancel()

	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		remote.release()
		<-result
		t.Fatal("preflight command ignored its attempt context")
	}
	require.Eventually(t, func() bool {
		return remote.sessionClosed()
	}, time.Second, 10*time.Millisecond)
}

// findRuneServerCmd returns the recorded command that launches the remote
// `rune` workspace server, i.e. the invocation whose args contain `-x`.
func findRuneServerCmd(t *testing.T, rec *recordingRemote) workspaceapi.Cmd {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, c := range rec.commands {
		if slices.Contains(c.Args, "-x") {
			return c
		}
	}
	t.Fatalf("no rune workspace-server command found in %+v", rec.commands)
	return workspaceapi.Cmd{}
}

func newProvisionTestScheme(rec *recordingRemote, provisionFn func() string) (*scheme, workspaceapi.URI) {
	s := new(scheme)
	s.ctx, s.cancelCtx = context.WithCancel(context.Background())
	s.cfg.skipPreflight = true
	s.cfg.provisionPackages = true
	s.provisionFn = provisionFn
	s.remoteFn = func(context.Context, sshConfig, workspaceapi.URI) (remote, error) {
		return rec, nil
	}
	s.getUser = func() (*user.User, error) {
		return &user.User{Username: "test", HomeDir: "/home/test"}, nil
	}
	s.ui = errorUI{}
	uri, _ := workspaceapi.ParseURI("ssh://test@example.com/tmp")
	s.user, s.homedir, s.hostPort, s.basePath, _ = parseWorkspaceURI(uri, s.getUser)
	return s, uri
}

// TestConnectSchemeProvisionManifest asserts that a non-empty provisioning
// manifest is threaded into the remote `rune -x` invocation as
// `--install <manifest>`, immediately after `-x <path>` and as a single
// unquoted token.
func TestConnectSchemeProvisionManifest(t *testing.T) {
	rec := &recordingRemote{}
	manifest := "rune-go@1.2.3,rune-python@4.5.6"
	s, uri := newProvisionTestScheme(rec, func() string { return manifest })
	defer s.cancelCtx()

	scheme, err := s.connectScheme(context.Background(), uri, func(error) {})
	require.NoError(t, err)
	if scheme != nil {
		_ = scheme.Close()
	}

	cmd := findRuneServerCmd(t, rec)
	require.Contains(t, cmd.Args, "--install",
		"non-empty manifest must add --install; got %+v", cmd.Args)
	idx := slices.Index(cmd.Args, "--install")
	require.Less(t, idx+1, len(cmd.Args), "--install must be followed by a value")
	assert.Equal(t, manifest, cmd.Args[idx+1],
		"manifest must be a single unquoted token")

	// --install must come right after `-x <path>`.
	xIdx := slices.Index(cmd.Args, "-x")
	require.GreaterOrEqual(t, xIdx, 0)
	assert.Equal(t, xIdx+2, idx,
		"--install must directly follow `-x <path>`; got %+v", cmd.Args)
}

// TestConnectSchemeRemoteDataDir asserts that WithRemoteDataDir threads an
// explicit `--datadir ~/<name>` into the remote `rune -x` invocation so the
// remote provisions into a known location (~ expands on the remote shell),
// placed right after `-x <path>` and before any `--install` manifest.
func TestConnectSchemeRemoteDataDir(t *testing.T) {
	rec := &recordingRemote{}
	manifest := "rune-go@1.2.3"
	s, uri := newProvisionTestScheme(rec, func() string { return manifest })
	s.remoteDataDir = ".runedev"
	defer s.cancelCtx()

	scheme, err := s.connectScheme(context.Background(), uri, func(error) {})
	require.NoError(t, err)
	if scheme != nil {
		_ = scheme.Close()
	}

	cmd := findRuneServerCmd(t, rec)
	ddIdx := slices.Index(cmd.Args, "--datadir")
	require.GreaterOrEqual(t, ddIdx, 0, "must add --datadir; got %+v", cmd.Args)
	require.Less(t, ddIdx+1, len(cmd.Args), "--datadir must be followed by a value")
	assert.Equal(t, "~/.runedev", cmd.Args[ddIdx+1],
		"--datadir value must be ~/<name>; got %+v", cmd.Args)

	xIdx := slices.Index(cmd.Args, "-x")
	require.GreaterOrEqual(t, xIdx, 0)
	assert.Equal(t, xIdx+2, ddIdx,
		"--datadir must directly follow `-x <path>`; got %+v", cmd.Args)

	instIdx := slices.Index(cmd.Args, "--install")
	require.GreaterOrEqual(t, instIdx, 0)
	assert.Greater(t, instIdx, ddIdx,
		"--install must come after --datadir; got %+v", cmd.Args)
}

// TestConnectSchemeNoRemoteDataDir asserts that when WithRemoteDataDir is
// unset the remote invocation omits --datadir, leaving the remote default.
func TestConnectSchemeNoRemoteDataDir(t *testing.T) {
	rec := &recordingRemote{}
	s, uri := newProvisionTestScheme(rec, func() string { return "" })
	defer s.cancelCtx()

	scheme, err := s.connectScheme(context.Background(), uri, func(error) {})
	require.NoError(t, err)
	if scheme != nil {
		_ = scheme.Close()
	}

	cmd := findRuneServerCmd(t, rec)
	assert.NotContains(t, cmd.Args, "--datadir",
		"empty remote data dir must omit --datadir; got %+v", cmd.Args)
}

// TestConnectSchemeNoProvisionManifest asserts that no --install flag is
// added when the provision function is nil or returns an empty manifest.
func TestConnectSchemeNoProvisionManifest(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func() string
	}{
		{"nil", nil},
		{"empty", func() string { return "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingRemote{}
			s, uri := newProvisionTestScheme(rec, tc.fn)
			defer s.cancelCtx()

			scheme, err := s.connectScheme(context.Background(), uri, func(error) {})
			require.NoError(t, err)
			if scheme != nil {
				_ = scheme.Close()
			}

			cmd := findRuneServerCmd(t, rec)
			assert.NotContains(t, cmd.Args, "--install",
				"empty manifest must omit --install; got %+v", cmd.Args)
		})
	}
}

// TestConnectSchemeProvisionPackagesDisabled asserts that when
// workspace.ssh.provision_packages is false, no --install flag is threaded
// into the remote invocation even if the manifest is non-empty.
func TestConnectSchemeProvisionPackagesDisabled(t *testing.T) {
	rec := &recordingRemote{}
	s, uri := newProvisionTestScheme(rec, func() string { return "rune-go@1.2.3" })
	s.cfg.provisionPackages = false
	defer s.cancelCtx()

	scheme, err := s.connectScheme(context.Background(), uri, func(error) {})
	require.NoError(t, err)
	if scheme != nil {
		_ = scheme.Close()
	}

	cmd := findRuneServerCmd(t, rec)
	assert.NotContains(t, cmd.Args, "--install",
		"provision_packages=false must omit --install; got %+v", cmd.Args)
}

func TestParseWorkspaceURIHomeDir(t *testing.T) {
	tsuite := []struct {
		desc        string
		uri         string
		getUser     func() (*user.User, error)
		wantUser    string
		wantHomeDir string
	}{
		{
			desc:        "non-root user under /home",
			uri:         "ssh://test@example.com/~/src",
			wantUser:    "test",
			wantHomeDir: "/home/test",
		},
		{
			desc:        "root user maps to /root",
			uri:         "ssh://root@example.com/~/src",
			wantUser:    "root",
			wantHomeDir: "/root",
		},
		{
			desc: "empty user falls back to local user",
			uri:  "ssh://example.com/~/src",
			getUser: func() (*user.User, error) {
				return &user.User{Username: "root"}, nil
			},
			wantUser:    "",
			wantHomeDir: "/root",
		},
	}

	for _, tc := range tsuite {
		t.Run(tc.desc, func(t *testing.T) {
			uri, err := workspaceapi.ParseURI(tc.uri)
			require.NoError(t, err)

			getUser := tc.getUser
			if getUser == nil {
				getUser = func() (*user.User, error) {
					return &user.User{Username: "test", HomeDir: "/home/test"}, nil
				}
			}

			gotUser, gotHomeDir, _, gotBasePath, err := parseWorkspaceURI(uri, getUser)
			require.NoError(t, err)
			assert.Equal(t, tc.wantUser, gotUser)
			assert.Equal(t, tc.wantHomeDir, gotHomeDir,
				"~ should expand to the remote user's real home")
			assert.Equal(t, filepath.Join(tc.wantHomeDir, "src"), gotBasePath,
				"basePath must expand ~ using homeDir")
		})
	}
}

func TestIntegrationCanWorkspaceURI(t *testing.T) {
	tsuite := []struct {
		workspaceURI string
		uri          string
		expectedOut  bool
	}{
		{"ssh://ernest.photography/", "ssh://ernest.photography/tmp", true},
		{"ssh://ernest.photography/", "file://ernest.photography/tmp", false},
		{"ssh://ernest.photography/", "ssh://git@ernest.photography/tmp", false},
		{"ssh://unstablebuild@ernest.photography/", "ssh://git@ernest.photography/tmp", false},
		{"ssh://git@ernest.photography/", "ssh://git@ernest.photography/tmp", true},
		{"ssh://unstablebuild@ernest.photography/", "ssh://git@ernest.photography/tmp", false},
		{"ssh://git@ernest.photography/", "ssh://git@ernest.photography:12222/tmp", false},
		{"ssh://git@ernest.photography:12222/", "ssh://git@ernest.photography/tmp", false},
		{"ssh://ernest.photography/tmp", "ssh://ernest.photography/tmp", true},
		{"ssh://ernest.photography:122/tmp", "ssh://ernest.photography:122/tmp", true},
		{"ssh://git@ernest.photography:122/tmp", "ssh://ernest.photography:122/tmp", false},
		{"ssh://ernest.photography:122/tmp", "ssh://git@ernest.photography:122/tmp", false},
		{"ssh://ernest.photography/var", "ssh://ernest.photography/tmp", true}, // diff tree, but scheme should be able to handle it
		{"ssh://ernest.photography/var", "ssh://ernest.photography/var/file.txt", true},
		{"ssh://ernest.photography/var", "ssh://ernest.photography/var/dir/dir/dir/file.txt", true},
		{"ssh://ernest.photography:22/var", "ssh://ernest.photography:22/var/dir/dir/dir/file.txt", true},
		{"ssh://ernest.photography/var/", "ssh://ernest.photography/var/file.txt", true},
		{"ssh://root@ernest.photography/var/", "ssh://root@ernest.photography/var/file.txt", true},
		{"ssh://root@ernest.photography:1999/var/", "ssh://root@ernest.photography:1999/var/file.txt", true},
	}

	for i, tcase := range tsuite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			inURI, err := workspaceapi.ParseURI(tcase.uri)
			require.NoError(t, err)

			inWorkspaceURI, err := workspaceapi.ParseURI(tcase.workspaceURI)
			require.NoError(t, err)

			fileScheme := newNopScheme(t, inWorkspaceURI)
			inWorkspace := workspace.NewSchemeWorkspace(inWorkspaceURI, fileScheme, inlineSchedule)

			// sut
			actual, err := workspace.IsWorkspaceURI(inWorkspace, inURI)
			require.NoError(t, err)
			assert.Equal(t, tcase.expectedOut, actual)
		})
	}
}

func TestIntegrationManagerIsWorkspaceFile(t *testing.T) {
	fileWorkspacePath, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	fileWorkspaceURI, err := workspaceapi.ParseURI(filepath.Join("file://", fileWorkspacePath))
	require.NoError(t, err)

	sshWorkspaceURI, err := workspaceapi.ParseURI("ssh://ernest.photography/~/")
	require.NoError(t, err)

	manager := workspace.NewManager(config.NopConfig(), inlineSchedule)
	require.NoError(t, manager.RegisterScheme(workspace.FileScheme, workspace.NewFileScheme))
	require.NoError(t, manager.RegisterScheme(Scheme,
		func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (schemeapi.Scheme, error) {
			return newTestScheme(cfg, uri, nil)
		}))

	ctx := context.Background()

	fileWorkspace, err := manager.AddWorkspace(ctx, fileWorkspaceURI)
	require.NoError(t, err)

	sshWorkspace, err := manager.AddWorkspace(ctx, sshWorkspaceURI)
	require.NoError(t, err)

	tsuite := []struct {
		uri               string
		expectedWorkspace workspace.Workspace
		expectedFound     bool
	}{
		{"ssh://ernest.photography/~/hello.txt", sshWorkspace, true},
		{"ssh://unstable.build/~/hello.txt", nil, false},
		{"file:///hello.txt", fileWorkspace, true},
		{"file:///home/git/hello.txt", fileWorkspace, true},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.uri, func(t *testing.T) {
			inURI, err := workspaceapi.ParseURI(tcase.uri)
			require.NoError(t, err)

			// sut
			actual, ok, err := manager.Workspace(inURI)
			require.NoError(t, err)
			assert.Equal(t, ok, tcase.expectedFound)
			assert.Equal(t, tcase.expectedWorkspace, actual)
		})
	}
}

func TestSSHScheme(t *testing.T) {
	t.Run("with memory scheme remote", func(t *testing.T) {
		workspacetest.TestWorkspaceSchemeFiles(t, func(t *testing.T) schemeapi.Scheme {
			workspaceURI, err := workspaceapi.ParseURI("ssh://test@host.com/")
			require.NoError(t, err)
			remoteURI, err := workspaceapi.ParseURI("memory:///")
			require.NoError(t, err)

			memScheme, err := workspace.NewMemoryScheme(
				context.Background(), config.NopConfig(), remoteURI)
			require.NoError(t, err)

			s, err := newTestScheme(config.NopConfig(), workspaceURI,
				func(ctx context.Context, uri workspaceapi.URI,
					closeHook func(error)) (schemeapi.Scheme, error) {
					return memScheme, nil
				})
			require.NoError(t, err)
			t.Cleanup(func() {
				_ = s.Close()
			})
			return s
		})
	})

	t.Run("with file scheme remote", func(t *testing.T) {
		workspacetest.TestWorkspaceSchemeFiles(t, func(t *testing.T) schemeapi.Scheme {
			dir, err := os.MkdirTemp("", "ssh_scheme_suite")
			require.NoError(t, err)

			fileURI, err := workspaceapi.ParseURI("file://" + dir)
			require.NoError(t, err)

			workspaceURI, err := workspaceapi.ParseURI("ssh://host.com" + dir)
			require.NoError(t, err)

			fileScheme, err := workspace.NewFileScheme(
				context.Background(), config.NopConfig(), fileURI)
			require.NoError(t, err)

			s, err := newTestScheme(config.NopConfig(), workspaceURI,
				func(ctx context.Context, uri workspaceapi.URI,
					closeHook func(error)) (schemeapi.Scheme, error) {
					return fileScheme, nil
				})
			require.NoError(t, err)
			t.Cleanup(func() {
				_ = s.Close()
				_ = os.RemoveAll(dir)
			})
			return s
		})
	})
}

type nopExecutor struct {
}

func (n nopExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	return 0, nil
}

func (n nopExecutor) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}
func (n nopExecutor) Close() error {
	return nil
}

type nopRemote struct {
}

func (n nopRemote) NewSession() (schemeapi.Executor, error) {
	return nopExecutor{}, nil
}

func (n nopRemote) Close() error {
	return nil
}

// recordingRemote captures every command issued via NewSession/StartCommand
// so tests can assert on the binary that connectScheme runs on the remote.
// Each StartCommand is treated as a successful exit (status 0) by signaling
// the supplied ProcessWatcher with nil.
type recordingRemote struct {
	mu       sync.Mutex
	commands []workspaceapi.Cmd
}

func (r *recordingRemote) NewSession() (schemeapi.Executor, error) {
	return &recordingExecutor{remote: r}, nil
}

func (r *recordingRemote) Close() error { return nil }

type recordingExecutor struct {
	remote *recordingRemote
}

func (e *recordingExecutor) StartCommand(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	e.remote.mu.Lock()
	e.remote.commands = append(e.remote.commands, cmd)
	e.remote.mu.Unlock()
	if slices.Contains(cmd.Args, "-x") {
		if cmd.Stderr != nil {
			if line, err := encodeServerReady(); err == nil {
				_, _ = io.WriteString(cmd.Stderr, line)
			}
		}
		return 1, nil
	}
	if cmd.Watcher != nil && cmd.Watcher.WatchProcess() != nil {
		go func() { cmd.Watcher.WatchProcess() <- nil }()
	}
	return 1, nil
}

func (e *recordingExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (e *recordingExecutor) Close() error                                  { return nil }

type blockingRemote struct {
	started chan struct{}
	session *blockingExecutor
}

func newBlockingRemote() *blockingRemote {
	return &blockingRemote{started: make(chan struct{})}
}

func (r *blockingRemote) NewSession() (schemeapi.Executor, error) {
	r.session = &blockingExecutor{
		started: r.started,
		done:    make(chan struct{}),
	}
	return r.session, nil
}

func (r *blockingRemote) Close() error { return nil }

func (r *blockingRemote) release() {
	if r.session != nil {
		_ = r.session.Close()
	}
}

func (r *blockingRemote) sessionClosed() bool {
	return r.session != nil && r.session.closed.Load()
}

type blockingExecutor struct {
	started   chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
}

func (e *blockingExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	close(e.started)
	go debug.CapturePanicReport(func() {
		select {
		case <-ctx.Done():
			cmd.Watcher.WatchProcess() <- ctx.Err()
		case <-e.done:
			cmd.Watcher.WatchProcess() <- nil
		}
	})
	return 1, nil
}

func (e *blockingExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }

func (e *blockingExecutor) Close() error {
	e.closed.Store(true)
	e.closeOnce.Do(func() { close(e.done) })
	return nil
}

// errorUI fails any prompt; tests use it because newTestScheme stubs the
// remote, so prompts should never fire.
type errorUI struct{}

func (errorUI) PromptSecret(context.Context, string) (string, error) {
	return "", fmt.Errorf("unexpected prompt: secret")
}

func (errorUI) PromptText(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("unexpected prompt: text")
}

func (errorUI) PromptChoice(context.Context, string, []string) (int, error) {
	return -1, fmt.Errorf("unexpected prompt: choice")
}

func (errorUI) Notify(NotificationLevel, string) string { return "" }

func (errorUI) UpdateNotificationProgress(string, string, int, int) {}

func newTestScheme(
	cfg config.Config, workspaceURI workspaceapi.URI,
	connectSchemeFn func(ctx context.Context,
		uri workspaceapi.URI, closeHook func(error)) (schemeapi.Scheme, error),
) (schemeapi.Scheme, error) {
	s := new(scheme)
	s.ctx, s.cancelCtx = context.WithCancel(context.Background())
	s.remoteFn = func(context.Context, sshConfig, workspaceapi.URI) (remote, error) {
		return nopRemote{}, nil
	}
	s.getUser = func() (*user.User, error) {
		return &user.User{Username: "git", HomeDir: "/home/git"}, nil
	}
	s.ui = errorUI{}
	if connectSchemeFn == nil {
		connectSchemeFn = func(ctx context.Context, uri workspaceapi.URI, closeHook func(error)) (
			schemeapi.Scheme, error,
		) {
			return workspacetest.NewNopScheme("test")(ctx, config.NopConfig(), uri)
		}
	}
	s.connectSchemeFn = connectSchemeFn

	err := s.init(context.Background(), sshConfig{}, workspaceURI)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func newNopScheme(t *testing.T, workspaceURI workspaceapi.URI) *scheme {
	s, err := newTestScheme(config.NopConfig(), workspaceURI, nil)
	require.NoError(t, err)
	return s.(*scheme)
}

// TestScanRemoteStderrNotifiesAndTails feeds a synthetic stderr stream with
// interleaved JSON progress and plain lines and asserts that progress lines
// drive a single live progress notification (with a failed package also raised
// as its own warning), the bar is NOT closed by the done line, is closed only
// when the ServerReady line arrives, plain lines never notify but land in the
// exit-error tail, and the scanner stops at EOF.
func TestScanRemoteStderrNotifiesAndTails(t *testing.T) {
	installing, err := EncodeProvisionProgress(ProvisionProgress{
		Index: 1, Total: 2, Package: "pkg-a", Version: "1.0.0",
		Phase: ProvisionPhaseInstalling,
	})
	require.NoError(t, err)
	failed, err := EncodeProvisionProgress(ProvisionProgress{
		Index: 2, Total: 2, Package: "pkg-b", Version: "2.0.0",
		Phase: ProvisionPhaseFailed,
	})
	require.NoError(t, err)
	done, err := EncodeProvisionProgress(ProvisionProgress{
		Index: 2, Total: 2, Phase: ProvisionPhaseDone,
	})
	require.NoError(t, err)
	finalizing, err := EncodeProvisionProgress(ProvisionProgress{
		Phase: ProvisionPhaseFinalizing,
	})
	require.NoError(t, err)
	readyLine, err := encodeServerReady()
	require.NoError(t, err)

	stream := "starting remote server\n" +
		installing +
		"warning: something noisy\n" +
		failed +
		done +
		finalizing +
		readyLine +
		"remote server exiting\n"

	ui := &recordingUI{}
	s := &scheme{ui: ui}
	tail := newStderrTail()

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		s.scanRemoteStderr(strings.NewReader(stream), tail, make(chan struct{}))
	}()

	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("scanRemoteStderr did not stop at EOF")
	}

	// Notify is called once to open the progress notification (info) and once
	// for the failed package (warning). The done/finalizing/ready updates flow
	// through the progress bar, not a new Notify.
	levels, msgs := ui.notifications()
	assert.Equal(t, []string{
		"Installing toolchain (1/2): pkg-a@1.0.0",
		"Failed to install pkg-b@2.0.0",
	}, msgs, "Notify opens the progress bar and raises the failure warning")
	assert.Equal(t, []NotificationLevel{
		NotificationInfo, NotificationWarning,
	}, levels)

	// The bar advances monotonically on a synthetic /100 scale, stays strictly
	// below total for every provisioning line (including done and finalizing),
	// and completes only when the ServerReady line closes it.
	progress := ui.progressUpdates()
	require.Len(t, progress, 5)
	wantID := "noti-Installing toolchain (1/2): pkg-a@1.0.0"
	for _, u := range progress {
		assert.Equal(t, wantID, u.id)
		assert.Equal(t, provisionProgressBarTotal, u.total)
	}
	assert.Equal(t, "Installing toolchain (1/2): pkg-a@1.0.0", progress[0].message)
	assert.Equal(t, 0, progress[0].progress)
	assert.Equal(t, "Failed to install pkg-b@2.0.0", progress[1].message)
	assert.Equal(t, packageRegionEnd, progress[1].progress)
	assert.Equal(t, "Installed 2/2 toolchain packages", progress[2].message)
	assert.Equal(t, packageRegionEnd, progress[2].progress,
		"the done line must not close the bar")
	assert.Equal(t, "Finalizing workspace…", progress[3].message)
	assert.Equal(t, finalizeFraction, progress[3].progress)
	assert.Equal(t, "Workspace ready", progress[4].message)
	assert.Equal(t, provisionProgressBarTotal, progress[4].progress,
		"only ServerReady closes the bar")

	got := tail.String()
	assert.Contains(t, got, "starting remote server")
	assert.Contains(t, got, "warning: something noisy")
	assert.Contains(t, got, "remote server exiting")
	assert.NotContains(t, got, "provision", "progress lines must not leak into the tail")
}

// TestProvisionProgressNotifierMonotonicAcrossLifecycle drives the notifier
// directly through a full two-package lifecycle (installing → downloading →
// activating per package → finalizing → serving) and asserts the bar advances
// monotonically, holds strictly below total until finish, and reaches total
// only when finish (serving-ready) closes it.
func TestProvisionProgressNotifierMonotonicAcrossLifecycle(t *testing.T) {
	ui := &recordingUI{}
	var n provisionProgressNotifier

	lines := []ProvisionProgress{
		{Index: 1, Total: 2, Package: "pkg-a", Version: "1.0.0", Phase: ProvisionPhaseInstalling},
		{Index: 1, Total: 2, Package: "pkg-a", Version: "1.0.0", Phase: ProvisionPhaseDownloading, Done: 5, Of: 10, Units: "KiB"},
		{Index: 1, Total: 2, Package: "pkg-a", Version: "1.0.0", Phase: ProvisionPhaseActivating},
		{Index: 2, Total: 2, Package: "pkg-b", Version: "2.0.0", Phase: ProvisionPhaseInstalling},
		{Index: 2, Total: 2, Package: "pkg-b", Version: "2.0.0", Phase: ProvisionPhaseDownloading, Done: 8, Of: 10, Units: "KiB"},
		{Index: 2, Total: 2, Package: "pkg-b", Version: "2.0.0", Phase: ProvisionPhaseActivating},
		{Phase: ProvisionPhaseFinalizing},
	}
	for _, p := range lines {
		n.report(ui, p)
	}
	n.finish(ui)

	updates := ui.progressUpdates()
	require.Len(t, updates, len(lines)+1)

	prev := -1
	for i, u := range updates {
		assert.GreaterOrEqual(t, u.progress, prev,
			"progress must be monotonically non-decreasing at step %d", i)
		prev = u.progress
		assert.Equal(t, provisionProgressBarTotal, u.total)
		if i < len(updates)-1 {
			assert.Less(t, u.progress, provisionProgressBarTotal,
				"the bar must stay below total until finish at step %d", i)
		}
	}
	last := updates[len(updates)-1]
	assert.Equal(t, provisionProgressBarTotal, last.progress,
		"finish completes the bar")
	assert.Equal(t, "Workspace ready", last.message)
}

// TestProvisionProgressNotifierFinishNoopWithoutBar asserts that finish is a
// no-op when no provisioning line ever opened the bar (a launch with no
// --install manifest), so a plain serving-ready launch does not synthesize a
// spurious progress notification.
func TestProvisionProgressNotifierFinishNoopWithoutBar(t *testing.T) {
	ui := &recordingUI{}
	var n provisionProgressNotifier
	n.finish(ui)
	assert.Empty(t, ui.progressUpdates(), "finish must not open a bar")
	levels, msgs := ui.notifications()
	assert.Empty(t, msgs)
	assert.Empty(t, levels)
}

// TestScanRemoteStderrClosesReadyOnServerReady asserts the ServerReady sentinel
// line closes the readiness channel exactly once, is not surfaced as a
// notification, and does not leak into the exit-error tail (it is a control
// line, like progress).
func TestScanRemoteStderrClosesReadyOnServerReady(t *testing.T) {
	readyLine, err := encodeServerReady()
	require.NoError(t, err)

	// Two ready lines exercise the close-once guard: a second close would
	// panic if the guard were missing.
	stream := "booting\n" + readyLine + "serving now\n" + readyLine

	ui := &recordingUI{}
	s := &scheme{ui: ui}
	tail := newStderrTail()
	ready := make(chan struct{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.scanRemoteStderr(strings.NewReader(stream), tail, ready)
	}()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("ServerReady line did not close the readiness channel")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scanRemoteStderr did not stop at EOF")
	}

	levels, msgs := ui.notifications()
	assert.Empty(t, msgs, "ready lines must not notify")
	assert.Empty(t, levels)

	got := tail.String()
	assert.Contains(t, got, "booting")
	assert.Contains(t, got, "serving now")
	assert.NotContains(t, got, "ready", "ready lines must not leak into the tail")
}

// TestConnectSchemeReturnsErrorWhenRemoteExitsBeforeServing asserts that if the
// remote process ends before emitting the ServerReady sentinel, connectScheme
// surfaces an error carrying the stderr tail instead of handing back a client
// whose first RPC would block forever. This is the whole point of gating the
// client on readiness.
func TestConnectSchemeReturnsErrorWhenRemoteExitsBeforeServing(t *testing.T) {
	rec := &failingServerRemote{stderr: "provisioning failed: disk full\n"}

	s := new(scheme)
	s.ctx, s.cancelCtx = context.WithCancel(context.Background())
	defer s.cancelCtx()
	s.cfg.skipPreflight = true
	s.remoteFn = func(context.Context, sshConfig, workspaceapi.URI) (remote, error) {
		return rec, nil
	}
	s.getUser = func() (*user.User, error) {
		return &user.User{Username: "test", HomeDir: "/home/test"}, nil
	}
	s.ui = errorUI{}
	uri, err := workspaceapi.ParseURI("ssh://test@example.com/tmp")
	require.NoError(t, err)
	s.user, s.homedir, s.hostPort, s.basePath, err = parseWorkspaceURI(uri, s.getUser)
	require.NoError(t, err)

	done := make(chan struct{})
	var scheme schemeapi.Scheme
	var connErr error
	go func() {
		defer close(done)
		scheme, connErr = s.connectScheme(context.Background(), uri, func(error) {})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connectScheme did not return when the remote exited before serving")
	}

	require.Error(t, connErr, "connectScheme must fail when the remote exits before serving")
	assert.Nil(t, scheme, "no client must be returned when readiness never arrives")
	assert.Contains(t, connErr.Error(), "provisioning failed: disk full",
		"the error must carry the remote stderr tail")
}

// failingServerRemote is a remote whose workspace-server command (-x) writes a
// line to stderr and then exits WITHOUT emitting the ServerReady sentinel,
// standing in for a remote that dies during provisioning.
type failingServerRemote struct {
	stderr string
}

func (r *failingServerRemote) NewSession() (schemeapi.Executor, error) {
	return &failingServerExecutor{stderr: r.stderr}, nil
}

func (r *failingServerRemote) Close() error { return nil }

type failingServerExecutor struct {
	stderr string
}

func (e *failingServerExecutor) StartCommand(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	if cmd.Stderr != nil && e.stderr != "" {
		_, _ = io.WriteString(cmd.Stderr, e.stderr)
	}
	// Exit with a failure but no ServerReady line, closing the stderr write
	// end so the scanner observes EOF and the tail is complete.
	if cmd.Stderr != nil {
		if closer, ok := cmd.Stderr.(io.Closer); ok {
			_ = closer.Close()
		}
	}
	if cmd.Watcher != nil && cmd.Watcher.WatchProcess() != nil {
		go func() { cmd.Watcher.WatchProcess() <- fmt.Errorf("exit status 1") }()
	}
	return 1, nil
}

func (e *failingServerExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (e *failingServerExecutor) Close() error                                  { return nil }

// TestConnectSchemeUnblocksWhenAttemptContextCancelled is a regression test for
// a shutdown deadlock: connectScheme must honor the per-attempt context
// maintainConnection passes it. If the remote connects but never becomes
// serving-ready (provisioning stalls) and never exits, cancelling that context
// (IDE shutdown / retry abort) must unblock connectScheme. Watching only the
// scheme's own long-lived context here is not enough — that context is rooted
// in context.Background() and is not cancelled by shutdown, so connectScheme
// would hang forever, state() would never return, and Close's WaitGroup.Wait
// would deadlock.
func TestConnectSchemeUnblocksWhenAttemptContextCancelled(t *testing.T) {
	rec := &hangingServerRemote{}

	s := new(scheme)
	s.ctx, s.cancelCtx = context.WithCancel(context.Background())
	defer s.cancelCtx()
	s.cfg.skipPreflight = true
	s.remoteFn = func(context.Context, sshConfig, workspaceapi.URI) (remote, error) {
		return rec, nil
	}
	s.getUser = func() (*user.User, error) {
		return &user.User{Username: "test", HomeDir: "/home/test"}, nil
	}
	s.ui = errorUI{}
	uri, err := workspaceapi.ParseURI("ssh://test@example.com/tmp")
	require.NoError(t, err)
	s.user, s.homedir, s.hostPort, s.basePath, err = parseWorkspaceURI(uri, s.getUser)
	require.NoError(t, err)

	// The attempt context maintainConnection would pass; cancelling it must
	// unblock the readiness wait even though s.ctx stays live.
	attemptCtx, cancelAttempt := context.WithCancel(context.Background())

	done := make(chan struct{})
	var connScheme schemeapi.Scheme
	var connErr error
	go func() {
		defer close(done)
		connScheme, connErr = s.connectScheme(attemptCtx, uri, func(error) {})
	}()

	// Give the readiness wait time to block, then cancel the attempt context.
	select {
	case <-done:
		t.Fatal("connectScheme returned before the remote served or was cancelled")
	case <-time.After(100 * time.Millisecond):
	}
	cancelAttempt()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connectScheme did not unblock when the attempt context was " +
			"cancelled; this is the shutdown deadlock")
	}

	require.Error(t, connErr, "connectScheme must fail when its attempt context is cancelled")
	assert.Nil(t, connScheme, "no client must be returned when readiness never arrives")

	// Let the hung command goroutine exit so the test does not leak it.
	rec.release()
}

// hangingServerRemote is a remote whose workspace-server command (-x) connects
// (writes some stderr) but neither emits the ServerReady sentinel nor exits,
// standing in for a remote stuck in provisioning. Its command blocks until
// release is called or the command context is cancelled.
type hangingServerRemote struct {
	execs []*hangingServerExecutor
	mu    sync.Mutex
}

func (r *hangingServerRemote) NewSession() (schemeapi.Executor, error) {
	e := &hangingServerExecutor{done: make(chan struct{})}
	r.mu.Lock()
	r.execs = append(r.execs, e)
	r.mu.Unlock()
	return e, nil
}

func (r *hangingServerRemote) Close() error { return nil }

func (r *hangingServerRemote) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.execs {
		e.closeOnce.Do(func() { close(e.done) })
	}
}

type hangingServerExecutor struct {
	done      chan struct{}
	closeOnce sync.Once
}

func (e *hangingServerExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	if cmd.Stderr != nil {
		_, _ = io.WriteString(cmd.Stderr, "provisioning toolchain...\n")
	}
	go debug.CapturePanicReport(func() {
		// Block without emitting ServerReady or exiting until released or the
		// command context is cancelled, then report exit like a killed remote.
		select {
		case <-e.done:
		case <-ctx.Done():
		}
		if cmd.Watcher != nil && cmd.Watcher.WatchProcess() != nil {
			cmd.Watcher.WatchProcess() <- fmt.Errorf("killed")
		}
	})
	return 1, nil
}

func (e *hangingServerExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (e *hangingServerExecutor) Close() error {
	e.closeOnce.Do(func() { close(e.done) })
	return nil
}

// TestStderrTailBounded asserts the tail retains only the most recent
// stderrTailCap bytes so a chatty remote cannot grow it without bound.
func TestStderrTailBounded(t *testing.T) {
	tail := newStderrTail()
	for range 10000 {
		tail.append([]byte("0123456789abcdef"))
	}
	assert.LessOrEqual(t, len(tail.String()), stderrTailCap)
	assert.Contains(t, tail.String(), "0123456789abcdef", "recent lines must be retained")
}

// inlineSchedule is a synchronous workspace.ScheduleNextTick stub
// that runs fn on the calling goroutine. Test-only: production code
// must use the host event-loop scheduler so reload's buffer
// mutations do not run on a worker goroutine.
func inlineSchedule(fn func()) bool {
	fn()
	return true
}
