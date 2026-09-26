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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func newTestFileScheme(uri workspaceapi.URI) (*fileScheme, error) {
	ret := new(fileScheme)
	ret.osStat = func(path string) (os.FileInfo, error) {
		if strings.Contains(path, ".txt") || strings.Contains(path, ".md") {
			return testFileInfo{name: path, isDir: false}, nil
		}
		return testFileInfo{name: path, isDir: true}, nil
	}
	ret.getUser = func() (*user.User, error) {
		return &user.User{Username: "git", HomeDir: "/home/git"}, nil
	}
	ret.lookupUser = func(username string) (*user.User, error) {
		return &user.User{Username: username, HomeDir: fmt.Sprintf("/home/%s", username)}, nil
	}
	err := ret.init(config.NopConfig(), uri)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func TestNewScheme(t *testing.T) {
	tsuite := []struct {
		desc         string
		workspaceURI string
		expectedErr  string
	}{
		{"no port no user workspace absolute returns error", "file://ernest.photography", "invalid file URI"},
		{"port no user workspace absolute returns error", "file://ernest.photography:4222", "invalid file URI"},
		{"port user workspace absolute returns error", "file://ernie@ernest.photography:4222", "invalid file URI"},
		{"port user workspace absolute slash returns error", "file://ernie@ernest.photography:4222/", "invalid file URI"},
		{"no port user workspace absolute slash returns error", "file://ernie@ernest.photography/", "invalid file URI"},
		{"different scheme returns error", "ssh:///tmp", "invalid file URI"},
		{"no host regular folder success", "file:///tmp", ""},
		{"root", "file:///", ""},
		{"file uri should return error", "file:///tmp/file.txt",
			"workspaceapi.URI does not refer to a directory: file:///tmp/file.txt"}, // newTestFileScheme sets osStat based on file name
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			workspaceURI, err := workspaceapi.ParseURI(tcase.workspaceURI)
			require.NoError(t, err)

			// sut
			_, err = newTestFileScheme(workspaceURI)
			if tcase.expectedErr != "" {
				assert.EqualError(t, err, tcase.expectedErr)
			} else {
				assert.NoError(t, err)
			}

		})
	}
}

