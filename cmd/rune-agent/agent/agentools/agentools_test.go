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
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace/walkdir"
)

// localFS implements workspaceapi.FileSystem using local OS calls for testing.
// When root is set, relative paths are resolved against it; otherwise they are
// resolved against the process working directory (via filepath.Abs).
type localFS struct {
	root string
}

func (f localFS) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if f.root != "" {
		return filepath.Join(f.root, path)
	}
	abs, _ := filepath.Abs(path)
	return abs
}

func (f localFS) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + f.resolve(path))
}

func (f localFS) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(f.resolve(path), flag, mode)
}

func (f localFS) Remove(path string) error {
	return os.Remove(f.resolve(path))
}

func (f localFS) Stat(path string) (os.FileInfo, error) {
	return os.Stat(f.resolve(path))
}

func (f localFS) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(f.resolve(name))
}

func (f localFS) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(f.resolve(path), perm)
}

// localExec implements workspaceapi.Executor using os/exec for testing.
type localExec struct{}

func (localExec) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Dir = cmd.Dir
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	c.Env = cmd.Env

	if err := c.Start(); err != nil {
		return 0, err
	}

	pid := workspaceapi.Pid(c.Process.Pid)

	// Send result to watcher when process exits.
	go func() {
		err := c.Wait()
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- err
		}
	}()

	return pid, nil
}

func (localExec) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return err
	}
	return proc.Signal(sig)
}

func (localExec) Close() error { return nil }

// recordingExec captures the Cmd passed to Start for inspection in tests.
type recordingExec struct {
	startFn func(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error)
}

func (r *recordingExec) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return r.startFn(ctx, cmd)
}

func (r *recordingExec) Signal(_ workspaceapi.Pid, _ syscall.Signal) error { return nil }
func (r *recordingExec) Close() error                                      { return nil }

func dirURI(dir string) workspaceapi.URI {
	u, _ := workspaceapi.ParseURI("file://" + dir)
	return u
}

func TestDefaultTools(t *testing.T) {
	tools, tracker := DefaultTools(localFS{}, localExec{}, dirURI("/workspace"), nil, Config{}, configedit.NopConfig())
	require.Len(t, tools, 6)
	require.NotNil(t, tracker)

	expectedNames := map[string]bool{
		"read_file":      false,
		"apply_patch":    false,
		"search_content": false,
		"find_files":     false,
		"bash":           false,
		"compact":        false,
	}
	for _, tool := range tools {
		def := tool.Definition()
		assert.Equal(t, llmapi.ToolTypeFunction, def.Type)
		name := def.Function.Name
		_, ok := expectedNames[name]
		assert.True(t, ok, "unexpected tool name: %s", name)
		expectedNames[name] = true
	}
	for name, found := range expectedNames {
		assert.True(t, found, "tool %q not returned by DefaultTools", name)
	}
}

func TestSessionTools(t *testing.T) {
	spawner := &mockSpawner{}
	tools := SessionTools(spawner, nil, nil, nil)
	require.Len(t, tools, 1)

	def := tools[0].Definition()
	assert.Equal(t, llmapi.ToolTypeFunction, def.Type)
	assert.Equal(t, "agent", def.Function.Name)
}

func TestDefinitions(t *testing.T) {
	dir := t.TempDir()
	fs := localFS{}
	ex := localExec{}
	tests := []struct {
		name     string
		tool     agent.Tool
		wantName string
	}{
		{"read_file", newReadFile(fs, dirURI(dir), NewFileTracker(), 0), "read_file"},
		{"apply_patch", newApplyPatch(fs, dirURI(dir), NewFileTracker(), &stubLSP{}), "apply_patch"},
		{"search_content", newSearch(fs, dirURI(dir), NewFileTracker(), nil), "search_content"},
		{"find_files", newFindFiles(fs, dirURI(dir), NewFileTracker(), nil), "find_files"},
		{"list_dir", NewListDir(fs, dirURI(dir)), "list_dir"},
		{"bash", newBash(ex, dirURI(dir), configedit.NopConfig()), "bash"},
		{"compact", newCompact(), "compact"},
		{"exec_command", NewExecCommand(NewSessionManager(context.Background(), ex, nil), dirURI(dir), configedit.NopConfig()), "exec_command"},
		{"write_stdin", NewWriteStdin(NewSessionManager(context.Background(), ex, nil)), "write_stdin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := tt.tool.Definition()
			assert.Equal(t, llmapi.ToolTypeFunction, def.Type)
			assert.Equal(t, tt.wantName, def.Function.Name)
			assert.NotEmpty(t, def.Function.Description)
			assert.NotNil(t, def.Function.Parameters)
		})
	}
}

func TestToolDescriptionsCrossReferenceSemanticSkills(t *testing.T) {
	dir := t.TempDir()
	fs := localFS{}

	tests := []struct {
		name     string
		tool     agent.Tool
		contains []string
	}{
		{
			"read_file mentions outline_file and describe_symbol tools",
			newReadFile(fs, dirURI(dir), NewFileTracker(), 0),
			[]string{"outline_file tool", "describe_symbol tool"},
		},
		{
			"search_content mentions search_symbols and find_definition tools",
			newSearch(fs, dirURI(dir), NewFileTracker(), nil),
			[]string{"search_symbols", "find_definition"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc := tt.tool.Definition().Function.Description
			for _, want := range tt.contains {
				assert.Contains(t, desc, want)
			}
		})
	}
}

