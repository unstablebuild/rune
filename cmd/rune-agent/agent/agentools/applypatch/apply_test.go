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

package applypatch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func dirURI(dir string) workspaceapi.URI {
	u, _ := workspaceapi.ParseURI("file://" + dir)
	return u
}

// osFS implements workspaceapi.FileSystem for testing against a real directory.
type osFS struct{ root string }

func (f osFS) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(f.root, path)
}

func (f osFS) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + f.resolve(path))
}

func (f osFS) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(path, flag, mode)
}

func (f osFS) Remove(path string) error                     { return os.Remove(path) }
func (f osFS) Stat(path string) (os.FileInfo, error)        { return os.Stat(path) }
func (f osFS) ReadDir(name string) ([]os.DirEntry, error)   { return os.ReadDir(name) }
func (f osFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

func TestApply(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string)
		patch     Patch
		wantApply int
		wantErrs  int
		verify    func(t *testing.T, dir string)
	}{
		{
			name: "add new file",
			patch: Patch{Ops: []FileOp{
				{
					Type: OpAdd,
					Path: "hello.txt",
					Lines: []Line{
						{Kind: LineAdd, Content: "hello world"},
					},
				},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
				require.NoError(t, err)
				assert.Equal(t, "hello world", string(data))
			},
		},
		{
			name: "add nested file",
			patch: Patch{Ops: []FileOp{
				{
					Type: OpAdd,
					Path: "a/b/c.txt",
					Lines: []Line{
						{Kind: LineAdd, Content: "deep"},
					},
				},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "a", "b", "c.txt"))
				require.NoError(t, err)
				assert.Equal(t, "deep", string(data))
			},
		},
		{
			name: "add file under dollar directory keeps name literal",
			patch: Patch{Ops: []FileOp{
				{
					Type:  OpAdd,
					Path:  "routes/$RUNE_TEST_UNSET_VAR/index.tsx",
					Lines: []Line{{Kind: LineAdd, Content: "route"}},
				},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(
					filepath.Join(dir, "routes", "$RUNE_TEST_UNSET_VAR", "index.tsx"))
				require.NoError(t, err)
				assert.Equal(t, "route", string(data))
			},
		},
		{
			name: "add file already exists errors",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "exists.txt"), []byte("old"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type:  OpAdd,
					Path:  "exists.txt",
					Lines: []Line{{Kind: LineAdd, Content: "new"}},
				},
			}},
			wantApply: 0,
			wantErrs:  1,
		},
		{
			name: "add multi-line file",
			patch: Patch{Ops: []FileOp{
				{
					Type: OpAdd,
					Path: "multi.txt",
					Lines: []Line{
						{Kind: LineAdd, Content: "line1"},
						{Kind: LineAdd, Content: "line2"},
						{Kind: LineAdd, Content: "line3"},
					},
				},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "multi.txt"))
				require.NoError(t, err)
				assert.Equal(t, "line1\nline2\nline3", string(data))
			},
		},
		{
			name: "delete file",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "del.txt"), []byte("bye"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{Type: OpDelete, Path: "del.txt"},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				_, err := os.Stat(filepath.Join(dir, "del.txt"))
				assert.True(t, os.IsNotExist(err))
			},
		},
		{
			name: "delete nonexistent errors",
			patch: Patch{Ops: []FileOp{
				{Type: OpDelete, Path: "nope.txt"},
			}},
			wantApply: 0,
			wantErrs:  1,
		},
		{
			name: "single hunk update exact match",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
					[]byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type: OpUpdate,
					Path: "main.go",
					Hunks: []Hunk{
						{
							Lines: []Line{
								{Kind: LineContext, Content: "func main() {"},
								{Kind: LineRemove, Content: "\tfmt.Println(\"hello\")"},
								{Kind: LineAdd, Content: "\tfmt.Println(\"world\")"},
								{Kind: LineContext, Content: "}"},
							},
						},
					},
				},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "main.go"))
				require.NoError(t, err)
				assert.Contains(t, string(data), "world")
				assert.NotContains(t, string(data), "hello")
			},
		},
		{
			name: "single hunk fuzzy match trailing whitespace",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "ws.txt"),
					[]byte("alpha  \nbeta\t\ngamma\n"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type: OpUpdate,
					Path: "ws.txt",
					Hunks: []Hunk{
						{
							Lines: []Line{
								{Kind: LineContext, Content: "alpha"},
								{Kind: LineRemove, Content: "beta"},
								{Kind: LineAdd, Content: "BETA"},
							},
						},
					},
				},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "ws.txt"))
				require.NoError(t, err)
				assert.Contains(t, string(data), "BETA")
			},
		},
		{
			name: "multi hunk update",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "multi.go"),
					[]byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type: OpUpdate,
					Path: "multi.go",
					Hunks: []Hunk{
						{
							Lines: []Line{
								{Kind: LineRemove, Content: "import \"fmt\""},
								{Kind: LineAdd, Content: "import \"log\""},
							},
						},
						{
							Lines: []Line{
								{Kind: LineContext, Content: "func main() {"},
								{Kind: LineRemove, Content: "\tfmt.Println(\"hi\")"},
								{Kind: LineAdd, Content: "\tlog.Println(\"hi\")"},
								{Kind: LineContext, Content: "}"},
							},
						},
					},
				},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "multi.go"))
				require.NoError(t, err)
				s := string(data)
				assert.Contains(t, s, "import \"log\"")
				assert.Contains(t, s, "log.Println")
				assert.NotContains(t, s, "fmt")
			},
		},
		{
			name: "hunk not found errors",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "nope.go"),
					[]byte("package main\n"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type: OpUpdate,
					Path: "nope.go",
					Hunks: []Hunk{
						{
							Lines: []Line{
								{Kind: LineContext, Content: "does not exist"},
								{Kind: LineRemove, Content: "nope"},
							},
						},
					},
				},
			}},
			wantApply: 0,
			wantErrs:  1,
			verify:    func(t *testing.T, dir string) {},
		},
		{
			name: "hunk not found error includes divergence details",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "diverge.go"),
					[]byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type: OpUpdate,
					Path: "diverge.go",
					Hunks: []Hunk{
						{
							Lines: []Line{
								{Kind: LineContext, Content: "func main() {"},
								{Kind: LineRemove, Content: "\tlog.Println(\"hello\")"},
								{Kind: LineContext, Content: "}"},
							},
						},
					},
				},
			}},
			wantApply: 0,
			wantErrs:  1,
			verify:    func(t *testing.T, dir string) {},
		},
		{
			name: "hunk not found error includes context hint",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "hint.go"),
					[]byte("package main\n\nfunc main() {\n}\n"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type: OpUpdate,
					Path: "hint.go",
					Hunks: []Hunk{
						{
							ContextHint: "func helper",
							Lines: []Line{
								{Kind: LineContext, Content: "func helper() {"},
								{Kind: LineRemove, Content: "\treturn nil"},
								{Kind: LineContext, Content: "}"},
							},
						},
					},
				},
			}},
			wantApply: 0,
			wantErrs:  1,
			verify:    func(t *testing.T, dir string) {},
		},
		{
			name: "update with move to",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "old.go"),
					[]byte("package old\n// comment\n"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type:   OpUpdate,
					Path:   "old.go",
					MoveTo: "new.go",
					Hunks: []Hunk{
						{
							Lines: []Line{
								{Kind: LineRemove, Content: "package old"},
								{Kind: LineAdd, Content: "package new"},
							},
						},
					},
				},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				_, err := os.Stat(filepath.Join(dir, "old.go"))
				assert.True(t, os.IsNotExist(err))

				data, err := os.ReadFile(filepath.Join(dir, "new.go"))
				require.NoError(t, err)
				assert.Contains(t, string(data), "package new")
			},
		},
		{
			name:      "empty patch",
			patch:     Patch{},
			wantApply: 0,
		},
		{
			name: "mixed patch",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "keep.txt"),
					[]byte("line one\nline two\nline three\n"), 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "remove.txt"),
					[]byte("goodbye"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type:  OpAdd,
					Path:  "created.txt",
					Lines: []Line{{Kind: LineAdd, Content: "fresh"}},
				},
				{Type: OpDelete, Path: "remove.txt"},
				{
					Type: OpUpdate,
					Path: "keep.txt",
					Hunks: []Hunk{
						{
							Lines: []Line{
								{Kind: LineContext, Content: "line one"},
								{Kind: LineRemove, Content: "line two"},
								{Kind: LineAdd, Content: "line TWO"},
							},
						},
					},
				},
			}},
			wantApply: 3,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "created.txt"))
				require.NoError(t, err)
				assert.Equal(t, "fresh", string(data))

				_, err = os.Stat(filepath.Join(dir, "remove.txt"))
				assert.True(t, os.IsNotExist(err))

				data, err = os.ReadFile(filepath.Join(dir, "keep.txt"))
				require.NoError(t, err)
				assert.Contains(t, string(data), "line TWO")
			},
		},
		{
			name: "add empty file writes empty content",
			patch: Patch{Ops: []FileOp{
				{Type: OpAdd, Path: "empty.txt"},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "empty.txt"))
				require.NoError(t, err)
				assert.Equal(t, "", string(data))
			},
		},
		{
			name: "update preserves trailing newline",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "nl.txt"),
					[]byte("alpha\nbeta\n"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type: OpUpdate,
					Path: "nl.txt",
					Hunks: []Hunk{
						{
							Lines: []Line{
								{Kind: LineRemove, Content: "alpha"},
								{Kind: LineAdd, Content: "ALPHA"},
							},
						},
					},
				},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "nl.txt"))
				require.NoError(t, err)
				assert.Equal(t, "ALPHA\nbeta\n", string(data))
			},
		},
		{
			name: "update without trailing newline preserved",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "nonl.txt"),
					[]byte("alpha\nbeta"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type: OpUpdate,
					Path: "nonl.txt",
					Hunks: []Hunk{
						{
							Lines: []Line{
								{Kind: LineContext, Content: "alpha"},
								{Kind: LineRemove, Content: "beta"},
								{Kind: LineAdd, Content: "BETA"},
							},
						},
					},
				},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "nonl.txt"))
				require.NoError(t, err)
				assert.Equal(t, "alpha\nBETA", string(data))
			},
		},
		{
			name: "move with no hunks relocates verbatim",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "src.txt"),
					[]byte("unchanged body\n"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{Type: OpUpdate, Path: "src.txt", MoveTo: "moved/dst.txt"},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				_, err := os.Stat(filepath.Join(dir, "src.txt"))
				assert.True(t, os.IsNotExist(err))
				data, err := os.ReadFile(filepath.Join(dir, "moved", "dst.txt"))
				require.NoError(t, err)
				assert.Equal(t, "unchanged body\n", string(data))
			},
		},
		{
			name: "update nonexistent file errors",
			patch: Patch{Ops: []FileOp{
				{
					Type: OpUpdate,
					Path: "ghost.txt",
					Hunks: []Hunk{
						{Lines: []Line{{Kind: LineRemove, Content: "x"}}},
					},
				},
			}},
			wantApply: 0,
			wantErrs:  1,
		},
		{
			name: "second op fails first still applied",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "present.txt"),
					[]byte("keep me\n"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type:  OpAdd,
					Path:  "good.txt",
					Lines: []Line{{Kind: LineAdd, Content: "ok"}},
				},
				{Type: OpDelete, Path: "missing.txt"},
			}},
			wantApply: 1,
			wantErrs:  1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "good.txt"))
				require.NoError(t, err)
				assert.Equal(t, "ok", string(data))
			},
		},
		{
			name: "fuzzy match leading and trailing whitespace",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "indent.txt"),
					[]byte("\t  alpha  \n  beta\n"), 0o644))
			},
			patch: Patch{Ops: []FileOp{
				{
					Type: OpUpdate,
					Path: "indent.txt",
					Hunks: []Hunk{
						{
							Lines: []Line{
								{Kind: LineContext, Content: "alpha"},
								{Kind: LineRemove, Content: "beta"},
								{Kind: LineAdd, Content: "BETA"},
							},
						},
					},
				},
			}},
			wantApply: 1,
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "indent.txt"))
				require.NoError(t, err)
				assert.Contains(t, string(data), "BETA")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			fs := osFS{root: dir}

			if tt.setup != nil {
				tt.setup(t, dir)
			}

			result := Apply(fs, dirURI(dir), tt.patch)
			assert.Equal(t, tt.wantApply, result.Applied)
			assert.Equal(t, tt.wantErrs, len(result.Errors), "errors: %v", result.Errors)

			if tt.verify != nil {
				tt.verify(t, dir)
			}
		})
	}
}

func TestSplitJoinRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"single line no newline", "hello"},
		{"single line with newline", "hello\n"},
		{"multi lines", "a\nb\nc\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := splitLines(tt.input)
			assert.Equal(t, tt.input, joinLines(lines))
		})
	}
}

func TestHunkMatchError(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		hunks    []Hunk
		wantMsgs []string
	}{
		{
			name: "partial match shows expected vs got",
			file: "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n",
			hunks: []Hunk{
				{
					Lines: []Line{
						{Kind: LineContext, Content: "func main() {"},
						{Kind: LineRemove, Content: "\tlog.Println(\"hello\")"},
						{Kind: LineContext, Content: "}"},
					},
				},
			},
			wantMsgs: []string{
				"hunk 1: no match found",
				"best partial match",
				"1/3 lines matched",
				"expected",
				"got",
			},
		},
		{
			name: "no match at all shows first expected line",
			file: "package main\n",
			hunks: []Hunk{
				{
					Lines: []Line{
						{Kind: LineContext, Content: "does not exist anywhere"},
						{Kind: LineRemove, Content: "nope"},
					},
				},
			},
			wantMsgs: []string{
				"hunk 1: no match found",
				"could not match any context lines",
				"first expected line",
			},
		},
		{
			name: "context hint is included",
			file: "package main\n\nfunc main() {\n}\n",
			hunks: []Hunk{
				{
					ContextHint: "func helper",
					Lines: []Line{
						{Kind: LineContext, Content: "func helper() {"},
						{Kind: LineRemove, Content: "\treturn nil"},
						{Kind: LineContext, Content: "}"},
					},
				},
			},
			wantMsgs: []string{
				"hunk 1: no match found",
				"near \"func helper\"",
			},
		},
		{
			name: "past eof indicated",
			file: "alpha\nbeta",
			hunks: []Hunk{
				{
					Lines: []Line{
						{Kind: LineContext, Content: "alpha"},
						{Kind: LineContext, Content: "beta"},
						{Kind: LineRemove, Content: "gamma"},
					},
				},
			},
			wantMsgs: []string{
				"hunk 1: no match found",
				"2/3 lines matched",
				"reached end of file",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			fs := osFS{root: dir}

			require.NoError(t, os.WriteFile(filepath.Join(dir, "test.go"),
				[]byte(tt.file), 0o644))

			result := Apply(fs, dirURI(dir), Patch{Ops: []FileOp{
				{Type: OpUpdate, Path: "test.go", Hunks: tt.hunks},
			}})
			require.Equal(t, 1, len(result.Errors), "expected exactly 1 error")
			for _, msg := range tt.wantMsgs {
				assert.Contains(t, result.Errors[0], msg)
			}
		})
	}
}
