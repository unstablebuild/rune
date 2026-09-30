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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/cmd/rune-agent/agent"
)

func TestFileTracker_Reads(t *testing.T) {
	t.Run("RecordRead and StaleReads returns recorded IDs", func(t *testing.T) {
		ft := NewFileTracker()
		ft.RecordRead("/a/b.go", "read_file", "", "call_1")
		ft.RecordRead("/a/b.go", "read_file", "", "call_2")

		ids := ft.StaleReads("/a/b.go")
		assert.Equal(t, []string{"call_1", "call_2"}, ids)
	})

	t.Run("StaleReads clears tracked IDs", func(t *testing.T) {
		ft := NewFileTracker()
		ft.RecordRead("/a/b.go", "read_file", "", "call_1")

		_ = ft.StaleReads("/a/b.go")
		ids := ft.StaleReads("/a/b.go")
		assert.Nil(t, ids)
	})

	t.Run("StaleReads on unknown path returns nil", func(t *testing.T) {
		ft := NewFileTracker()
		ids := ft.StaleReads("/no/such/file.go")
		assert.Nil(t, ids)
	})

	t.Run("StaleReads returns IDs from all tools", func(t *testing.T) {
		ft := NewFileTracker()
		ft.RecordRead("/x.go", "read_file", "", "c1")
		ft.RecordRead("/x.go", "outline_file", "", "c2")
		ft.RecordRead("/x.go", "read_file", "", "c3")

		ids := ft.StaleReads("/x.go")
		sort.Strings(ids)
		assert.Equal(t, []string{"c1", "c2", "c3"}, ids)
	})

	t.Run("different paths are independent", func(t *testing.T) {
		ft := NewFileTracker()
		ft.RecordRead("/a.go", "read_file", "", "c1")
		ft.RecordRead("/b.go", "read_file", "", "c2")

		assert.Equal(t, []string{"c1"}, ft.StaleReads("/a.go"))
		assert.Equal(t, []string{"c2"}, ft.StaleReads("/b.go"))
	})

	t.Run("Forget clears all tools for path", func(t *testing.T) {
		ft := NewFileTracker()
		ft.RecordRead("/a.go", "read_file", "", "c1")
		ft.RecordRead("/a.go", "outline_file", "", "c2")

		ft.Forget("/a.go")

		ids := ft.StaleReads("/a.go")
		assert.Nil(t, ids)
	})

	t.Run("TrackRead only drops same tool same file", func(t *testing.T) {
		ft := NewFileTracker()
		ft.RecordRead("/a.go", "read_file", "", "c1")
		ft.RecordRead("/a.go", "check_file_errors", "", "c2")

		// A second read_file should only drop c1, not c2.
		ctx := agent.WithParentToolCallID(t.Context(), "c3")
		stale := ft.TrackRead(ctx, "read_file", "/a.go", "")
		assert.Equal(t, []string{"c1"}, stale)

		// check_file_errors read is still tracked.
		remaining := ft.StaleReads("/a.go")
		// Should contain c2 (check_file_errors) and c3 (new read_file).
		sort.Strings(remaining)
		assert.Equal(t, []string{"c2", "c3"}, remaining)
	})

	t.Run("TrackRead with different variants are independent", func(t *testing.T) {
		ft := NewFileTracker()
		// Simulate reading lines 1-50 and lines 100-50 of the same file.
		ctx1 := agent.WithParentToolCallID(t.Context(), "c1")
		stale1 := ft.TrackRead(ctx1, "read_file", "/a.go", "1:50")
		assert.Nil(t, stale1)

		ctx2 := agent.WithParentToolCallID(t.Context(), "c2")
		stale2 := ft.TrackRead(ctx2, "read_file", "/a.go", "100:50")
		assert.Nil(t, stale2, "different variant should not drop c1")

		// Both reads should still be tracked.
		ids := ft.StaleReads("/a.go")
		sort.Strings(ids)
		assert.Equal(t, []string{"c1", "c2"}, ids)
	})

	t.Run("TrackRead with same variant drops previous", func(t *testing.T) {
		ft := NewFileTracker()
		ctx1 := agent.WithParentToolCallID(t.Context(), "c1")
		ft.TrackRead(ctx1, "read_file", "/a.go", "100:50")

		ctx2 := agent.WithParentToolCallID(t.Context(), "c2")
		stale := ft.TrackRead(ctx2, "read_file", "/a.go", "100:50")
		assert.Equal(t, []string{"c1"}, stale)
	})

	t.Run("TrackRead on nil receiver returns nil", func(t *testing.T) {
		var ft *FileTracker
		ids := ft.TrackRead(t.Context(), "read_file", "/a.go", "")
		assert.Nil(t, ids)
	})
}