func TestFileSchemeURI(t *testing.T) {
	tsuite := []struct {
		desc         string
		workspaceURI string
		inPath       string
		expectedOut  string
		expectedErr  string
	}{
		{"absolute root", "file:///", "/",
			"file:///", ""},
		{"absolute root, non-root workspace", "file:///home/ernicles", "/",
			"file:///", ""},
		{"absolute root 2", "file:///var", "/tmp/file",
			"file:///tmp/file", ""},
		{"absolute folder is equal to workspace", "file:///home/ernicles", "/home/ernicles",
			"file:///home/ernicles", ""},
		{"absolute folder file", "file:///home/ernicles", "/home/ernicles/file.txt",
			"file:///home/ernicles/file.txt", ""},
		{"absolute other folder file", "file:///home/ernicles", "/home/git/file.txt",
			"file:///home/git/file.txt", ""},
		{"relative file", "file:///home/ernicles", "file.txt",
			"file:///home/ernicles/file.txt", ""},
		{"relative file to home, non workspace", "file:///home/src/blue", "~/file.txt",
			"file:///home/git/file.txt", ""},
		{"relative file to home, workspace", "file:///home/git/", "~/file.txt",
			"file:///home/git/file.txt", ""},
		{"relative upwards workspace tree", "file:///home/git/src/blue", "../../",
			"file:///home/git/src/blue/../../", ""},
		{"relative upwards workspace tree ending slash", "file:///home/git/src/blue/", "../../",
			"file:///home/git/src/blue/../../", ""},
		{"relative upwards workspace tree ./ path", "file:///home/git/src/blue", "./../../file.txt",
			"file:///home/git/src/blue/./../../file.txt", ""},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			workspaceURI, err := workspaceapi.ParseURI(tcase.workspaceURI)
			require.NoError(t, err)

			s, err := newTestFileScheme(workspaceURI)
			require.NoError(t, err)
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

func TestFileAssumptions(t *testing.T) {
	t.Run("Write overwrites data", func(t *testing.T) {
		f, err := os.CreateTemp("", "")
		name := f.Name()
		n, err := f.Write([]byte("12345"))
		require.NoError(t, err)
		require.NoError(t, f.Close())

		f, err = os.OpenFile(name, os.O_RDWR, 0)
		require.NoError(t, err)
		n, err = f.Write([]byte("ZZ"))
		require.NoError(t, err)
		assert.Equal(t, 2, n)

		nn, err := f.Seek(0, 0)
		require.NoError(t, err)
		assert.Equal(t, int64(0), nn)

		data, err := io.ReadAll(f)
		require.NoError(t, err)
		assert.Equal(t, "ZZ345", string(data))
	})

	t.Run("Read uses write offset", func(t *testing.T) {
		f, err := os.CreateTemp("", "")
		name := f.Name()
		n, err := f.Write([]byte("12345"))
		require.NoError(t, err)
		require.NoError(t, f.Close())

		f, err = os.OpenFile(name, os.O_RDWR, 0)
		require.NoError(t, err)

		n, err = f.Write([]byte("ZZ"))
		require.NoError(t, err)
		assert.Equal(t, 2, n)

		data, err := io.ReadAll(f)
		require.NoError(t, err)
		assert.Equal(t, "345", string(data))
	})
}

func TestStartCommand(t *testing.T) {
	t.Run("Cmd.Dir makes command run on that directory", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)

		s, err := newTestFileScheme(uri)
		require.NoError(t, err)

		var stdout bytes.Buffer
		var stderr bytes.Buffer

		ch := make(chan error)
		ctx := context.Background()

		// If Dir is passed runs there
		cmd := workspaceapi.Cmd{
			Path:    "/bin/sh",
			Args:    []string{"-c", "pwd"},
			Dir:     "/bin",
			Watcher: workspaceapi.ChanProcessWatcher(ch),
			Stdout:  &stdout,
			Stderr:  &stderr,
		}

		_, err = s.StartCommand(ctx, cmd)
		require.NoError(t, err)

		err = <-ch
		assert.Equal(t, err, nil)

		assert.Equal(t, stderr.String(), "")
		assert.Equal(t, stdout.String(), "/bin\n")

		stderr.Reset()
		stdout.Reset()

	})

	t.Run("local command inherits live process env", func(t *testing.T) {
		// Documents why os.Setenv is sufficient for future local launches:
		// StartCommand seeds the child env from the live process environment
		// at launch time, so a gui.env live-apply via os.Setenv is observed
		// by subsequently launched local commands.
		t.Setenv("RUNE_FILE_SCHEME_ENV_TEST", "inherited")

		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)

		s, err := newTestFileScheme(uri)
		require.NoError(t, err)

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		ch := make(chan error)
		ctx := context.Background()

		cmd := workspaceapi.Cmd{
			Path:    "/bin/sh",
			Args:    []string{"-c", "printf %s \"$RUNE_FILE_SCHEME_ENV_TEST\""},
			Watcher: workspaceapi.ChanProcessWatcher(ch),
			Stdout:  &stdout,
			Stderr:  &stderr,
		}

		_, err = s.StartCommand(ctx, cmd)
		require.NoError(t, err)

		require.NoError(t, <-ch)
		assert.Equal(t, "", stderr.String())
		assert.Equal(t, "inherited", stdout.String())
	})

	t.Run("env vars in Cmd.Path are expanded", func(t *testing.T) {
		t.Setenv("RUNE_FILE_SCHEME_BIN_TEST", "/bin")

		s, err := newTestFileScheme(dirURI(t, t.TempDir()))
		require.NoError(t, err)
		defer s.Close()

		var stdout bytes.Buffer
		ch := make(chan error)
		_, err = s.StartCommand(context.Background(), workspaceapi.Cmd{
			Path:    "$RUNE_FILE_SCHEME_BIN_TEST/sh",
			Args:    []string{"-c", "printf ok"},
			Watcher: workspaceapi.ChanProcessWatcher(ch),
			Stdout:  &stdout,
		})
		require.NoError(t, err)
		require.NoError(t, <-ch)
		assert.Equal(t, "ok", stdout.String())
	})

	t.Run("omitting Cmd.Dir makes command run on workspace dir", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)

		s, err := newTestFileScheme(uri)
		require.NoError(t, err)

		var stdout bytes.Buffer
		var stderr bytes.Buffer

		ch := make(chan error)
		ctx := context.Background()

		cmd := workspaceapi.Cmd{
			Path:    "/bin/sh",
			Args:    []string{"-c", "pwd"},
			Watcher: workspaceapi.ChanProcessWatcher(ch),
			Stdout:  &stdout,
			Stderr:  &stderr,
		}

		_, err = s.StartCommand(ctx, cmd)
		require.NoError(t, err)

		err = <-ch
		assert.Equal(t, err, nil)

		assert.Equal(t, stderr.String(), "")
		assert.Equal(t, stdout.String(), tmpDir+"\n")
	})

	t.Run("empty Cmd.Path resolves to host's $SHELL", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)

		s, err := newTestFileScheme(uri)
		require.NoError(t, err)

		t.Setenv("SHELL", "/bin/sh")

		var stdout bytes.Buffer
		ch := make(chan error)
		ctx := context.Background()

		cmd := workspaceapi.Cmd{
			// Empty Path — protocol contract for "use the user's
			// login shell on the executor's host".
			Args:    []string{"-c", "echo hello"},
			Watcher: workspaceapi.ChanProcessWatcher(ch),
			Stdout:  &stdout,
		}

		_, err = s.StartCommand(ctx, cmd)
		require.NoError(t, err)
		require.NoError(t, <-ch)

		assert.Equal(t, "hello\n", stdout.String(),
			"empty Path should be resolved to /bin/sh from $SHELL")
	})

	t.Run("empty Cmd.Path falls back when $SHELL unset", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)

		s, err := newTestFileScheme(uri)
		require.NoError(t, err)

		t.Setenv("SHELL", "")

		var stdout bytes.Buffer
		ch := make(chan error)
		ctx := context.Background()

		cmd := workspaceapi.Cmd{
			Args:    []string{"-c", "echo ok"},
			Watcher: workspaceapi.ChanProcessWatcher(ch),
			Stdout:  &stdout,
		}

		_, err = s.StartCommand(ctx, cmd)
		require.NoError(t, err)
		require.NoError(t, <-ch)

		assert.Equal(t, "ok\n", stdout.String(),
			"empty Path with unset $SHELL must still launch a shell (/bin/sh fallback)")
	})

	t.Run("empty Cmd.Path falls back when $SHELL is not absolute", func(t *testing.T) {
		// Some misbehaving environments set SHELL to a bare name
		// like "zsh" that may not resolve via PATH inside the
		// stripped exec environment we run with. The fallback
		// guards against that footgun.
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)

		s, err := newTestFileScheme(uri)
		require.NoError(t, err)

		t.Setenv("SHELL", "zsh") // not absolute

		var stdout bytes.Buffer
		ch := make(chan error)
		ctx := context.Background()

		cmd := workspaceapi.Cmd{
			Args:    []string{"-c", "echo fallback"},
			Watcher: workspaceapi.ChanProcessWatcher(ch),
			Stdout:  &stdout,
		}

		_, err = s.StartCommand(ctx, cmd)
		require.NoError(t, err)
		require.NoError(t, <-ch)

		assert.Equal(t, "fallback\n", stdout.String(),
			"empty Path with non-absolute $SHELL must fall back to /bin/sh")
	})

	t.Run("empty Cmd.Path defaults Args to --login -i", func(t *testing.T) {
		// vte.Component leaves Path AND Args empty when the user
		// hasn't configured a shell; the fileScheme owns the
		// login-shell defaults so the same Cmd is transport-agnostic
		// (works locally and over workspacessh+workspacerpc).
		//
		// We point SHELL at a shell-script wrapper that prints its
		// own argv. That gives us a portable way to verify the
		// resolved args without depending on a real shell's
		// runtime behaviour.
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)

		s, err := newTestFileScheme(uri)
		require.NoError(t, err)

		bin := filepath.Join(tmpDir, "fake-shell")
		require.NoError(t, os.WriteFile(bin,
			[]byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755))
		t.Setenv("SHELL", bin)

		var stdout bytes.Buffer
		ch := make(chan error)
		ctx := context.Background()

		cmd := workspaceapi.Cmd{
			// Empty Path AND empty Args.
			Stdout:  &stdout,
			Watcher: workspaceapi.ChanProcessWatcher(ch),
		}

		_, err = s.StartCommand(ctx, cmd)
		require.NoError(t, err)
		require.NoError(t, <-ch)

		assert.Equal(t, "--login\n-i\n", stdout.String(),
			"empty Args must default to [--login, -i] so empty-cmd "+
				"Cmds produce a usable login shell")
	})

	t.Run("empty Cmd.Path with zsh exports ZDOTDIR from config", func(t *testing.T) {
		// The fileScheme reads zdotdir from the workspace config
		// at init time. That keeps the resolution local to the
		// host that will actually run the shell — for SSH
		// workspaces the remote rune sees its own config, so the
		// IDE host's ZDOTDIR doesn't leak across the wire.
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)

		// Bypass newTestFileScheme so we can pass a non-nop config.
		s := new(fileScheme)
		s.osStat = os.Stat
		s.getUser = func() (*user.User, error) {
			return &user.User{Username: "git", HomeDir: "/home/git"}, nil
		}
		s.lookupUser = func(name string) (*user.User, error) {
			return &user.User{Username: name, HomeDir: "/home/" + name}, nil
		}
		require.NoError(t, s.init(
			config.MapConfig(map[string]any{"zdotdir": "/zdot/dir"}),
			uri,
		))

		// We can't rely on zsh being installed in CI, so we point
		// SHELL at a script named "zsh" (so filepath.Base of the
		// resolved shell is "zsh") that just trampolines into
		// /bin/sh. The fileScheme only injects ZDOTDIR when the
		// resolved binary's basename matches "zsh", which is what
		// we want to verify here.
		bin := filepath.Join(tmpDir, "zsh")
		require.NoError(t, os.WriteFile(bin,
			[]byte("#!/bin/sh\nexec /bin/sh \"$@\"\n"), 0o755))
		t.Setenv("SHELL", bin)

		var stdout bytes.Buffer
		ch := make(chan error)
		ctx := context.Background()

		cmd := workspaceapi.Cmd{
			Args:    []string{"-c", "echo ZDOTDIR=$ZDOTDIR"},
			Watcher: workspaceapi.ChanProcessWatcher(ch),
			Stdout:  &stdout,
		}

		_, err = s.StartCommand(ctx, cmd)
		require.NoError(t, err)
		require.NoError(t, <-ch)

		assert.Equal(t, "ZDOTDIR=/zdot/dir\n", stdout.String(),
			"fileScheme should export ZDOTDIR when the resolved "+
				"shell is zsh and zdotdir is set in config")
	})

	t.Run("empty Cmd.Path without zsh leaves ZDOTDIR untouched", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)

		s := new(fileScheme)
		s.osStat = os.Stat
		s.getUser = func() (*user.User, error) {
			return &user.User{Username: "git", HomeDir: "/home/git"}, nil
		}
		s.lookupUser = func(name string) (*user.User, error) {
			return &user.User{Username: name, HomeDir: "/home/" + name}, nil
		}
		require.NoError(t, s.init(
			config.MapConfig(map[string]any{"zdotdir": "/zdot/dir"}),
			uri,
		))

		t.Setenv("SHELL", "/bin/sh")
		// Make sure the parent's ZDOTDIR is unset so the test
		// only sees what fileScheme adds (or doesn't add).
		t.Setenv("ZDOTDIR", "")

		var stdout bytes.Buffer
		ch := make(chan error)
		ctx := context.Background()

		cmd := workspaceapi.Cmd{
			Args:    []string{"-c", "echo ZDOTDIR=${ZDOTDIR:-unset}"},
			Watcher: workspaceapi.ChanProcessWatcher(ch),
			Stdout:  &stdout,
		}

		_, err = s.StartCommand(ctx, cmd)
		require.NoError(t, err)
		require.NoError(t, <-ch)

		assert.Equal(t, "ZDOTDIR=unset\n", stdout.String(),
			"non-zsh shells must not receive the configured ZDOTDIR; "+
				"got %q", stdout.String())
	})

	t.Run("empty Cmd.Path with bash exports INPUTRC only when zdotdir has one", func(t *testing.T) {
		// readline does not fall back to ~/.inputrc when $INPUTRC names a
		// missing file, so a stale zdotdir must not be exported.
		for _, tc := range []struct {
			name        string
			withInputrc bool
			want        func(zdotDir string) string
		}{
			{"inputrc present", true, func(zdotDir string) string {
				return "INPUTRC=" + filepath.Join(zdotDir, "inputrc") + "\n"
			}},
			{"inputrc missing", false, func(string) string { return "INPUTRC=unset\n" }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				zdotDir := t.TempDir()
				if tc.withInputrc {
					require.NoError(t, os.WriteFile(
						filepath.Join(zdotDir, "inputrc"), nil, 0o644))
				}
				s, dir := newZdotDirFileScheme(t, zdotDir)
				t.Setenv("INPUTRC", "")

				got := runShellCommand(t, s, dir, "bash",
					"#!/bin/sh\nexec /bin/sh \"$@\"\n",
					workspaceapi.Cmd{Args: []string{"-c", "echo INPUTRC=${INPUTRC:-unset}"}})
				assert.Equal(t, tc.want(zdotDir), got)
			})
		}
	})

	t.Run("empty Cmd.Path with fish adds the init command to default args only", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args []string
			want string
		}{
			{"default args", nil, "--login\n-i\n-C\n" + FishInitCommand + "\n"},
			{"explicit args", []string{"-c", "true"}, "-c\ntrue\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s, dir := newZdotDirFileScheme(t, t.TempDir())
				got := runShellCommand(t, s, dir, "fish",
					"#!/bin/sh\nprintf '%s\\n' \"$@\"\n",
					workspaceapi.Cmd{Args: tc.args})
				assert.Equal(t, tc.want, got)
			})
		}
	})

	// Reproduces the bug from RUNE-184: a Cmd.Path beginning with ~
	// was forwarded verbatim to fork/exec because StartCommand
	// didn't expand it, even though every other path-taking
	// fileScheme method (OpenFile, Stat, ReadDir, ...) does. The
	// expansion must live in fileScheme because that's the only
	// layer that knows the executor host's getUser; for remote
	// schemes the same code runs on the remote rune so ~ resolves
	// to the *remote* user's home.
	t.Run("Cmd.Path expands ~ via the executor's user", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		// Use tmpDir as the fake user home so we can place a real
		// script at "~/script.sh".
		fakeHome := tmpDir
		script := filepath.Join(fakeHome, "script.sh")
		require.NoError(t, os.WriteFile(script,
			[]byte("#!/bin/sh\necho ok\n"), 0o755))

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)

		s := new(fileScheme)
		s.osStat = os.Stat
		s.getUser = func() (*user.User, error) {
			return &user.User{Username: "git", HomeDir: fakeHome}, nil
		}
		s.lookupUser = func(name string) (*user.User, error) {
			return &user.User{Username: name, HomeDir: fakeHome}, nil
		}
		require.NoError(t, s.init(config.NopConfig(), uri))

		var stdout bytes.Buffer
		ch := make(chan error)
		ctx := context.Background()

		cmd := workspaceapi.Cmd{
			Path:    "~/script.sh",
			Watcher: workspaceapi.ChanProcessWatcher(ch),
			Stdout:  &stdout,
		}

		_, err = s.StartCommand(ctx, cmd)
		require.NoError(t, err,
			"~-prefixed Cmd.Path must be expanded by fileScheme "+
				"before fork/exec; otherwise the kernel sees a "+
				"literal '~/script.sh' and reports 'no such file "+
				"or directory'")
		require.NoError(t, <-ch)
		assert.Equal(t, "ok\n", stdout.String())
	})

	// Commands launched behind an intermediary (`go run`, `uv run`,
	// `cargo run`) exec the real program as a grandchild. SIGKILL
	// cannot be forwarded, so killing only the direct child leaves the
	// grandchild running forever.
	t.Run("cancelling a process group leader kills its tree", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)
		s, err := newTestFileScheme(uri)
		require.NoError(t, err)

		pidPath := filepath.Join(tmpDir, "grandchild.pid")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		_, err = s.StartCommand(ctx, workspaceapi.Cmd{
			Path: "/bin/sh",
			Args: []string{"-c", fmt.Sprintf(
				"sleep 60 & echo $! > %s; wait", pidPath)},
			SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
		})
		require.NoError(t, err)

		var grandchild int
		require.Eventually(t, func() bool {
			data, err := os.ReadFile(pidPath)
			if err != nil {
				return false
			}
			grandchild, err = strconv.Atoi(strings.TrimSpace(string(data)))
			return err == nil && grandchild > 0 &&
				syscall.Kill(grandchild, 0) == nil
		}, 10*time.Second, 10*time.Millisecond,
			"grandchild never started")

		cancel()

		require.Eventually(t, func() bool {
			return syscall.Kill(grandchild, 0) != nil
		}, 10*time.Second, 10*time.Millisecond,
			"grandchild outlived the cancelled command")
	})

	// Cancellation is not the only way a tree is left behind: a
	// program that exits on its own can leave helpers running, and
	// nothing cancels the context in that case.
	t.Run("a group leader's tree dies when it exits on its own", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

		uri, err := workspaceapi.ParseURI("file://" + tmpDir)
		require.NoError(t, err)
		s, err := newTestFileScheme(uri)
		require.NoError(t, err)

		pidPath := filepath.Join(tmpDir, "helper.pid")
		ch := make(chan error, 1)

		_, err = s.StartCommand(context.Background(), workspaceapi.Cmd{
			Path: "/bin/sh",
			Args: []string{"-c", fmt.Sprintf(
				"sleep 60 & echo $! > %s", pidPath)},
			SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
			Watcher:     workspaceapi.ChanProcessWatcher(ch),
		})
		require.NoError(t, err)
		require.NoError(t, <-ch, "the command exits successfully on its own")

		data, err := os.ReadFile(pidPath)
		require.NoError(t, err)
		helper, err := strconv.Atoi(strings.TrimSpace(string(data)))
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			return syscall.Kill(helper, 0) != nil
		}, 10*time.Second, 10*time.Millisecond,
			"helper outlived the command that spawned it")
	})
}

