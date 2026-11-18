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

package ide

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/extension/extensionv2"
	"unstable.build/rune/internal/ide/console/pkgconsole"
	"unstable.build/rune/internal/ide/hostenv"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/localstorage"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace"
)

func TestStartInstalledExtensions(t *testing.T) {
	t.Parallel()

	newHandler := func(runners ...*recordingRunner) *workspaceManagerHandler {
		h := &workspaceManagerHandler{mu: new(sync.Mutex)}
		for i, runner := range runners {
			ws := &workspaceHandler{}
			ws.Extensions.Store(extension.Runner(runner))
			h.workspaces[i] = ws
		}
		h.workspaceCount = len(runners)
		return h
	}

	t.Run("starts on workspace runners but not home", func(t *testing.T) {
		t.Parallel()
		cfg := configWithExtension(t, "rune-agent", "rune-agent-bin")
		home := &recordingRunner{}
		wsRunner := &recordingRunner{}
		h := newHandler(wsRunner)
		h.homeRunner = home

		started := h.startInstalledExtensions(cfg, []string{"rune-agent", "other-ext"})
		assert.Equal(t, []string{"rune-agent"}, started,
			"only ids present in the config are reported")

		assert.Empty(t, home.runCalls(),
			"home runner must not start user extensions")

		wsCalls := wsRunner.runCalls()
		require.Len(t, wsCalls, 1)
		assert.Equal(t, "rune-agent", wsCalls[0].id)
		assert.Equal(t, "rune-agent-bin", wsCalls[0].path)
	})

	t.Run("ignores ids absent from the config", func(t *testing.T) {
		t.Parallel()
		cfg := configWithExtension(t, "rune-agent", "rune-agent-bin")
		home := &recordingRunner{}
		h := newHandler()
		h.homeRunner = home

		started := h.startInstalledExtensions(cfg, []string{"other-ext"})
		assert.Empty(t, started)
		assert.Empty(t, home.runCalls())
	})

	t.Run("no ids is a no-op", func(t *testing.T) {
		t.Parallel()
		wsRunner := &recordingRunner{}
		h := newHandler(wsRunner)
		assert.Empty(t, h.startInstalledExtensions(
			configWithExtension(t, "rune-agent", "rune-agent-bin"), nil))
		assert.Empty(t, wsRunner.runCalls())
	})

	t.Run("already-running runner still counts as started", func(t *testing.T) {
		t.Parallel()
		cfg := configWithExtension(t, "rune-agent", "rune-agent-bin")
		wsRunner := &recordingRunner{
			runErr: fmt.Errorf("extension %q: %w", "rune-agent",
				extensionv2.ErrExtensionAlreadyRunning),
		}
		h := newHandler(wsRunner)
		started := h.startInstalledExtensions(cfg, []string{"rune-agent"})
		assert.Equal(t, []string{"rune-agent"}, started)
		assert.Len(t, wsRunner.runCalls(), 1)
	})

	t.Run("skips extensions configured without a path", func(t *testing.T) {
		t.Parallel()
		wsRunner := &recordingRunner{}
		h := newHandler(wsRunner)

		started := h.startInstalledExtensions(configWithConfigOnlyExtension(t),
			[]string{"rune-agent", "other-ext"})
		assert.Equal(t, []string{"other-ext"}, started,
			"configuring an extension does not start it")
		calls := wsRunner.runCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, "other-ext", calls[0].id)
	})

	t.Run("starts nothing when every id only configures an extension", func(t *testing.T) {
		t.Parallel()
		cfg := ideConfig{
			cfg: map[string]any{"extensions": map[string]any{
				"rune-agent": map[string]any{
					"config": map[string]any{"status_bar": map[string]any{"enabled": false}},
				},
				"fuzzy_search": map[string]any{"config": map[string]any{"limit": 10}},
			}},
			errors: map[string]error{},
		}
		wsRunner := &recordingRunner{}
		h := newHandler(wsRunner)

		assert.Empty(t, h.startInstalledExtensions(cfg, []string{"rune-agent", "fuzzy_search"}))
		assert.Empty(t, wsRunner.runCalls())
	})

	t.Run("skips extensions without a path on every workspace", func(t *testing.T) {
		t.Parallel()
		first, second := &recordingRunner{}, &recordingRunner{}
		h := newHandler(first, second)

		started := h.startInstalledExtensions(configWithConfigOnlyExtension(t),
			[]string{"rune-agent", "other-ext"})
		assert.Equal(t, []string{"other-ext"}, started)
		for _, runner := range []*recordingRunner{first, second} {
			calls := runner.runCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, "other-ext", calls[0].id)
			assert.Equal(t, "other-ext-bin", calls[0].path)
		}
	})

	t.Run("starts an extension once a later install gives it a path", func(t *testing.T) {
		t.Parallel()
		cfg := configWithConfigOnlyExtension(t)
		wsRunner := &recordingRunner{}
		h := newHandler(wsRunner)

		assert.Empty(t, h.startInstalledExtensions(cfg, []string{"rune-agent"}),
			"a theme styling Rune Agent installs before the agent")
		assert.Empty(t, wsRunner.runCalls())

		agent := cfg.cfg["extensions"].(map[string]any)["rune-agent"].(map[string]any)
		agent["path"] = "rune-agent-bin"
		assert.Equal(t, []string{"rune-agent"},
			h.startInstalledExtensions(cfg, []string{"rune-agent"}))
		calls := wsRunner.runCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, "rune-agent", calls[0].id)
		assert.Equal(t, "rune-agent-bin", calls[0].path)
		assert.Equal(t, map[string]any{"status_bar": map[string]any{"enabled": true}},
			configValues(calls[0].cfg),
			"the extension starts with the config installed before it")
	})
}

