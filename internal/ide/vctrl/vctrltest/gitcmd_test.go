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

package vctrltest

import (
	"context"
	"errors"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace"
)

func TestCmdDiff(t *testing.T) {
	testGitDiff(t, setupGitCmdService)
}

func TestCmdGitCurrentCommit(t *testing.T) {
	testGitCurrentCommit(t, setupGitCmdService)
}

func TestCmdGitShortRef(t *testing.T) {
	testGitShortRef(t, setupGitCmdService)
}

func TestCmdGitRemoteURL(t *testing.T) {
	testGitRemoteURL(t, setupGitCmdService)
}

func TestCmdRelPath(t *testing.T) {
	testRelPath(t, setupGitCmdService)
}

func setupGitCmdService(t *testing.T, cwd workspaceapi.URI) vctrl.Service {
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), cwd,
	)
	require.NoError(t, err)

	// gitCliExecutor runs git commands on real repos extracted from tarballs
	gitCliExecutor := new(gitTestExecutor)
	gitCliExecutor.schemeExecutor = scheme

	return vctrl.NewGitCommand(cwd, gitCliExecutor, scheme)
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

func (*gitTestExecutor) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}
