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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/handler"
	runemcp "unstable.build/rune/cmd/rune-agent/mcp"
)

// answeringPrompter picks options from answers in turn, or dismisses
// the prompt for a negative index, and records every prompt it shows.
type answeringPrompter struct {
	mu      sync.Mutex
	answers []int
	shown   []browserapi.PromptConfig
}

func (p *answeringPrompter) Prompt(
	cfg browserapi.PromptConfig, h handler.PromptHandler,
) (browserapi.Window, error) {
	p.mu.Lock()
	p.shown = append(p.shown, cfg)
	idx := -1
	if len(p.answers) > 0 {
		idx, p.answers = p.answers[0], p.answers[1:]
	}
	p.mu.Unlock()
	if idx >= 0 {
		h.OnSelect(idx, cfg.Options[idx])
	}
	_ = h.OnClose()
	return nil, nil
}

func (p *answeringPrompter) prompts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.shown)
}

// workspaceServers declares servers the way a workspace .mcp.json does.
func workspaceServers(t *testing.T, raw map[string]any) map[string]runemcp.ServerConfig {
	t.Helper()
	servers, err := runemcp.ParseServers(raw)
	require.NoError(t, err)
	return runemcp.Merge(nil, servers)
}

func TestMCPTrustAllow(t *testing.T) {
	t.Parallel()

	root := mustURI(t, "file:///work/repo")
	remote := map[string]any{
		"type":    "http",
		"url":     "https://mcp.example.com/mcp",
		"headers": map[string]any{"Authorization": "Bearer ${DOCS_TOKEN}"},
	}
	changed := map[string]any{
		"type":    "http",
		"url":     "https://mcp.example.com/mcp",
		"headers": map[string]any{"Authorization": "Bearer ${OTHER_TOKEN}"},
	}
	local := map[string]any{
		"command": "npx",
		"args":    []any{"-y", "docs-server"},
		"env":     map[string]any{"DOCS_DIR": "./docs"},
	}
	otherArgs := map[string]any{
		"command": "npx",
		"args":    []any{"-y", "docs-server@evil"},
		"env":     map[string]any{"DOCS_DIR": "./docs"},
	}
	otherEnv := map[string]any{
		"command": "npx",
		"args":    []any{"-y", "docs-server"},
		"env":     map[string]any{"DOCS_DIR": "./docs", "NODE_OPTIONS": "--require ./x.js"},
	}
	tests := []struct {
		name string
		// configs are checked in order against the same trust store.
		configs []map[string]any
		user    bool
		answers []int
		want    []bool
		prompts int
	}{
		{
			name:    "trust is remembered",
			configs: []map[string]any{remote, remote},
			answers: []int{0},
			want:    []bool{true, true},
			prompts: 1,
		},
		{
			name:    "trust once lasts for the session",
			configs: []map[string]any{remote, remote},
			answers: []int{1},
			want:    []bool{true, true},
			prompts: 1,
		},
		{
			name:    "declining is not remembered",
			configs: []map[string]any{remote, remote},
			answers: []int{2, 0},
			want:    []bool{false, true},
			prompts: 2,
		},
		{
			name:    "dismissing declines",
			configs: []map[string]any{remote},
			answers: []int{-1},
			want:    []bool{false},
			prompts: 1,
		},
		{
			name:    "a changed config asks again",
			configs: []map[string]any{remote, changed},
			answers: []int{0, 2},
			want:    []bool{true, false},
			prompts: 2,
		},
		{
			name:    "stdio servers ask too",
			configs: []map[string]any{local, local},
			answers: []int{0},
			want:    []bool{true, true},
			prompts: 1,
		},
		{
			name:    "changed stdio arguments ask again",
			configs: []map[string]any{local, otherArgs},
			answers: []int{0, 2},
			want:    []bool{true, false},
			prompts: 2,
		},
		{
			name:    "a changed stdio environment asks again",
			configs: []map[string]any{local, otherEnv},
			answers: []int{0, 2},
			want:    []bool{true, false},
			prompts: 2,
		},
		{
			name:    "user servers skip the prompt",
			configs: []map[string]any{remote, local},
			user:    true,
			want:    []bool{true, true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			prompter := &answeringPrompter{answers: tt.answers}
			trust := newMCPTrust(storagestub.NewInMemoryService(), prompter)
			for i, raw := range tt.configs {
				servers, err := runemcp.ParseServers(map[string]any{"docs": raw})
				require.NoError(t, err)
				if tt.user {
					servers = runemcp.Merge(servers, nil)
				} else {
					servers = runemcp.Merge(nil, servers)
				}
				got, err := trust.Allow(context.Background(), root, "docs", servers["docs"],
					runemcp.Expansion{Referenced: []string{"DOCS_TOKEN"}})
				require.NoError(t, err)
				assert.Equal(t, tt.want[i], got, "check %d", i)
			}
			assert.Equal(t, tt.prompts, prompter.prompts())
		})
	}
}