func TestInitExtensions(t *testing.T) {
	t.Parallel()

	type started struct {
		path   string
		config map[string]any
	}
	statusBar := map[string]any{"status_bar": map[string]any{"enabled": false}}

	tests := []struct {
		name       string
		builtins   map[string]Extension
		extensions map[string]any
		want       map[string]started
	}{
		{
			name: "starts an extension with its path and config",
			extensions: map[string]any{
				"ext": map[string]any{"path": "ext-bin", "config": statusBar},
			},
			want: map[string]started{"ext": {path: "ext-bin", config: statusBar}},
		},
		{
			name:       "starts an extension without config with an empty config",
			extensions: map[string]any{"ext": map[string]any{"path": "ext-bin"}},
			want:       map[string]started{"ext": {path: "ext-bin", config: map[string]any{}}},
		},
		{
			name: "does not start an extension configured without a path",
			extensions: map[string]any{
				"rune-agent": map[string]any{"config": statusBar},
				"ext":        map[string]any{"path": "ext-bin"},
			},
			want: map[string]started{"ext": {path: "ext-bin", config: map[string]any{}}},
		},
		{
			name: "starts nothing when every extension is configured without a path",
			extensions: map[string]any{
				"rune-agent":   map[string]any{"config": statusBar},
				"fuzzy_search": map[string]any{"config": map[string]any{"limit": 10}},
			},
		},
		{
			name:       "does not start an extension with an empty path",
			extensions: map[string]any{"ext": map[string]any{"path": "", "config": statusBar}},
		},
		{
			name:       "does not start an extension whose path is not a string",
			extensions: map[string]any{"ext": map[string]any{"path": 42}},
		},
		{
			name: "starts built-in extensions beside extensions without a path",
			builtins: map[string]Extension{
				"builtin": {ID: "builtin", CmdAndArgs: "builtin-bin"},
			},
			extensions: map[string]any{"rune-agent": map[string]any{"config": statusBar}},
			want: map[string]started{
				"builtin": {path: "builtin-bin", config: map[string]any{}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := &recordingRunner{}
			h := &workspaceManagerHandler{builtinExtensions: tt.builtins}
			cfg := ideConfig{
				cfg:    map[string]any{"extensions": tt.extensions},
				errors: map[string]error{},
			}

			done := make(chan struct{})
			go debug.CapturePanicReport(func() {
				defer close(done)
				h.initExtensions(runner, cfg)
			})
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("initExtensions did not return after starting its extensions")
			}

			calls := runner.runCalls()
			var got map[string]started
			for _, c := range calls {
				if got == nil {
					got = map[string]started{}
				}
				got[c.id] = started{path: c.path, config: configValues(c.cfg)}
			}
			assert.Equal(t, tt.want, got)
			assert.Len(t, calls, len(tt.want), "each extension starts once")
		})
	}
}

