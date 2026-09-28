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

package textrpc

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdktextrpc "github.com/unstablebuild/rune-go-sdk/api/textapi/textrpc"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/termrpc"
	"google.golang.org/protobuf/proto"
)

func protoRows(n int) []*termrpc.CellRow {
	rows := make([]*termrpc.CellRow, n)
	for i := range rows {
		var pc termrpc.Cell
		pc.FromModel(term.NewCell('x', 1, term.Attributes{}))
		rows[i] = &termrpc.CellRow{Cells: []*termrpc.Cell{&pc}}
	}
	return rows
}

// Anything an interrupted command streams afterwards must not land in
// the command that follows it.
func TestREPLCommandClientStreamHandleCancelLifecycle(t *testing.T) {
	stream := newWaitableREPLServerStream()
	c := newREPLCommandClientStream(context.Background(), stream, true, true)
	go func() { _ = c.receiveMessages() }()

	abandoned, err := c.HandleCommand(
		context.Background(), repl.Command{Name: "dream"}, nil)
	require.NoError(t, err)
	msg := requireSent(t, stream)
	require.Equal(t, sdktextrpc.ServerREPLCommandMessage_Handle, msg.GetType())
	firstID := msg.GetHandle().GetId()
	require.NotZero(t, firstID)

	require.NoError(t, abandoned.Close())
	msg = requireSent(t, stream)
	require.Equal(t,
		sdktextrpc.ServerREPLCommandMessage_HandleCancel, msg.GetType())
	require.Equal(t, firstID, msg.GetHandleCancel().GetId())

	pw := &recordingProgressWriter{}
	next, err := c.HandleCommand(
		context.Background(), repl.Command{Name: "help"}, pw)
	require.NoError(t, err)
	msg = requireSent(t, stream)
	secondID := msg.GetHandle().GetId()
	require.NotEqual(t, firstID, secondID)

	stream.recv <- &sdktextrpc.ClientREPLCommandMessage{
		Type: sdktextrpc.ClientREPLCommandMessage_HandleValue,
		HandleValue: &sdktextrpc.HandleREPLCommandValue{
			Id: firstID, Rows: protoRows(1),
		},
	}
	stream.recv <- &sdktextrpc.ClientREPLCommandMessage{
		Type: sdktextrpc.ClientREPLCommandMessage_HandleProgress,
		HandleProgress: &sdktextrpc.HandleREPLCommandProgress{
			Id: firstID, Progress: 7, Total: 9,
		},
	}
	stream.recv <- &sdktextrpc.ClientREPLCommandMessage{
		Type: sdktextrpc.ClientREPLCommandMessage_HandleDone,
		HandleDone: &sdktextrpc.HandleREPLCommandDone{
			Id: firstID, Error: "context canceled",
		},
	}
	stream.recv <- &sdktextrpc.ClientREPLCommandMessage{
		Type: sdktextrpc.ClientREPLCommandMessage_HandleProgress,
		HandleProgress: &sdktextrpc.HandleREPLCommandProgress{
			Id: secondID, Progress: 1, Total: 1,
		},
	}
	stream.recv <- &sdktextrpc.ClientREPLCommandMessage{
		Type: sdktextrpc.ClientREPLCommandMessage_HandleValue,
		HandleValue: &sdktextrpc.HandleREPLCommandValue{
			Id: secondID, Rows: protoRows(2),
		},
	}

	val, ok := next.Next(context.Background())
	require.True(t, ok, "the abandoned command's done must not end this one")
	require.Equal(t, 2, val.Height(1),
		"the running command received the abandoned command's output")
	require.Equal(t, []progressSample{{progress: 1, total: 1}}, pw.get(),
		"the running command's progress bar showed the abandoned "+
			"command's progress")
}

func requireSent(
	t *testing.T, stream *waitableREPLServerStream,
) *sdktextrpc.ServerREPLCommandMessage {
	t.Helper()
	select {
	case msg := <-stream.sent:
		return msg
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a message on the stream")
		return nil
	}
}

type waitableREPLServerStream struct {
	sent chan *sdktextrpc.ServerREPLCommandMessage
	recv chan *sdktextrpc.ClientREPLCommandMessage
}

func newWaitableREPLServerStream() *waitableREPLServerStream {
	return &waitableREPLServerStream{
		sent: make(chan *sdktextrpc.ServerREPLCommandMessage, 8),
		recv: make(chan *sdktextrpc.ClientREPLCommandMessage, 8),
	}
}

