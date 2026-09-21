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

package text

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
)

func TestDefaultConfigPkgManagerReturnsNotFound(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	it, err := cfg.PkgManager.LibDir(context.Background(), "go")
	require.Nil(t, it)
	require.ErrorIs(t, err, storageapi.ErrNotFound)
}

// TestGetSwapDir checks that the editor resolves a file's swap
// directory from Config.SwapDirectory, on the host that owns the file.
func TestGetSwapDir(t *testing.T) {
	t.Parallel()

	tsuite := []struct {
		name    string
		swapDir string
		file    string
		want    string
	}{{
		name: "the default keeps the swap next to the file",
		file: "file:///Users/x/src/proj/main.go",
		want: "file:///Users/x/src/proj",
	}, {
		name:    "a configured directory is used verbatim",
		swapDir: "/Users/x/.rune/swap",
		file:    "file:///Users/x/src/proj/main.go",
		want:    "file:///Users/x/.rune/swap",
	}, {
		name:    "a remote file keeps its swap on the remote host",
		swapDir: "~/.rune/swap",
		file:    "ssh://user@my_host/srv/main.go",
		want:    "ssh://user@my_host/~/.rune/swap",
	}}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			file, err := workspaceapi.ParseURI(tcase.file)
			require.NoError(t, err)

			c := &Component{config: Config{SwapDirectory: tcase.swapDir}}
			swapDir, err := c.getSwapDir(file)
			require.NoError(t, err)
			assert.Equal(t, tcase.want, swapDir.String())
			assert.Equal(t, file.Scheme(), swapDir.Scheme())
			assert.Equal(t, file.User(), swapDir.User())
		})
	}
}

func TestWithComments(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	assert.Empty(t, cfg.Comments)

	comments := CommentConfig{
		"go": {
			Line:  []string{"//"},
			Block: []CommentBlock{{Start: "/*", End: "*/"}},
		},
	}
	WithComments(comments)(&cfg)
	assert.Equal(t, comments, cfg.Comments)

	uri, err := workspaceapi.ParseURI("memory:///foo.go")
	require.NoError(t, err)
	spec, ok := CommentSpecForURI(uri, cfg.Comments)
	require.True(t, ok)
	assert.True(t, spec.HasLine())
	assert.True(t, spec.HasBlock())
	assert.Equal(t, []CommentBlock{{Start: "/*", End: "*/"}}, spec.Block)
}

func TestCommentConfigForLanguageNil(t *testing.T) {
	t.Parallel()

	var cc CommentConfig
	spec, ok := cc.ForLanguage("go")
	assert.False(t, ok)
	assert.Equal(t, CommentSpec{}, spec)
}

func TestCommentSpecForURIUnknownLanguage(t *testing.T) {
	t.Parallel()

	comments := CommentConfig{"go": {Line: []string{"//"}}}
	// A file without an extension cannot be resolved to a language ID.
	uri, err := workspaceapi.ParseURI("memory:///Makefilelike")
	require.NoError(t, err)
	spec, ok := CommentSpecForURI(uri, comments)
	assert.False(t, ok)
	assert.Equal(t, CommentSpec{}, spec)
}

func TestIndentConfigForURI(t *testing.T) {
	t.Parallel()

	indents := IndentConfig{
		"yaml": IndentRuneSpace,
		"go":   IndentRuneTab,
	}

	uri, err := workspaceapi.ParseURI("memory:///foo.yaml")
	require.NoError(t, err)
	r, n, ok := IndentConfigForURI(uri, nil, indents, 4)
	require.True(t, ok)
	assert.Equal(t, IndentRuneSpace, r)
	assert.Equal(t, 4, n)
}

func newBufferWithContent(t *testing.T, content string) *cell.Buffer {
	t.Helper()
	buf := new(cell.Buffer)
	buf.Init()
	buf.WriteString(content)
	return buf
}

