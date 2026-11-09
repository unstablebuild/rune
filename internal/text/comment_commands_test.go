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
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestSubscribeCommentCommands(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "comment.go")

	registry := newIndentTestWorkspaceRegistry()
	fileRegistry := NewFileCommandRegistry(cwd, registry)
	h := &indentTestHandler{uri: uri}
	c := setupCursorContent(t, 80, 10, "a\nb", false)
	attachCommentTestView(c, CommentSpec{
		Line:  []string{"//"},
		Block: []CommentBlock{{Start: "/*", End: "*/"}},
	})

	wrapped, err := SubscribeCommentCommands(uri, fileRegistry, c, h)
	require.NoError(t, err)
	commands := registry.sub[cwd.String()]
	require.Contains(t, commands, CommandToggleLineComment)
	require.Contains(t, commands, CommandToggleBlockComment)

	err = commands[CommandToggleLineComment].HandleCommand(context.Background(), textapi.Command{
		URI:  uri,
		Name: CommandToggleLineComment,
	})
	require.NoError(t, err)
	assert.Equal(t, "// a\nb", c.buffer().String())

	require.True(t, c.SelectRange(term.Coordinates{Y: 1, X: 0}, term.Coordinates{Y: 1, X: 1}))
	err = commands[CommandToggleBlockComment].HandleCommand(context.Background(), textapi.Command{
		URI:  uri,
		Name: CommandToggleBlockComment,
	})
	require.NoError(t, err)
	assert.Equal(t, "// a\n/*b*/", c.buffer().String())

	require.NoError(t, wrapped.Close())
	assert.NotContains(t, registry.sub[cwd.String()], CommandToggleLineComment)
	assert.NotContains(t, registry.sub[cwd.String()], CommandToggleBlockComment)
}

func TestCommentCommandsUndoInOneStep(t *testing.T) {
	spec := CommentSpec{Line: []string{"//"}, Block: []CommentBlock{{Start: "/*", End: "*/"}}}
	at := func(y, x int) term.Coordinates { return term.Coordinates{Y: y, X: x} }
	selectRange := func(from, to term.Coordinates) func(*testing.T, *Cursor) {
		return func(t *testing.T, c *Cursor) { require.True(t, c.SelectRange(from, to)) }
	}
	moveTo := func(pos term.Coordinates) func(*testing.T, *Cursor) {
		return func(t *testing.T, c *Cursor) { setCursorAtOrFail(t, c, pos) }
	}

	tests := []struct {
		name    string
		content string
		spec    CommentSpec
		arrange func(*testing.T, *Cursor)
		command string
		want    string
		wantErr bool
	}{
		{
			name: "line comment over a range", content: "a\nb\nc", spec: spec,
			arrange: selectRange(at(0, 0), at(2, 1)), command: CommandToggleLineComment,
			want: "// a\n// b\n// c",
		},
		{
			name: "line comment skips a line the range only reaches", content: "a\nb\nc", spec: spec,
			arrange: selectRange(at(0, 0), at(2, 0)), command: CommandToggleLineComment,
			want: "// a\n// b\nc",
		},
		{
			name: "line comment aligns tab-indented lines", content: "\t\ta\n\tb", spec: spec,
			arrange: selectRange(at(0, 0), at(1, 2)), command: CommandToggleLineComment,
			want: "\t// \ta\n\t// b",
		},
		{
			name: "line comment on the caret's line of emoji", content: "界\n👨‍👧‍👦x", spec: spec,
			arrange: moveTo(at(1, 1)), command: CommandToggleLineComment,
			want: "界\n// 👨‍👧‍👦x",
		},
		{
			name: "line uncomment", content: "// a\n\t// b", spec: spec,
			arrange: selectRange(at(0, 0), at(1, 5)), command: CommandToggleLineComment,
			want: "a\n\tb",
		},
		{
			name: "block comment across lines", content: "ab\ncd", spec: spec,
			arrange: selectRange(at(0, 1), at(1, 1)), command: CommandToggleBlockComment,
			want: "a/*b\nc*/d",
		},
		{
			name: "block comment around wide runes", content: "界界界", spec: spec,
			arrange: selectRange(at(0, 1), at(0, 2)), command: CommandToggleBlockComment,
			want: "界/*界*/界",
		},
		{
			name: "block uncomment", content: "x /*ab*/", spec: spec,
			arrange: moveTo(at(0, 4)), command: CommandToggleBlockComment,
			want: "x ab",
		},
		{
			name: "line comment on a blank line fails", content: "a\n \t\nb", spec: spec,
			arrange: moveTo(at(1, 0)), command: CommandToggleLineComment, wantErr: true,
		},
		{
			name: "line comment without a spec fails", content: "a", command: CommandToggleLineComment,
			wantErr: true,
		},
		{
			name: "block comment with neither a selection nor a block fails", content: "a", spec: spec,
			command: CommandToggleBlockComment, wantErr: true,
		},
		{
			name: "block comment in an empty buffer fails", content: "", spec: spec,
			command: CommandToggleBlockComment, wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cwd, err := workspaceapi.ParseURI("memory:///")
			require.NoError(t, err)
			uri := workspaceapi.Join(cwd, "comment.go")
			registry := newIndentTestWorkspaceRegistry()
			c := setupCursorContent(t, 80, 10, tc.content, false)
			attachCommentTestView(c, tc.spec)
			wrapped, err := SubscribeCommentCommands(uri, NewFileCommandRegistry(cwd, registry), c,
				&indentTestHandler{uri: uri})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, wrapped.Close()) })
			if tc.arrange != nil {
				tc.arrange(t, c)
			}

			err = registry.sub[cwd.String()][tc.command].HandleCommand(
				context.Background(), textapi.Command{URI: uri, Name: tc.command})
			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, tc.content, c.buffer().String())
				assert.False(t, c.Undo(), "a failed command leaves nothing to undo")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, c.buffer().String())

			require.True(t, c.Undo())
			assert.Equal(t, tc.content, c.buffer().String(), "one undo reverts the command")
			assert.False(t, c.Undo())
			require.True(t, c.Redo())
			assert.Equal(t, tc.want, c.buffer().String(), "one redo reapplies it")
		})
	}

	t.Run("repeated commands undo one command at a time", func(t *testing.T) {
		cwd, err := workspaceapi.ParseURI("memory:///")
		require.NoError(t, err)
		uri := workspaceapi.Join(cwd, "comment.go")
		registry := newIndentTestWorkspaceRegistry()
		c := setupCursorContent(t, 80, 10, "a\nb", false)
		attachCommentTestView(c, spec)
		wrapped, err := SubscribeCommentCommands(uri, NewFileCommandRegistry(cwd, registry), c,
			&indentTestHandler{uri: uri})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, wrapped.Close()) })
		run := func() {
			require.True(t, c.SelectRange(at(0, 0), at(1, c.buffer().Columns(1))))
			require.NoError(t, registry.sub[cwd.String()][CommandToggleLineComment].HandleCommand(
				context.Background(), textapi.Command{URI: uri, Name: CommandToggleLineComment}))
		}

		run()
		require.Equal(t, "// a\n// b", c.buffer().String())
		run()
		require.Equal(t, "a\nb", c.buffer().String())
		run()
		require.Equal(t, "// a\n// b", c.buffer().String())
		for _, want := range []string{"a\nb", "// a\n// b", "a\nb"} {
			require.True(t, c.Undo())
			assert.Equal(t, want, c.buffer().String())
		}
		assert.False(t, c.Undo())
	})
}