func TestStartUserExtensionIdempotent(t *testing.T) {
	t.Parallel()
	p := configWithExtension(t, "rune-agent", "rune-agent-bin").
		extensions()["rune-agent"]
	path, pconfig := extensionRunArgs(p)

	t.Run("already running treated as success", func(t *testing.T) {
		t.Parallel()
		runner := &recordingRunner{
			runErr: fmt.Errorf("extension %q: %w", "rune-agent",
				extensionv2.ErrExtensionAlreadyRunning),
		}
		assert.NoError(t, startUserExtension(runner, "rune-agent", path, pconfig))
	})

	t.Run("other error propagates", func(t *testing.T) {
		t.Parallel()
		runner := &recordingRunner{runErr: errors.New("boom")}
		err := startUserExtension(runner, "rune-agent", path, pconfig)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
}

func TestApplyConfigChange(t *testing.T) {
	t.Parallel()

	// newHandler returns a handler running with old whose next reload
	// reads cur.
	newHandler := func(
		t *testing.T, old, cur ideConfig,
		hook func(ConfigChange) (ConfigChangeResult, error),
	) (*workspaceManagerHandler, *recordingRunner) {
		t.Helper()
		wsRunner := &recordingRunner{}
		ws := &workspaceHandler{}
		ws.Extensions.Store(extension.Runner(wsRunner))
		h := &workspaceManagerHandler{
			mu:               new(sync.Mutex),
			homeRunner:       &recordingRunner{},
			reloadConfig:     func() (ideConfig, error) { return cur, nil },
			configChangeHook: hook,
			tutorialsInstalled: func(names []string) ([]string, error) {
				t.Fatalf("tutorialsInstalled(%v) must not be called", names)
				return nil, nil
			},
		}
		h.configChange.lastApplied = old.cfg
		h.workspaces[0] = ws
		h.workspaceCount = 1
		return h, wsRunner
	}
	base := func() ideConfig {
		return ideConfig{cfg: map[string]any{"editor": map[string]any{"mode": "vim"}},
			errors: map[string]error{}}
	}
	withExt := configWithExtension(t, "rune-agent", "rune-agent-bin")
	withEnv := ideConfig{cfg: map[string]any{
		"editor": map[string]any{"mode": "vim"},
		"gui":    map[string]any{"env": map[string]any{"FOO": "bar"}},
	}, errors: map[string]error{}}
	liveEnv := func(change ConfigChange) (ConfigChangeResult, error) {
		if !change.TouchesPath("gui", "env") {
			return ConfigChangeResult{}, nil
		}
		return ConfigChangeResult{LivePaths: [][]string{{"gui", "env"}}}, nil
	}

	t.Run("an unchanged config applies nothing", func(t *testing.T) {
		t.Parallel()
		h, wsRunner := newHandler(t, withExt, withExt,
			func(ConfigChange) (ConfigChangeResult, error) {
				t.Fatal("the hook must not run for an unchanged config")
				return ConfigChangeResult{}, nil
			})
		change, result, err := h.applyConfigChange("test")
		require.NoError(t, err)
		assert.Nil(t, change.New)
		assert.Empty(t, result.LivePaths)
		assert.Empty(t, wsRunner.runCalls())
	})

	t.Run("a failed reload keeps the config in effect", func(t *testing.T) {
		t.Parallel()
		h, _ := newHandler(t, base(), withExt, nil)
		h.reloadConfig = func() (ideConfig, error) { return ideConfig{}, errors.New("boom") }
		change, _, err := h.applyConfigChange("test")
		require.ErrorContains(t, err, "boom")
		assert.Nil(t, change.New)
		assert.Equal(t, base().cfg, h.configChange.lastApplied)
	})

	t.Run("gui.env only runs the hook, no extension start", func(t *testing.T) {
		t.Parallel()
		var hookCalls int
		h, wsRunner := newHandler(t, base(), withEnv,
			func(change ConfigChange) (ConfigChangeResult, error) {
				hookCalls++
				assert.Equal(t, "test", change.Source)
				return liveEnv(change)
			})
		change, result, err := h.applyConfigChange("test")
		require.NoError(t, err)
		assert.Equal(t, withEnv.cfg, change.New)
		assert.Equal(t, [][]string{{"gui", "env"}}, result.LivePaths)
		assert.Equal(t, 1, hookCalls)
		assert.Empty(t, wsRunner.runCalls())
		assert.Equal(t, withEnv.cfg, h.configChange.lastApplied)
	})

	t.Run("an added extension starts after the hook ran", func(t *testing.T) {
		t.Parallel()
		var wsRunner *recordingRunner
		h, runner := newHandler(t, base(), withExt,
			func(ConfigChange) (ConfigChangeResult, error) {
				assert.Empty(t, wsRunner.runCalls(), "the hook runs before extensions start")
				return ConfigChangeResult{}, nil
			})
		wsRunner = runner
		_, result, err := h.applyConfigChange("test")
		require.NoError(t, err)
		assert.Equal(t, [][]string{{"extensions", "rune-agent"}}, result.LivePaths)
		require.Len(t, wsRunner.runCalls(), 1)
		assert.Equal(t, "rune-agent", wsRunner.runCalls()[0].id)
	})

	t.Run("a hook error still starts the extension", func(t *testing.T) {
		t.Parallel()
		h, wsRunner := newHandler(t, base(), withExt,
			func(ConfigChange) (ConfigChangeResult, error) {
				return ConfigChangeResult{}, errors.New("boom")
			})
		_, result, err := h.applyConfigChange("test")
		require.ErrorContains(t, err, "boom")
		assert.Equal(t, [][]string{{"extensions", "rune-agent"}}, result.LivePaths)
		require.Len(t, wsRunner.runCalls(), 1)
	})

	t.Run("no hook still starts the extension", func(t *testing.T) {
		t.Parallel()
		h, wsRunner := newHandler(t, base(), withExt, nil)
		_, result, err := h.applyConfigChange("test")
		require.NoError(t, err)
		assert.Equal(t, [][]string{{"extensions", "rune-agent"}}, result.LivePaths)
		require.Len(t, wsRunner.runCalls(), 1)
	})

	t.Run("an extension added without a path is neither started nor live", func(t *testing.T) {
		t.Parallel()
		cur := ideConfig{cfg: map[string]any{"extensions": map[string]any{
			"rune-agent": map[string]any{
				"config": map[string]any{"status_bar": map[string]any{"enabled": true}},
			},
		}}, errors: map[string]error{}}
		h, wsRunner := newHandler(t, base(), cur, nil)
		_, result, err := h.applyConfigChange("test")
		require.NoError(t, err)
		assert.Empty(t, result.LivePaths,
			"the config waits for the package that installs the extension")
		assert.Empty(t, wsRunner.runCalls())
	})

	t.Run("only extensions given a path start", func(t *testing.T) {
		t.Parallel()
		h, wsRunner := newHandler(t, base(), configWithConfigOnlyExtension(t), nil)
		_, result, err := h.applyConfigChange("test")
		require.NoError(t, err)
		assert.Equal(t, [][]string{{"extensions", "other-ext"}}, result.LivePaths)
		calls := wsRunner.runCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, "other-ext", calls[0].id)
	})

	t.Run("the config of an extension with the same path is live", func(t *testing.T) {
		t.Parallel()
		cur := configWithExtension(t, "rune-agent", "rune-agent-bin")
		cur.cfg["extensions"].(map[string]any)["rune-agent"].(map[string]any)["config"] =
			map[string]any{"status_bar": map[string]any{"enabled": false}}
		h, wsRunner := newHandler(t, withExt, cur, nil)
		_, result, err := h.applyConfigChange("test")
		require.NoError(t, err)
		assert.Equal(t, [][]string{{"extensions", "rune-agent"}}, result.LivePaths,
			"the running extension fetches its config again")
		assert.Empty(t, wsRunner.runCalls(), "it is not started twice")
	})

	t.Run("an extension whose path changed needs a restart", func(t *testing.T) {
		t.Parallel()
		h, wsRunner := newHandler(t, withExt,
			configWithExtension(t, "rune-agent", "rune-agent-v2"), nil)
		_, result, err := h.applyConfigChange("test")
		require.NoError(t, err)
		assert.Empty(t, result.LivePaths)
		assert.Empty(t, wsRunner.runCalls())
	})

	t.Run("added tutorials are registered", func(t *testing.T) {
		t.Parallel()
		cur := ideConfig{cfg: map[string]any{"tutorials": map[string]any{
			"go-intro": "go-intro.star", "loaded": "loaded.star",
		}}, errors: map[string]error{}}
		h, _ := newHandler(t, base(), cur, nil)
		var gotNames []string
		h.tutorialsInstalled = func(names []string) ([]string, error) {
			gotNames = names
			return []string{"go-intro"}, nil
		}
		_, result, err := h.applyConfigChange("test")
		require.NoError(t, err)
		assert.Equal(t, [][]string{{"tutorials", "go-intro"}}, result.LivePaths,
			"only the tutorials made live are reported")
		assert.Equal(t, []string{"go-intro", "loaded"}, gotNames)
	})

	t.Run("a tutorial error is joined onto the error", func(t *testing.T) {
		t.Parallel()
		cur := ideConfig{cfg: map[string]any{"tutorials": map[string]any{
			"go-intro": "go-intro.star",
		}}, errors: map[string]error{}}
		h, _ := newHandler(t, base(), cur, nil)
		h.tutorialsInstalled = func([]string) ([]string, error) {
			return nil, errors.New("tutorial boom")
		}
		_, _, err := h.applyConfigChange("test")
		require.ErrorContains(t, err, "tutorial boom")
	})

	t.Run("sounds are live", func(t *testing.T) {
		t.Parallel()
		cur := ideConfig{cfg: map[string]any{"sounds": map[string]any{
			"ding": "$RUNE_DATADIR/lib/pkg/ding.wav",
		}}, errors: map[string]error{}}
		h, _ := newHandler(t, base(), cur, nil)
		_, result, err := h.applyConfigChange("test")
		require.NoError(t, err)
		assert.Equal(t, [][]string{{"sounds"}}, result.LivePaths)
	})

	t.Run("other keys need a restart", func(t *testing.T) {
		t.Parallel()
		cur := base()
		cur.cfg["editor"] = map[string]any{"mode": "emacs"}
		h, _ := newHandler(t, base(), cur, liveEnv)
		change, result, err := h.applyConfigChange("test")
		require.NoError(t, err)
		assert.Empty(t, result.LivePaths)
		assert.Equal(t, [][]string{{"editor", "mode"}}, change.ChangedPaths())
	})
}

func TestConfigChangedCoalescesConcurrentCalls(t *testing.T) {
	t.Parallel()
	cur := configWithExtension(t, "rune-agent", "rune-agent-bin")
	var reloads atomic.Int32
	gate := make(chan struct{})
	var once sync.Once
	h := &workspaceManagerHandler{
		mu: new(sync.Mutex),
		reloadConfig: func() (ideConfig, error) {
			reloads.Add(1)
			once.Do(func() { <-gate })
			return cur, nil
		},
		tutorialsInstalled: func([]string) ([]string, error) { return nil, nil },
	}
	h.notifications = newWorkspaceNotifications(nil, notificationsConfig(), h)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.configChanged("first")
	}()
	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		time.Second, time.Millisecond, "the first call is reloading")
	h.configChanged("second")
	h.configChanged("third")
	close(gate)
	<-done
	assert.Equal(t, int32(2), reloads.Load(),
		"the calls made during a run coalesce into one more run")
	assert.Equal(t, cur.cfg, h.configChange.lastApplied)
	assert.False(t, h.configChange.running)
}

