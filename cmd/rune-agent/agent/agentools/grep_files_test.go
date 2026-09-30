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

package agentools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/cmd/rune-agent/agent"
)

func TestGrepFiles(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		setup    func(t *testing.T, dir string)
		assertFn func(t *testing.T, result agent.ToolResult)
	}{
		{
			name: "regex match returns file paths only",
			args: `{"pattern": "hello"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "hello.txt")
				// Should return only file paths, not line content.
				assert.NotContains(t, result.Content, "hello world")
			},
		},
		{
			name: "directory scoping restricts search",
			args: `{"pattern": "Foo", "path": "sub"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "nested.go")
			},
		},
		{
			name: "glob filter restricts to matching files",
			args: `{"pattern": ".*", "include": "*.go"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "nested.go")
				assert.NotContains(t, result.Content, "hello.txt")
			},
		},
		{
			name: "no matches returns message",
			args: `{"pattern": "zzzznotfound"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Equal(t, "No matches found.", result.Content)
			},
		},
		{
			name: "empty pattern returns error",
			args: `{"pattern": ""}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "pattern must not be empty")
			},
		},
		{
			name: "invalid regex returns error",
			args: `{"pattern": "[invalid"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "invalid regex")
			},
		},
		{
			name: "invalid JSON returns error",
			args: `{broken`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "invalid arguments")
			},
		},
		{
			name: "skips .git directory",
			args: `{"pattern": "gitfile"}`,
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"),
					[]byte("gitfile content"), 0o644))
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.Equal(t, "No matches found.", result.Content)
			},
		},
		{
			name: "results sorted by modification time descending",
			args: `{"pattern": "content"}`,
			setup: func(t *testing.T, dir string) {
				old := filepath.Join(dir, "old.txt")
				require.NoError(t, os.WriteFile(old, []byte("content old"), 0o644))
				require.NoError(t, os.Chtimes(old, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour)))

				mid := filepath.Join(dir, "mid.txt")
				require.NoError(t, os.WriteFile(mid, []byte("content mid"), 0o644))
				require.NoError(t, os.Chtimes(mid, time.Now().Add(-1*time.Hour), time.Now().Add(-1*time.Hour)))

				recent := filepath.Join(dir, "recent.txt")
				require.NoError(t, os.WriteFile(recent, []byte("content recent"), 0o644))
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				lines := strings.Split(result.Content, "\n")
				var recentIdx, midIdx, oldIdx int
				for i, line := range lines {
					switch line {
					case "recent.txt":
						recentIdx = i
					case "mid.txt":
						midIdx = i
					case "old.txt":
						oldIdx = i
					}
				}
				assert.Less(t, recentIdx, midIdx, "recent.txt should appear before mid.txt")
				assert.Less(t, midIdx, oldIdx, "mid.txt should appear before old.txt")
			},
		},
		{
			name: "limit restricts number of results",
			args: `{"pattern": "line", "limit": 1}`,
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("line a"), 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("line b"), 0o644))
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				lines := strings.Split(result.Content, "\n")
				var filePaths []string
				for _, l := range lines {
					if l != "" && !strings.HasPrefix(l, "(") {
						filePaths = append(filePaths, l)
					}
				}
				assert.Equal(t, 1, len(filePaths))
				assert.Contains(t, result.Content, "(results truncated at 1 files)")
			},
		},
		{
			name: "each file listed only once",
			args: `{"pattern": "line"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				count := strings.Count(result.Content, "hello.txt")
				assert.Equal(t, 1, count)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupWorkspace(t)
			tool := NewGrepFiles(localFS{root: dir}, dirURI(dir), NewFileTracker(), nil, nil)

			if tt.setup != nil {
				tt.setup(t, dir)
			}

			result := tool.Execute(context.Background(), tt.args)
			tt.assertFn(t, result)
		})
	}
}

func TestGrepFiles_definition(t *testing.T) {
	tool := NewGrepFiles(localFS{}, dirURI("/workspace"), NewFileTracker(), nil, nil)
	def := tool.Definition()
	assert.Equal(t, "grep_files", def.Function.Name)
	assert.Equal(t, "Finds files whose contents match the pattern and lists them by modification time.",
		def.Function.Description)
}

func TestGrepFiles_tracksDiscovery(t *testing.T) {
	dir := setupWorkspace(t)
	tracker := NewFileTracker()
	tool := NewGrepFiles(localFS{root: dir}, dirURI(dir), tracker, nil, nil)

	ctx := agent.WithParentToolCallID(t.Context(), "grep_1")
	result := tool.Execute(ctx, `{"pattern":"hello"}`)
	require.False(t, result.IsError)

	// Reading the discovered file should consume the discovery.
	readTool := newReadFile(localFS{}, dirURI(dir), tracker, 0)
	readCtx := agent.WithParentToolCallID(t.Context(), "read_1")
	readResult := readTool.Execute(readCtx, `{"path":"hello.txt","offset":null,"limit":null}`)
	require.False(t, readResult.IsError)
	assert.Equal(t, []string{"grep_1"}, readResult.DropToolResultIDs)
}

func TestGrepFiles_summary(t *testing.T) {
	tool := NewGrepFiles(localFS{}, dirURI("/workspace"), NewFileTracker(), nil, nil)
	tests := []struct {
		name     string
		args     string
		expected string
	}{
		{"pattern only", `{"pattern":"TODO"}`, `"TODO"`},
		{"with path", `{"pattern":"TODO","path":"src"}`, `"TODO" in src`},
		{"invalid json", `bad`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tool.Summary(tt.args))
		})
	}
}

// TestGrepFiles_skipsBinaryFiles verifies that binary files are not
// reported even when their bytes happen to contain the search pattern.
// Regression for RUNE-179.
func TestGrepFiles_skipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	// Binary blob: has a NUL in the first 8 KiB and includes the
	// literal token "needle" embedded between the binary bytes.
	binBlob := []byte("\x7fELF\x02\x01\x01\x00\x00\x00needle\x00\x00\x00data\x01\x02")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bin.dat"), binBlob, 0o644))
	// Plain text file with the same token.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "text.txt"), []byte("a needle in a haystack\n"), 0o644))

	tool := NewGrepFiles(localFS{root: dir}, dirURI(dir), NewFileTracker(), nil, nil)
	result := tool.Execute(t.Context(), `{"pattern":"needle"}`)

	require.False(t, result.IsError)
	assert.Contains(t, result.Content, "text.txt")
	assert.NotContains(t, result.Content, "bin.dat")
}
