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
	"fmt"
	"io"

	multierr "github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/bluenet"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/procattr"
)

type serverCommandStreamer struct {
	cmd       workspaceapi.Cmd
	stream    workspacerpc.Executor_StartCommandServer
	doneCh    chan error
	stdinCh   chan bluenet.ReadResult
	stdoutCh  chan bluenet.ReadResult
	stderrCh  chan bluenet.ReadResult
	stdinFd   uint32
	stdoutFd  uint32
	stderrFd  uint32
	ctx       context.Context
	cancelCtx func()
	closers   []io.Closer
}

func newServerCommandStreamer(
	ctx context.Context,
	cancelCtx func(),
	stream workspacerpc.Executor_StartCommandServer,
	path, dir string, args, env []string,
	stdinSet, stdoutSet, stderrSet bool,
	stdinFd, stdoutFd, stderrFd uint32,
	stdinName, stdoutName, stderrName string,
	setsid, setctty bool,
	scheme schemeapi.Scheme,
) (*serverCommandStreamer, error) {
	doneCh := make(chan error)

	cmd := workspaceapi.Cmd{
		Path:    path,
		Dir:     dir,
		Args:    args,
		Env:     env,
		Watcher: workspaceapi.ChanProcessWatcher(doneCh),
	}
	if setsid || setctty {
		cmd.SysProcAttr = procattr.NewSession(setsid, setctty)
	}

	ret := new(serverCommandStreamer)

	// it's important that this channels are not buffered
	// so when Cmd.Wait returns, it means that all the data
	// has been drained from the connection
	stdinCh := make(chan bluenet.ReadResult)
	stdoutCh := make(chan bluenet.ReadResult)
	stderrCh := make(chan bluenet.ReadResult)

	// always set these channels so we don't need to worry
	// about nil conditions below
	ret.stdinCh = stdinCh
	ret.stdoutCh = stdoutCh
	ret.stderrCh = stderrCh

	if stdinSet {
		if stdinFd != 0 {
			cmd.Stdin = scheme.NewFile(uintptr(stdinFd), stdinName)
			if cmd.Stdin == nil {
				return nil, fmt.Errorf("invalid stdin file descriptor: %d", stdinFd)
			}
			ret.stdinFd = stdinFd
		} else {
			stdinOutCh := make(chan bluenet.ReadResult)
			// use ChanConn as io.Writer and io.Reader,
			// which means that addrs can be nil
			stdin := bluenet.ChanConn(nil, nil /* addrs */, stdinCh, stdinOutCh)
			cmd.Stdin = stdin
			ret.closers = append(ret.closers, stdin)
		}
	}

	if stdoutSet {
		if stdoutFd != 0 {
			cmd.Stdout = scheme.NewFile(uintptr(stdoutFd), stdoutName)
			if cmd.Stdout == nil {
				return nil, fmt.Errorf("invalid stdout file descriptor: %d", stdoutFd)
			}
			ret.stdoutFd = stdoutFd
		} else {
			stdoutInCh := make(chan bluenet.ReadResult)
			stdout := bluenet.ChanConn(nil, nil /* addrs */, stdoutInCh, stdoutCh)
			cmd.Stdout = stdout
			ret.closers = append(ret.closers, stdout)
		}
	}

	if stderrSet {
		if stderrFd != 0 {
			cmd.Stderr = scheme.NewFile(uintptr(stderrFd), stderrName)
			if cmd.Stderr == nil {
				return nil, fmt.Errorf("invalid stderr file descriptor: %d", stderrFd)
			}
			ret.stderrFd = stderrFd
		} else {
			stderrInCh := make(chan bluenet.ReadResult)
			stderr := bluenet.ChanConn(nil, nil /* addrs */, stderrInCh, stderrCh)
			cmd.Stderr = stderr
			ret.closers = append(ret.closers, stderr)
		}
	}

	ret.cmd = cmd
	ret.stream = stream
	ret.doneCh = doneCh

	ret.ctx = ctx
	ret.cancelCtx = cancelCtx

	return ret, nil
}

func (s *serverCommandStreamer) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithFields(log.Fields{logging.KeyClass: "serverCommandStreamer"}).
		Logf(level, msg, args...)
}