func TestNotifyConfigChange(t *testing.T) {
	t.Parallel()
	old := map[string]any{"editor": map[string]any{"mode": "vim"}}
	for _, tt := range []struct {
		name  string
		new   map[string]any
		live  [][]string
		level browserapi.NotificationLevel
		msg   string
	}{
		{
			name: "all live",
			new: map[string]any{
				"editor": map[string]any{"mode": "vim"},
				"gui":    map[string]any{"env": map[string]any{"FOO": "bar"}},
				"extensions": map[string]any{
					"rune-agent": map[string]any{"path": "rune-agent-bin"},
				},
			},
			live:  [][]string{{"gui", "env"}, {"extensions", "rune-agent"}},
			level: browserapi.LevelSuccess,
			msg: "Config changed (package pkg). All changes are in effect now: " +
				"extensions.rune-agent, gui.env.",
		},
		{
			name:  "none live",
			new:   map[string]any{"editor": map[string]any{"mode": "emacs", "tabspaces": 2}},
			level: browserapi.LevelInfo,
			msg: "Config changed (package pkg). None of the changes are in effect yet; " +
				"restart the program to load: editor.mode, editor.tabspaces.",
		},
		{
			name: "some live",
			new: map[string]any{
				"editor": map[string]any{"mode": "vim"},
				"sounds": map[string]any{"ding": "ding.wav"},
				"extensions": map[string]any{
					"rune-agent": map[string]any{"config": map[string]any{"a": 1}},
				},
			},
			live:  [][]string{{"sounds"}},
			level: browserapi.LevelInfo,
			msg: "Config changed (package pkg). In effect now: sounds.ding. " +
				"Restart the program to load: extensions.rune-agent.",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := idepkgtest.NewNotifications(t)
			notifyConfigChange(n, ConfigChange{Old: old, New: tt.new, Source: "package pkg"},
				ConfigChangeResult{LivePaths: tt.live})
			active := n.Active()
			require.Len(t, active, 1)
			for _, noti := range active {
				assert.Equal(t, tt.level, noti.Level)
				assert.Equal(t, tt.msg, noti.Msg)
			}
		})
	}

	t.Run("nothing changed notifies nothing", func(t *testing.T) {
		t.Parallel()
		n := idepkgtest.NewNotifications(t)
		notifyConfigChange(n, ConfigChange{Old: old, New: old, Source: "x"}, ConfigChangeResult{})
		assert.Empty(t, n.Active())
	})
}

