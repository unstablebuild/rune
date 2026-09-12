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

package plugin

import (
	"context"
	"errors"
	"strings"

	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestPluginHandlerCursor(t *testing.T) {
	makeHandler := func(mock *handler.TestHandler) *Handler {
		h := new(Handler)
		h.initState(nopBrowser{}, []string{"cmd"}, 100 /* width */, defaultConfig())
		h.liveHandler = mock
		h.Resize(100, 100)
		return h
	}
	t.Run("corrects coordinates past width-1 and height-1", func(t *testing.T) {
		h := makeHandler(&handler.TestHandler{CursorPos: term.Coordinates{X: 1000, Y: 1000}})
		pos, _, show := h.Cursor()
		require.True(t, show)
		assert.Equal(t, term.Coordinates{X: 99, Y: 99}, pos)
	})
	t.Run("corrects negative coordinates", func(t *testing.T) {
		h := makeHandler(&handler.TestHandler{CursorPos: term.Coordinates{X: -99, Y: -99}})
		pos, _, show := h.Cursor()
		require.True(t, show)
		assert.Equal(t, term.Coordinates{X: 0, Y: 0}, pos)
	})
}

func TestPluginHandlerTitle(t *testing.T) {
	t.Run("defaults to the command and args", func(t *testing.T) {
		h := new(Handler)
		h.initState(nopBrowser{}, []string{"echo", "hi"}, 100, defaultConfig())
		assert.Equal(t, "echo hi", h.Title())
	})
	t.Run("WithTitle overrides", func(t *testing.T) {
		cfg := defaultConfig()
		WithTitle("custom")(&cfg)
		h := new(Handler)
		h.initState(nopBrowser{}, []string{"echo", "hi"}, 100, cfg)
		assert.Equal(t, "custom", h.Title())
	})
}

// TestWithoutBarCommand covers that the option drops the command from
// the plugin's own bar while the status and elapsed components stay.
func TestWithoutBarCommand(t *testing.T) {
	cfg := defaultConfig()
	WithoutBarCommand()(&cfg)
	h := new(Handler)
	h.initState(nopBrowser{}, []string{"echo", "hi"}, 100, cfg)
	assert.Nil(t, h.bar.command, "command component must not be built")
	assert.NotNil(t, h.bar.status)
	assert.NotNil(t, h.bar.elapsed)
	assert.Equal(t, "echo hi", h.Title(), "title is unaffected")
}

func TestPluginHandler(t *testing.T) {
	if ci := os.Getenv("CI"); ci == "true" {
		// it's inherently impossible to know when sh will actually
		// have written in the vte's buffer; do not run
		// on constrained environemnts.
		t.SkipNow()
	}

	suite := []struct {
		description    string
		cmdAndArgs     string
		maxWidth       int
		drawnComponent string
		waitProcess    bool
		alignBottom    bool
	}{
		{
			description: "command with arg",
			cmdAndArgs:  "sleep 2",
			maxWidth:    4,
			waitProcess: true,
			drawnComponent: `
 ▀ sleep 2  0s
              
              
              
              
              `,
		},
		{
			description: "max width 0 doesn't panic",
			cmdAndArgs:  "sleep 2",
			maxWidth:    0,
			waitProcess: true,
			drawnComponent: `
 ▀ sleep 2  0s
              
              
              
              
              `,
		},
		{
			description: "bar at the bottom",
			cmdAndArgs:  "sleep 2",
			maxWidth:    4,
			alignBottom: true,
			waitProcess: true,
			drawnComponent: `
              
              
              
              
              
 ▀ sleep 2  0s`,
		},
	}

	// important so test correctness doesn't depend on host
	shell := os.Getenv("SHELL")
	defer os.Setenv("SHELL", shell)
	os.Setenv("SHELL", "sh")

	ps1 := os.Getenv("PS1")
	os.Setenv("PS1", "$ ")
	defer os.Setenv("PS1", ps1)

	const (
		width  = 14
		height = 6
	)

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			ctx := context.Background()
			tempDir, err := os.MkdirTemp("", "")
			require.NoError(t, err)
			uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
			require.NoError(t, err)
			fileScheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
			require.NoError(t, err)

			ch := make(chan struct{})
			cherr1 := make(chan error)
			cherr2 := make(chan error)
			cherr3 := make(chan error)
			waitInterrupt := term.FuncInterrupter(func(context.Context) error {
				select {
				case ch <- struct{}{}:
				default:
				}
				return nil
			})
			vteCfg := vte.DefaultConfig()
			vteCfg.Watcher = workspaceapi.ChanProcessWatcher(cherr2)
			h := new(Handler)
			barCfg := DefaultBarConfig()
			barCfg.AlignBottom = test.alignBottom
			// do not call Init, which initializes ticker to rebuild elapsed time
			err = h.init(nopBrowser{interrupt: waitInterrupt}, nopBrowser{}, fileScheme,
				fileScheme, nopBrowser{}, strings.Split(test.cmdAndArgs, " "), test.maxWidth,
				WithFrame(false),
				// test order of watchers
				WithProcessWatcher(workspaceapi.ChanProcessWatcher(cherr1)),
				WithVTEConfig(vteCfg),
				WithProcessWatcher(workspaceapi.ChanProcessWatcher(cherr3)),
				WithBarConfig(barCfg),
			)
			require.NoError(t, err)
			h.Resize(width, height)

			w := term.NewStringWriter(width, height)

			tests := []comptest.TestCase{
				{Action: nil, Expected: test.drawnComponent},
			}

			<-ch
			comptest.TestComponent(t, h, w, tests)
			if test.waitProcess {
				assert.NoError(t, <-cherr1)
				assert.NoError(t, <-cherr2)
				assert.NoError(t, <-cherr3)
			}
		})
	}
}