func TestFileTracker_Discoveries(t *testing.T) {
	t.Run("ConsumeDiscoveries returns discovery ID when path matches", func(t *testing.T) {
		ft := NewFileTracker()
		ctx := agent.WithParentToolCallID(t.Context(), "search_1")
		ft.TrackDiscovery(ctx, []string{"/a.go", "/b.go", "/c.go"})

		ids := ft.ConsumeDiscoveries("/b.go")
		assert.Equal(t, []string{"search_1"}, ids)
	})

	t.Run("ConsumeDiscoveries removes the discovery", func(t *testing.T) {
		ft := NewFileTracker()
		ctx := agent.WithParentToolCallID(t.Context(), "search_1")
		ft.TrackDiscovery(ctx, []string{"/a.go", "/b.go"})

		_ = ft.ConsumeDiscoveries("/a.go")
		// Same discovery should not be returned again.
		ids := ft.ConsumeDiscoveries("/b.go")
		assert.Nil(t, ids)
	})

	t.Run("ConsumeDiscoveries on unknown path returns nil", func(t *testing.T) {
		ft := NewFileTracker()
		ctx := agent.WithParentToolCallID(t.Context(), "search_1")
		ft.TrackDiscovery(ctx, []string{"/a.go"})

		ids := ft.ConsumeDiscoveries("/z.go")
		assert.Nil(t, ids)
	})

	t.Run("multiple discoveries consumed independently", func(t *testing.T) {
		ft := NewFileTracker()
		ctx1 := agent.WithParentToolCallID(t.Context(), "search_1")
		ft.TrackDiscovery(ctx1, []string{"/a.go", "/b.go"})
		ctx2 := agent.WithParentToolCallID(t.Context(), "find_1")
		ft.TrackDiscovery(ctx2, []string{"/b.go", "/c.go"})

		// Reading /b.go should consume both discoveries.
		ids := ft.ConsumeDiscoveries("/b.go")
		sort.Strings(ids)
		assert.Equal(t, []string{"find_1", "search_1"}, ids)
	})

	t.Run("TrackDiscovery with no paths is a no-op", func(t *testing.T) {
		ft := NewFileTracker()
		ctx := agent.WithParentToolCallID(t.Context(), "search_1")
		ft.TrackDiscovery(ctx, nil)

		ids := ft.ConsumeDiscoveries("/a.go")
		assert.Nil(t, ids)
	})

	t.Run("TrackDiscovery with no tool call ID is a no-op", func(t *testing.T) {
		ft := NewFileTracker()
		ft.TrackDiscovery(t.Context(), []string{"/a.go"})

		ids := ft.ConsumeDiscoveries("/a.go")
		assert.Nil(t, ids)
	})

	t.Run("nil receiver is safe", func(t *testing.T) {
		var ft *FileTracker
		ft.TrackDiscovery(t.Context(), []string{"/a.go"})
		assert.Nil(t, ft.ConsumeDiscoveries("/a.go"))
	})
}

func TestReadFile_consumesDiscoveries(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644))

	tracker := NewFileTracker()

	// Simulate a search_content that discovered a.txt.
	searchCtx := agent.WithParentToolCallID(t.Context(), "search_1")
	tracker.TrackDiscovery(searchCtx, []string{filepath.Join(dir, "a.txt")})

	// read_file should consume the discovery.
	readTool := newReadFile(localFS{}, dirURI(dir), tracker, 0)
	readCtx := agent.WithParentToolCallID(t.Context(), "read_1")
	result := readTool.Execute(readCtx, `{"path":"a.txt","offset":null,"limit":null}`)
	require.False(t, result.IsError)
	assert.Equal(t, []string{"search_1"}, result.DropToolResultIDs)
}