func TestPkgInstallStartsExtensionWithPackageEnv(t *testing.T) {
	const (
		envKey = "RUNE_TEST_PKG_EXT_ENV"
		envVal = "from-package"
		extID  = "pkgext"
		pkgID  = "envextpkg"
	)
	t.Setenv(envKey, "")

	dir := t.TempDir()
	extPath := filepath.Join(dir, "pkgext-bin")

	configFile, err := os.CreateTemp(dir, "config.*.yaml")
	require.NoError(t, err)
	_, err = configFile.WriteString("editor:\n  mode: modal\n")
	require.NoError(t, err)
	require.NoError(t, configFile.Close())
	configPath := configFile.Name()

	pkgs := idepkgtest.MakePackages(release.Package{Name: pkgID, Latest: "1"})
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: pkgID, Version: "1"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.SetMissProgressComplete(true)
	rm.SetTarball(pkgID, makeEnvExtPkgTarball(t, envKey, envVal, extID, extPath))

	recorder := &recordingRunner{captureEnv: envKey}

	// envHook mirrors cmd/rune's configChangeHook: when the change
	// touches gui.env it applies the package environment to the live process.
	envHook := func(change ConfigChange) (ConfigChangeResult, error) {
		if !change.TouchesPath("gui", "env") {
			return ConfigChangeResult{}, nil
		}
		if err := os.Setenv(envKey, envVal); err != nil {
			return ConfigChangeResult{}, err
		}
		return ConfigChangeResult{LivePaths: [][]string{{"gui", "env"}}}, nil
	}

	m := newPkgInstallExtHandler(t, configPath, rm,
		recordingExtensionsRunner{runner: recorder}, envHook)
	defer m.Close()

	// User extensions never start on the home workspace, so open a real
	// workspace for the freshly-installed extension to start on.
	wsURI, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	require.NoError(t, m.addWorkspace(wsURI, true, false, -1))
	m.quiesce()

	h := pkgconsole.New(pkgconsole.Config{Manager: m.pkgmanager.pkg})
	_, err = h.HandleCommand(context.Background(), repl.Command{
		Name: pkgconsole.CommandName,
		Args: []string{"install", pkgID},
	}, repl.NopProgressWriter())
	require.NoError(t, err)

	// The package config on disk must carry the extension entry, and the
	// user config include it.
	merged, err := os.ReadFile(filepath.Join(m.sixDir, "etc", pkgID+".yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(merged), extID,
		"install must write the package config to etc")
	userCfg, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(userCfg), "$RUNE_DATADIR/etc/"+pkgID+".yaml",
		"install must include the package config in the user config")

	var calls []runCall
	require.Eventually(t, func() bool {
		calls = recorder.runCalls()
		return len(calls) > 0
	}, 10*time.Second, 20*time.Millisecond,
		"installing a package with an extensions entry must start the extension")

	var started *runCall
	for i := range calls {
		if calls[i].id == extID {
			started = &calls[i]
			break
		}
	}
	require.NotNil(t, started, "extension %q was not started, calls: %+v", extID, calls)
	assert.Equal(t, envVal, started.envVal,
		"extension must be started after the package gui.env was applied so it "+
			"inherits the package environment")
}

func TestPkgInstallExtensionConfigWithoutPath(t *testing.T) {
	const (
		themeConfig = "extensions:\n  pkgext:\n    config:\n      status_bar:\n" +
			"        enabled: false\n"
		extConfig = "extensions:\n  pkgext:\n    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID/ext\n"
	)
	dir := t.TempDir()
	h := &restartHost{dir: dir, dataDir: t.TempDir(), configPath: filepath.Join(dir, "rune.yaml")}
	h.rm = idepkgtest.NewReleaseManager(
		idepkgtest.MakePackages(
			release.Package{Name: "themepkg", Latest: "1"},
			release.Package{Name: "extpkg", Latest: "1"}),
		idepkgtest.MakeBundles(
			[]release.Bundle{{Package: "themepkg", Version: "1"}},
			[]release.Bundle{{Package: "extpkg", Version: "1"}}))
	h.rm.SetMissProgressComplete(true)
	h.rm.SetTarball("themepkg", idepkgtest.MakeTarball(t, map[string]string{
		"config.yaml":    themeConfig,
		"lib/readme.txt": "themepkg\n",
	}))
	h.rm.SetTarball("extpkg", idepkgtest.MakeTarball(t, map[string]string{
		"config.yaml":    extConfig,
		"lib/readme.txt": "extpkg\n",
	}))
	// userext starts with every workspace, so its start shows the workspace
	// has started the extensions it is going to.
	h.writeConfig(t, "editor:\n  mode: modal\n"+
		"extensions:\n  userext:\n    path: userext-bin\n")

	callsFor := func(e *startedEditor, id string) []runCall {
		return slices.DeleteFunc(e.runner.runCalls(), func(c runCall) bool { return c.id != id })
	}
	openWorkspace := func(e *startedEditor) {
		t.Helper()
		e.open(t)
		require.Eventually(t, func() bool { return len(callsFor(e, "userext")) > 0 },
			10*time.Second, 20*time.Millisecond, "the workspace never started userext")
	}
	install := func(e *startedEditor, pkgID string) {
		t.Helper()
		ui := recordingPkgUI{Notifications: idepkgtest.NewNotifications(t), t: t}
		require.NoError(t, e.PackageManager().InstallPackageVersion(
			idepkg.WithUI(context.Background(), ui), pkgID, "1", repl.NopProgressWriter()))
		e.drain()
	}
	pkgext := func(e *startedEditor) any {
		t.Helper()
		e.mu.Lock()
		defer e.mu.Unlock()
		cfg, err := e.workspaceHandler.reloadConfig()
		require.NoError(t, err)
		return cfg.cfg["extensions"].(map[string]any)["pkgext"]
	}

	e := h.start(t, launch{})
	openWorkspace(e)

	install(e, "themepkg")
	assert.Contains(t, flatScreen(e.screen()), "Config changed (package themepkg). None of the "+
		"changes are in effect yet; restart the program to load: extensions.pkgext.")
	assert.Equal(t, map[string]any{
		"config": map[string]any{"status_bar": map[string]any{"enabled": false}},
	}, pkgext(e))
	assert.Empty(t, callsFor(e, "pkgext"), "installing config for an extension does not start it")
	e.stop(t)

	e = h.start(t, launch{})
	openWorkspace(e)
	assert.Never(t, func() bool { return len(callsFor(e, "pkgext")) > 0 },
		200*time.Millisecond, 10*time.Millisecond,
		"opening a workspace does not start an extension that has no path")

	install(e, "extpkg")
	assert.Contains(t, flatScreen(e.screen()),
		"Config changed (package extpkg). All changes are in effect now: extensions.pkgext.")
	assert.Equal(t, map[string]any{
		"config": map[string]any{"status_bar": map[string]any{"enabled": false}},
		"path":   "$RUNE_DATADIR/lib/extpkg/ext",
	}, pkgext(e), "the extension's package adds the path beside the theme's config")
	calls := callsFor(e, "pkgext")
	require.Len(t, calls, 1, "the extension starts on the open workspace")
	assert.Equal(t, filepath.Join(h.dataDir, "lib", "extpkg", "ext"),
		hostenv.ExpandDataDir(calls[0].path, h.dataDir))
	assert.Equal(t, map[string]any{"status_bar": map[string]any{"enabled": false}},
		configValues(calls[0].cfg), "the extension starts with the theme's config")
}

