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

package ideauthorizer

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
)

type fakePluginPromptOpener struct {
	option  string
	close   bool
	calls   int
	message string
}

func (f *fakePluginPromptOpener) Prompt(
	message string, options []string,
	bindings []term.KeyComb,
	promptHandler handler.PromptHandler,
) browser.Window {
	f.calls++
	f.message = message
	if f.close {
		if err := promptHandler.OnClose(); err != nil {
			panic(err)
		}
		return browsertest.NopWindow()
	}
	for i, opt := range options {
		if opt == f.option {
			promptHandler.OnSelect(i, opt)
			return browsertest.NopWindow()
		}
	}
	panic("test prompt option was not provided")
}

func TestPermissionPrompter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		option       string
		close        bool
		wantDecision PermissionDecision
	}{
		{name: "yes", option: PromptOptionYes,
			wantDecision: PermissionAllowOnce},
		{name: "yes all", option: PromptOptionYesAlways,
			wantDecision: PermissionAllowAlways},
		{name: "no", option: PromptOptionNo,
			wantDecision: PermissionDenyOnce},
		{name: "never", option: PromptOptionNoNever,
			wantDecision: PermissionDenyAlways},
		{name: "close", option: PromptOptionYes, close: true,
			wantDecision: PermissionDenyOnce},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			promptOpener := &fakePluginPromptOpener{
				option: tc.option,
				close:  tc.close,
			}
			noti := &capturingNotifications{}
			prompter := newPermissionPrompter(promptOpener, func(fn func()) bool {
				fn()
				return true
			}, noti)

			decision, err := prompter.PromptPermission(context.Background(),
				PermissionRequest{
					Path:       "/bin/test",
					Args:       []string{"--flag"},
					Permission: extensionapi.PermissionBrowserWindowManager,
				})
			require.NoError(t, err)
			assert.Equal(t, tc.wantDecision, decision)
			assert.Equal(t, 1, promptOpener.calls)
			assert.Contains(t, promptOpener.message,
				"Program /bin/test with args [--flag] wants to **manage the window manager**.")

			// Creating the prompt also emits exactly one warning
			// notification with the same permission context.
			captured := noti.captured()
			require.Len(t, captured, 1)
			assert.Equal(t, browserapi.LevelWarn, captured[0].Level)
			assert.Contains(t, captured[0].Msg, "authorization")
			assert.Contains(t, captured[0].Msg, "/bin/test")
		})
	}
}

func TestPermissionPrompterNotificationIncludesScopeLabel(t *testing.T) {
	t.Parallel()

	promptOpener := &fakePluginPromptOpener{option: PromptOptionYes}
	noti := &capturingNotifications{}
	prompter := newPermissionPrompter(promptOpener, func(fn func()) bool {
		fn()
		return true
	}, noti)

	decision, err := prompter.PromptPermission(context.Background(), PermissionRequest{
		Path:              "/usr/local/bin/plugin-cli",
		Args:              []string{"run"},
		Permission:        extensionapi.PermissionExecute,
		CommandPath:       "/bin/grep",
		CommandArgs:       []string{"foo", "file.txt"},
		CommandScopeLabel: "/bin/grep *",
	})
	require.NoError(t, err)
	assert.Equal(t, PermissionAllowOnce, decision)

	captured := noti.captured()
	require.Len(t, captured, 1)
	assert.Equal(t, browserapi.LevelWarn, captured[0].Level)
	assert.Contains(t, captured[0].Msg, "/bin/grep")
	assert.NotContains(t, captured[0].Msg, "∗")
	assert.NotContains(t, captured[0].Msg, "/bin/grep *")
}

func TestPluginPermissionPromptMessageWithLauncher(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		Path:         "/usr/local/bin/trusted-cli",
		Args:         []string{"run"},
		LauncherPath: "/bin/zsh",
		LauncherArgs: []string{"--login"},
		Permission:   extensionapi.PermissionBrowserWindowManager,
	})
	assert.Contains(t, message,
		"Program /usr/local/bin/trusted-cli with args [run] running inside /bin/zsh [--login] wants to **manage the window manager**.")
}