func TestFindFiles_tracksDiscovery(t *testing.T) {
	dir := setupWorkspace(t)
	tracker := NewFileTracker()
	findTool := newFindFiles(localFS{root: dir}, dirURI(dir), tracker, nil)

	ctx := agent.WithParentToolCallID(t.Context(), "find_1")
	result := findTool.Execute(ctx, `{"pattern":"\\.go$"}`)
	require.False(t, result.IsError)

	// Reading a discovered file should consume the discovery.
	readTool := newReadFile(localFS{}, dirURI(dir), tracker, 0)
	readCtx := agent.WithParentToolCallID(t.Context(), "read_1")
	readResult := readTool.Execute(readCtx, `{"path":"sub/nested.go","offset":null,"limit":null}`)
	require.False(t, readResult.IsError)
	assert.Equal(t, []string{"find_1"}, readResult.DropToolResultIDs)
}

func TestSearchContent_tracksDiscovery(t *testing.T) {
	dir := setupWorkspace(t)
	tracker := NewFileTracker()
	searchTool := newSearch(localFS{root: dir}, dirURI(dir), tracker, nil, nil, nil)

	ctx := agent.WithParentToolCallID(t.Context(), "search_1")
	result := searchTool.Execute(ctx, `{"pattern":"hello"}`)
	require.False(t, result.IsError)

	// Reading the discovered file should consume the discovery.
	readTool := newReadFile(localFS{}, dirURI(dir), tracker, 0)
	readCtx := agent.WithParentToolCallID(t.Context(), "read_1")
	readResult := readTool.Execute(readCtx, `{"path":"hello.txt","offset":null,"limit":null}`)
	require.False(t, readResult.IsError)
	assert.Equal(t, []string{"search_1"}, readResult.DropToolResultIDs)
}

func TestApplyPatch_consumesDiscoveries(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"),
		[]byte("hello\nsecond line\n"), 0o644))

	tracker := NewFileTracker()

	// Simulate a discovery of a.txt.
	searchCtx := agent.WithParentToolCallID(t.Context(), "search_1")
	tracker.TrackDiscovery(searchCtx, []string{filepath.Join(dir, "a.txt")})

	// Read first (required by apply_patch verification).
	readTool := newReadFile(localFS{}, dirURI(dir), tracker, 0)
	readCtx := agent.WithParentToolCallID(t.Context(), "read_1")
	r := readTool.Execute(readCtx, `{"path":"a.txt","offset":null,"limit":null}`)
	require.False(t, r.IsError)
	// The read already consumed the discovery.
	assert.Equal(t, []string{"search_1"}, r.DropToolResultIDs)

	// Apply a patch — should still drop the read.
	patchTool := newApplyPatch(localFS{}, dirURI(dir), tracker, &stubLSP{})
	patchResult := patchTool.Execute(t.Context(),
		`{"patch":"*** Begin Patch\n*** Update File: a.txt\n@@\n hello\n-second line\n+SECOND LINE\n*** End Patch"}`)
	require.False(t, patchResult.IsError)
	assert.Equal(t, []string{"read_1"}, patchResult.DropToolResultIDs)
}