func TestCommentCommandLineWithoutSpecReturnsError(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "comment.go")

	registry := newIndentTestWorkspaceRegistry()
	fileRegistry := NewFileCommandRegistry(cwd, registry)
	h := &indentTestHandler{uri: uri}
	c := setupCursorContent(t, 80, 10, "a", false)

	wrapped, err := SubscribeCommentCommands(uri, fileRegistry, c, h)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, wrapped.Close()) })

	err = registry.sub[cwd.String()][CommandToggleLineComment].HandleCommand(
		context.Background(), textapi.Command{URI: uri, Name: CommandToggleLineComment})
	require.EqualError(t, err, "line comment not configured for this file")
}

func TestCommentCommandBlockWithoutSpecReturnsError(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "comment.go")

	registry := newIndentTestWorkspaceRegistry()
	fileRegistry := NewFileCommandRegistry(cwd, registry)
	h := &indentTestHandler{uri: uri}
	c := setupCursorContent(t, 80, 10, "a", false)

	wrapped, err := SubscribeCommentCommands(uri, fileRegistry, c, h)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, wrapped.Close()) })

	err = registry.sub[cwd.String()][CommandToggleBlockComment].HandleCommand(
		context.Background(), textapi.Command{URI: uri, Name: CommandToggleBlockComment})
	require.EqualError(t, err, "block comment not available at the current position")
}

func TestCommentCommandHandlerPointerLeaves(t *testing.T) {
	t.Parallel()
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "comment.go")
	wrap := func() Handler {
		fileRegistry := NewFileCommandRegistry(cwd, newIndentTestWorkspaceRegistry())
		ed := newPointerEditor(t, uri)
		h, err := SubscribeCommentCommands(uri, fileRegistry, ed.cursor, ed)
		require.NoError(t, err)
		return h
	}
	for _, size := range []struct{ width, height int }{{40, 10}, {1, 1}} {
		handlertest.RunPointerLeaves(t, wrap(), size.width, size.height)
	}
	requirePointerLeavesKeepState(t, wrap(), 40, 10,
		term.Coordinates{X: 3, Y: 2}, term.Coordinates{X: 12, Y: 4})
}
