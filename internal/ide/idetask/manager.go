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
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ernestrc/go-multierror"
	"github.com/ernestrc/logd-go/logging"
	"github.com/go-git/go-git/v6/plumbing/format/gitignore"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/ide/vctrl"
)

const (
	// loopWindow and loopThreshold define when a task is considered stuck
	// in a build -> file-change -> rebuild infinite loop: when a single
	// file triggers loopThreshold reruns within loopWindow.
	loopWindow    = 5 * time.Second
	loopThreshold = 5
)

// Manager runs and manages tasks, which are processes that
// run in response to changes in the workspace. See Task for more details.
type Manager struct {
	b                Browser
	tm               browser.TabManager
	scheme           schemeapi.Scheme
	terminal         schemeapi.Terminal
	pluginOpts       []plugin.Option
	ctx              context.Context
	cancelCtx        func()
	tasks            sync.Map
	width            int
	height           int
	frameAttr        term.Attributes
	focusFrameAttr   term.Attributes
	newPlugin        ProgramBuilder
	scheduleNextTick func(func()) bool
}

// Browser extends a browser.Browser with RemoveTab.
type Browser interface {
	browser.Browser
	RemoveTab(h browserapi.Handler) error
}

// NewManager allocates storage for a new Manager and initializes it.
func NewManager(
	b Browser, tm browser.TabManager, scheme schemeapi.Scheme,
	terminal schemeapi.Terminal,
	scheduleNextTick func(func()) bool,
	opts ...plugin.Option,
) *Manager {
	m := new(Manager)
	m.Init(b, tm, scheme, terminal, scheduleNextTick, opts...)
	return m
}

// SetFrameAttr configures the default non-focus frame attrs used by minimized
// task windows. Task status colors override only Bg, preserving Fg/Attrs from
// this value.
func (m *Manager) SetFrameAttr(attr term.Attributes) {
	m.frameAttr = attr
}

// SetFocusFrameAttr configures the frame attrs used when a task window is
// focused (unminimized). Without this, focused task windows would render with
// the non-focus frame attrs and miss the focus highlight.
func (m *Manager) SetFocusFrameAttr(attr term.Attributes) {
	m.focusFrameAttr = attr
}

// Init initializes this Manager with the given browser, scheme,
// terminal and options. terminal is separate from scheme because task
// plugins must spawn their pty through the caller-owned terminal
// decorator rather than the raw workspace.
func (m *Manager) Init(
	b Browser, tm browser.TabManager, scheme schemeapi.Scheme,
	terminal schemeapi.Terminal,
	scheduleNextTick func(func()) bool,
	opts ...plugin.Option,
) {
	m.b = b
	m.tm = tm
	m.scheme = scheme
	m.terminal = terminal
	m.scheduleNextTick = scheduleNextTick
	m.pluginOpts = opts
	m.ctx, m.cancelCtx = context.WithCancel(context.Background())
	m.newPlugin = func(
		publisher browser.EventPublisher, notifications browser.Notifications,
		e schemeapi.Executor, t schemeapi.Terminal, _ browser.TabManager,
		cmdAndArgs []string, maxWidth int, opts ...plugin.Option,
	) (browser.ScrollableFloating, error) {
		// use TabManager passed to this constructor, so we don't need to worry
		// about synchronizing
		h, err := plugin.New(publisher, notifications, e, t, tm,
			cmdAndArgs, maxWidth, opts...)
		if err != nil {
			// An explicit nil keeps the interface nil-comparable for
			// the caller (a typed nil *plugin.Handler would not be).
			return nil, err
		}
		return h, nil
	}
}

// SetProgramBuilder makes the runs started from then on start their
// program with build instead of in a terminal of the workspace.
func (m *Manager) SetProgramBuilder(build ProgramBuilder) {
	m.newPlugin = build
}

// SetMaxWidthHeight is used to ensure that a task's initial VTE is resized
// to an approppiate initial width and height.
func (m *Manager) SetMaxWidthHeight(width, height int) {
	m.width = width
	m.height = height
	m.tasks.Range(func(k, v any) bool {
		v.(*Task).setMaxWidthHeight(width, height)
		return true
	})
}

// FocusTask switches the window manager's focus to the tasks window or tab.
func (m *Manager) FocusTask(name string) bool {
	taskIfc, loaded := m.tasks.Load(name)
	if !loaded {
		return false
	}
	t := taskIfc.(*Task)
	win := t.win
	if win.Closed() {
		var ok bool
		win, ok = t.tab.Window()
		if !ok || win.Closed() {
			return false
		}
	}
	_, err := t.b.SetFocus(win)
	return err == nil
}

