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
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config/configrpc"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace/processctx"
)

func fetchLiveConfig(t *testing.T, c *LiveConfig, id string) map[string]any {
	t.Helper()
	ctx := context.Background()
	if id != "" {
		ctx = processctx.ContextWithExtensionID(ctx, id)
	}
	res, err := liveConfigServer{c: c, locker: new(sync.Mutex)}.Get(ctx, &configrpc.GetRequest{})
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.GetData()), &got))
	return got
}

func TestLiveConfigGet(t *testing.T) {
	cfg := map[string]any{
		"editor": map[string]any{"mode": "vim"},
		"extensions": map[string]any{
			"rune-agent": map[string]any{
				"path":   "/bin/rune-agent",
				"config": map[string]any{"status_bar": map[string]any{"enabled": true}},
			},
			"git":    map[string]any{"path": "/bin/git-ext"},
			"broken": "not a map",
		},
	}
	tests := []struct {
		name   string
		caller string
		want   map[string]any
	}{
		{
			name:   "the caller sees only its own block",
			caller: "rune-agent",
			want: map[string]any{
				"editor": map[string]any{"mode": "vim"},
				"extensions": map[string]any{
					"rune-agent": map[string]any{
						"config": map[string]any{"status_bar": map[string]any{"enabled": true}},
					},
				},
			},
		},
		{name: "a caller without a block", caller: "git", want: map[string]any{"editor": map[string]any{"mode": "vim"}}},
		{name: "a caller whose entry is not a map", caller: "broken", want: map[string]any{"editor": map[string]any{"mode": "vim"}}},
		{name: "an unknown caller", caller: "other", want: map[string]any{"editor": map[string]any{"mode": "vim"}}},
		{name: "a caller without an ID", want: map[string]any{"editor": map[string]any{"mode": "vim"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, fetchLiveConfig(t, NewLiveConfig(cfg), tt.caller))
		})
	}
}

func TestLiveConfigSet(t *testing.T) {
	c := NewLiveConfig(map[string]any{"theme": "old"})
	assert.Equal(t, map[string]any{"theme": "old"}, fetchLiveConfig(t, c, "rune-agent"))
	c.Set(map[string]any{
		"theme":      "new",
		"extensions": map[string]any{"rune-agent": map[string]any{"config": map[string]any{"x": "y"}}},
	})
	assert.Equal(t, map[string]any{
		"theme":      "new",
		"extensions": map[string]any{"rune-agent": map[string]any{"config": map[string]any{"x": "y"}}},
	}, fetchLiveConfig(t, c, "rune-agent"))
}

func TestLiveConfigConcurrentGetAndSet(t *testing.T) {
	c := NewLiveConfig(map[string]any{"n": "0"})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(2)
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			for range 50 {
				c.Set(map[string]any{"n": "1"})
			}
		})
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			for range 50 {
				_, err := liveConfigServer{c: c, locker: new(sync.Mutex)}.Get(context.Background(), &configrpc.GetRequest{})
				assert.NoError(t, err)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, map[string]any{"n": "1"}, fetchLiveConfig(t, c, ""))
}
