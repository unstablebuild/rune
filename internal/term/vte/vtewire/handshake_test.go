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

package vtewire

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/debug"
)

func TestHandshake(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc      string
		hello     *Hello
		reply     Frame
		serverErr bool
		wantCode  ErrCode
		wantOK    *HelloOK
	}{
		{
			desc:   "attach accepted",
			hello:  &Hello{VTEID: "deadbeef", Width: 80, Height: 24},
			reply:  &HelloOK{VTEID: "deadbeef", URI: "file:///dev/ttys001"},
			wantOK: &HelloOK{VTEID: "deadbeef", URI: "file:///dev/ttys001"},
		},
		{
			desc: "create accepted",
			hello: &Hello{
				Width: 100, Height: 40,
				Create: CreateOptions{CommandAndArgs: []string{"zsh"}},
			},
			reply:  &HelloOK{VTEID: "fresh", Width: 100, Height: 40},
			wantOK: &HelloOK{VTEID: "fresh", Width: 100, Height: 40},
		},
		{
			desc:     "emulator missing",
			hello:    &Hello{VTEID: "gone"},
			reply:    &HelloErr{Code: CodeNotFound, Message: "no such emulator"},
			wantCode: CodeNotFound,
		},
		{
			desc:     "scheme owns no terminals",
			hello:    &Hello{},
			reply:    &HelloErr{Code: CodeUnsupported},
			wantCode: CodeUnsupported,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			clientEnd, serverEnd := net.Pipe()
			t.Cleanup(func() {
				_ = clientEnd.Close()
				_ = serverEnd.Close()
			})

			server := NewConn(serverEnd)
			accepted := make(chan *Hello, 1)
			go debug.CapturePanicReport(func() {
				hello, err := Accept(server)
				if err != nil {
					close(accepted)
					return
				}
				_ = server.WriteFrame(tc.reply)
				accepted <- hello
			})

			got, err := Dial(NewConn(clientEnd), tc.hello)
			if tc.wantOK == nil {
				var wireErr *Error
				require.ErrorAs(t, err, &wireErr)
				assert.Equal(t, tc.wantCode, wireErr.Code)
				assert.Equal(t, tc.wantCode == CodeNotFound, wireErr.NotFound())
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantOK, got)
			}

			hello := <-accepted
			require.NotNil(t, hello)
			assert.Equal(t, Version, hello.Version)
			assert.Equal(t, tc.hello.VTEID, hello.VTEID)
			assert.Equal(t, tc.hello.Create, hello.Create)
		})
	}
}

// foreignHello is a HELLO from a client of another protocol version,
// whose layout after the version need not match this one.
type foreignHello struct {
	version uint16
	rest    []byte
}

func (*foreignHello) Type() FrameType { return TypeHello }

func (f *foreignHello) encode(e *enc) {
	e.u16(f.version)
	e.raw(f.rest)
}

// TestAcceptRejectsForeignVersion pins that a version mismatch is
// reported as such rather than misparsed into a working attachment, or
// rejected as malformed because the other version lays HELLO out
// differently.
func TestAcceptRejectsForeignVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc  string
		hello Frame
	}{
		{desc: "same layout", hello: &Hello{Version: Version + 1}},
		{
			desc:  "shorter layout",
			hello: &foreignHello{version: Version - 1, rest: []byte{0, 0, 0, 0}},
		},
		{
			desc:  "longer layout",
			hello: &foreignHello{version: Version + 1, rest: make([]byte, 64)},
		},
		{desc: "nothing after the version", hello: &foreignHello{version: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			clientEnd, serverEnd := net.Pipe()
			t.Cleanup(func() {
				_ = clientEnd.Close()
				_ = serverEnd.Close()
			})

			server := NewConn(serverEnd)
			serverErr := make(chan error, 1)
			go debug.CapturePanicReport(func() {
				_, err := Accept(server)
				serverErr <- err
			})

			// A server that rejects the HELLO as malformed never answers.
			require.NoError(t, clientEnd.SetDeadline(time.Now().Add(10*time.Second)))
			client := NewConn(clientEnd)
			require.NoError(t, client.WriteFrame(tc.hello))

			frame, err := client.ReadFrame()
			require.NoError(t, err)
			reply, ok := frame.(*HelloErr)
			require.True(t, ok, "got %s", frame.Type())
			assert.Equal(t, CodeVersion, reply.Code)
			spoken, ok := (&Error{Code: reply.Code, Message: reply.Message}).ServerVersion()
			assert.True(t, ok, "the client learns the server's version from %q", reply.Message)
			assert.Equal(t, Version, spoken)

			var wireErr *Error
			require.True(t, errors.As(<-serverErr, &wireErr))
			assert.Equal(t, CodeVersion, wireErr.Code)
		})
	}
}