// RunTask runs a task in the background and creates a minimized floating window
// that displays the status of the task. When a Task's window is un-minimized,
// the full stdout and stderr of the task can be visualized.
func (m *Manager) RunTask(t Task) error {
	validateTask(t)
	if _, loaded := m.tasks.LoadOrStore(t.Name, &t); loaded {
		return ErrTaskExists
	}
	t.defaultFrameAttr = m.frameAttr
	t.focusFrameAttr = m.focusFrameAttr
	ctx, _, err := t.init(m.ctx, m.b, m.scheme, m.terminal,
		m.newPlugin, m.width, m.height, func() {
			m.tasks.Delete(t.Name)
		}, m.scheduleNextTick, m.pluginOpts...)
	if err != nil {
		m.tasks.Delete(t.Name)
		return fmt.Errorf("init task: %w", err)
	}

	m.startWatch(&t, ctx)

	return nil
}

// startWatch launches the goroutine that reruns t on matching workspace
// changes. ctx is the task lifetime context, so closing the task (which
// cancels ctx) also stops the watcher. The watcher's own cancel is
// recorded on the task so ReplaceTask can stop just this watch and start a
// fresh one without disturbing the task window.
//
// The watch is established inside the goroutine rather than by the caller:
// on Linux notify has no native recursive watcher and falls back to a
// synchronous walk of the whole workspace, which would freeze the GUI
// event loop if Watch ran on the caller. A watch that fails to arm only
// disables auto-rerun: the task is torn down into the visible halted/error
// state (see Task.watchFailed) so the user is told to recreate it, rather
// than left as a silent zombie.
func (m *Manager) startWatch(t *Task, ctx context.Context) {
	watchCtx, watchCancel := context.WithCancel(ctx)
	t.mu.Lock()
	t.watchCancel = watchCancel
	filter := t.Filter
	t.mu.Unlock()
	go debug.CapturePanicReport(func() {
		ch := make(chan schemeapi.EventInfo)
		id, err := m.scheme.Watch("./...", ch, schemeapi.AllEvents()...)
		if err != nil {
			m.log(log.ErrorLevel, "workspace watch: %v", err)
			t.watchFailed(err)
			return
		}
		defer m.scheme.StopWatch(id) //nolint:errcheck
		// The task may have been closed (ctx cancelled) while Watch was
		// arming; stop the freshly-armed watch instead of running it.
		if watchCtx.Err() != nil {
			return
		}
		t.mu.Lock()
		t.watchID = id
		t.mu.Unlock()
		ignore, err := vctrl.LoadGitignore(m.scheme)
		if err != nil {
			m.log(log.ErrorLevel, "load gitignore: %v", err)
			ignore = vctrl.NopMatcher(false)
		}
		matcher := vctrl.NopMatcher(true)
		var filters []gitignore.Pattern
		if filter != "" {
			for segment := range strings.SplitSeq(filter, ",") {
				segment = strings.TrimSpace(segment)
				if segment == "" {
					continue
				}
				filters = append(filters, gitignore.ParsePattern(segment, nil))
			}
		}
		if len(filters) > 0 {
			matcher, err = vctrl.MatcherFromPatterns(m.scheme, filters...)
			if err != nil {
				matcher = vctrl.NopMatcher(true)
				m.log(log.ErrorLevel, "new matcher: %v", err)
			}
		}
		rerunsByFile := make(map[string][]time.Time)
		for {
			select {
			case <-watchCtx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					// Schemes that proxy the watch over a stream close
					// the channel when it ends, and so does closing the
					// workspace; a receive then yields a nil EventInfo.
					// A cancelled watchCtx means this watcher is already
					// being torn down or replaced, so the task it points
					// at must not be halted by a stale watcher.
					if watchCtx.Err() == nil {
						t.watchEnded()
					}
					return
				}
				isDir, _ := ev.IsDir()
				if ignore.Match(ev.URI(), isDir) {
					m.log(log.TraceLevel, "ignoring %s due to gitignore", ev.URI().Path())
					continue
				}

				if !matcher.Match(ev.URI(), isDir) {
					m.log(log.TraceLevel, "ignoring %s due to not matching filters: %v", ev.URI().Path(), filters)
					continue
				}

				var icon string
				switch ev.Event() {
				case schemeapi.Create:
					icon = " "
				case schemeapi.Rename:
					fallthrough // files are flushed by means of renaming them
				case schemeapi.Write:
					icon = " "
				case schemeapi.Remove:
					icon = " "
				}

				if !t.tryRunning(m.b, m.scheme, m.terminal,
					icon+ev.URI().Name()) {
					continue
				}

				path := ev.URI().Path()
				now := time.Now()
				cutoff := now.Add(-loopWindow)
				kept := rerunsByFile[path][:0]
				for _, ts := range rerunsByFile[path] {
					if ts.After(cutoff) {
						kept = append(kept, ts)
					}
				}
				kept = append(kept, now)
				rerunsByFile[path] = kept
				if len(kept) >= loopThreshold {
					m.log(log.ErrorLevel,
						"task %q halted: %s retriggered it %d times within %s",
						t.Name, path, len(kept), loopWindow)
					t.haltLoop(path)
					return
				}
			}
		}
	})
}

