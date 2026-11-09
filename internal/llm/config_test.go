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

package llm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/llm/llamaserver"
)

func TestCodexClientConfigDisablesMaxOutputTokens(t *testing.T) {
	assert.True(t, (Config{}).CodexClientConfig().DisableMaxOutputTokens)
}

func TestLocalConfigEmbeddingService(t *testing.T) {
	local := DefaultConfig().Local
	local.Service.ContextWindow = 32768
	local.Service.BatchSize = 512
	local.Service.NGPULayers = 12
	local.Service.Threads = 6
	local.Service.FlashAttention = true
	local.Service.MaxOutputTokens = 4096
	local.Service.ChatTemplate = "chatml"
	local.Service.NCacheReuse = 256
	local.Service.Host = "127.0.0.2"
	local.Service.ServerBinPath = "/opt/llama-server"
	local.Service.ExtraArgs = []string{"--jinja"}
	local.Service.MaxServers = 3
	local.Service.IdleTimeout = 10 * time.Minute
	local.Service.StartupTimeout = time.Minute

	assert.Equal(t, llamaserver.Config{
		ContextWindow:  2048,
		BatchSize:      2048,
		NGPULayers:     12,
		Threads:        6,
		FlashAttention: true,
		Host:           "127.0.0.2",
		ServerBinPath:  "/opt/llama-server",
		IdleTimeout:    45 * time.Second,
		MaxServers:     1,
		StartupTimeout: time.Minute,
	}, local.EmbeddingService(45*time.Second))
}
