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
	"fmt"
	"io"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/config/configrpc"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"unstable.build/rune/internal/rpc"
	"unstable.build/rune/internal/workspace/processctx"
)

// LiveConfig is the config extensions fetch. Each caller gets the shared
// settings and only its own extensions.<id>.config block, as
// extensions.<id>.config; a caller without an extension ID gets no
// extensions key. Set changes what every later fetch returns. It is safe
// for concurrent use.
type LiveConfig struct {
	mu  sync.Mutex
	cfg map[string]any
}

// NewLiveConfig returns a LiveConfig serving cfg. See Set.
func NewLiveConfig(cfg map[string]any) *LiveConfig {
	return &LiveConfig{cfg: cfg}
}

// Set serves cfg from now on. cfg must not be modified afterwards.
func (c *LiveConfig) Set(cfg map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = cfg
}

// forCaller returns the config the extension with id sees.
func (c *LiveConfig) forCaller(id string) map[string]any {
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if k != "extensions" {
			out[k] = v
		}
	}
	if id == "" {
		return out
	}
	exts, _ := cfg["extensions"].(map[string]any)
	ext, _ := exts[id].(map[string]any)
	if own, ok := ext["config"]; ok {
		out["extensions"] = map[string]any{id: map[string]any{"config": own}}
	}
	return out
}

// Register implements ResourceRegistrar.
func (c *LiveConfig) Register(registrar rpc.ServiceRegistrar, locker sync.Locker) (io.Closer, error) {
	configrpc.RegisterConfigServer(registrar, liveConfigServer{c: c, locker: locker})
	return nopCloser{}, nil
}

type liveConfigServer struct {
	configrpc.UnimplementedConfigServer
	c *LiveConfig
	// locker guards the maps the config shares with the IDE's.
	locker sync.Locker
}

func (s liveConfigServer) Get(ctx context.Context, _ *configrpc.GetRequest) (*configrpc.GetResponse, error) {
	id, _ := processctx.ExtensionIDFromContext(ctx)
	s.locker.Lock()
	cfg := config.JSONFromConfig(config.MapConfig(s.c.forCaller(id)))
	data, err := cfg.MarshalText()
	s.locker.Unlock()
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	return &configrpc.GetResponse{Data: string(data)}, nil
}

// ConfigResources returns a map of Permission to a ResourceServer
// serving c.
func ConfigResources(c *LiveConfig) map[extensionapi.Permission]ResourceRegistrar {
	return map[extensionapi.Permission]ResourceRegistrar{
		extensionapi.PermissionConfig: c,
	}
}

type nopCloser struct {
}

func (c nopCloser) Close() error {
	return nil
}