func TestPluginPermissionPromptMessageWithCommand(t *testing.T) {
	t.Parallel()

	longPython := "python -c " + strings.Repeat("print('markdown **is not bold**');", 4)
	pythonEOF := "python <<'PY'\nimport json\nprint(json.dumps({'ok': True}))\nPY"
	perlScript := "perl -0777 -ne 'print if /BEGIN.*END/s' README.md"
	complexBash := "echo `git rev-parse --show-toplevel` && printf %s $(python - <<'PY'\nprint('nested')\nPY\n)"

	tests := []struct {
		name        string
		req         PermissionRequest
		contains    []string
		notContains []string
	}{
		{
			name: "short simple args stay inline",
			req: PermissionRequest{
				Path:              "/usr/local/bin/plugin-cli",
				Args:              []string{"run"},
				Permission:        extensionapi.PermissionExecute,
				CommandPath:       "/bin/grep",
				CommandArgs:       []string{"foo", "/tmp/workspace"},
				CommandDir:        "/tmp/workspace",
				CommandScopeLabel: "/bin/grep *",
			},
			contains: []string{
				"Program **/usr/local/bin/plugin-cli** with args **[run]**",
				"wants to run **/bin/grep**",
				"with args **[foo /tmp/workspace]**",
				"in **/tmp/workspace**",
				"\n\nChoosing **Always** approves **/bin/grep** for all future authorization requests, for any argument combination.",
			},
			notContains: []string{"**/bin/grep ∗**", "```"},
		},
		{
			name: "long python script arg uses code block",
			req: PermissionRequest{
				Path:              "/usr/local/bin/plugin-cli",
				Args:              []string{"run"},
				Permission:        extensionapi.PermissionExecute,
				CommandPath:       "/bin/bash",
				CommandArgs:       []string{"-c", longPython},
				CommandScopeLabel: "/bin/bash *",
			},
			contains: []string{
				"run **/bin/bash** with args:\n\n```",
				"-c\n" + longPython,
				"\n\nChoosing **Always** approves **/bin/bash** for all future authorization requests, for any argument combination.",
			},
			notContains: []string{"with args **[-c "},
		},
		{
			name: "multiline script arg uses code block",
			req: PermissionRequest{
				Path:              "/usr/local/bin/plugin-cli",
				Args:              []string{"run"},
				Permission:        extensionapi.PermissionExecute,
				CommandPath:       "/bin/bash",
				CommandArgs:       []string{"-c", "echo first\necho second"},
				CommandScopeLabel: "/bin/bash *",
			},
			contains: []string{
				"run **/bin/bash** with args:\n\n```",
				"-c\necho first\necho second",
				"\n\nChoosing **Always** approves **/bin/bash** for all future authorization requests, for any argument combination.",
			},
			notContains: []string{"with args **[-c "},
		},
		{
			name: "markdown args use code block",
			req: PermissionRequest{
				Path:              "/usr/local/bin/plugin-cli",
				Args:              []string{"run"},
				Permission:        extensionapi.PermissionExecute,
				CommandPath:       "/bin/echo",
				CommandArgs:       []string{"**bold** [link](target) # heading"},
				CommandScopeLabel: "/bin/echo *",
			},
			contains: []string{
				"run **/bin/echo** with args:\n\n```",
				"**bold** [link](target) # heading",
				"\n\nChoosing **Always** approves **/bin/echo** for all future authorization requests, for any argument combination.",
			},
			notContains: []string{"run **/bin/echo** with args **["},
		},
		{
			name: "backtick args use safe code fence",
			req: PermissionRequest{
				Path:              "/usr/local/bin/plugin-cli",
				Args:              []string{"run"},
				Permission:        extensionapi.PermissionExecute,
				CommandPath:       "/bin/bash",
				CommandArgs:       []string{"-c", "echo ```fence```"},
				CommandScopeLabel: "/bin/bash *",
			},
			contains: []string{
				"run **/bin/bash** with args:\n\n````\n-c\necho ```fence```\n````",
				"\n\nChoosing **Always** approves **/bin/bash** for all future authorization requests, for any argument combination.",
			},
			notContains: []string{"with args **[-c "},
		},
		{
			name: "perl one-liner script arg uses code block",
			req: PermissionRequest{
				Path:              "/usr/local/bin/plugin-cli",
				Args:              []string{"run"},
				Permission:        extensionapi.PermissionExecute,
				CommandPath:       "/usr/bin/perl",
				CommandArgs:       []string{"-e", perlScript},
				CommandScopeLabel: "perl *",
			},
			contains: []string{
				"run **/usr/bin/perl** with args:\n\n```",
				"-e\n" + perlScript,
				"\n\nChoosing **Always** approves **perl** for all future authorization requests, for any argument combination.",
			},
			notContains: []string{"run **/usr/bin/perl** with args **["},
		},
		{
			name: "python heredoc shell script uses code block",
			req: PermissionRequest{
				Path:              "/usr/local/bin/plugin-cli",
				Args:              []string{"run"},
				Permission:        extensionapi.PermissionExecute,
				CommandPath:       "/bin/bash",
				CommandArgs:       []string{"-c", pythonEOF},
				CommandScopeLabel: "python *",
			},
			contains: []string{
				"run **/bin/bash** with args:\n\n```",
				"-c\n" + pythonEOF,
				"\n\nChoosing **Always** approves **python** for all future authorization requests, for any argument combination.",
			},
			notContains: []string{"with args **[-c "},
		},
		{
			name: "complex bash substitutions use safe fence and scope list",
			req: PermissionRequest{
				Path:               "/usr/local/bin/plugin-cli",
				Args:               []string{"run"},
				Permission:         extensionapi.PermissionExecute,
				CommandPath:        "/bin/bash",
				CommandArgs:        []string{"-c", complexBash},
				CommandScopeLabels: []string{"echo *", "git *", "printf *", "python *"},
			},
			contains: []string{
				"run **/bin/bash** with args:\n\n``",
				"-c\n" + complexBash,
				"Choosing **Always** approves **echo**, **git**, **printf**, **python** for all future authorization requests, for any argument combination.",
			},
			notContains: []string{"with args **[-c "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			message := pluginPermissionPromptMessage(tt.req)
			for _, want := range tt.contains {
				assert.Contains(t, message, want)
			}
			for _, unwanted := range tt.notContains {
				assert.NotContains(t, message, unwanted)
			}
		})
	}
}