func TestReadFile_dropsStaleReads(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644))

	tracker := NewFileTracker()
	tool := newReadFile(localFS{}, dirURI(dir), tracker, 0)

	t.Run("second read returns first read ID as stale", func(t *testing.T) {
		ctx := agent.WithParentToolCallID(t.Context(), "call_1")
		r1 := tool.Execute(ctx, `{"path":"a.txt","offset":null,"limit":null}`)
		require.False(t, r1.IsError)
		assert.Empty(t, r1.DropToolResultIDs, "first read should not drop anything")

		ctx2 := agent.WithParentToolCallID(t.Context(), "call_2")
		r2 := tool.Execute(ctx2, `{"path":"a.txt","offset":null,"limit":null}`)
		require.False(t, r2.IsError)
		assert.Equal(t, []string{"call_1"}, r2.DropToolResultIDs)
	})

	t.Run("different ranges do not drop each other", func(t *testing.T) {
		// Write a file large enough to have distinct ranges.
		largeDir := t.TempDir()
		var content strings.Builder
		for i := range 200 {
			fmt.Fprintf(&content, "line %d\n", i+1)
		}
		require.NoError(t, os.WriteFile(filepath.Join(largeDir, "big.txt"),
			[]byte(content.String()), 0o644))

		tracker3 := NewFileTracker()
		tool3 := newReadFile(localFS{}, dirURI(largeDir), tracker3, 0)

		// Read lines 1-50.
		ctx1 := agent.WithParentToolCallID(t.Context(), "range_1")
		r1 := tool3.Execute(ctx1, `{"path":"big.txt","offset":1,"limit":50}`)
		require.False(t, r1.IsError)
		assert.Empty(t, r1.DropToolResultIDs)

		// Read lines 100-150 — should NOT drop the first range.
		ctx2 := agent.WithParentToolCallID(t.Context(), "range_2")
		r2 := tool3.Execute(ctx2, `{"path":"big.txt","offset":100,"limit":50}`)
		require.False(t, r2.IsError)
		assert.Empty(t, r2.DropToolResultIDs,
			"reading a different range should not drop the first range")
	})

	t.Run("same range drops previous", func(t *testing.T) {
		tracker4 := NewFileTracker()
		tool4 := newReadFile(localFS{}, dirURI(dir), tracker4, 0)

		ctx1 := agent.WithParentToolCallID(t.Context(), "r1")
		r1 := tool4.Execute(ctx1, `{"path":"a.txt","offset":1,"limit":1}`)
		require.False(t, r1.IsError)

		ctx2 := agent.WithParentToolCallID(t.Context(), "r2")
		r2 := tool4.Execute(ctx2, `{"path":"a.txt","offset":1,"limit":1}`)
		require.False(t, r2.IsError)
		assert.Equal(t, []string{"r1"}, r2.DropToolResultIDs)
	})

	t.Run("failed read does not drop previous read", func(t *testing.T) {
		tracker2 := NewFileTracker()
		tool2 := newReadFile(localFS{}, dirURI(dir), tracker2, 0)

		ctx := agent.WithParentToolCallID(t.Context(), "call_1")
		r1 := tool2.Execute(ctx, `{"path":"a.txt","offset":null,"limit":null}`)
		require.False(t, r1.IsError)

		// Read a non-existent file — should fail and not drop anything.
		ctx2 := agent.WithParentToolCallID(t.Context(), "call_2")
		r2 := tool2.Execute(ctx2, `{"path":"nonexistent.txt","offset":null,"limit":null}`)
		assert.True(t, r2.IsError)
		assert.Nil(t, r2.DropToolResultIDs)
	})
}

func TestApplyPatch_dropsStaleReads(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"),
		[]byte("hello\nsecond line\n"), 0o644))

	tracker := NewFileTracker()
	readTool := newReadFile(localFS{}, dirURI(dir), tracker, 0)
	patchTool := newApplyPatch(localFS{}, dirURI(dir), tracker, &stubLSP{})

	// Read the file first.
	ctx := agent.WithParentToolCallID(t.Context(), "read_1")
	r := readTool.Execute(ctx, `{"path":"a.txt","offset":null,"limit":null}`)
	require.False(t, r.IsError)

	// Apply a patch — should drop the read.
	patchResult := patchTool.Execute(t.Context(),
		`{"patch":"*** Begin Patch\n*** Update File: a.txt\n@@\n hello\n-second line\n+SECOND LINE\n*** End Patch"}`)
	require.False(t, patchResult.IsError)
	assert.Equal(t, []string{"read_1"}, patchResult.DropToolResultIDs)
}

func TestApplyPatch_dropsAllToolReads(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"),
		[]byte("hello\nsecond line\n"), 0o644))

	tracker := NewFileTracker()
	readTool := newReadFile(localFS{}, dirURI(dir), tracker, 0)
	patchTool := newApplyPatch(localFS{}, dirURI(dir), tracker, &stubLSP{})

	// read_file then simulate an outline_file read.
	ctx := agent.WithParentToolCallID(t.Context(), "read_1")
	r := readTool.Execute(ctx, `{"path":"a.txt","offset":null,"limit":null}`)
	require.False(t, r.IsError)
	tracker.RecordRead(filepath.Join(dir, "a.txt"), "outline_file", "", "outline_1")

	// Apply a patch — should drop both read_file and outline_file reads.
	patchResult := patchTool.Execute(t.Context(),
		`{"patch":"*** Begin Patch\n*** Update File: a.txt\n@@\n hello\n-second line\n+SECOND LINE\n*** End Patch"}`)
	require.False(t, patchResult.IsError)
	sort.Strings(patchResult.DropToolResultIDs)
	assert.Equal(t, []string{"outline_1", "read_1"}, patchResult.DropToolResultIDs)
}