// flatScreen is the text of a drawn screen without its frames, each run
// of blanks one space, so a wrapped notification reads as one line.
func flatScreen(screen string) string {
	text := strings.Map(func(r rune) rune {
		if strings.ContainsRune("│┌┐└┘├┤┬┴┼─", r) {
			return ' '
		}
		return r
	}, screen)
	return strings.Join(strings.Fields(text), " ")
}

func TestPkgInstallRegistersTutorialLive(t *testing.T) {
	const (
		pkgID    = "tutpkg"
		tutName  = "go-intro"
		tutorial = "def run():\n    wait_event(event=\"open\", title=\"hi\", text=\"hello\")\n" +
			"tutorial(entry=run)\n"
	)

	dir := t.TempDir()
	tutPath := filepath.Join(dir, "go-intro.star")
	require.NoError(t, os.WriteFile(tutPath, []byte(tutorial), 0o644))

	configPath := filepath.Join(dir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("editor:\n  mode: modal\n"), 0o644))

	wsFile := filepath.Join(dir, "seed.go")
	require.NoError(t, os.WriteFile(wsFile, nil, 0o644))

	pkgs := idepkgtest.MakePackages(release.Package{Name: pkgID, Latest: "1"})
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: pkgID, Version: "1"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.SetMissProgressComplete(true)
	rm.SetTarball(pkgID, makeTutorialPkgTarball(t, map[string]string{tutName: tutPath}))

	mu := new(sync.Mutex)
	// dataDir/storage live outside the workspace (like production's
	// ~/.rune) so background storage writes don't feed the
	// workspace FS watcher while the install is in flight.
	dataDir := t.TempDir()
	sched, drainSched := newTestScheduler(t, mu)
	i, err := New(dir, configPath, dataDir, idepkgtest.TrustStore(), newTestStorage(t, dataDir),
		WithReleaseManager(rm),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(mu),
		WithScheduleNextTick(sched),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })
	_ = i.Ready()
	drainSched()
	i.WaitWorkspaces()

	uri, err := workspaceapi.CurrentUserHostURI(wsFile)
	require.NoError(t, err)
	mu.Lock()
	require.NoError(t, i.Open(uri))
	i.root.Resize(80, 24)
	mu.Unlock()

	require.False(t, tutorialRegistered(i, mu, tutName),
		"tutorial must not be registered before install")

	h := pkgconsole.New(pkgconsole.Config{Manager: i.workspaceHandler.pkgmanager.pkg})
	mu.Lock()
	_, err = h.HandleCommand(context.Background(), repl.Command{
		Name: pkgconsole.CommandName,
		Args: []string{"install", pkgID},
	}, repl.NopProgressWriter())
	mu.Unlock()
	require.NoError(t, err)

	etc, err := os.ReadFile(filepath.Join(dataDir, "etc", pkgID+".yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(etc), tutName,
		"install must write the tutorials entry to the package config")
	userCfg, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(userCfg), "$RUNE_DATADIR/etc/"+pkgID+".yaml",
		"install must include the package config")

	require.Eventually(t, func() bool {
		return tutorialRegistered(i, mu, tutName)
	}, 10*time.Second, 20*time.Millisecond,
		"installing a package with a tutorials entry must register the tutorial live")
}

// recordingPkgUI records the notices of the package installs made for it.
type recordingPkgUI struct {
	*idepkgtest.Notifications
	t *testing.T
}

func (u recordingPkgUI) PromptConfig(idepkg.ConfigPrompt, func(bool)) {
	u.t.Error("the install must not prompt")
}

func TestPkgInstallMultipleTutorialsPromptsOnce(t *testing.T) {
	const pkgID = "multitutpkg"
	tutNames := []string{"alpha-intro", "beta-intro"}
	tutorialSrc := "def run():\n    wait_event(event=\"open\", title=\"hi\", text=\"hello\")\n" +
		"tutorial(entry=run)\n"

	dir := t.TempDir()
	tutorials := make(map[string]string, len(tutNames))
	for _, name := range tutNames {
		p := filepath.Join(dir, name+".star")
		require.NoError(t, os.WriteFile(p, []byte(tutorialSrc), 0o644))
		tutorials[name] = p
	}

	configPath := filepath.Join(dir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("editor:\n  mode: modal\n"), 0o644))

	wsFile := filepath.Join(dir, "seed.go")
	require.NoError(t, os.WriteFile(wsFile, nil, 0o644))

	pkgs := idepkgtest.MakePackages(release.Package{Name: pkgID, Latest: "1"})
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: pkgID, Version: "1"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.SetMissProgressComplete(true)
	rm.SetTarball(pkgID, makeTutorialPkgTarball(t, tutorials))

	mu := new(sync.Mutex)
	// See TestPkgInstallRegistersTutorialLive: keep dataDir/storage
	// outside the watched workspace.
	dataDir := t.TempDir()
	sched, drainSched := newTestScheduler(t, mu)
	i, err := New(dir, configPath, dataDir, idepkgtest.TrustStore(), newTestStorage(t, dataDir),
		WithReleaseManager(rm),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(mu),
		WithScheduleNextTick(sched),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })
	_ = i.Ready()
	drainSched()
	i.WaitWorkspaces()

	uri, err := workspaceapi.CurrentUserHostURI(wsFile)
	require.NoError(t, err)
	mu.Lock()
	require.NoError(t, i.Open(uri))
	i.root.Resize(80, 24)
	mu.Unlock()

	require.Equal(t, 0, countFloatingWindows(i, mu),
		"no prompt should be open before install")

	h := pkgconsole.New(pkgconsole.Config{Manager: i.workspaceHandler.pkgmanager.pkg})
	mu.Lock()
	_, err = h.HandleCommand(context.Background(), repl.Command{
		Name: pkgconsole.CommandName,
		Args: []string{"install", pkgID},
	}, repl.NopProgressWriter())
	mu.Unlock()
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return tutorialRegistered(i, mu, tutNames[0]) &&
			tutorialRegistered(i, mu, tutNames[1])
	}, 10*time.Second, 20*time.Millisecond,
		"every installed tutorial must be registered live")

	assert.Equal(t, 1, countFloatingWindows(i, mu),
		"installing multiple tutorials must open exactly one prompt, not one per tutorial")
}