func TestResolveLoginShell(t *testing.T) {
	t.Run("absolute SHELL pointing at non-existent file falls back", func(t *testing.T) {
		// Reproduces the user-reported "fork/exec /usr/bin/bash: no
		// such file or directory" error: $SHELL points at a path
		// that exists in some context (e.g. a login shell) but not
		// in the rune-x process's view of the filesystem.
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

		bogus := filepath.Join(tmpDir, "definitely-not-here")
		t.Setenv("SHELL", bogus)

		got := resolveLoginShell()
		assert.NotEqual(t, bogus, got,
			"resolveLoginShell must not return a $SHELL that doesn't "+
				"exist on disk; that produces a confusing fork/exec "+
				"error far from this code")
		// Whatever it picked must itself be executable so the
		// caller can actually run it.
		assert.True(t, isExecutableFile(got),
			"fallback %q must itself be executable", got)
	})

	t.Run("absolute SHELL pointing at a directory falls back", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

		t.Setenv("SHELL", tmpDir) // directory, not a file

		got := resolveLoginShell()
		assert.NotEqual(t, tmpDir, got)
		assert.True(t, isExecutableFile(got))
	})

	t.Run("SHELL with stray whitespace is trimmed", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

		bin := filepath.Join(tmpDir, "fake-shell")
		require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))

		t.Setenv("SHELL", "  "+bin+"\n")

		got := resolveLoginShell()
		assert.Equal(t, bin, got,
			"resolveLoginShell must trim whitespace from $SHELL; "+
				"some sshd-spawned environments propagate a trailing "+
				"newline that breaks fork/exec")
	})

	t.Run("absolute existing executable SHELL is returned", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

		bin := filepath.Join(tmpDir, "good-shell")
		require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))

		t.Setenv("SHELL", bin)
		assert.Equal(t, bin, resolveLoginShell())
	})
}

