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

package vctrlcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace"
)

type notiRecord struct {
	level browserapi.NotificationLevel
	text  string
}

type testNotifications struct {
	msg []notiRecord
}

func (n *testNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	n.msg = append(n.msg, notiRecord{
		level: level,
		text:  fmt.Sprintf(msg, args...)},
	)
	return strconv.Itoa(len(n.msg)), nil
}

func (n *testNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *testNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return nil
}

func assertNoti(
	t *testing.T, noti notiRecord, level browserapi.NotificationLevel, text string,
) {
	assert.Equal(t, notiRecord{level: level, text: text}, noti)
}

// setupCommandHandler prepares git repos and constructs the CommandHandler to
// run test against with all its dependencies.
func setupCommandHandler(t *testing.T) (
	h *copyRemoteURL,
	reposPath string,
	fileURI workspaceapi.URI,
	git vctrl.Service,
	noti *testNotifications,
) {
	reposPath = setupGitRepos(t)

	workspaceCwd := reposPath + "/gitproj6_two-remotes"
	workspaceCwdURI, err := workspaceapi.ParseURI("file://" + workspaceCwd)
	require.NoError(t, err)

	git = setupGitService(t, workspaceCwdURI)
	noti = &testNotifications{}
	clip := clipboard.NewInMemory()

	fileURI = workspaceapi.Join(workspaceCwdURI, "recipes/guasacaca.md")

	h = newCopyRemoteURL(
		git, clip, noti,
	)

	return
}

func TestCommandHandler(t *testing.T) {
	t.Run("happy command", func(t *testing.T) {
		h, reposPath, fileURI, _, testNoti := setupCommandHandler(t)
		defer tearDownGitRepos(reposPath)

		err := h.HandleCommand(context.Background(), textapi.Command{
			Name: commandGitLink,
			Args: []string{}, // without args uses "origin"
			URI:  fileURI,
			Cursor: struct {
				Content term.Coordinates
				Window  term.Coordinates
			}{Content: term.Coordinates{Y: 3}},
		})

		require.NoError(t, err)
		paste, err := h.clip.Paste(clipboard.DefaultRegisterID)

		expected := "https://git.unstable.build/unstablebuild/gitproj6" +
			"/src/commit/5367f818c4e7092a224ae0f32da1b7bab8843cc0" +
			"/recipes/guasacaca.md#L4"

		assert.Len(t, testNoti.msg, 1)
		assertNoti(t, testNoti.msg[0],
			browserapi.LevelSuccess,
			"copied "+expected,
		)

		require.NoError(t, err)
		assert.Equal(t, expected, paste.Text)
	})
}

func TestWeblinkGenerator(t *testing.T) {
	reposPath := setupGitRepos(t)
	defer tearDownGitRepos(reposPath)

	tsuite := []struct {
		name         string
		mustError    bool
		workspaceCwd string
		remoteName   string
		inputFile    string
		inputLine    int
		expect       string
	}{
		{
			name:         "file from repo within cwd and line",
			workspaceCwd: reposPath + "/gitproj6_two-remotes",
			remoteName:   "origin",
			inputFile:    reposPath + "/gitproj6_two-remotes/recipes/guasacaca.md",
			inputLine:    33,
			expect: "https://git.unstable.build/unstablebuild/gitproj6" +
				"/src/commit/5367f818c4e7092a224ae0f32da1b7bab8843cc0/recipes/guasacaca.md#L33",
		},
		{
			name:         "absolute file from repo on other tree not in cwd",
			workspaceCwd: reposPath + "/gitproj6_two-remotes",
			remoteName:   "origin",
			inputFile:    reposPath + "/gitproj5_one-remote/recipes/chile-colorado.md",
			inputLine:    8,
			expect: "https://git.unstable.build/unstablebuild/gitproj5" +
				"/src/commit/b0c7a3e628f4bfb0cb60f2ee048b7e47017c57e7/recipes/chile-colorado.md#L8",
		},
		{
			name:         "remote name is honored",
			workspaceCwd: reposPath + "/gitproj6_two-remotes",
			remoteName:   "private-mirror",
			inputFile:    reposPath + "/gitproj6_two-remotes/recipes/guasacaca.md",
			inputLine:    8,
			expect: "https://git.unstable.build/unstablebuild/gitproj6-mirror" +
				"/src/commit/5367f818c4e7092a224ae0f32da1b7bab8843cc0/recipes/guasacaca.md#L8",
		},
		{
			name:      "unexistent remote name",
			mustError: true,
			// ERROR: remote url: process exit with non-zero status (exit
			// status 2) error: No such remote 'unexistent-remote-name'
			workspaceCwd: reposPath + "/gitproj6_two-remotes",
			remoteName:   "unexistent-remote-name",
			inputFile:    reposPath + "/gitproj6_two-remotes/recipes/chile-colorado.md",
			inputLine:    8,
		},
		{
			name:      "empty remote name",
			mustError: true,
			// ERROR: remote url: must pass remote name
			workspaceCwd: reposPath + "/gitproj6_two-remotes",
			remoteName:   "",
			inputFile:    reposPath + "/gitproj6_two-remotes/recipes/chile-colorado.md",
			inputLine:    8,
		},
		{
			name:         "github remotes",
			workspaceCwd: reposPath + "/gitproj6_two-remotes",
			remoteName:   "public-mirror",
			inputFile:    reposPath + "/gitproj6_two-remotes/recipes/guasacaca.md",
			inputLine:    8,
			expect: "https://github.com/unstablebuild/gitproj6-mirror" +
				"/blob/5367f818c4e7092a224ae0f32da1b7bab8843cc0/recipes/guasacaca.md#L8",
		},
	}

	c := copyRemoteURL{}
	c.providerResolver = &stringsContainsResolver{}
	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			workspaceCwdURI, err := workspaceapi.ParseURI("file://" + tcase.workspaceCwd)
			require.NoError(t, err)

			git := setupGitService(t, workspaceCwdURI)
			c.git = git

			uri, err := workspaceapi.ParseURI("file://" + tcase.inputFile)
			require.NoError(t, err)
			res, err := c.generate(context.Background(),
				uri, tcase.remoteName, tcase.inputLine)
			if tcase.mustError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tcase.expect, res)
		})
	}
}