func TestSummary(t *testing.T) {
	dir := t.TempDir()
	fs := localFS{}
	ex := localExec{}
	spawner := &mockSpawner{}

	tests := []struct {
		name     string
		tool     agent.Tool
		args     string
		expected string
	}{
		// read_file
		{"read_file path only", newReadFile(fs, dirURI(dir), NewFileTracker(), 0), `{"path":"src/main.go"}`, "src/main.go"},
		{"read_file with offset and limit", newReadFile(fs, dirURI(dir), NewFileTracker(), 0), `{"path":"src/main.go","offset":10,"limit":20}`, "src/main.go:10-29"},
		{"read_file with offset no limit", newReadFile(fs, dirURI(dir), NewFileTracker(), 0), `{"path":"src/main.go","offset":10}`, "src/main.go:10-"},
		{"read_file invalid json", newReadFile(fs, dirURI(dir), NewFileTracker(), 0), `bad`, ""},
		// apply_patch
		{"apply_patch single file", newApplyPatch(fs, dirURI(dir), NewFileTracker(), &stubLSP{}), `{"patch":"*** Begin Patch\n*** Update File: main.go\n@@ \n-old\n+new\n*** End Patch"}`, "main.go"},
		{"apply_patch multi file", newApplyPatch(fs, dirURI(dir), NewFileTracker(), &stubLSP{}), `{"patch":"*** Begin Patch\n*** Add File: a.go\n+pkg\n*** Delete File: b.go\n*** End Patch"}`, "a.go, b.go"},
		{"apply_patch invalid json", newApplyPatch(fs, dirURI(dir), NewFileTracker(), &stubLSP{}), `bad`, ""},
		// search_content
		{"search pattern only", newSearch(fs, dirURI(dir), NewFileTracker(), nil), `{"pattern":"TODO"}`, `"TODO"`},
		{"search with path", newSearch(fs, dirURI(dir), NewFileTracker(), nil), `{"pattern":"TODO","path":"src"}`, `"TODO" in src`},
		{"search invalid json", newSearch(fs, dirURI(dir), NewFileTracker(), nil), `bad`, ""},
		// find_files
		{"find pattern only", newFindFiles(fs, dirURI(dir), NewFileTracker(), nil), `{"pattern":"*.go"}`, `"*.go"`},
		{"find with path", newFindFiles(fs, dirURI(dir), NewFileTracker(), nil), `{"pattern":"*.go","path":"lib"}`, `"*.go" in lib`},
		{"find invalid json", newFindFiles(fs, dirURI(dir), NewFileTracker(), nil), `bad`, ""},
		// bash — Summary always returns the command (not description).
		{"bash with description returns command", newBash(ex, dirURI(dir), configedit.NopConfig()), `{"command":"go test ./...","description":"Run all tests"}`, "go test ./..."},
		{"bash empty description returns command", newBash(ex, dirURI(dir), configedit.NopConfig()), `{"command":"go test ./...","description":""}`, "go test ./..."},
		{"bash no description returns command", newBash(ex, dirURI(dir), configedit.NopConfig()), `{"command":"go test ./..."}`, "go test ./..."},
		{"bash invalid json", newBash(ex, dirURI(dir), configedit.NopConfig()), `bad`, ""},
		// web_fetch
		{"web_fetch", NewWebFetch(&stubFetcher{}), `{"url":"https://example.com"}`, "https://example.com"},
		{"web_fetch invalid json", NewWebFetch(&stubFetcher{}), `bad`, ""},
		// agent
		{"agent with description", NewAgentTool(spawner, nil, nil, nil), `{"description":"search code","prompt":"find tests"}`, "search code"},
		{"agent without description short prompt", NewAgentTool(spawner, nil, nil, nil), `{"prompt":"short task"}`, "short task"},
		{"agent without description long prompt", NewAgentTool(spawner, nil, nil, nil), `{"prompt":"` + strings.Repeat("a", 80) + `"}`, strings.Repeat("a", 60) + "..."},
		{"agent invalid json", NewAgentTool(spawner, nil, nil, nil), `bad`, ""},
		// list_dir
		// The workspace root is synthetic: summaryPath collapses the
		// "../" chain, so a t.TempDir() root would make the expected
		// output depend on how deep the host's temp dir sits.
		{"list_dir", NewListDir(fs, dirURI("/ws/root/dir")), `{"dir_path":"/tmp/project"}`, ".../tmp/project"},
		{"list_dir invalid json", NewListDir(fs, dirURI(dir)), `bad`, ""},
		// exec_command
		{"exec_command", NewExecCommand(NewSessionManager(context.Background(), ex, nil), dirURI(dir), configedit.NopConfig()), `{"cmd":"echo hello"}`, "echo hello"},
		{"exec_command invalid json", NewExecCommand(NewSessionManager(context.Background(), ex, nil), dirURI(dir), configedit.NopConfig()), `bad`, ""},
		// write_stdin
		{"write_stdin with chars", NewWriteStdin(NewSessionManager(context.Background(), ex, nil)), `{"session_id":1000,"chars":"hello\n"}`, "session 1000: hello\n"},
		{"write_stdin poll", NewWriteStdin(NewSessionManager(context.Background(), ex, nil)), `{"session_id":1000,"chars":""}`, "poll session 1000"},
		{"write_stdin invalid json", NewWriteStdin(NewSessionManager(context.Background(), ex, nil)), `bad`, ""},
		// request_skill
		{"request_skill", NewRequestSkill(&mockPrompter{}), `{"skill":"web_search","description":"Search the web"}`, "web_search"},
		{"request_skill invalid json", NewRequestSkill(&mockPrompter{}), `bad`, ""},
		// compact
		{"compact", newCompact(), `{}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.tool.Summary(tt.args))
		})
	}
}

func TestResolvePath(t *testing.T) {
	tests := []struct {
		name     string
		root     string
		path     string
		wantPath string
	}{
		{
			name:     "relative path is joined with root",
			root:     "/workspace",
			path:     "src/main.go",
			wantPath: "/workspace/src/main.go",
		},
		{
			name:     "absolute path is returned as-is",
			root:     "/workspace",
			path:     "/tmp/file.txt",
			wantPath: "/tmp/file.txt",
		},
		{
			name:     "dot path resolves to root",
			root:     "/workspace",
			path:     ".",
			wantPath: "/workspace",
		},
		{
			name:     "absolute path with spaces is returned as-is",
			root:     "/workspace",
			path:     "/tmp/file with spaces.txt",
			wantPath: "/tmp/file with spaces.txt",
		},
		{
			name:     "relative path with spaces is joined with root",
			root:     "/workspace root",
			path:     "sub dir/file with spaces.txt",
			wantPath: "/workspace root/sub dir/file with spaces.txt",
		},
		{
			name:     "dollar in name is literal",
			root:     "/workspace",
			path:     "routes/$RUNE_TEST_UNSET_VAR/index.tsx",
			wantPath: "/workspace/routes/$RUNE_TEST_UNSET_VAR/index.tsx",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePath(dirURI(tt.root), tt.path)
			assert.Equal(t, tt.wantPath, got)
		})
	}
}

func TestReadFile(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		setup    func(t *testing.T, dir string)
		assertFn func(t *testing.T, result agent.ToolResult)
	}{
		{
			name: "happy path reads full file with line numbers",
			args: `{"path": "hello.txt"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "L1: hello world")
				assert.Contains(t, result.Content, "L2: second line")
				assert.Contains(t, result.Content, "L3: third line")
			},
		},
		{
			name: "file not found returns error",
			args: `{"path": "nonexistent.txt"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "error:")
			},
		},
		{
			name: "offset and limit select line range",
			args: `{"path": "hello.txt", "offset": 2, "limit": 1}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "L2: second line")
				assert.NotContains(t, result.Content, "L1: hello world")
				assert.NotContains(t, result.Content, "L3: third line")
			},
		},
		{
			name: "offset beyond file length returns empty content",
			args: `{"path": "hello.txt", "offset": 999}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Empty(t, strings.TrimSpace(result.Content))
			},
		},
		{
			name: "invalid JSON returns error",
			args: `not json`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "invalid arguments")
			},
		},
		{
			name: "absolute path reads file directly",
			args: "", // set in setup
			setup: func(t *testing.T, dir string) {
				// create file and set args with absolute path
				absPath := filepath.Join(dir, "abs_test.txt")
				require.NoError(t, os.WriteFile(absPath, []byte("absolute content"), 0o644))
				t.Setenv("ABS_PATH", absPath)
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "absolute content")
			},
		},
		{
			name: "absolute path with spaces reads file directly",
			args: "", // set in setup
			setup: func(t *testing.T, dir string) {
				absPath := filepath.Join(dir, "file with spaces.txt")
				require.NoError(t, os.WriteFile(absPath, []byte("content with spaces path"), 0o644))
				t.Setenv("ABS_PATH_WITH_SPACES", absPath)
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "content with spaces path")
			},
		},
		{
			name: "relative path with spaces reads file",
			args: `{"path": "sub dir/file with spaces.txt"}`,
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub dir"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "sub dir", "file with spaces.txt"), []byte("spaced relative path"), 0o644))
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "spaced relative path")
			},
		},
		{
			name: "nested file via relative path",
			args: `{"path": "sub/nested.go"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "package sub")
				assert.Contains(t, result.Content, "func Foo()")
			},
		},
		{
			name: "empty file returns empty content",
			args: `{"path": "empty.txt"}`,
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.txt"), []byte(""), 0o644))
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupWorkspace(t)
			tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)

			if tt.setup != nil {
				tt.setup(t, dir)
			}

			args := tt.args
			// Special case for absolute path test
			if tt.name == "absolute path reads file directly" {
				absPath := os.Getenv("ABS_PATH")
				args = `{"path": "` + absPath + `"}`
			}
			if tt.name == "absolute path with spaces reads file directly" {
				absPath := os.Getenv("ABS_PATH_WITH_SPACES")
				args = `{"path": "` + absPath + `"}`
			}

			result := tool.Execute(context.Background(), args)
			tt.assertFn(t, result)
		})
	}
}

