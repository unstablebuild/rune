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

package idetask

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unstable.build/rune/internal/handler/handlertest"
)

func TestTaskPointerLeaves(t *testing.T) {
	for _, shown := range []bool{false, true} {
		b := newFakeBrowser()
		m := newTestManager(b, newFakeScheme())
		require.NoError(t, m.RunTask(Task{Name: "build", Cmd: "make", Args: []string{"-j8", "all"}}))
		taskIfc, ok := m.tasks.Load("build")
		require.True(t, ok)
		task := taskIfc.(*Task)
		task.WaitInflight()
		if shown {
			task.onFocus()
		}
		_, gotShown := task.Shown()
		require.Equal(t, shown, gotShown)
		before := task.Info()
		for _, size := range []struct{ w, h int }{{60, 15}, {1, 1}} {
			handlertest.RunPointerLeaves(t, task, size.w, size.h)
		}
		after := task.Info()
		assert.Equal(t, before.Running, after.Running, "the pointer must not pause or restart the task")
		assert.Equal(t, before.Runs, after.Runs, "the pointer must not restart the task")
		assert.Equal(t, before.WindowMinimized, after.WindowMinimized)
	}
}

func TestTaskCloseLeavesItsWindow(t *testing.T) {
	b := newFakeBrowser()
	m := newTestManager(b, newFakeScheme())
	require.NoError(t, m.RunTask(Task{Name: "build", Cmd: "make"}))
	taskIfc, ok := m.tasks.Load("build")
	require.True(t, ok)
	task := taskIfc.(*Task)
	task.WaitInflight()

	require.NoError(t, task.Close())

	wins := b.Created()
	require.Len(t, wins, 1)
	assert.False(t, wins[0].Closed(), "the window may already show other content")
	assert.Empty(t, m.ListTasks())
	handlers := b.CreatedHandlers()
	require.Len(t, handlers, 1)
	assert.Error(t, handlers[0].Close(), "the run must be stopped")
}