func (s *waitableREPLServerStream) Send(
	msg *sdktextrpc.ServerREPLCommandMessage,
) error {
	s.sent <- msg
	return nil
}

func (s *waitableREPLServerStream) RecvMsg(msg any) error {
	next, ok := <-s.recv
	if !ok {
		return io.EOF
	}
	proto.Merge(msg.(*sdktextrpc.ClientREPLCommandMessage), next)
	return nil
}

// See TestCommandClientStreamCompleteCancelLifecycle: the REPL stream
// must abandon completions the same way.
func TestREPLCommandClientStreamCompleteCancelLifecycle(t *testing.T) {
	cases := []struct {
		name           string
		supportsCancel bool
	}{
		{name: "extension supports cancel", supportsCancel: true},
		{name: "legacy extension", supportsCancel: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream := newWaitableREPLServerStream()
			c := newREPLCommandClientStream(
				context.Background(), stream, tc.supportsCancel, tc.supportsCancel)
			var logs syncBuffer
			c.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
				Level: slog.LevelWarn,
			}))
			go func() { _ = c.receiveMessages() }()

			it, err := c.Complete(context.Background(), "cmd", nil)
			require.NoError(t, err)
			select {
			case msg := <-stream.sent:
				require.Equal(t,
					sdktextrpc.ServerREPLCommandMessage_Complete, msg.GetType())
			case <-time.After(time.Second):
				t.Fatal("Complete did not send the request")
			}

			require.NoError(t, it.Close())

			if tc.supportsCancel {
				select {
				case msg := <-stream.sent:
					require.Equal(t,
						sdktextrpc.ServerREPLCommandMessage_CompleteCancel,
						msg.GetType())
					require.Equal(t, int64(1), msg.GetCompleteCancel().GetId())
				case <-time.After(time.Second):
					t.Fatal("close did not cancel the completion")
				}
			} else {
				select {
				case msg := <-stream.sent:
					t.Fatalf("sent %v to an extension that cannot cancel",
						msg.GetType())
				case <-time.After(50 * time.Millisecond):
				}
			}

			stream.recv <- &sdktextrpc.ClientREPLCommandMessage{
				Type: sdktextrpc.ClientREPLCommandMessage_CompleteValue,
				CompleteValue: &sdktextrpc.CompleteCommandValue{
					Id: 1, Value: "late",
				},
			}
			stream.recv <- &sdktextrpc.ClientREPLCommandMessage{
				Type:         sdktextrpc.ClientREPLCommandMessage_CompleteDone,
				CompleteDone: &sdktextrpc.CompleteCommandDone{Id: 1},
			}
			// help values for an inactive request are logged by the same
			// receive loop, which gives the test an in-order marker.
			stream.recv <- &sdktextrpc.ClientREPLCommandMessage{
				Type:      sdktextrpc.ClientREPLCommandMessage_HelpValue,
				HelpValue: &sdktextrpc.HelpCommandValue{},
			}
			require.Eventually(t, func() bool {
				return logs.String() != ""
			}, time.Second, 10*time.Millisecond,
				"receive loop did not process the marker message")
			require.NotContains(t, logs.String(), "unknown stream")
			_, ok := c.completers.Load(int64(1))
			require.False(t, ok, "completion done must clear the tombstone")
		})
	}
}

func TestResponsiveFromProtoRows_PreservesPerCellAttributes(t *testing.T) {
	width := 4
	attrs := []term.Attributes{
		{Fg: term.ColorFuchsia, Attrs: term.AttrBold},
		{Fg: term.ColorGreen},
		{Fg: term.ColorGray},
		{Fg: term.ColorRed, Bg: term.ColorBlack},
	}
	runes := []rune{'a', 'b', 'c', 'd'}

	row := &termrpc.CellRow{Cells: make([]*termrpc.Cell, width)}
	for i := range width {
		var pc termrpc.Cell
		pc.FromModel(term.NewCell(runes[i], 1, attrs[i]))
		row.Cells[i] = &pc
	}

	r := responsiveFromProtoRows([]*termrpc.CellRow{row})
	r.Resize(width, 1)
	w := term.NewStringWriter(width, 1)
	require.NoError(t, w.Clear(term.Attributes{}))
	r.Draw(w)

	for x := range width {
		got := w.Cells()[x]
		assert.Equal(t, runes[x], got.Ch, "cell %d rune", x)
		assert.Equal(t, attrs[x], got.Attributes(),
			"cell %d attributes should round-trip per-cell; "+
				"if they collapse to the zero value the client side "+
				"is dropping per-cell colors", x)
	}
}