func TestImageMediaType(t *testing.T) {
	tests := []struct {
		path     string
		wantMIME string
		wantOK   bool
	}{
		{"photo.png", "image/png", true},
		{"photo.jpg", "image/jpeg", true},
		{"photo.jpeg", "image/jpeg", true},
		{"anim.gif", "image/gif", true},
		{"photo.webp", "image/webp", true},
		{"icon.svg", "", false},
		{"readme.txt", "", false},
		{"PHOTO.PNG", "image/png", true}, // case-insensitive via strings.ToLower
		{"noext", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			mime, ok := imageMediaType(tt.path)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantMIME, mime)
		})
	}
}

func TestReadFile_image(t *testing.T) {
	// Create a minimal valid PNG (1x1 pixel).
	makePNG := func(t *testing.T) []byte {
		t.Helper()
		img := image.NewRGBA(image.Rect(0, 0, 1, 1))
		img.Set(0, 0, color.RGBA{R: 255, A: 255})
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		return buf.Bytes()
	}

	t.Run("image returns MultiContent", func(t *testing.T) {
		dir := t.TempDir()
		pngData := makePNG(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "test.png"), pngData, 0o644))

		tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
		result := tool.Execute(context.Background(), `{"path":"test.png"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "Read image file: test.png")
		assert.Contains(t, result.Content, "image/png")
		require.Len(t, result.MultiContent, 2)
		assert.Equal(t, llmapi.ContentPartTypeText, result.MultiContent[0].Type)
		assert.Equal(t, llmapi.ContentPartTypeImageURL, result.MultiContent[1].Type)
		assert.True(t, strings.HasPrefix(result.MultiContent[1].ImageURL, "data:image/png;base64,"))
	})

	t.Run("image too large", func(t *testing.T) {
		dir := t.TempDir()
		// Write a file with a .png extension that exceeds maxImageBytes.
		bigData := make([]byte, maxImageBytes+1)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "big.png"), bigData, 0o644))

		tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
		result := tool.Execute(context.Background(), `{"path":"big.png"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "too large")
	})

	t.Run("dimensions too large", func(t *testing.T) {
		dir := t.TempDir()
		img := image.NewRGBA(image.Rect(0, 0, 3000, 1000))
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "tall.png"), buf.Bytes(), 0o644))

		tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
		result := tool.Execute(context.Background(), `{"path":"tall.png"}`)

		assert.True(t, result.IsError)
		assert.Nil(t, result.MultiContent)
		assert.Contains(t, result.Content, "3000x1000")
		assert.Contains(t, result.Content, "2000px")
		assert.Contains(t, result.Content, "Resize")
	})

	t.Run("within dimension limit", func(t *testing.T) {
		dir := t.TempDir()
		img := image.NewRGBA(image.Rect(0, 0, 2000, 2000))
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cap.png"), buf.Bytes(), 0o644))

		tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
		result := tool.Execute(context.Background(), `{"path":"cap.png"}`)

		assert.False(t, result.IsError)
		require.Len(t, result.MultiContent, 2)
		assert.Equal(t, llmapi.ContentPartTypeImageURL, result.MultiContent[1].Type)
	})

	t.Run("jpeg over dimension limit", func(t *testing.T) {
		dir := t.TempDir()
		img := image.NewRGBA(image.Rect(0, 0, 4000, 500))
		var buf bytes.Buffer
		require.NoError(t, jpeg.Encode(&buf, img, nil))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "wide.jpg"), buf.Bytes(), 0o644))

		tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
		result := tool.Execute(context.Background(), `{"path":"wide.jpg"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "4000x500")
		assert.Contains(t, result.Content, "2000px")
	})

	t.Run("image ignores offset and limit", func(t *testing.T) {
		dir := t.TempDir()
		pngData := makePNG(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "test.png"), pngData, 0o644))

		tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
		result := tool.Execute(context.Background(), `{"path":"test.png","offset":5,"limit":10}`)

		assert.False(t, result.IsError)
		require.Len(t, result.MultiContent, 2)
		// Full image is returned despite offset/limit.
		assert.True(t, strings.HasPrefix(result.MultiContent[1].ImageURL, "data:image/png;base64,"))
	})

	t.Run("text file unaffected", func(t *testing.T) {
		dir := setupWorkspace(t)
		tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
		result := tool.Execute(context.Background(), `{"path":"hello.txt"}`)

		assert.False(t, result.IsError)
		assert.Nil(t, result.MultiContent)
		assert.Contains(t, result.Content, "L1: hello world")
	})
}

func TestReadFile_imagePathWithSpaces(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "Screenshot 2026-03-25 at 6.55.08 AM.png")

	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	require.NoError(t, png.Encode(&buf, img))
	require.NoError(t, os.WriteFile(imgPath, buf.Bytes(), 0o644))

	tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
	result := tool.Execute(context.Background(), `{"path": "`+imgPath+`"}`)

	assert.False(t, result.IsError)
	assert.Equal(t, "Read image file: Screenshot 2026-03-25 at 6.55.08 AM.png (73 bytes, image/png)", result.Content)
	require.Len(t, result.MultiContent, 2)
	assert.Equal(t, llmapi.ContentPartTypeText, result.MultiContent[0].Type)
	assert.Equal(t, llmapi.ContentPartTypeImageURL, result.MultiContent[1].Type)
	assert.Contains(t, result.MultiContent[1].ImageURL, "data:image/png;base64,")
}

func TestReadFile_lineTruncation(t *testing.T) {
	dir := t.TempDir()

	// Write a file with a very long line.
	longLine := strings.Repeat("x", 800)
	content := "short line\n" + longLine + "\nlast line\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "long.txt"), []byte(content), 0o644))

	t.Run("default limit truncates long lines", func(t *testing.T) {
		tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
		result := tool.Execute(context.Background(), `{"path":"long.txt"}`)
		require.False(t, result.IsError)
		// Short lines should be intact.
		assert.Contains(t, result.Content, "L1: short line")
		assert.Contains(t, result.Content, "L3: last line")
		// Long line should be truncated with marker.
		assert.Contains(t, result.Content, "[truncated line]")
		assert.NotContains(t, result.Content, longLine)
	})

	t.Run("custom limit truncates at configured size", func(t *testing.T) {
		tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 50)
		result := tool.Execute(context.Background(), `{"path":"long.txt"}`)
		require.False(t, result.IsError)
		// The long line (line 2) should be truncated.
		assert.Contains(t, result.Content, "[truncated line]")
		// Extract line 2 content (after "L2: " prefix).
		for _, line := range strings.Split(result.Content, "\n") {
			after, ok := strings.CutPrefix(line, "L2: ")
			if ok {
				assert.LessOrEqual(t, len(after), 50)
				break
			}
		}
	})

	t.Run("lines within limit are not truncated", func(t *testing.T) {
		tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 1000)
		result := tool.Execute(context.Background(), `{"path":"long.txt"}`)
		require.False(t, result.IsError)
		// All lines fit within 1000 bytes, so no truncation.
		assert.Contains(t, result.Content, longLine)
		assert.NotContains(t, result.Content, "[truncated line]")
	})
}

// TestReadFile_binaryFileReturnsStub verifies that read_file refuses to
// inline binary content and returns a metadata stub instead. Regression
// for RUNE-179.
func TestReadFile_binaryFileReturnsStub(t *testing.T) {
	dir := t.TempDir()
	// ELF-ish blob with a NUL in the first 8 KiB.
	data := []byte("\x7fELF\x02\x01\x01\x00binary\x00\x00payload")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.out"), data, 0o644))

	tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
	result := tool.Execute(context.Background(), `{"path":"a.out"}`)

	require.False(t, result.IsError)
	// Header carries identity (name, size, sha256).
	header := regexp.MustCompile(`^<binary file: a\.out, \d+ bytes, sha256=[0-9a-f]{64}`)
	assert.Regexp(t, header, result.Content)
	// Body steers the model toward bash-based inspection rather than
	// looping on the same read_file path.
	assert.Contains(t, result.Content, "use bash")
	assert.Contains(t, result.Content, "xxd")
}

// TestReadFile_textWithStrayBytesSanitised verifies that text files
// with a stray invalid UTF-8 byte (e.g. a latin-1 log line) are passed
// through with U+FFFD replacement and a trailing marker.
func TestReadFile_textWithStrayBytesSanitised(t *testing.T) {
	dir := t.TempDir()
	// One latin-1 byte (0xff) inside an otherwise ASCII log line.
	data := []byte("normal line\nbad byte here: \xff oops\nmore text\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "latin1.log"), data, 0o644))

	tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
	result := tool.Execute(context.Background(), `{"path":"latin1.log"}`)

	require.False(t, result.IsError)
	assert.True(t, utf8.ValidString(result.Content),
		"sanitised content must be valid UTF-8")
	assert.Contains(t, result.Content, "\ufffd",
		"invalid byte must be replaced with U+FFFD")
	assert.Contains(t, result.Content, "(1 invalid UTF-8 byte replaced with U+FFFD)")
}

// TestReadFile_validUTF8PassesThroughVerbatim verifies that a normal
// UTF-8 source file is returned without a sanitisation marker.
func TestReadFile_validUTF8PassesThroughVerbatim(t *testing.T) {
	dir := t.TempDir()
	data := []byte("héllo · 世界\nL2 ascii\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "utf8.txt"), data, 0o644))

	tool := newReadFile(localFS{}, dirURI(dir), NewFileTracker(), 0)
	result := tool.Execute(context.Background(), `{"path":"utf8.txt"}`)

	require.False(t, result.IsError)
	assert.Contains(t, result.Content, "héllo · 世界")
	assert.NotContains(t, result.Content, "invalid UTF-8")
	assert.NotContains(t, result.Content, "\ufffd")
}

func TestSearch(t *testing.T) {
	tests := []struct {
		name      string
		args      string
		setup     func(t *testing.T, dir string)
		assertFn  func(t *testing.T, result agent.ToolResult)
		useIgnore bool
	}{
		{
			name: "regex match returns file:line:content",
			args: `{"pattern": "hello"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "hello.txt:1:hello world")
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
			name: "no matches returns no matches message",
			args: `{"pattern": "zzzznotfound"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "no matches found")
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
				assert.Contains(t, result.Content, "no matches found")
			},
		},
		{
			name:      "skips editor swap files when ignore filter is set",
			args:      `{"pattern": "swapcontent"}`,
			useIgnore: true,
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".hello.txt.rswp"),
					[]byte("swapcontent"), 0o644))
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.Contains(t, result.Content, "no matches found")
			},
		},
		{
			name:      "skips gitignored files when ignore filter is set",
			args:      `{"pattern": "secretcontent"}`,
			useIgnore: true,
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
					[]byte("ignored.txt\n"), 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored.txt"),
					[]byte("secretcontent"), 0o644))
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.Contains(t, result.Content, "no matches found")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupWorkspace(t)
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			var filter walkdir.Filter
			if tt.useIgnore {
				m, err := vctrl.LoadGitignore(localFS{root: dir})
				require.NoError(t, err)
				filter = m
			}
			tool := newSearch(localFS{root: dir}, dirURI(dir), NewFileTracker(), filter)

			result := tool.Execute(context.Background(), tt.args)
			tt.assertFn(t, result)
		})
	}
}

func TestFindFiles(t *testing.T) {
	tests := []struct {
		name      string
		args      string
		setup     func(t *testing.T, dir string)
		assertFn  func(t *testing.T, result agent.ToolResult)
		useIgnore bool
	}{
		{
			name: "regex pattern matches files",
			args: `{"pattern": "\\.go$", "recursive": true}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "nested.go")
			},
		},
		{
			name: "non-recursive search only checks given directory",
			args: `{"pattern": ".*", "recursive": false}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "hello.txt")
				assert.NotContains(t, result.Content, "nested.go")
			},
		},
		{
			name: "non-recursive search checks files directly in scoped directory",
			args: `{"pattern": ".*", "path": "sub", "recursive": false}`,
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "deep", "deeper.go"), nil, 0o644))
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "sub/nested.go")
				assert.NotContains(t, result.Content, "deeper.go")
			},
		},
		{
			name: "no matches returns message",
			args: `{"pattern": "\\.xyz$"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "no files found")
			},
		},
		{
			name: "invalid JSON returns error",
			args: `invalid`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "invalid arguments")
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
			name: "path parameter scopes search",
			args: `{"pattern": "\\.go$", "path": "sub"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "nested.go")
			},
		},
		{
			name: "wildcard matches all files",
			args: `{"pattern": ".*"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "hello.txt")
				assert.Contains(t, result.Content, "nested.go")
			},
		},
		{
			name: "partial match on directory component",
			args: `{"pattern": "nested"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "nested.go")
			},
		},
		{
			name: "skips .git directory",
			args: `{"pattern": ".*"}`,
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"),
					[]byte("ref: refs/heads/main"), 0o644))
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.NotContains(t, result.Content, ".git")
				assert.NotContains(t, result.Content, "HEAD")
			},
		},
		{
			name:      "skips editor swap files when ignore filter is set",
			args:      `{"pattern": ".*"}`,
			useIgnore: true,
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".hello.txt.rswp"),
					[]byte("swap"), 0o644))
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.NotContains(t, result.Content, ".rswp")
			},
		},
		{
			name:      "skips gitignored files when ignore filter is set",
			args:      `{"pattern": ".*"}`,
			useIgnore: true,
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
					[]byte("ignored.txt\n"), 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored.txt"),
					[]byte("secret"), 0o644))
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.NotContains(t, result.Content, "ignored.txt")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupWorkspace(t)
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			var filter walkdir.Filter
			if tt.useIgnore {
				m, err := vctrl.LoadGitignore(localFS{root: dir})
				require.NoError(t, err)
				filter = m
			}
			tool := newFindFiles(localFS{root: dir}, dirURI(dir), NewFileTracker(), filter)

			result := tool.Execute(context.Background(), tt.args)
			tt.assertFn(t, result)
		})
	}
}

func TestFindFilesDefinitionDescribesRecursiveScope(t *testing.T) {
	tool := newFindFiles(localFS{}, dirURI("/workspace"), NewFileTracker(), nil)
	parameters, ok := tool.Definition().Function.Parameters.(map[string]any)
	require.True(t, ok)
	properties, ok := parameters["properties"].(map[string]any)
	require.True(t, ok)
	recursive, ok := properties["recursive"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "boolean", recursive["type"])
	description, ok := recursive["description"].(string)
	require.True(t, ok)
	assert.Contains(t, description, "only files directly inside path")
	assert.Contains(t, description, "files in child directories are excluded")
	assert.Contains(t, description, "every descendant subdirectory under path, at any depth")
	assert.Contains(t, parameters["required"], "recursive")
	assert.NotContains(t, tool.Definition().Function.Description, "editor swap files")
}

func TestBash_args_do_not_include_program_name(t *testing.T) {
	var recorded workspaceapi.Cmd
	rec := &recordingExec{startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
		recorded = cmd
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- nil
		}
		return 1, nil
	}}
	tool := newBash(rec, dirURI("/workspace"), configedit.NopConfig())
	tool.Execute(context.Background(), `{"command": "ls -R .", "description": "List files recursively"}`)

	assert.Equal(t, "bash", recorded.Path)
	// Args must NOT include the program name — the executor prepends it.
	assert.Equal(t, []string{"-c", "ls -R ."}, recorded.Args,
		"Args must not include the program name; the executor adds it from Path")
}

func TestBash_env_is_nil_so_host_inherits(t *testing.T) {
	// The bash tool runs inside the rune-agent extension process whose
	// os.Environ() may be missing PATH entries the user expects (e.g.
	// Homebrew on macOS GUI launch). When Env is nil, the host executor
	// falls back to its own os.Environ(), which carries the shell-loaded
	// PATH and gui.env overrides. Setting Env from the extension would
	// shadow those values because Go's os/exec lets the last duplicate
	// key win.
	var recorded workspaceapi.Cmd
	rec := &recordingExec{startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
		recorded = cmd
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- nil
		}
		return 1, nil
	}}
	tool := newBash(rec, dirURI("/workspace"), configedit.NopConfig())
	tool.Execute(context.Background(), `{"command": "true", "description": "noop"}`)

	assert.Nil(t, recorded.Env,
		"Env must be nil so the host executor inherits its own environment")
}

func TestBash(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		ctx      func() context.Context
		assertFn func(t *testing.T, result agent.ToolResult)
	}{
		{
			name: "success returns stdout",
			args: `{"command": "echo hello", "description": "Print hello"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "hello")
			},
		},
		{
			name: "captures stderr",
			args: `{"command": "echo stderr >&2", "description": "Print to stderr"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "stderr")
			},
		},
		{
			name: "nonzero exit returns error",
			args: `{"command": "exit 1", "description": "Exit with error"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "error:")
			},
		},
		{
			name: "cancelled context returns error",
			args: `{"command": "sleep 10", "description": "Sleep"}`,
			ctx: func() context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 0)
				_ = cancel
				return ctx
			},
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.True(t, result.IsError)
			},
		},
		{
			name: "invalid JSON returns error",
			args: `garbage`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "invalid arguments")
			},
		},
		{
			name: "working_dir parameter changes directory",
			args: `{"command": "pwd", "description": "Print working dir", "working_dir": "sub"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "sub")
			},
		},
		{
			name: "command not found returns error",
			args: `{"command": "nonexistent_command_xyz_123", "description": "Run missing command"}`,
			assertFn: func(t *testing.T, result agent.ToolResult) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "error:")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupWorkspace(t)
			tool := newBash(localExec{}, dirURI(dir), configedit.NopConfig())

			ctx := context.Background()
			if tt.ctx != nil {
				ctx = tt.ctx()
			}

			result := tool.Execute(ctx, tt.args)
			tt.assertFn(t, result)
		})
	}
}

func TestBash_execute_uses_caller_context_without_adding_deadline(t *testing.T) {
	var capturedCtx context.Context
	rec := &recordingExec{startFn: func(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
		capturedCtx = ctx
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- nil
		}
		return 1, nil
	}}
	tool := newBash(rec, dirURI("/workspace"), configedit.NopConfig())
	tool.Execute(context.Background(), `{"command": "echo hi", "description": "test"}`)

	_, ok := capturedCtx.Deadline()
	assert.False(t, ok, "bash should not add its own deadline")
}

func TestBash_definition_does_not_expose_timeout_parameter(t *testing.T) {
	tool := newBash(localExec{}, dirURI("/workspace"), configedit.NopConfig())
	def := tool.Definition()
	params, ok := def.Function.Parameters.(map[string]any)
	require.True(t, ok)
	props, ok := params["properties"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, props, "timeout")
}

// TestBash_truncation_snaps_to_rune_boundary verifies that the 100 KiB
// cap in bash.go does not slice through a multi-byte UTF-8 rune and
// emit invalid UTF-8 to the model. Regression for RUNE-179.
func TestBash_truncation_snaps_to_rune_boundary(t *testing.T) {
	dir := setupWorkspace(t)
	// Build a payload that places a multi-byte rune across the
	// 100 KiB truncation boundary, then trails a sentinel that must be
	// cut. printf 'a%.0s' is portable across bash/macOS.
	const cap = 100 * 1024
	script := fmt.Sprintf(
		`printf 'a%%.0s' {1..%d}; printf '\xe2\x86\x92'; printf 'TAILSENTINEL'`,
		cap-1,
	)
	tool := newBash(localExec{}, dirURI(dir), configedit.NopConfig())
	args := fmt.Sprintf(`{"command": %q, "description": "boundary"}`, script)

	result := tool.Execute(context.Background(), args)

	require.False(t, result.IsError, "expected success, got: %s", result.Content)
	require.True(t, utf8.ValidString(result.Content),
		"bash output must be valid UTF-8 after truncation; bytes=%d", len(result.Content))
	assert.Contains(t, result.Content, "(output truncated at",
		"expected truncation notice")
	// The trailing sentinel lives past the cap and must be dropped.
	assert.NotContains(t, result.Content, "TAILSENTINEL")
}

func TestApplyPatch(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		setup    func(t *testing.T, dir string)
		assertFn func(t *testing.T, result agent.ToolResult, dir string)
	}{
		{
			name: "create new file via patch",
			args: `{"patch": "*** Begin Patch\n*** Add File: created.txt\n+hello world\n*** End Patch"}`,
			assertFn: func(t *testing.T, result agent.ToolResult, dir string) {
				assert.False(t, result.IsError)
				assert.Contains(t, result.Content, "1/1")

				data, err := os.ReadFile(filepath.Join(dir, "created.txt"))
				require.NoError(t, err)
				assert.Equal(t, "hello world", string(data))
			},
		},
		{
			name: "delete file via patch",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "doomed.txt"), []byte("bye"), 0o644))
			},
			args: `{"patch": "*** Begin Patch\n*** Delete File: doomed.txt\n*** End Patch"}`,
			assertFn: func(t *testing.T, result agent.ToolResult, dir string) {
				assert.False(t, result.IsError)
				_, err := os.Stat(filepath.Join(dir, "doomed.txt"))
				assert.True(t, os.IsNotExist(err))
			},
		},
		{
			name: "update file via patch",
			args: `{"patch": "*** Begin Patch\n*** Update File: hello.txt\n@@\n hello world\n-second line\n+SECOND LINE\n*** End Patch"}`,
			assertFn: func(t *testing.T, result agent.ToolResult, dir string) {
				assert.False(t, result.IsError)

				data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
				require.NoError(t, err)
				assert.Contains(t, string(data), "SECOND LINE")
				assert.NotContains(t, string(data), "second line")
			},
		},
		{
			name: "parse error returns error",
			args: `{"patch": "not a valid patch"}`,
			assertFn: func(t *testing.T, result agent.ToolResult, dir string) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "parse patch")
			},
		},
		{
			name: "invalid JSON returns error",
			args: `bad json`,
			assertFn: func(t *testing.T, result agent.ToolResult, dir string) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "invalid arguments")
			},
		},
		{
			name: "hunk mismatch returns error",
			args: `{"patch": "*** Begin Patch\n*** Update File: hello.txt\n@@\n nonexistent context\n-nope\n+yep\n*** End Patch"}`,
			assertFn: func(t *testing.T, result agent.ToolResult, dir string) {
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content, "errors")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupWorkspace(t)
			tool := newApplyPatch(localFS{}, dirURI(dir), NewFileTracker(), &stubLSP{})

			if tt.setup != nil {
				tt.setup(t, dir)
			}

			result := tool.Execute(context.Background(), tt.args)
			tt.assertFn(t, result, dir)
		})
	}
}

func TestFileTracker(t *testing.T) {
	t.Run("verify returns nil when no hash recorded", func(t *testing.T) {
		ft := NewFileTracker()
		err := ft.Verify("/some/path", []byte("content"))
		assert.NoError(t, err)
	})

	t.Run("verify returns nil when content matches", func(t *testing.T) {
		ft := NewFileTracker()
		data := []byte("hello world")
		ft.Record("/some/path", data)
		assert.NoError(t, ft.Verify("/some/path", data))
	})

	t.Run("verify returns error when content changed", func(t *testing.T) {
		ft := NewFileTracker()
		ft.Record("/some/path", []byte("original"))
		err := ft.Verify("/some/path", []byte("modified"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "has changed since it was last read")
	})

	t.Run("forget removes tracking", func(t *testing.T) {
		ft := NewFileTracker()
		ft.Record("/some/path", []byte("content"))
		ft.Forget("/some/path")
		assert.NoError(t, ft.Verify("/some/path", []byte("different")))
	})

	t.Run("record updates existing hash", func(t *testing.T) {
		ft := NewFileTracker()
		ft.Record("/some/path", []byte("v1"))
		ft.Record("/some/path", []byte("v2"))
		assert.NoError(t, ft.Verify("/some/path", []byte("v2")))
		assert.Error(t, ft.Verify("/some/path", []byte("v1")))
	})
}

func TestApplyPatch_stale_file(t *testing.T) {
	dir := setupWorkspace(t)
	tracker := NewFileTracker()
	readTool := newReadFile(localFS{}, dirURI(dir), tracker, 0)
	patchTool := newApplyPatch(localFS{}, dirURI(dir), tracker, &stubLSP{})

	// Read the file (records hash).
	result := readTool.Execute(context.Background(), `{"path": "hello.txt"}`)
	require.False(t, result.IsError)

	// Modify the file externally.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"),
		[]byte("externally modified\n"), 0o644))

	// Patch should fail: file has changed.
	result = patchTool.Execute(context.Background(),
		`{"patch": "*** Begin Patch\n*** Update File: hello.txt\n@@\n hello world\n-second line\n+SECOND LINE\n*** End Patch"}`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "has changed since it was last read")
}

func TestApplyPatch_touchedFiles(t *testing.T) {
	tests := []struct {
		name         string
		args         string
		setup        func(t *testing.T, dir string)
		wantTouched  int
		wantError    bool
		wantContains string // substring expected in first TouchedFiles entry
	}{
		{
			name:         "single add populates touched files",
			args:         `{"patch": "*** Begin Patch\n*** Add File: new.txt\n+content\n*** End Patch"}`,
			wantTouched:  1,
			wantContains: "new.txt",
		},
		{
			name:         "single update populates touched files",
			args:         `{"patch": "*** Begin Patch\n*** Update File: hello.txt\n@@\n hello world\n-second line\n+SECOND LINE\n*** End Patch"}`,
			wantTouched:  1,
			wantContains: "hello.txt",
		},
		{
			name: "delete-only yields no touched files",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "doomed.txt"), []byte("bye"), 0o644))
			},
			args:        `{"patch": "*** Begin Patch\n*** Delete File: doomed.txt\n*** End Patch"}`,
			wantTouched: 0,
		},
		{
			name:        "multi-file patch populates multiple touched files",
			args:        `{"patch": "*** Begin Patch\n*** Add File: a.txt\n+a\n*** Add File: b.txt\n+b\n*** End Patch"}`,
			wantTouched: 2,
		},
		{
			name: "mixed add and delete only counts non-delete",
			setup: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old"), 0o644))
			},
			args:         `{"patch": "*** Begin Patch\n*** Delete File: old.txt\n*** Add File: new2.txt\n+new\n*** End Patch"}`,
			wantTouched:  1,
			wantContains: "new2.txt",
		},
		{
			name:        "error result has no touched files",
			args:        `{"patch": "not a valid patch"}`,
			wantError:   true,
			wantTouched: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupWorkspace(t)
			tool := newApplyPatch(localFS{}, dirURI(dir), NewFileTracker(), &stubLSP{})

			if tt.setup != nil {
				tt.setup(t, dir)
			}

			result := tool.Execute(context.Background(), tt.args)

			if tt.wantError {
				assert.True(t, result.IsError)
				assert.Empty(t, result.TouchedFiles)
				return
			}

			assert.False(t, result.IsError)
			assert.Len(t, result.TouchedFiles, tt.wantTouched)
			if tt.wantContains != "" && len(result.TouchedFiles) > 0 {
				assert.Contains(t, result.TouchedFiles[0], tt.wantContains)
			}
		})
	}
}

func TestApplyPatch_forgets_hash_after_apply(t *testing.T) {
	dir := setupWorkspace(t)
	tracker := NewFileTracker()
	readTool := newReadFile(localFS{}, dirURI(dir), tracker, 0)
	patchTool := newApplyPatch(localFS{}, dirURI(dir), tracker, &stubLSP{})

	// Read and patch.
	result := readTool.Execute(context.Background(), `{"path": "hello.txt"}`)
	require.False(t, result.IsError)

	result = patchTool.Execute(context.Background(),
		`{"patch": "*** Begin Patch\n*** Update File: hello.txt\n@@\n hello world\n-second line\n+SECOND LINE\n*** End Patch"}`)
	require.False(t, result.IsError)

	// Hash was forgotten, so a second patch without re-reading
	// should proceed (no hash to verify against).
	result = patchTool.Execute(context.Background(),
		`{"patch": "*** Begin Patch\n*** Update File: hello.txt\n@@\n hello world\n-SECOND LINE\n+final line\n*** End Patch"}`)
	assert.False(t, result.IsError)
}

func setupWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"),
		[]byte("hello world\nsecond line\nthird line\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "nested.go"),
		[]byte("package sub\n\nfunc Foo() {}\n"), 0o644))

	return dir
}

// blockingFS blocks the first OpenFile of blockFile until release is
// closed, so tests can cancel a search while it is in flight.
type blockingFS struct {
	localFS
	blockFile string
	opened    chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (f *blockingFS) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	if filepath.Base(path) == f.blockFile {
		f.once.Do(func() { close(f.opened) })
		<-f.release
	}
	return f.localFS.OpenFile(path, flag, mode)
}

// TestSearchTools_explicitFilePath verifies that naming a single file
// searches only that file. walkdir.ListFiles promotes a file path to
// its nearest parent directory, which silently searched the whole
// surrounding tree. Regression for RUNE-305.
func TestSearchTools_explicitFilePath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "target.txt"),
		[]byte("needle in target\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sibling.txt"),
		[]byte("needle in sibling\n"), 0o644))

	tests := []struct {
		name string
		tool agent.Tool
		args string
	}{
		{
			"search_content",
			newSearch(localFS{root: dir}, dirURI(dir), NewFileTracker(), nil),
			`{"pattern":"needle","path":"target.txt","include":null}`,
		},
		{
			"find_files",
			newFindFiles(localFS{root: dir}, dirURI(dir), NewFileTracker(), nil),
			`{"pattern":".*","path":"target.txt"}`,
		},
		{
			"grep_files",
			NewGrepFiles(localFS{root: dir}, dirURI(dir), NewFileTracker()),
			`{"pattern":"needle","path":"target.txt"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.tool.Execute(t.Context(), tt.args)
			require.False(t, result.IsError, result.Content)
			assert.Contains(t, result.Content, "target.txt")
			assert.NotContains(t, result.Content, "sibling.txt")
		})
	}
}