func TestParseRemoteURL(t *testing.T) {
	tsuite := []struct {
		name      string
		input     string
		mustError bool
		expect    remoteURLParts
	}{
		{
			name:  "git http url",
			input: "https://git.unstable.build/unstablebuild/go-tui.git",
			expect: remoteURLParts{
				domain: "git.unstable.build",
				owner:  "unstablebuild",
				repo:   "go-tui",
			},
		},
		{
			name:  "git ssh url",
			input: "git@git.unstable.build:unstablebuild/go-tui.git",
			expect: remoteURLParts{
				domain: "git.unstable.build",
				owner:  "unstablebuild",
				repo:   "go-tui",
			},
		},
		{
			name:  "not ending with .git suffix",
			input: "git@git.unstable.build:unstablebuild/go-tui",
			expect: remoteURLParts{
				domain: "git.unstable.build",
				owner:  "unstablebuild",
				repo:   "go-tui",
			},
		},
		{
			name:      "owner part is namespaced",
			input:     "git@git.unstable.build:unstablebuild/namespace/go-tui.git",
			mustError: true,
		},
		{
			name:      "illegal url",
			input:     "git@git.unstable.build:unstablebuild/$%2go-tui.git",
			mustError: true,
		},
	}

	c := copyRemoteURL{}
	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			res, err := c.parseRemoteURL(tcase.input)
			if tcase.mustError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tcase.expect, res)
		})
	}
}

func TestExpand(t *testing.T) {
	tsuite := []struct {
		name        string
		mustError   bool
		inputString string
		inputValues map[string]string
		expect      string
	}{
		{
			name:        "expand fills placeholders",
			inputString: ".:{a}-{b}:.",
			inputValues: map[string]string{"a": "1", "b": "2"},
			expect:      ".:1-2:.",
		},
		{
			name:      "expand leaving unfilled placeholders",
			mustError: true,
			// ERROR: template left with empty placeholders: .:1-{b}:.
			inputString: ".:{a}-{b}:.",
			inputValues: map[string]string{"a": "1"},
		},
		{
			name:        "expand giving extra placeholders",
			inputString: ".:{a}-{b}:.",
			inputValues: map[string]string{"a": "1", "b": "2", "c": "3"},
			expect:      ".:1-2:.",
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			res, err := expand(tcase.inputString, tcase.inputValues)
			if tcase.mustError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tcase.expect, res)
		})
	}
}

type gitTestExecutor struct {
	schemeExecutor schemeapi.Executor
}

func (e *gitTestExecutor) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return e.schemeExecutor.StartCommand(ctx, cmd)
}

func (e *gitTestExecutor) Signal(pid workspaceapi.Pid, signal syscall.Signal) error {
	return nil
}

func (e *gitTestExecutor) Close() error {
	return nil
}

func tearDownGitRepos(reposFolderPath string) {
	os.RemoveAll(reposFolderPath)
}

func setupGitService(t *testing.T, cwd workspaceapi.URI) vctrl.Service {
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), cwd,
	)
	require.NoError(t, err)

	// gitCliExecutor runs git commands on real repos extracted from tarballs
	gitCliExecutor := new(gitTestExecutor)
	gitCliExecutor.schemeExecutor = scheme

	return vctrl.NewGitCommand(cwd, gitCliExecutor, scheme)
}

func setupGitRepos(t *testing.T) (reposPath string) {
	tmpDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)

	// make tmpDir a canonical path, since usually on macOS is
	// `/var/folders/...` but when you get the repo path using the git cli it
	// returns canonical `/private/var/folders`, so we need to eval symlinks
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	require.NoError(t, err)

	reposTarball := "../vctrltest/testdata/repos.tar"

	// Tar run as root keeps the archive's owner, and git refuses
	// repositories owned by someone else.
	out, err := exec.Command(
		"tar", "--no-same-owner", "-xf", reposTarball, "-C", tmpDir,
	).CombinedOutput()
	require.NoError(t, err, "%s", out)

	reposPath = tmpDir + "/repos"

	return
}

func (*gitTestExecutor) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}
