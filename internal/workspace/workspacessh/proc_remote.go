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
	"errors"
	"fmt"
	"strings"
	"syscall"

	"github.com/ernestrc/go-multierror"
	"github.com/unstablebuild/blue/bluectx"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/remotescheme"
)

type procRemote struct {
	cmd  string
	args []string

	executor  schemeapi.Executor
	sessions  []*procSession
	ctx       context.Context
	cancelCtx func()
}

type procSession struct {
	sshCmd   string
	sshArgs  []string
	executor schemeapi.Executor
	ctx      context.Context
}

func newProcRemote(ctx context.Context, cfg sshConfig, uri workspaceapi.URI) (
	remote, error,
) {
	port := uri.Port()
	cmd := cfg.command
	if port != "" {
		if !strings.Contains(cfg.command, "%p") {
			return nil, fmt.Errorf("unable to set custom port: 'command' value is missing %%p")
		}
		cmd = strings.ReplaceAll(cfg.command, "%p", port)
	}

	host := uri.Hostname()
	if uri.User() != "" {
		host = fmt.Sprintf("%s@%s", uri.User(), host)
	}
	cmd = strings.ReplaceAll(cmd, "%h", host)

	args := strings.Split(cmd, " ")

	// make sure NewFileScheme will not return an error
	uri, err := workspaceapi.CurrentUserHostURI(".")
	if err != nil {
		return nil, fmt.Errorf("could not get current user host URI: %s", err)
	}

	// local because we are going to execute commands locally with command ssh sessions
	localExecutor, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	if err != nil {
		return nil, fmt.Errorf("could initialize local executor on URI %q: %s", uri.String(), err)
	}

	ret := &procRemote{executor: localExecutor, cmd: args[0], args: args[1:]}
	ret.ctx, ret.cancelCtx = context.WithCancel(context.Background())
	return ret, nil
}

func (m *procRemote) NewSession() (schemeapi.Executor, error) {
	ses := &procSession{
		sshCmd:   m.cmd,
		sshArgs:  m.args,
		executor: m.executor,
		ctx:      m.ctx,
	}

	m.sessions = append(m.sessions, ses)
	return ses, nil
}

func (m *procRemote) Close() (ret error) {
	m.cancelCtx()
	for _, ses := range m.sessions {
		if err := ses.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	m.sessions = nil

	if err := m.executor.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}

	return ret
}

func (m *procRemote) CommandString(name string, arg ...string) (string, []string) {
	ses := procSession{
		sshCmd:  m.cmd,
		sshArgs: m.args,
	}
	return ses.CommandString(name, arg...)
}

func (s *procSession) CommandString(name string, arg ...string) (string, []string) {
	arg = append(s.sshArgs, append([]string{name}, arg...)...)
	name = s.sshCmd
	return name, arg
}

func (s *procSession) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (
	workspaceapi.Pid, error,
) {
	if cmd.Path == "" {
		return 0, errors.New("no command")
	}

	cmd.Path, cmd.Args = s.CommandString(cmd.Path, cmd.Args...)
	ctx, cancelFn := bluectx.First(s.ctx, ctx)
	cmd.Watcher = remotescheme.NewCancelWatcher(cmd.Watcher, cancelFn)
	pid, err := s.executor.StartCommand(ctx, cmd)
	if err != nil {
		return workspaceapi.Pid(0), fmt.Errorf("create ssh command: %s", err)
	}
	return pid, nil
}

func (s *procSession) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	return s.executor.Signal(pid, sig)
}

// LookPath reports errors.ErrUnsupported: the session hands its command
// to the login shell of the remote user, which alone decides what runs.
func (s *procSession) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (s *procSession) Close() (ret error) {
	return ret
}