func TestPluginPermissionPromptMessageWithLauncherAndCommand(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		Path:              "/usr/local/bin/plugin-cli",
		Args:              []string{"run"},
		LauncherPath:      "/bin/zsh",
		LauncherArgs:      []string{"--login"},
		Permission:        extensionapi.PermissionExecute,
		CommandPath:       "/bin/rm",
		CommandArgs:       []string{"-rf", "foo"},
		CommandScopeLabel: "/bin/rm *",
	})
	assert.Contains(t, message,
		"Program **/usr/local/bin/plugin-cli** with args **[run]** running inside **/bin/zsh** **[--login]**")
	assert.Contains(t, message, "wants to run **/bin/rm**")
	assert.Contains(t, message, "with args **[-rf foo]**")
	assert.Contains(t, message, "\n\nChoosing **Always** approves **/bin/rm** for all future authorization requests, for any argument combination.")
}

func TestPermissionPromptMessageHighlightsExtensionCommand(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		ExtensionID:       "test-extension",
		ExtensionName:     "Test Extension",
		DeveloperID:       "dev-id",
		Permission:        extensionapi.PermissionExecute,
		CommandPath:       "/bin/grep",
		CommandArgs:       []string{"foo"},
		CommandDir:        "/tmp/workspace",
		CommandScopeLabel: "/bin/grep *",
	})
	assert.Contains(t, message,
		"Extension Test Extension by dev-id wants to run **/bin/grep** with args **[foo]** in **/tmp/workspace**.")
	assert.Contains(t, message,
		"\n\nChoosing **Always** approves **/bin/grep** for all future authorization requests, for any argument combination.")
}

func TestPluginPermissionPromptMessageWithCommandHighlightsApprovalScope(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		Path:              "/usr/local/bin/plugin-cli",
		Args:              []string{"run"},
		Permission:        extensionapi.PermissionExecute,
		CommandPath:       "/bin/grep",
		CommandArgs:       []string{"foo", "file.txt"},
		CommandDir:        "/tmp/workspace",
		CommandScopeLabel: "/bin/grep *",
	})

	assert.Contains(t, message, "run **/bin/grep** with args **[foo file.txt]**")
	assert.Contains(t, message, "\n\nChoosing **Always** approves **/bin/grep** for all future authorization requests, for any argument combination.")
	assert.Contains(t, message, "for all future authorization requests, for any argument combination")
}