// TestFileSchemeRecursiveWatchIgnoresPreexistingFiles pins the Watch
// contract: a watch reports changes made after it was established,
// never an inventory of what was already there.
//
// Linux's inotify backend has no recursive watch, so the notify
// library walks the tree to install one watch per directory and
// reports every entry it discovers as a Create. Those synthetic
// events are indistinguishable from real ones downstream: the IDE
// reloaded every open tab (with a "changed on disk" notification)
// after a workspace reload, and the symbol indexer re-walked files
// that had not changed. macOS's FSEvents watches recursively and
// emits nothing, which is why this only ever bit on Linux.
func TestFileSchemeRecursiveWatchIgnoresPreexistingFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	for _, name := range []string{"a.txt", "b.txt", "sub/c.txt"} {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, name), []byte("x"), 0o666))
	}

	// FSEvents hands a new stream the changes its daemon recorded in
	// the instant before the stream started, so a watch opened in the
	// same millisecond as the fixture writes sees them. Real
	// workspaces are not created microseconds before being watched;
	// let the daemon flush so the assertion below is about watch
	// establishment and not about clock proximity.
	time.Sleep(500 * time.Millisecond)

	uri, err := makeLocalURI(dir)
	require.NoError(t, err)
	scheme, err := newTestFileScheme(uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	ch := make(chan schemeapi.EventInfo, 64)
	id, err := scheme.Watch(filepath.Join(dir, "..."), ch,
		schemeapi.Create, schemeapi.Write, schemeapi.Remove,
		schemeapi.Rename)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.StopWatch(id) })

	select {
	case ei := <-ch:
		t.Fatalf("watch reported %v for pre-existing %s; establishing "+
			"a watch must not synthesise events for files that were "+
			"already on disk", ei.Event(), ei.URI().Path())
	case <-time.After(500 * time.Millisecond):
	}

	// A real create after establishment must still be delivered,
	// so the suppression cannot simply be "drop every Create".
	created := filepath.Join(dir, "sub", "new.txt")
	require.NoError(t, os.WriteFile(created, []byte("y"), 0o666))
	select {
	case ei := <-ch:
		assert.Equal(t, filepath.Base(created),
			filepath.Base(ei.URI().Path()))
	case <-time.After(5 * time.Second):
		t.Fatal("create after watch establishment was not delivered")
	}
}

func TestFileSchemeCloseClosesTrackedFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	uri, err := workspaceapi.ParseURI("file://" + tmpDir)
	require.NoError(t, err)

	s, err := newTestFileScheme(uri)
	require.NoError(t, err)

	path1 := tmpDir + "/a.txt"
	path2 := tmpDir + "/b.txt"
	require.NoError(t, os.WriteFile(path1, []byte("hello"), 0644))
	require.NoError(t, os.WriteFile(path2, []byte("world"), 0644))

	f1, err := s.OpenFile(path1, os.O_RDONLY, 0)
	require.NoError(t, err)
	f2, err := s.OpenFile(path2, os.O_RDONLY, 0)
	require.NoError(t, err)

	// Sanity: both files tracked before close.
	var countBefore int
	s.files.Range(func(_, _ any) bool { countBefore++; return true })
	assert.Equal(t, 2, countBefore)

	require.NoError(t, s.Close())

	// After Close, tracked files should have been removed and underlying files
	// closed (a second close on the underlying *os.File returns an error).
	var countAfter int
	s.files.Range(func(_, _ any) bool { countAfter++; return true })
	assert.Equal(t, 0, countAfter)

	// Closing again should be a no-op (idempotent) for the wrapped file.
	assert.NoError(t, f1.Close())
	assert.NoError(t, f2.Close())
}

