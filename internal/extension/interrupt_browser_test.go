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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
)

type mockBrowserServerForInterrupt struct {
	browserrpc.UnimplementedWindowManagerServer
	browserrpc.UnimplementedEventPublisherServer
	browserrpc.UnimplementedNotificationsServer
	browserrpc.UnimplementedResourceOpenerServer

	gotReq *browserrpc.SetTabNameRequest
}

func (m *mockBrowserServerForInterrupt) SetTabName(
	ctx context.Context, req *browserrpc.SetTabNameRequest,
) (*browserrpc.SetTabNameResponse, error) {
	m.gotReq = req
	return &browserrpc.SetTabNameResponse{}, nil
}

func TestInterruptBrowserSetTabName(t *testing.T) {
	mock := &mockBrowserServerForInterrupt{}
	interrupted := false
	ib := interruptBrowserServer(mock, func() {
		interrupted = true
	})

	req := &browserrpc.SetTabNameRequest{
		ResourceId: "rune-agent://test/123",
		Name:       "new tab name",
	}
	res, err := ib.SetTabName(context.Background(), req)
	require.NoError(t, err)
	assert.NotNil(t, res)
	assert.Equal(t, req, mock.gotReq)
	assert.True(t, interrupted, "SetTabName must trigger interruptDraw")
}
