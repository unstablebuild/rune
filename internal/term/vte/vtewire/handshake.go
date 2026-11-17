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
	"fmt"
)

// Error is a rejected or dropped attachment. Its Code is what lets a
// caller tell a confirmed missing emulator from every other failure,
// which is the only case where recreating a terminal from a snapshot
// is safe, and a drop it should recover from by attaching again.
type Error struct {
	Code    ErrCode
	Message string
}

func (e *Error) Error() string {
	what := "attach rejected"
	if e.Code == CodeSlow {
		what = "attachment dropped"
	}
	if e.Message == "" {
		return fmt.Sprintf("vtewire: %s (code %d)", what, uint8(e.Code))
	}
	return "vtewire: " + what + ": " + e.Message
}

// NotFound reports whether the server confirmed the emulator does not
// exist.
func (e *Error) NotFound() bool { return e.Code == CodeNotFound }

// Slow reports whether the server dropped the attachment for falling
// behind, which a fresh attachment recovers from.
func (e *Error) Slow() bool { return e.Code == CodeSlow }

// versionMismatch is the message of the HELLO_ERR refusing a client's
// version. Servers of every version send it in this form, which is what
// lets a client learn the version of a server it cannot talk to.
const versionMismatch = "server speaks version %d, client speaks %d"

// ServerVersion reports the version a server that refused the client's
// version speaks, and false for any other error or a server that did
// not say.
func (e *Error) ServerVersion() (uint16, bool) {
	if e.Code != CodeVersion {
		return 0, false
	}
	var server, client uint16
	if _, err := fmt.Sscanf(e.Message, versionMismatch, &server, &client); err != nil {
		return 0, false
	}
	return server, true
}

// Dial performs the client side of the handshake over an established
// connection. On success the caller reads the snapshot and then the
// live frame stream.
func Dial(conn *Conn, hello *Hello) (*HelloOK, error) {
	hello.Version = Version
	if err := conn.WriteFrame(hello); err != nil {
		return nil, fmt.Errorf("send hello: %w", err)
	}

	frame, err := conn.ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("read hello reply: %w", err)
	}
	switch reply := frame.(type) {
	case *HelloOK:
		return reply, nil
	case *HelloErr:
		return nil, &Error{Code: reply.Code, Message: reply.Message}
	default:
		return nil, fmt.Errorf("%w: expected HELLO_OK, got %s",
			ErrMalformed, frame.Type())
	}
}

// Accept performs the server side of the handshake: it reads the
// opening HELLO and checks the protocol version, answering HELLO_ERR
// itself when the version is not implemented.
func Accept(conn *Conn) (*Hello, error) {
	frame, err := conn.ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("read hello: %w", err)
	}
	hello, ok := frame.(*Hello)
	if !ok {
		return nil, fmt.Errorf("%w: expected HELLO, got %s",
			ErrMalformed, frame.Type())
	}
	if hello.Version != Version {
		err := &Error{
			Code:    CodeVersion,
			Message: fmt.Sprintf(versionMismatch, Version, hello.Version),
		}
		_ = conn.WriteFrame(&HelloErr{Code: err.Code, Message: err.Message})
		return nil, err
	}
	return hello, nil
}