// TestFileSchemeFdReuseDoesNotAliasFiles models the OS recycling a
// descriptor number: a stale wrapper whose number has since been
// re-registered by a successor must neither unwrap to the successor's
// file nor delete the successor's registration when closed.
func TestFileSchemeFdReuseDoesNotAliasFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	uri, err := workspaceapi.ParseURI("file://" + tmpDir)
	require.NoError(t, err)
	s, err := newTestFileScheme(uri)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	pathA := tmpDir + "/a.txt"
	pathB := tmpDir + "/b.txt"
	require.NoError(t, os.WriteFile(pathA, []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(pathB, []byte("b"), 0o644))

	fa, err := s.OpenFile(pathA, os.O_RDONLY, 0)
	require.NoError(t, err)
	stale := fa.(*fileSchemeFile)
	fb, err := s.OpenFile(pathB, os.O_RDONLY, 0)
	require.NoError(t, err)
	successor := fb.(*fileSchemeFile)

	// Simulate the OS handing stale's number to the successor after
	// stale's descriptor was closed elsewhere.
	s.files.Store(stale.fd, successor)

	// Unwrapping the stale wrapper must resolve by identity, not by
	// the recycled number.
	assert.True(t, s.tryUnwrapFileWriter(stale) == io.Writer(stale.File),
		"stale wrapper must unwrap to its own file, not the recycled number's owner")
	assert.True(t, s.tryUnwrapFileReader(stale) == io.Reader(stale.File),
		"stale wrapper must unwrap to its own file, not the recycled number's owner")

	// Closing the stale wrapper must not delete the successor's
	// registration under the recycled number.
	require.NoError(t, stale.Close())
	v, ok := s.files.Load(stale.fd)
	require.True(t, ok,
		"closing a stale wrapper must not drop the recycled number's registration")
	assert.Same(t, successor, v)
}