func TestPkgInstallTutorialDoesNotPromptDuringActiveTutorial(t *testing.T) {
	const (
		pkgID   = "livetutpkg"
		tutName = "gamma-intro"
		tutSrc  = "def run():\n    wait_event(event=\"open\", title=\"hi\", text=\"hello\")\n" +
			"tutorial(entry=run)\n"
	)

	dir := t.TempDir()
	tutPath := filepath.Join(dir, tutName+".star")
	require.NoError(t, os.WriteFile(tutPath, []byte(tutSrc), 0o644))

	configPath := filepath.Join(dir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("editor:\n  mode: modal\n"), 0o644))

	wsFile := filepath.Join(dir, "seed.go")
	require.NoError(t, os.WriteFile(wsFile, nil, 0o644))

	pkgs := idepkgtest.MakePackages(release.Package{Name: pkgID, Latest: "1"})
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: pkgID, Version: "1"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.SetMissProgressComplete(true)
	rm.SetTarball(pkgID, makeTutorialPkgTarball(t, map[string]string{tutName: tutPath}))

	mu := new(sync.Mutex)
	dataDir := t.TempDir()
	sched, drainSched := newTestScheduler(t, mu)
	i, err := New(dir, configPath, dataDir, idepkgtest.TrustStore(), newTestStorage(t, dataDir),
		WithReleaseManager(rm),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(FuncExtensionsRunner(testRunnerFn)),
		WithLocker(mu),
		WithScheduleNextTick(sched),
		WithStarlarkTutorial("basics", minimalStarTutorial),
		WithStartingTutorial("basics"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })
	_ = i.Ready()
	drainSched()
	i.WaitWorkspaces()

	uri, err := workspaceapi.CurrentUserHostURI(wsFile)
	require.NoError(t, err)
	mu.Lock()
	require.NoError(t, i.Open(uri))
	i.root.Resize(80, 24)
	mu.Unlock()

	// The starting tutorial must be running before the install.
	require.Eventually(t, func() bool {
		return activeTutorialName(i, mu) == "basics"
	}, 10*time.Second, 20*time.Millisecond,
		"the basics tutorial should be running before install")

	h := pkgconsole.New(pkgconsole.Config{Manager: i.workspaceHandler.pkgmanager.pkg})
	mu.Lock()
	_, err = h.HandleCommand(context.Background(), repl.Command{
		Name: pkgconsole.CommandName,
		Args: []string{"install", pkgID},
	}, repl.NopProgressWriter())
	mu.Unlock()
	require.NoError(t, err)

	// The installed tutorial is still registered live...
	require.Eventually(t, func() bool {
		return tutorialRegistered(i, mu, tutName)
	}, 10*time.Second, 20*time.Millisecond,
		"installing a tutorial must register it live even while another runs")

	// ...but the active tutorial is untouched and no prompt was opened.
	assert.Equal(t, "basics", activeTutorialName(i, mu),
		"install must not switch away from the running tutorial")
	assert.Equal(t, 0, countFloatingWindows(i, mu),
		"install must not open a run-it-now prompt over an active tutorial")
}

func countFloatingWindows(i *IDE, mu sync.Locker) int {
	mu.Lock()
	defer mu.Unlock()
	n := 0
	i.Browser().IterateWindows(func(w browser.Window) {
		if w.IsFloating() {
			n++
		}
	})
	return n
}

func tutorialRegistered(i *IDE, mu sync.Locker, name string) bool {
	mu.Lock()
	defer mu.Unlock()
	return i.tutorial.has(name)
}

func activeTutorialName(i *IDE, mu sync.Locker) string {
	mu.Lock()
	defer mu.Unlock()
	return i.tutorial.activeName
}

// makeTutorialPkgTarball builds a gzipped tar of a package whose config.yaml
// adds a tutorials entry for each name->path in tutorials. Installing it
// exercises the real config-merge and live tutorial-registration path.
func makeTutorialPkgTarball(t *testing.T, tutorials map[string]string) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString("tutorials:\n")
	for name, tutPath := range tutorials {
		b.WriteString("  " + name + ": " + tutPath + "\n")
	}
	configYAML := b.String()
	files := map[string]string{
		"config.yaml":    configYAML,
		"lib/readme.txt": "# lib placeholder\n",
	}
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)),
		}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	return buf.Bytes()
}

// recordingRunner is a fake extension.Runner that records the Run calls it
// receives. runErr, when set, is returned from every Run call.
type recordingRunner struct {
	mu     sync.Mutex
	calls  []runCall
	runErr error
	// captureEnv, when non-empty, snapshots os.Getenv(captureEnv) at the
	// moment Run is invoked, mirroring how a real extension subprocess
	// inherits os.Environ() at fork time.
	captureEnv string
	// processes is how many processes SignalReload reports it signalled.
	processes int
	reloads   int
}

type runCall struct {
	id     string
	path   string
	cfg    config.Config
	envVal string
}

func (r *recordingRunner) Run(id, path string, cfg config.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := runCall{id: id, path: path, cfg: cfg}
	if r.captureEnv != "" {
		call.envVal = os.Getenv(r.captureEnv)
	}
	r.calls = append(r.calls, call)
	return r.runErr
}

func (r *recordingRunner) Close() error { return nil }