func TestErrorServerVersion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		desc   string
		err    *Error
		want   uint16
		wantOK bool
	}{
		{
			desc: "older server",
			err: &Error{Code: CodeVersion,
				Message: "server speaks version 3, client speaks 4"},
			want: 3, wantOK: true,
		},
		{
			desc: "newer server",
			err: &Error{Code: CodeVersion,
				Message: "server speaks version 9, client speaks 4"},
			want: 9, wantOK: true,
		},
		{
			desc: "another code",
			err: &Error{Code: CodeNotFound,
				Message: "server speaks version 3, client speaks 4"},
		},
		{desc: "no message", err: &Error{Code: CodeVersion}},
		{desc: "other wording", err: &Error{Code: CodeVersion, Message: "unsupported"}},
		{
			desc: "out of range",
			err: &Error{Code: CodeVersion,
				Message: "server speaks version 70000, client speaks 4"},
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			got, ok := tc.err.ServerVersion()
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestErrorString(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err      *Error
		want     string
		notFound bool
		slow     bool
	}{
		{
			err:      &Error{Code: CodeNotFound, Message: "no such emulator"},
			want:     "vtewire: attach rejected: no such emulator",
			notFound: true,
		},
		{
			err:  &Error{Code: CodeUnknown},
			want: "vtewire: attach rejected (code 0)",
		},
		{
			err:  &Error{Code: CodeSlow, Message: "4 MiB behind"},
			want: "vtewire: attachment dropped: 4 MiB behind",
			slow: true,
		},
		{
			err:  &Error{Code: CodeSlow},
			want: "vtewire: attachment dropped (code 4)",
			slow: true,
		},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.err.Error())
			assert.Equal(t, tc.notFound, tc.err.NotFound())
			assert.Equal(t, tc.slow, tc.err.Slow())
		})
	}
}

// TestHandshakeRejectsUnexpectedFrames pins that neither side treats a
// stray frame as a completed handshake.
func TestHandshakeRejectsUnexpectedFrames(t *testing.T) {
	t.Parallel()

	t.Run("client gets output instead of hello reply", func(t *testing.T) {
		t.Parallel()
		clientEnd, serverEnd := net.Pipe()
		t.Cleanup(func() {
			_ = clientEnd.Close()
			_ = serverEnd.Close()
		})

		server := NewConn(serverEnd)
		go debug.CapturePanicReport(func() {
			_, _ = server.ReadFrame()
			_ = server.WriteFrame(&Output{Data: []byte("early")})
		})

		_, err := Dial(NewConn(clientEnd), &Hello{})
		assert.ErrorIs(t, err, ErrMalformed)
	})

	t.Run("server gets input instead of hello", func(t *testing.T) {
		t.Parallel()
		clientEnd, serverEnd := net.Pipe()
		t.Cleanup(func() {
			_ = clientEnd.Close()
			_ = serverEnd.Close()
		})

		go debug.CapturePanicReport(func() {
			_ = NewConn(clientEnd).WriteFrame(&Input{Data: []byte("ls")})
		})

		_, err := Accept(NewConn(serverEnd))
		assert.ErrorIs(t, err, ErrMalformed)
	})
}