func TestOpenFileClosesSchemeOnCallerClose(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	path := tmpDir + "/a.txt"
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0644))

	f, err := OpenFile(path, os.O_RDONLY, 0)
	require.NoError(t, err)

	owned, ok := f.(*ownedSchemeFile)
	require.True(t, ok, "OpenFile should return an ownedSchemeFile")

	// Closing the returned file should also close the owning scheme's context,
	// which we can observe via the wrapped fileSchemeFile deregistering itself.
	inner, ok := owned.File.(*fileSchemeFile)
	require.True(t, ok, "wrapped file should be a fileSchemeFile")
	scheme := inner.p
	var tracked int
	scheme.files.Range(func(_, _ any) bool { tracked++; return true })
	require.Equal(t, 1, tracked)

	require.NoError(t, f.Close())

	// After Close, scheme should no longer track the file and its context
	// should be cancelled.
	tracked = 0
	scheme.files.Range(func(_, _ any) bool { tracked++; return true })
	assert.Equal(t, 0, tracked)
	select {
	case <-scheme.ctx.Done():
	default:
		t.Fatal("scheme context should be cancelled after close")
	}

	// Closing again should remain a no-op.
	assert.NoError(t, f.Close())
}

func dirURI(t *testing.T, dir string) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	return uri
}

func TestReadFileClosesFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	path := tmpDir + "/a.txt"
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0644))

	data, err := ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
	// Calling ReadFile many times should not accumulate open FDs; if it did
	// we'd eventually hit EMFILE. This is a coarse smoke-check.
	for i := range 1024 {
		_, err := ReadFile(path)
		require.NoError(t, err, "iteration %d", i)
	}
}

// TestFileSchemeCloseDoesNotRaceStartCommand guards StartCommand's
// fork/exec window against Close force-closing the scheme's tracked
// files: os/exec reads each std file's fd during StartProcess, and
// closing the *os.File concurrently is a data race on the fd state
// (and can hand the child a recycled descriptor). This mirrors the
// IDE teardown closing a workspace while a VTE warm-up is mid
// StartCommand.
//
// The race only fires when Close's teardown lands inside the
// unwrap-to-fork window, so the pair runs repeatedly from a common
// barrier to cover the interleavings deterministically enough for
// the race detector.
// newZdotDirFileScheme returns a fileScheme rooted at a temp dir whose
// config sets zdotdir, as the IDE does for bundled shell dotfiles.
func newZdotDirFileScheme(t *testing.T, zdotDir string) (*fileScheme, string) {
	t.Helper()
	tmpDir := t.TempDir()
	uri, err := workspaceapi.ParseURI("file://" + tmpDir)
	require.NoError(t, err)

	s := new(fileScheme)
	s.osStat = os.Stat
	s.getUser = func() (*user.User, error) {
		return &user.User{Username: "git", HomeDir: "/home/git"}, nil
	}
	s.lookupUser = func(name string) (*user.User, error) {
		return &user.User{Username: name, HomeDir: "/home/" + name}, nil
	}
	require.NoError(t, s.init(
		config.MapConfig(map[string]any{"zdotdir": zdotDir}), uri))
	return s, tmpDir
}