// TestNewReturnsNilHandlerOnError pins the contract that a failed
// build hands back no handler: callers close successful builds they
// abandon, and closing a partially-initialized handler would
// dereference its nil vte.
func TestNewReturnsNilHandlerOnError(t *testing.T) {
	t.Parallel()

	h, err := New(nopBrowser{}, nopBrowser{}, &pluginTestExecutor{},
		&pluginTestExecutor{}, nopBrowser{}, nil, 80)
	require.Error(t, err)
	assert.Nil(t, h)
}

func TestPluginDoneHandlerKeepsLiveVTEPrimaryBufferInPerformanceMode(t *testing.T) {
	t.Parallel()

	h := new(Handler)
	h.cfg = defaultConfig().cfg
	h.bar = newPluginHandlerBar("done", nil, DefaultBarConfig())
	t.Cleanup(func() { _ = h.bar.Close() })
	h.bar.setDone(nil)
	h.width = 5
	h.height = 3

	vteh, err := vte.NewHandler(nopBrowser{}, nopBrowser{},
		&pluginTestExecutor{}, &pluginTestExecutor{}, nopBrowser{}, h.cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = vteh.Close() })
	h.emulator = vteh
	h.liveHandler = h.newUnion(vteh)

	snapshot := vte.Snapshot{
		Width:  5,
		Height: 3,
		Primary: vte.ScreenSnapshot{
			Cells:  term.StringToCells("a\nb\nc\nd\ne\nf"),
			Cursor: term.Coordinates{Y: 5},
		},
	}
	require.NoError(t, h.emulator.RestoreFromSnapshot(snapshot))
	require.False(t, h.emulator.Component().UsedAlternateBuffer())

	require.NotPanics(t, func() {
		h.initializeDoneHandler()
	})

	require.NotPanics(t, func() {
		h.emulator.Resize(5, 2)
	})
}

type nopBrowser struct {
	interrupt term.Interrupter
}

func (n nopBrowser) PublishEvent(ev term.Event) error {
	if ev.Type != term.EventInterrupt {
		return errors.New("unexpected event type")
	}
	if n.interrupt != nil {
		n.interrupt.Interrupt(context.Background())
	}
	return nil
}

func (n nopBrowser) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (n nopBrowser) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (n nopBrowser) Tab(
	uri workspaceapi.URI, icon rune, name string, h browserapi.Handler,
) (
	browserapi.Handler, error,
) {
	panic("should not be called")
}

func (n nopBrowser) SetTabName(workspaceapi.URI, string, term.Attributes) error {
	panic("should not be called")
}

func (n nopBrowser) OnTabExit(workspaceapi.URI) bool {
	panic("should not be called")
}

func (n nopBrowser) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return nil
}

type pluginTestExecutor struct{}

func (e *pluginTestExecutor) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return 0, nil
}

func (e *pluginTestExecutor) Signal(pid workspaceapi.Pid, signal syscall.Signal) error {
	return nil
}

func (e *pluginTestExecutor) Close() error {
	return nil
}

func (e *pluginTestExecutor) NewPty(context.Context) (workspaceapi.Pty, error) {
	mockPtyFile := workspacetest.File{}
	return workspaceapi.Pty{Master: &mockPtyFile, Slave: &mockPtyFile}, nil
}

func (e *pluginTestExecutor) SetPtySize(p workspaceapi.Pty, width, height int) error {
	return nil
}