func TestIndentConfigForURI_Detection(t *testing.T) {
	t.Parallel()

	knownURI, err := workspaceapi.ParseURI("memory:///foo.py")
	require.NoError(t, err)
	unknownURI, err := workspaceapi.ParseURI("memory:///Makefilelike")
	require.NoError(t, err)

	tabsCfg := IndentConfig{"python": IndentRuneTab}
	spacesCfg := IndentConfig{"python": IndentRuneSpace}
	emptyCfg := IndentConfig{}

	tests := []struct {
		name             string
		uri              workspaceapi.URI
		content          string
		useBuf           bool
		indents          IndentConfig
		defaultTabspaces int
		wantR            rune
		wantN            int
		wantOK           bool
	}{
		{
			name:             "unknown language with empty buffer",
			uri:              unknownURI,
			useBuf:           true,
			indents:          tabsCfg,
			defaultTabspaces: 4,
			wantN:            4,
			wantOK:           false,
		},
		{
			name:             "unknown language with tab-indented buffer",
			uri:              unknownURI,
			content:          "\tfoo\n",
			useBuf:           true,
			indents:          tabsCfg,
			defaultTabspaces: 4,
			wantN:            4,
			wantOK:           false,
		},
		{
			name:             "known language, no config, empty buffer",
			uri:              knownURI,
			useBuf:           true,
			indents:          emptyCfg,
			defaultTabspaces: 4,
			wantN:            4,
			wantOK:           false,
		},
		{
			name:             "known language, configured tabs, empty buffer",
			uri:              knownURI,
			useBuf:           true,
			indents:          tabsCfg,
			defaultTabspaces: 4,
			wantR:            IndentRuneTab,
			wantN:            4,
			wantOK:           true,
		},
		{
			name:             "known language, configured spaces, empty buffer",
			uri:              knownURI,
			useBuf:           true,
			indents:          spacesCfg,
			defaultTabspaces: 2,
			wantR:            IndentRuneSpace,
			wantN:            2,
			wantOK:           true,
		},
		{
			name:             "configured tabs but buffer uses spaces",
			uri:              knownURI,
			content:          "def foo():\n    return 1\n",
			useBuf:           true,
			indents:          tabsCfg,
			defaultTabspaces: 8,
			wantR:            IndentRuneSpace,
			wantN:            4,
			wantOK:           true,
		},
		{
			name:             "configured spaces but buffer uses tabs",
			uri:              knownURI,
			content:          "def foo():\n\treturn 1\n",
			useBuf:           true,
			indents:          spacesCfg,
			defaultTabspaces: 4,
			wantR:            IndentRuneTab,
			wantN:            4,
			wantOK:           true,
		},
		{
			name:             "configured tabs and buffer uses tabs",
			uri:              knownURI,
			content:          "def foo():\n\treturn 1\n",
			useBuf:           true,
			indents:          tabsCfg,
			defaultTabspaces: 4,
			wantR:            IndentRuneTab,
			wantN:            4,
			wantOK:           true,
		},
		{
			name:             "configured spaces and buffer uses spaces",
			uri:              knownURI,
			content:          "def foo():\n    return 1\n",
			useBuf:           true,
			indents:          spacesCfg,
			defaultTabspaces: 8,
			wantR:            IndentRuneSpace,
			wantN:            4,
			wantOK:           true,
		},
		{
			name:             "mixed indentation falls back to configured tabs",
			uri:              knownURI,
			content:          "def foo():\n\treturn 1\ndef bar():\n    return 2\n",
			useBuf:           true,
			indents:          tabsCfg,
			defaultTabspaces: 4,
			wantR:            IndentRuneTab,
			wantN:            4,
			wantOK:           true,
		},
		{
			name:             "no configured rune, detected spaces bridge the gap",
			uri:              knownURI,
			content:          "def foo():\n    return 1\n",
			useBuf:           true,
			indents:          emptyCfg,
			defaultTabspaces: 8,
			wantR:            IndentRuneSpace,
			wantN:            4,
			wantOK:           true,
		},
		{
			name:             "no configured rune, detected tabs bridge the gap",
			uri:              knownURI,
			content:          "def foo():\n\treturn 1\n",
			useBuf:           true,
			indents:          emptyCfg,
			defaultTabspaces: 4,
			wantR:            IndentRuneTab,
			wantN:            4,
			wantOK:           true,
		},
		{
			name:             "nil buffer falls back to configured rune",
			uri:              knownURI,
			useBuf:           false,
			indents:          spacesCfg,
			defaultTabspaces: 2,
			wantR:            IndentRuneSpace,
			wantN:            2,
			wantOK:           true,
		},
		{
			name:             "nil buffer with no config",
			uri:              knownURI,
			useBuf:           false,
			indents:          emptyCfg,
			defaultTabspaces: 4,
			wantN:            4,
			wantOK:           false,
		},
		{
			name:             "non-indented lines are inconclusive",
			uri:              knownURI,
			content:          "hello\nworld\n",
			useBuf:           true,
			indents:          tabsCfg,
			defaultTabspaces: 4,
			wantR:            IndentRuneTab,
			wantN:            4,
			wantOK:           true,
		},
		{
			name:             "single leading space is ignored",
			uri:              knownURI,
			content:          " a leading space line\nanother\n",
			useBuf:           true,
			indents:          tabsCfg,
			defaultTabspaces: 4,
			wantR:            IndentRuneTab,
			wantN:            4,
			wantOK:           true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf *cell.Buffer
			if tc.useBuf {
				buf = newBufferWithContent(t, tc.content)
			}
			r, n, ok := IndentConfigForURI(tc.uri, buf, tc.indents, tc.defaultTabspaces)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantN, n)
			if tc.wantOK {
				assert.Equal(t, tc.wantR, r)
			}
		})
	}
}

func TestDetectIndentConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		buf    *cell.Buffer
		wantR  rune
		wantN  int
		wantOK bool
	}{
		{
			name:   "nil buffer",
			buf:    nil,
			wantOK: false,
		},
		{
			name:   "empty buffer",
			buf:    newBufferWithContent(t, ""),
			wantOK: false,
		},
		{
			name:   "blank lines only",
			buf:    newBufferWithContent(t, "\n\n\n"),
			wantOK: false,
		},
		{
			name:   "non-indented content",
			buf:    newBufferWithContent(t, "hello\nworld\n"),
			wantOK: false,
		},
		{
			name:   "tabs only",
			buf:    newBufferWithContent(t, "def foo():\n\treturn 1\n\treturn 2\n"),
			wantR:  IndentRuneTab,
			wantOK: true,
		},
		{
			name:   "spaces only",
			buf:    newBufferWithContent(t, "def foo():\n    return 1\n    return 2\n"),
			wantR:  IndentRuneSpace,
			wantN:  4,
			wantOK: true,
		},
		{
			name:   "mixed tabs and spaces",
			buf:    newBufferWithContent(t, "def foo():\n\treturn 1\ndef bar():\n    return 2\n"),
			wantOK: false,
		},
		{
			name:   "single leading space is ignored",
			buf:    newBufferWithContent(t, " x\n"),
			wantOK: false,
		},
		{
			name:   "two or more leading spaces counts as spaces",
			buf:    newBufferWithContent(t, "  x\n"),
			wantR:  IndentRuneSpace,
			wantN:  2,
			wantOK: true,
		},
		{
			name:   "smallest leading-space run wins",
			buf:    newBufferWithContent(t, "  x\n    y\n      z\n"),
			wantR:  IndentRuneSpace,
			wantN:  2,
			wantOK: true,
		},
		{
			name:   "three-space indent detected",
			buf:    newBufferWithContent(t, "def foo():\n   return 1\n   return 2\n"),
			wantR:  IndentRuneSpace,
			wantN:  3,
			wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, n, ok := detectIndentConfig(tc.buf)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantR, r)
				assert.Equal(t, tc.wantN, n)
			}
		})
	}
}