// runShellCommand starts cmd through s with SHELL pointed at a script named
// shellName, so the fileScheme's shell detection sees that basename, and
// returns the script's stdout.
func runShellCommand(
	t *testing.T, s *fileScheme, dir, shellName, script string, cmd workspaceapi.Cmd,
) string {
	t.Helper()
	bin := filepath.Join(dir, shellName)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	t.Setenv("SHELL", bin)

	var stdout bytes.Buffer
	ch := make(chan error)
	cmd.Stdout = &stdout
	cmd.Watcher = workspaceapi.ChanProcessWatcher(ch)
	_, err := s.StartCommand(context.Background(), cmd)
	require.NoError(t, err)
	require.NoError(t, <-ch)
	return stdout.String()
}

func TestFileSchemeCloseDoesNotRaceStartCommand(t *testing.T) {
	dir := t.TempDir()
	uri, err := makeLocalURI(dir)
	require.NoError(t, err)

	for i := range 100 {
		scheme, err := newTestFileScheme(uri)
		require.NoError(t, err)

		out, err := scheme.OpenFile(
			filepath.Join(dir, fmt.Sprintf("out-%d.log", i)),
			os.O_CREATE|os.O_RDWR, 0666)
		require.NoError(t, err)

		barrier := make(chan struct{})
		execDone := make(chan struct{})
		go func() {
			defer close(execDone)
			<-barrier
			// The error is irrelevant: post-close starts may
			// fail, but they must not race the closing of the
			// unwrapped files.
			_, _ = scheme.StartCommand(context.Background(), workspaceapi.Cmd{
				Path:   "/bin/sh",
				Args:   []string{"-c", "true"},
				Stdout: out,
			})
		}()

		closeDone := make(chan struct{})
		go func() {
			defer close(closeDone)
			<-barrier
			_ = scheme.Close()
		}()

		close(barrier)
		select {
		case <-execDone:
		case <-time.After(10 * time.Second):
			t.Fatal("StartCommand never returned after scheme close")
		}
		select {
		case <-closeDone:
		case <-time.After(10 * time.Second):
			t.Fatal("Close never returned")
		}
	}
}

// TestFileSchemeStartCommandScrubsGitHookEnv guards the executor's
// base environment against git's per-repository overrides: rune (or
// its test suite) launched from a git hook inherits GIT_DIR and
// friends, and forwarding them to workspace commands points every
// git invocation (vctrl status/diff, console git, git grep) at the
// hook's repository instead of the workspace. Caller-provided
// cmd.Env is appended after the base and must still pass through.
func TestFileSchemeStartCommandScrubsGitHookEnv(t *testing.T) {
	dir := t.TempDir()
	uri, err := makeLocalURI(dir)
	require.NoError(t, err)
	p, err := newTestFileScheme(uri)
	require.NoError(t, err)
	defer p.Close()

	t.Setenv("GIT_DIR", "/hook/.git")
	t.Setenv("GIT_INDEX_FILE", "/hook/.git/index")
	t.Setenv("GIT_WORK_TREE", "/hook")
	t.Setenv("GIT_SSH_COMMAND", "ssh -i key")
	t.Setenv("HOOK_KEPT_VAR", "kept")

	var out bytes.Buffer
	watcher := workspaceapi.ChanProcessWatcher(make(chan error, 1))
	_, err = p.StartCommand(context.Background(), workspaceapi.Cmd{
		Path:    "sh",
		Args:    []string{"-c", "env"},
		Dir:     dir,
		Env:     []string{"CALLER_VAR=explicit"},
		Stdout:  &out,
		Stderr:  &out,
		Watcher: watcher,
	})
	require.NoError(t, err)
	select {
	case perr := <-watcher.WatchProcess():
		require.NoError(t, perr)
	case <-time.After(10 * time.Second):
		t.Fatal("command never exited")
	}

	env := out.String()
	assert.NotContains(t, env, "GIT_DIR=",
		"per-repository override must not reach workspace commands")
	assert.NotContains(t, env, "GIT_INDEX_FILE=",
		"per-repository override must not reach workspace commands")
	assert.NotContains(t, env, "GIT_WORK_TREE=",
		"per-repository override must not reach workspace commands")
	assert.Contains(t, env, "GIT_SSH_COMMAND=ssh -i key",
		"user-level git configuration must be preserved")
	assert.Contains(t, env, "HOOK_KEPT_VAR=kept",
		"non-git environment must be preserved")
	assert.Contains(t, env, "CALLER_VAR=explicit",
		"caller-provided cmd.Env must still pass through")
}
