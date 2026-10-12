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
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func TestGrepFiles_boundsWalkdirWorkers(t *testing.T) {
	dir := t.TempDir()
	for i := range 12 {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "file"+string(rune('a'+i))+".txt"),
			[]byte("match\n"), 0o644,
		))
	}

	fs := &recordingLocalFS{
		localFS: localFS{root: dir},
		delay:   20 * time.Millisecond,
	}
	tool := NewGrepFiles(fs, dirURI(dir), NewFileTracker(), nil, nil)

	result := tool.Execute(context.Background(), `{"pattern":"match"}`)
	require.False(t, result.IsError, result.Content)

	// grep sniffs each candidate for binary content on the single
	// goroutine that feeds the readers, so one open can overlap the
	// bounded pool. Runners with few CPUs shrink the pool enough for
	// that extra open to matter.
	wantMax := max(min(runtime.NumCPU(), maxAgentWalkdirWorkers), 1) + 1
	assert.LessOrEqual(t, int(fs.maxConcurrent.Load()), wantMax)
}

type recordingLocalFS struct {
	localFS
	delay time.Duration

	active        atomic.Int64
	maxConcurrent atomic.Int64
}

func (f *recordingLocalFS) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	active := f.active.Add(1)
	for {
		maxConcurrent := f.maxConcurrent.Load()
		if active <= maxConcurrent || f.maxConcurrent.CompareAndSwap(maxConcurrent, active) {
			break
		}
	}
	defer f.active.Add(-1)

	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.localFS.OpenFile(path, flag, mode)
}
