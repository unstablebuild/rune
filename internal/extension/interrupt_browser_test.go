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

package extension

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
)

func TestInterruptBrowserPrompt(t *testing.T) {
	opened := &browserrpc.PromptEvent{Event: &browserrpc.PromptEvent_Opened_{
		Opened: &browserrpc.PromptEvent_Opened{WindowId: 7},
	}}
	closed := &browserrpc.PromptEvent{Event: &browserrpc.PromptEvent_Closed_{
		Closed: new(browserrpc.PromptEvent_Closed),
	}}
	tests := []struct {
		name   string
		events []*browserrpc.PromptEvent
		err    error
		// seen is the number of redraws when each event is sent.
		seen    []int
		redraws int
	}{
		{"opened then closed", []*browserrpc.PromptEvent{opened, closed}, nil, []int{1, 1}, 2},
		{"fails before opening", nil, errors.New("invalid prompt"), nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redraws := 0
			var seen []int
			srv := &promptBrowserServer{events: tt.events, err: tt.err}
			b := interruptBrowserServer(srv, func() { redraws++ })
			err := b.Prompt(new(browserrpc.PromptRequest), &recordingPromptStream{
				sent: func(*browserrpc.PromptEvent) { seen = append(seen, redraws) },
			})
			assert.Equal(t, tt.err, err)
			assert.Equal(t, tt.seen, seen, "redraws when each event was sent")
			assert.Equal(t, tt.redraws, redraws, "redraws once the stream ends")
		})
	}
}

// promptBrowserServer streams events to every prompt, or fails with err.
type promptBrowserServer struct {
	browserrpc.UnimplementedEventPublisherServer
	browserrpc.UnimplementedNotificationsServer
	browserrpc.UnimplementedResourceOpenerServer
	browserrpc.UnimplementedWindowManagerServer
	events []*browserrpc.PromptEvent
	err    error
}

func (s *promptBrowserServer) Prompt(
	_ *browserrpc.PromptRequest, srv browserrpc.WindowManager_PromptServer,
) error {
	for _, ev := range s.events {
		if err := srv.Send(ev); err != nil {
			return err
		}
	}
	return s.err
}

type recordingPromptStream struct {
	browserrpc.WindowManager_PromptServer
	sent func(*browserrpc.PromptEvent)
}

func (s *recordingPromptStream) Send(ev *browserrpc.PromptEvent) error {
	s.sent(ev)
	return nil
}