// TestSearchContent_skipsBinaryFiles covers search_content's documented
// binary exclusion: the pattern is on a clean line before the first NUL
// byte, so the line scanner alone would still report the file.
func TestSearchContent_skipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bin.dat"),
		[]byte("needle in binary\n\x00\x00\x01\x02"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "text.txt"),
		[]byte("needle in text\n"), 0o644))

	tool := newSearch(localFS{root: dir}, dirURI(dir), NewFileTracker(), nil)
	result := tool.Execute(t.Context(), `{"pattern":"needle","path":"","include":null}`)

	require.False(t, result.IsError, result.Content)
	assert.Contains(t, result.Content, "text.txt")
	assert.NotContains(t, result.Content, "bin.dat")
}

// TestSearchTools_canceledContext verifies a canceled search reports an
// error instead of rendering as a successful (possibly empty) result.
func TestSearchTools_canceledContext(t *testing.T) {
	dir := setupWorkspace(t)
	tests := []struct {
		name string
		tool agent.Tool
		args string
	}{
		{
			"search_content",
			newSearch(localFS{root: dir}, dirURI(dir), NewFileTracker(), nil),
			`{"pattern":"hello","path":"","include":null}`,
		},
		{
			"find_files",
			newFindFiles(localFS{root: dir}, dirURI(dir), NewFileTracker(), nil),
			`{"pattern":".*","path":""}`,
		},
		{
			"grep_files",
			NewGrepFiles(localFS{root: dir}, dirURI(dir), NewFileTracker()),
			`{"pattern":"hello"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			result := tt.tool.Execute(ctx, tt.args)
			assert.True(t, result.IsError, result.Content)
		})
	}
}

// TestSearchContent_cancelDuringSearch verifies that matches collected
// before a cancellation are not returned as a successful result.
func TestSearchContent_cancelDuringSearch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "match.txt"),
		[]byte("needle here\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "block.txt"),
		[]byte("needle here\n"), 0o644))

	fs := &blockingFS{
		localFS:   localFS{root: dir},
		blockFile: "block.txt",
		opened:    make(chan struct{}),
		release:   make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-fs.opened
		cancel()
		close(fs.release)
	}()

	tool := newSearch(fs, dirURI(dir), NewFileTracker(), nil)
	result := tool.Execute(ctx, `{"pattern":"needle","path":"","include":null}`)

	assert.True(t, result.IsError, result.Content)
	assert.NotContains(t, result.Content, "match.txt")
}

func TestDefaultTools_wiresApplyPatchLSP(t *testing.T) {
	lsp := &stubLSP{}
	tools, _ := DefaultTools(localFS{}, localExec{}, dirURI("/workspace"), lsp, Config{}, configedit.NopConfig())

	// Verify the apply_patch tool got the lsp reference.
	var found bool
	for _, tool := range tools {
		if ap, ok := tool.(*applyPatchTool); ok {
			assert.Equal(t, lsp, ap.lsp)
			found = true
		}
	}
	assert.True(t, found, "apply_patch tool not found in DefaultTools")
}

func TestApplyPatch_notifiesLSP(t *testing.T) {
	dir := setupWorkspace(t)

	var received []semanticapi.FileEvent
	lsp := &stubLSP{
		didChangeWatchedFilesFn: func(p semanticapi.DidChangeWatchedFilesParams) error {
			received = append(received, p.Changes...)
			return nil
		},
	}

	tool := newApplyPatch(localFS{}, dirURI(dir), NewFileTracker(), lsp)

	// Successful patch should notify LSP.
	result := tool.Execute(context.Background(),
		`{"patch": "*** Begin Patch\n*** Update File: hello.txt\n@@\n hello world\n-second line\n+SECOND LINE\n*** End Patch"}`)
	require.False(t, result.IsError, result.Content)

	require.Len(t, received, 1)
	assert.Equal(t, semanticapi.FileChangeTypeChanged, received[0].Type)
	assert.Contains(t, received[0].URI, "hello.txt")
}

func TestApplyPatch_notifiesLSP_addAndDelete(t *testing.T) {
	dir := setupWorkspace(t)

	var received []semanticapi.FileEvent
	lsp := &stubLSP{
		didChangeWatchedFilesFn: func(p semanticapi.DidChangeWatchedFilesParams) error {
			received = append(received, p.Changes...)
			return nil
		},
	}

	tool := newApplyPatch(localFS{}, dirURI(dir), NewFileTracker(), lsp)

	result := tool.Execute(context.Background(),
		`{"patch": "*** Begin Patch\n*** Add File: new.go\n+package main\n*** Delete File: hello.txt\n*** End Patch"}`)
	require.False(t, result.IsError, result.Content)

	require.Len(t, received, 2)

	// Find add and delete events.
	var addEvent, deleteEvent semanticapi.FileEvent
	for _, e := range received {
		switch e.Type {
		case semanticapi.FileChangeTypeCreated:
			addEvent = e
		case semanticapi.FileChangeTypeDeleted:
			deleteEvent = e
		}
	}
	assert.Contains(t, addEvent.URI, "new.go")
	assert.Contains(t, deleteEvent.URI, "hello.txt")
}
