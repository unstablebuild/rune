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

package texttest

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace"
)

// externalEditor wraps a TestEditor and reports itself as
// externally-managed.
type externalEditor struct {
	*TestEditor
	readOnlyObserved *bool
	editCalls        *int
}

// IsExternal overrides TestEditor.IsExternal to return true.
func (externalEditor) IsExternal() bool { return true }

func (e externalEditor) Edit(
	ctx context.Context,
	file workspaceapi.URI, buf *cell.Buffer, readOnly, recovered bool,
) (text.Handler, error) {
	if e.readOnlyObserved != nil {
		*e.readOnlyObserved = readOnly
	}
	if e.editCalls != nil {
		*e.editCalls++
	}
	return e.TestEditor.Edit(ctx, file, buf, readOnly, recovered)
}

// recordingLoader wraps testLoader and records the readOnly flag
// passed to Load.
type recordingLoader struct {
	*testLoader
	readOnlyObserved *bool
}

func (l recordingLoader) Load(
	file workspaceapi.URI, buf *cell.Buffer, swapDir workspaceapi.URI,
	readOnly bool,
) (workspace.FlusherCloser, error) {
	*l.readOnlyObserved = readOnly
	return l.testLoader.Load(file, buf, swapDir, readOnly)
}

func TestExternalEditorForcesReadOnly(t *testing.T) {
	var loaderRO, editorRO bool
	ed := externalEditor{TestEditor: NopEditor(), readOnlyObserved: &editorRO}
	loader := &testLoader{
		openFile: workspace.NewMemoryFile(
			"external.txt", 1, 0, []byte("hi\n"), new(sync.Mutex)),
	}
	wrappedLoader := recordingLoader{testLoader: loader, readOnlyObserved: &loaderRO}
	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
	c, err := text.NewComponent(ed, wrappedLoader, cfg)
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("memory:///tmp/external.txt")
	require.NoError(t, err)

	// Caller asks for read/write; the external editor must coerce it.
	_, err = c.OpenFileTab(uri, false /* readOnly */)
	require.NoError(t, err)
	assert.True(t, loaderRO,
		"workspace.Load must receive readOnly=true under an external editor")
	assert.True(t, editorRO,
		"editor.Edit must receive readOnly=true under an external editor")
}

func TestExternalEditorDelegatesMarkdown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readOnly bool
	}{
		{name: "edit", readOnly: false},
		{name: "view", readOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var editCalls int
			ed := externalEditor{TestEditor: NopEditor(), editCalls: &editCalls}
			loader := &testLoader{
				openFile: workspace.NewMemoryFile(
					"README.md", 1, 0,
					[]byte("# hi\n"), new(sync.Mutex)),
			}
			cfg := text.DefaultConfig()
			cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
			c, err := text.NewComponent(ed, loader, cfg)
			require.NoError(t, err)

			uri, err := workspaceapi.ParseURI("memory:///tmp/README.md")
			require.NoError(t, err)

			_, err = c.OpenFileTab(uri, tc.readOnly)
			require.NoError(t, err)
			assert.Equal(t, 1, editCalls,
				"external editor.Edit must be called for .md "+
					"opens regardless of readOnly; got %d "+
					"calls (readOnly=%v)", editCalls, tc.readOnly)
		})
	}
}

func TestExternalEditorOpensMissingFile(t *testing.T) {
	dir := t.TempDir()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	inline := func(fn func()) bool { fn(); return true }
	ws := workspace.NewSchemeWorkspace(wsURI, scheme, inline)

	for _, streaming := range []bool{false, true} {
		name := "sync"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			ed := externalEditor{TestEditor: NopEditor()}
			cfg := text.DefaultConfig()
			cfg.ScheduleNextTick = inline
			cfg.StreamingOpen = streaming
			// The streaming path defers its missing-file sync
			// fallback to a scheduled tick; queue ticks so they
			// run serialized on this goroutine, like the real
			// event loop.
			var pending []func()
			if streaming {
				var mu sync.Mutex
				cfg.ScheduleNextTick = func(fn func()) bool {
					mu.Lock()
					pending = append(pending, fn)
					mu.Unlock()
					return true
				}
			}
			c, err := text.NewComponent(ed, ws, cfg)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })

			fpath := filepath.Join(dir, name+"-new.txt")
			fileURI, err := workspaceapi.ParseURI("file://" + fpath)
			require.NoError(t, err)

			h, err := c.OpenFileTab(fileURI, false)
			require.NoError(t, err)
			require.NotNil(t, h)
			if streaming {
				c.WaitStreamingLoads()
				for len(pending) > 0 {
					fn := pending[0]
					pending = pending[1:]
					fn()
				}
				_, err = c.Editor(fileURI)
				require.NoError(t, err,
					"missing-file fallback must open a writable mirror tab")
			}
		})
	}
}