func TestPluginPermissionPromptMessageWithShellWrappedCommandUsesInnerScope(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		Path:              "/usr/local/bin/plugin-cli",
		Args:              []string{"run"},
		Permission:        extensionapi.PermissionExecute,
		CommandPath:       "/bin/bash",
		CommandArgs:       []string{"-c", "grep foo file.txt"},
		CommandScopeLabel: "grep *",
	})

	assert.Contains(t, message, "run **/bin/bash** with args **[-c grep foo file.txt]**")
	assert.Contains(t, message, "\n\nChoosing **Always** approves **grep** for all future authorization requests, for any argument combination.")
}

func TestPluginPermissionPromptMessageWithUnknownShellScriptUsesExactScopeNote(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		Path:              "/usr/local/bin/plugin-cli",
		Args:              []string{"run"},
		Permission:        extensionapi.PermissionExecute,
		CommandPath:       "/bin/bash",
		CommandArgs:       []string{"-c", "$CMD foo"},
		CommandDir:        "/tmp",
		CommandScopeExact: true,
	})

	assert.Equal(t, "Program **/usr/local/bin/plugin-cli** with args **[run]** "+
		"wants to run **/bin/bash** with args **[-c $CMD foo]** in **/tmp**."+
		"\n\nChoosing **Always** approves only this exact command. "+
		"Any change to its arguments or working directory will prompt again.", message)
}

func TestPluginPermissionPromptMessageWithMultipleScopeLabels(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		Path:               "/usr/local/bin/plugin-cli",
		Args:               []string{"run"},
		Permission:         extensionapi.PermissionExecute,
		CommandPath:        "/bin/bash",
		CommandArgs:        []string{"-c", "grep foo && make test && rm file"},
		CommandScopeLabels: []string{"grep *", "make *", "rm *"},
		CommandScopeLabel:  "grep *, make *, rm *",
	})

	assert.Contains(t, message, "run **/bin/bash**")
	assert.Contains(t, message,
		"\n\nChoosing **Always** approves **grep**, **make**, **rm** for all future authorization requests, for any argument combination.")
}

func TestPermissionPromptMessageHighlightsExtensionIntent(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		ExtensionID:   "test-extension",
		ExtensionName: "Test Extension",
		DeveloperID:   "dev-id",
		Permission:    extensionapi.PermissionLSP,
	})
	assert.Equal(t,
		"Extension Test Extension by dev-id wants to **communicate with LSP servers**.",
		message)
}

func TestNewPermissionPrompterNilDependencies(t *testing.T) {
	t.Parallel()

	promptOpener := &fakePluginPromptOpener{option: PromptOptionYes}
	schedule := func(fn func()) bool { return true }

	assert.Nil(t, newPermissionPrompter(nil, schedule, nil))
	assert.Nil(t, newPermissionPrompter(promptOpener, nil, nil))
	// A nil notifications dependency is allowed; the prompter treats it
	// as a no-op notifier so prompts still work.
	assert.NotNil(t, newPermissionPrompter(promptOpener, schedule, nil))
	assert.NotNil(t, newPermissionPrompter(promptOpener, schedule,
		&capturingNotifications{}))
}

func TestPermissionPrompterUnscheduledDeniesOnce(t *testing.T) {
	t.Parallel()

	promptOpener := &fakePluginPromptOpener{option: PromptOptionYes}
	noti := &capturingNotifications{}
	prompter := newPermissionPrompter(promptOpener, func(fn func()) bool {
		return false
	}, noti)

	decision, err := prompter.PromptPermission(context.Background(),
		PermissionRequest{
			Path:       "/bin/test",
			Permission: extensionapi.PermissionBrowserWindowManager,
		})
	require.NoError(t, err)
	assert.Equal(t, PermissionDenyOnce, decision)
	assert.Zero(t, promptOpener.calls)
	// No prompt was opened, so no warning notification was emitted.
	assert.Empty(t, noti.captured())
}

func TestPermissionPrompterContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	promptOpener := &fakePluginPromptOpener{option: "not selected"}
	prompter := newPermissionPrompter(promptOpener, func(fn func()) bool {
		return true
	}, nil)

	decision, err := prompter.PromptPermission(ctx, PermissionRequest{
		Path:       "/bin/test",
		Permission: extensionapi.PermissionBrowserWindowManager,
	})
	assert.Equal(t, PermissionDenyOnce, decision)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, promptOpener.calls)
}

func TestPluginPromptDecisionUnknownOptionDeniesOnce(t *testing.T) {
	t.Parallel()

	assert.Equal(t, PermissionDenyOnce, pluginPromptDecision("something else"))
}

var _ browser.Window = browsertest.NopWindow()