func (r *recordingRunner) WaitReady(context.Context, string) error { return nil }

func (r *recordingRunner) SignalReload() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloads++
	return r.processes
}

func (r *recordingRunner) reloadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reloads
}

func (r *recordingRunner) runCalls() []runCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]runCall, len(r.calls))
	copy(out, r.calls)
	return out
}

var _ extension.Runner = (*recordingRunner)(nil)

// recordingExtensionsRunner is an ExtensionsRunner whose
// WorkspaceExtensionsRunner always hands back the same recordingRunner, so a
// test can observe the Run calls the IDE makes against the home/workspace
// runner it builds internally.
type recordingExtensionsRunner struct{ runner *recordingRunner }

func (r recordingExtensionsRunner) WorkspaceExtensionsRunner(
	_ workspaceapi.URI, _ map[extensionapi.Permission]extension.ResourceRegistrar,
	_ extensionv2.ServerAuthorizer,
	_ extensionv2.TrustVerifier,
	_, _ string, _ browser.Notifications, _, _ schemeapi.Executor,
	_ extension.Grantor, _ text.Editor,
	_ ideauthorizer.PromptOpener, _ storageapi.Service,
	_ func(func()) bool,
) (extension.Runner, error) {
	return r.runner, nil
}

// configWithExtension returns an ideConfig declaring a single user extension
// with the given id and path.
func configWithExtension(t *testing.T, id, path string) ideConfig {
	t.Helper()
	return ideConfig{
		cfg: map[string]any{
			"extensions": map[string]any{
				id: map[string]any{"path": path},
			},
		},
		errors: map[string]error{},
	}
}

// configWithConfigOnlyExtension returns an ideConfig declaring other-ext
// with a path, and config for rune-agent, as a package that styles another
// package's extension leaves it when that extension isn't installed.
func configWithConfigOnlyExtension(t *testing.T) ideConfig {
	t.Helper()
	return ideConfig{
		cfg: map[string]any{
			"extensions": map[string]any{
				"rune-agent": map[string]any{
					"config": map[string]any{"status_bar": map[string]any{"enabled": true}},
				},
				"other-ext": map[string]any{"path": "other-ext-bin"},
			},
		},
		errors: map[string]error{},
	}
}

// configValues returns the top-level keys of cfg, so a test can compare the
// config an extension was started with.
func configValues(cfg config.Config) map[string]any {
	values := map[string]any{}
	cfg.Iterate(func(k string, v any) { values[k] = v })
	return values
}

// makeEnvExtPkgTarball builds a gzipped tar of a package whose config.yaml adds
// both a gui.env block (the env the extension needs) and an extensions entry
// pointing at extPath. Installing it exercises the real config-merge path.
func makeEnvExtPkgTarball(t *testing.T, envKey, envVal, extID, extPath string) []byte {
	t.Helper()
	configYAML := "gui:\n  env:\n    " + envKey + ": " + envVal + "\n" +
		"extensions:\n  " + extID + ":\n    path: " + extPath + "\n"
	files := map[string]string{
		"config.yaml":    configYAML,
		"lib/readme.txt": "# lib placeholder\n",
	}
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)),
		}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	return buf.Bytes()
}

// newPkgInstallExtHandler builds a testWorkspaceManagerHandler wired like the
// production IDE for the pkg-install path: a real release manager, a real
// on-disk config at configPath that reloadConfig re-reads (so the config
// change observes the installed extensions entry), the given
// ExtensionsRunner, and the given config change hook.
func newPkgInstallExtHandler(
	t *testing.T, configPath string, releaseManager release.Manager,
	runner ExtensionsRunner,
	changeHook func(ConfigChange) (ConfigChangeResult, error),
	gitRemoteURL ...func(pkgID string) string,
) *testWorkspaceManagerHandler {
	t.Helper()

	cfg := defaultCfg()
	cfg.configPath = configPath

	homeURI, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	m := new(testWorkspaceManagerHandler)
	m.workspaceManagerHandler = new(workspaceManagerHandler)
	m.configChangeHook = changeHook
	m.tutorialsInstalled = func([]string) ([]string, error) { return nil, nil }
	if len(gitRemoteURL) > 0 {
		m.gitRemoteURL = gitRemoteURL[0]
	}

	// The scheduler, the workspace manager and the handler share one
	// lock: an inline scheduler would run the workspace install and
	// the buffer mutations of a reload on whichever worker goroutine
	// scheduled them, racing the fs-watcher goroutine that dispatches
	// under the same lock.
	mu, sched, drain := buildTestSchedulerForCfg(t, &cfg)
	interrupter := term.NopInterrupter()
	shRunner := new(shaderRunner)
	shRunner.init(handler.Nop(), interrupter, term.Attributes{},
		nopShutdownShaderConfig(), loadingShaderConfig{}, openShaderConfig{},
		component.FrameCharSetDefault())

	dir := t.TempDir()

	manager := workspace.NewManager(cfg.workspace(), sched)
	manager.RegisterScheme(workspace.FileScheme, workspace.NewFileScheme)

	notiCfg := notificationsConfig()
	storage := localstorage.New(context.Background(), dir, docbson.Marshaler())

	reload := func() (ideConfig, error) {
		ret := defaultCfg()
		ret.configPath = configPath
		ret.storage = cfg.storage
		if err := loadFileConfig(&ret, configPath, dir); err != nil {
			return ret, err
		}
		return ret, nil
	}
	startCfg, err := reload()
	require.NoError(t, err)
	m.configChange.lastApplied = startCfg.cfg

	require.NoError(t, m.workspaceManagerHandler.init(nil, homeURI, manager,
		notiCfg, cfg, storage, dir,
		func(term.Event) bool { return true },
		runner, idepkgtest.TrustStore(), mu, nil, reload,
		".sixrc", 0, 0, 0, '1', 0, 0, true, nil, releaseManager,
		shRunner, 0, nil, false, false, newCommandObserverRegistry()))
	m.schedDrain = drain
	return m
}