func (s *serverCommandStreamer) receiveCommandData() {
	defer s.log(log.TraceLevel, "done receiving command data")
	defer close(s.stdinCh)

	// propagate context cancel to context passed
	// to command Start; either because client is closing
	// stream, or client context passed to Start canceled.
	go debug.CapturePanicReport(func() {
		<-s.stream.Context().Done()
		s.cancelCtx()
	})

	if s.stdinFd != 0 {
		s.log(log.DebugLevel, "not reading from stdin goroutine: remote file mode")
		return
	}

	for {
		var msg workspacerpc.CommandPayload
		err := s.stream.RecvMsg(&msg)
		s.log(log.TraceLevel, "receive msg: err=%v", err)
		if err != nil {
			if err == io.EOF {
				return
			}
			if cerr := s.stream.Context().Err(); cerr != nil {
				return
			}
			select {
			case <-s.ctx.Done():
				return
			default:
				ack := make(chan struct{})
				select {
				case s.stdinCh <- bluenet.ReadResult{Error: err, Ch: ack}:
					<-ack
					continue
				case <-s.ctx.Done():
					return
				}
			}
		}

		var data []byte
		switch msg.Type {
		case workspacerpc.CommandPayload_TypeIO:
			io := msg.GetIo()
			switch io.GetType() {
			case workspacerpc.CommandPayload_IO_TypeStdin:
				data = io.GetData()
			default:
				err = fmt.Errorf("unexpected IO type received: %v", io.GetType())
			}
		default:
			err = fmt.Errorf("unexpected message type received: %v", msg.Type)
		}

		ack := make(chan struct{})
		select {
		case s.stdinCh <- bluenet.ReadResult{Error: err, Data: data, Ch: ack}:
			<-ack
			s.log(log.TraceLevel,
				"wrote to stdin: err=%v, data=%d", err, len(data))
			continue
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *serverCommandStreamer) streamReadResult(
	res bluenet.ReadResult, t workspacerpc.CommandPayload_IO_Type,
) error {
	defer close(res.Ch)

	if res.Error != nil {
		err := s.stream.Send(&workspacerpc.CommandPayload{
			Type:  workspacerpc.CommandPayload_TypeError,
			Error: res.Error.Error(),
		})
		s.log(log.TraceLevel, "send error msg: err=%v", err)
		if err != nil {
			return fmt.Errorf("send error msg: %v", err)
		}
		return fmt.Errorf("error reading from standard io %v: %v", t, res.Error)
	}
	err := s.stream.Send(&workspacerpc.CommandPayload{
		Type: workspacerpc.CommandPayload_TypeIO,
		Io:   &workspacerpc.CommandPayload_IO{Data: res.Data, Type: t},
	})
	s.log(log.TraceLevel, "send io msg: err=%v", err)
	if err != nil {
		return fmt.Errorf("send io msg: %v", err)
	}
	return nil
}

func (s *serverCommandStreamer) sendCommandData(pid workspaceapi.Pid) error {
	err := s.stream.Send(&workspacerpc.CommandPayload{
		Type: workspacerpc.CommandPayload_TypeStarted,
		Started: &workspacerpc.CommandPayload_Started{
			Pid: int64(pid),
		},
	})
	s.log(log.TraceLevel, "send started msg: err=%v", err)
	if err != nil {
		return fmt.Errorf("send msg started: %v", err)
	}
	stdoutCh := s.stdoutCh
	stderrCh := s.stderrCh
	for {
		var err error
		select {
		case <-s.ctx.Done():
			err = s.ctx.Err()
		case res, ok := <-stdoutCh:
			if s.stdoutFd != 0 {
				s.log(log.ErrorLevel, "read from stdout but using file mode: ok=%v, err=%v, data=%d",
					ok, res.Error, len(res.Data))
				return errors.New("unexpected data in stdout chan")
			}
			s.log(log.TraceLevel, "read from stdout: ok=%v, err=%v, data=%d",
				ok, res.Error, len(res.Data))
			if !ok {
				stdoutCh = nil
				continue
			}
			err = s.streamReadResult(res, workspacerpc.CommandPayload_IO_TypeStdout)
		case res, ok := <-stderrCh:
			if s.stderrFd != 0 {
				s.log(log.ErrorLevel, "read from stderr but using file mode: ok=%v, err=%v, data=%d",
					ok, res.Error, len(res.Data))
				return errors.New("unexpected data in stderr chan")
			}
			s.log(log.TraceLevel, "read from stderr: ok=%v, err=%v, data=%d",
				ok, res.Error, len(res.Data))
			if !ok {
				stderrCh = nil
				continue
			}
			err = s.streamReadResult(res, workspacerpc.CommandPayload_IO_TypeStderr)
		case doneErr := <-s.doneCh:
			var errStr string
			if doneErr != nil && doneErr != io.EOF {
				errStr = doneErr.Error()
			}
			err = s.stream.Send(&workspacerpc.CommandPayload{
				Type: workspacerpc.CommandPayload_TypeDone,
				Done: &workspacerpc.CommandPayload_Done{
					ExitError: errStr,
				},
			})
			if err != nil {
				err = fmt.Errorf("send done msg: %v", err)
				s.log(log.WarnLevel, "%v", err)
			} else {
				s.log(log.TraceLevel, "send done msg: success")
			}
			// after receiving Done, client will disconnect
			// and RecvMsg in receiveCommandData will return with canceled error.
			return err
		}
		if err != nil {
			return err
		}
	}
}

func (s *serverCommandStreamer) command() workspaceapi.Cmd {
	return s.cmd
}

func (s *serverCommandStreamer) Close() (ret error) {
	for _, closer := range s.closers {
		if err := closer.Close(); err != nil {
			ret = multierr.Append(ret, err)
		}
	}
	s.cancelCtx()
	return
}