// StatusTask returns the task whose current run reports its statuses
// under uri.
func (m *Manager) StatusTask(uri workspaceapi.URI) (*Task, bool) {
	var ret *Task
	m.tasks.Range(func(_, v any) bool {
		t := v.(*Task)
		if u, ok := t.StatusURI(); ok && u.Equal(uri) {
			ret = t
			return false
		}
		return true
	})
	return ret, ret != nil
}

// ListTasks creates a floating window that displays
// all the running tasks and its status details.
func (m *Manager) ListTasks() (ret []TaskInfo) {
	m.tasks.Range(func(k, v any) bool {
		ret = append(ret, v.(*Task).Info())
		return true
	})
	return
}

// StopTask stops the task with the given name or returns
// an error if the task doesn't exist or there was an error stopping
// it.
func (m *Manager) StopTask(name string) (err error) {
	info, ok := m.tasks.LoadAndDelete(name)
	if !ok {
		return errors.New("task with this name does not exist")
	}
	t := info.(*Task)
	t.doClose()
	<-info.(*Task).doneWaitCh
	return t.closeView()
}

// ReplaceTask updates an existing task's command, args, and filter in
// place and runs it, preserving the task's window or tab. The previous
// watcher is stopped and a fresh one is started so the new filter takes
// effect and a task that halted itself on an infinite loop is re-armed
// (its old watcher had already exited). The new watch arms asynchronously,
// so ReplaceTask returns before it is known to have armed; a watch that
// fails to arm tears the task into the visible halted/error state (see
// Task.watchFailed) rather than failing the replace.
func (m *Manager) ReplaceTask(spec Task) error {
	info, ok := m.tasks.Load(spec.Name)
	if !ok {
		return errors.New("task with this name does not exist")
	}
	task := info.(*Task)

	task.mu.Lock()
	task.Cmd = spec.Cmd
	task.Args = spec.Args
	task.Filter = spec.Filter
	task.cmdAndArgs = append([]string{task.Cmd}, task.Args...)
	task.loopHalted = false
	task.lastExit = nil
	watchCancel := task.watchCancel
	taskCtx := task.ctx
	task.mu.Unlock()

	if watchCancel != nil {
		watchCancel()
	}
	// Arm a fresh watch asynchronously so the new filter takes effect. A
	// watch that fails to arm tears the task into the halted/error state so
	// the user is told to recreate it rather than left with a silent zombie.
	m.startWatch(task, taskCtx)

	task.tryRunning(m.b, m.scheme, m.terminal, "  task")
	return nil
}

// OnFocus satisfies handler.WindowSubscriber.
func (m *Manager) OnFocus(prevFocus, newFocus handler.Window) {
	m.onFocus(prevFocus, newFocus)
}

// WaitInflight blocks until every currently-tracked task has settled
// its async expandAndStart spawn goroutine and the resulting watcher
// hand-off. Intended for tests where the async pipeline that drives
// task bar / done state must be drained before asserting rendered
// output.
func (m *Manager) WaitInflight() {
	m.tasks.Range(func(_, v any) bool {
		v.(*Task).WaitInflight()
		return true
	})
}

// Close stops all tasks and closes this Manager's resources.
func (m *Manager) Close() error {
	m.cancelCtx()
	var ret error
	m.tasks.Range(func(k, v any) bool {
		m.tasks.Delete(k)
		t := v.(*Task)
		t.doClose()
		<-t.doneWaitCh
		if err := t.closeView(); err != nil {
			ret = multierror.Append(ret, err)
		}
		return true
	})
	return ret
}

// allows calling it directly in tests with fake windows
type window interface {
	ID() uint64
	Content() tui.Handler
}

func (m *Manager) onFocus(prevFocus, newFocus window) {
	m.tasks.Range(func(k, v any) bool {
		task := v.(*Task)
		winID := task.win.WindowID()
		if winID == prevFocus.ID() {
			task.onUnfocus()
		} else if winID == newFocus.ID() {
			task.onFocus()
		}
		return true
	})
	t, ok := newFocus.Content().(*browser.Tab)
	if !ok {
		return
	}
	task, ok := t.Handler().(*Task)
	if !ok {
		return
	}
	if task.tab != nil {
		t.ResetAttrs()
	}
}

func (m *Manager) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithFields(log.Fields{
		logging.KeyClass: "idetask.Manager",
	}).Logf(level, msg, args...)
}

func validateTask(t Task) {
	if t.Cmd == "" {
		panic("task command must not be empty")
	}
	if t.Name == "" {
		panic("task name must not be empty")
	}
}
