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

	"unstable.build/rune/cmd/rune-agent/agent/taskstore"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

// tuiProgressUpdater implements agent.ProgressUpdater by sending task
// progress events to the TUI channel and the share of completed tasks
// to the chat's tab status.
type tuiProgressUpdater struct {
	tx  chan<- dialoguetui.MessageEvent
	tab *tabStatusReporter

	mu    sync.Mutex
	tasks map[string]string
}

func newTUIProgressUpdater(tx chan<- dialoguetui.MessageEvent, tab *tabStatusReporter) *tuiProgressUpdater {
	return &tuiProgressUpdater{tx: tx, tab: tab, tasks: make(map[string]string)}
}

func (u *tuiProgressUpdater) UpdateTaskProgress(ctx context.Context, task taskstore.Task) {
	u.tab.setProgress(u.track(task))
	select {
	case u.tx <- dialoguetui.MessageEvent{
		Type: dialoguetui.MessageEventTaskProgress,
		TaskProgress: dialoguetui.ProgressTaskEntry{
			ID:          task.ID,
			Subject:     task.Subject,
			Description: task.Description,
			ActiveForm:  task.ActiveForm,
			Status:      task.Status,
		},
	}:
	case <-ctx.Done():
	}
}

// track records the task's status and returns how many tasks are
// completed out of those not deleted.
func (u *tuiProgressUpdater) track(task taskstore.Task) (done, total int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if task.Status == "deleted" {
		delete(u.tasks, task.ID)
	} else {
		u.tasks[task.ID] = task.Status
	}
	for _, status := range u.tasks {
		total++
		if status == "completed" {
			done++
		}
	}
	return done, total
}