func TestMCPTrustIsPersistedPerWorkspace(t *testing.T) {
	t.Parallel()

	storage := storagestub.NewInMemoryService()
	cfg := workspaceServers(t, map[string]any{
		"docs": map[string]any{"url": "https://mcp.example.com/mcp"},
	})["docs"]
	repo := mustURI(t, "file:///work/repo")
	other := mustURI(t, "file:///work/other")

	first := &answeringPrompter{answers: []int{0}}
	ok, err := newMCPTrust(storage, first).Allow(context.Background(), repo, "docs", cfg, runemcp.Expansion{})
	require.NoError(t, err)
	require.True(t, ok)

	// A later run reads the trust back from storage.
	restarted := &answeringPrompter{}
	trust := newMCPTrust(storage, restarted)
	ok, err = trust.Allow(context.Background(), repo, "docs", cfg, runemcp.Expansion{})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Zero(t, restarted.prompts())

	ok, err = trust.Allow(context.Background(), other, "docs", cfg, runemcp.Expansion{})
	require.NoError(t, err)
	assert.False(t, ok, "another workspace declaring the same server is asked separately")
	assert.Equal(t, 1, restarted.prompts())
}

func TestMCPTrustWithoutPrompterDeclines(t *testing.T) {
	t.Parallel()

	cfg := workspaceServers(t, map[string]any{
		"docs": map[string]any{"url": "https://mcp.example.com/mcp"},
	})["docs"]
	ok, err := newMCPTrust(storagestub.NewInMemoryService(), nil).Allow(
		context.Background(), mustURI(t, "file:///work/repo"), "docs", cfg, runemcp.Expansion{})
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestMCPTrustMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      map[string]any
		want     []string
		wantNone []string
	}{
		{
			name: "remote",
			raw: map[string]any{
				"url":           "https://mcp.example.com/v1/mcp",
				"headers":       map[string]any{"X-Team": "core", "Authorization": "Bearer ${DOCS_TOKEN}"},
				"headersHelper": "./scripts/mcp-headers.sh",
			},
			want: []string{
				"asks Rune to connect to the MCP server `docs` at:",
				`https\:\/\/**mcp\.example\.com**\/v1\/mcp`,
				"- Headers sent: `Authorization`, `X-Team`",
				"- Your environment variables it receives: `DOCS_TOKEN`",
				"```sh\n./scripts/mcp-headers.sh\n```",
			},
			wantNone: []string{"core"},
		},
		{
			name: "local",
			raw: map[string]any{
				"command": "npx",
				"args":    []any{"-y", "docs server", "--token=${DOCS_TOKEN}"},
				"env":     map[string]any{"DOCS_DIR": "./docs", "API_KEY": "s3cret"},
			},
			want: []string{
				"asks Rune to start the MCP server `docs` on the workspace host, in the workspace root, with:",
				"```sh\nnpx -y 'docs server' '--token=${DOCS_TOKEN}'\n```",
				"- Environment variables it sets: `API_KEY`, `DOCS_DIR`",
				"- Your environment variables it receives: `DOCS_TOKEN`",
			},
			wantNone: []string{"s3cret", "Headers sent"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := workspaceServers(t, map[string]any{"docs": tt.raw})["docs"]
			prompter := &answeringPrompter{answers: []int{2}}
			_, err := newMCPTrust(storagestub.NewInMemoryService(), prompter).Allow(
				context.Background(), mustURI(t, "file:///work/repo"), "docs", cfg,
				runemcp.Expansion{Referenced: []string{"DOCS_TOKEN"}})
			require.NoError(t, err)

			require.Len(t, prompter.shown, 1)
			msg := prompter.shown[0].Message
			for _, w := range tt.want {
				assert.Contains(t, msg, w)
			}
			for _, w := range tt.wantNone {
				assert.NotContains(t, msg, w, "values stay out of the prompt")
			}
			assert.Equal(t, []string{trustOpt, trustOnceOpt, distrustOpt}, prompter.shown[0].Options)
		})
	}
}

func TestMCPTrustAllowStopsWithContext(t *testing.T) {
	t.Parallel()

	cfg := workspaceServers(t, map[string]any{
		"docs": map[string]any{"url": "https://mcp.example.com/mcp"},
	})["docs"]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The prompter never answers, so only ctx can end the wait.
	_, err := newMCPTrust(storagestub.NewInMemoryService(), silentPrompter{}).Allow(
		ctx, mustURI(t, "file:///work/repo"), "docs", cfg, runemcp.Expansion{})
	assert.ErrorIs(t, err, context.Canceled)
}

type silentPrompter struct{}

func (silentPrompter) Prompt(browserapi.PromptConfig, handler.PromptHandler) (browserapi.Window, error) {
	return nil, nil
}
