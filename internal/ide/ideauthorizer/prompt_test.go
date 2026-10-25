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
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/term/programstatus"
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
			status := &capturingStatus{}
			prompter := newPermissionPrompter(promptOpener, func(fn func()) bool {
				fn()
				return true
			}, status)

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

			// The prompt is also reported as exactly one blocked status
			// with the same permission context.
			blocked := status.blocked()
			require.Len(t, blocked, 1)
			assert.Contains(t, blocked[0].Msg, "authorization")
			assert.Contains(t, blocked[0].Msg, "/bin/test")
		})
	}
}

func TestPermissionPrompterNotificationIncludesScopeLabel(t *testing.T) {
	t.Parallel()

	promptOpener := &fakePluginPromptOpener{option: PromptOptionYes}
	status := &capturingStatus{}
	prompter := newPermissionPrompter(promptOpener, func(fn func()) bool {
		fn()
		return true
	}, status)

	decision, err := prompter.PromptPermission(context.Background(), PermissionRequest{
		Path:        "/usr/local/bin/plugin-cli",
		Args:        []string{"run"},
		Permission:  extensionapi.PermissionExecute,
		CommandPath: "/bin/grep",
		CommandArgs: []string{"foo", "file.txt"},
	})
	require.NoError(t, err)
	assert.Equal(t, PermissionAllowOnce, decision)

	blocked := status.blocked()
	require.Len(t, blocked, 1)
	assert.Contains(t, blocked[0].Msg, "/bin/grep")
	assert.NotContains(t, blocked[0].Msg, "∗")
	assert.NotContains(t, blocked[0].Msg, "/bin/grep *")
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
				Path:        "/usr/local/bin/plugin-cli",
				Args:        []string{"run"},
				Permission:  extensionapi.PermissionExecute,
				CommandPath: "/bin/grep",
				CommandArgs: []string{"foo", "/tmp/workspace"},
				CommandDir:  "/tmp/workspace",
			},
			contains: []string{
				"Program **/usr/local/bin/plugin-cli** with args **[run]**",
				"wants to run **/bin/grep**",
				"with args **[foo /tmp/workspace]**",
				"in **/tmp/workspace**",
				"\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.",
			},
			notContains: []string{"**/bin/grep ∗**", "```"},
		},
		{
			name: "long python script arg uses code block",
			req: PermissionRequest{
				Path:        "/usr/local/bin/plugin-cli",
				Args:        []string{"run"},
				Permission:  extensionapi.PermissionExecute,
				CommandPath: "/bin/bash",
				CommandArgs: []string{"-c", longPython},
			},
			contains: []string{
				"run **/bin/bash** with args:\n\n```",
				"-c\n" + longPython,
				"\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.",
			},
			notContains: []string{"with args **[-c "},
		},
		{
			name: "multiline script arg uses code block",
			req: PermissionRequest{
				Path:        "/usr/local/bin/plugin-cli",
				Args:        []string{"run"},
				Permission:  extensionapi.PermissionExecute,
				CommandPath: "/bin/bash",
				CommandArgs: []string{"-c", "echo first\necho second"},
			},
			contains: []string{
				"run **/bin/bash** with args:\n\n```",
				"-c\necho first\necho second",
				"\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.",
			},
			notContains: []string{"with args **[-c "},
		},
		{
			name: "markdown args use code block",
			req: PermissionRequest{
				Path:        "/usr/local/bin/plugin-cli",
				Args:        []string{"run"},
				Permission:  extensionapi.PermissionExecute,
				CommandPath: "/bin/echo",
				CommandArgs: []string{"**bold** [link](target) # heading"},
			},
			contains: []string{
				"run **/bin/echo** with args:\n\n```",
				"**bold** [link](target) # heading",
				"\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.",
			},
			notContains: []string{"run **/bin/echo** with args **["},
		},
		{
			name: "backtick args use safe code fence",
			req: PermissionRequest{
				Path:        "/usr/local/bin/plugin-cli",
				Args:        []string{"run"},
				Permission:  extensionapi.PermissionExecute,
				CommandPath: "/bin/bash",
				CommandArgs: []string{"-c", "echo ```fence```"},
			},
			contains: []string{
				"run **/bin/bash** with args:\n\n````\n-c\necho ```fence```\n````",
				"\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.",
			},
			notContains: []string{"with args **[-c "},
		},
		{
			name: "perl one-liner script arg uses code block",
			req: PermissionRequest{
				Path:        "/usr/local/bin/plugin-cli",
				Args:        []string{"run"},
				Permission:  extensionapi.PermissionExecute,
				CommandPath: "/usr/bin/perl",
				CommandArgs: []string{"-e", perlScript},
			},
			contains: []string{
				"run **/usr/bin/perl** with args:\n\n```",
				"-e\n" + perlScript,
				"\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.",
			},
			notContains: []string{"run **/usr/bin/perl** with args **["},
		},
		{
			name: "python heredoc shell script uses code block",
			req: PermissionRequest{
				Path:        "/usr/local/bin/plugin-cli",
				Args:        []string{"run"},
				Permission:  extensionapi.PermissionExecute,
				CommandPath: "/bin/bash",
				CommandArgs: []string{"-c", pythonEOF},
			},
			contains: []string{
				"run **/bin/bash** with args:\n\n```",
				"-c\n" + pythonEOF,
				"\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.",
			},
			notContains: []string{"with args **[-c "},
		},
		{
			name: "complex bash substitutions use safe fence and scope list",
			req: PermissionRequest{
				Path:        "/usr/local/bin/plugin-cli",
				Args:        []string{"run"},
				Permission:  extensionapi.PermissionExecute,
				CommandPath: "/bin/bash",
				CommandArgs: []string{"-c", complexBash},
			},
			contains: []string{
				"run **/bin/bash** with args:\n\n``",
				"-c\n" + complexBash,
				"\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.",
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
		Path:         "/usr/local/bin/plugin-cli",
		Args:         []string{"run"},
		LauncherPath: "/bin/zsh",
		LauncherArgs: []string{"--login"},
		Permission:   extensionapi.PermissionExecute,
		CommandPath:  "/bin/rm",
		CommandArgs:  []string{"-rf", "foo"},
	})
	assert.Contains(t, message,
		"Program **/usr/local/bin/plugin-cli** with args **[run]** running inside **/bin/zsh** **[--login]**")
	assert.Contains(t, message, "wants to run **/bin/rm**")
	assert.Contains(t, message, "with args **[-rf foo]**")
	assert.Contains(t, message, "\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.")
}

func TestPermissionPromptMessageHighlightsExtensionCommand(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		ExtensionID:   "test-extension",
		ExtensionName: "Test Extension",
		DeveloperID:   "dev-id",
		Permission:    extensionapi.PermissionExecute,
		CommandPath:   "/bin/grep",
		CommandArgs:   []string{"foo"},
		CommandDir:    "/tmp/workspace",
	})
	assert.Contains(t, message,
		"Extension Test Extension by dev-id wants to run **/bin/grep** with args **[foo]** in **/tmp/workspace**.")
	assert.Contains(t, message,
		"\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.")
}

func TestPluginPermissionPromptMessageWithCommandHighlightsApprovalScope(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		Path:        "/usr/local/bin/plugin-cli",
		Args:        []string{"run"},
		Permission:  extensionapi.PermissionExecute,
		CommandPath: "/bin/grep",
		CommandArgs: []string{"foo", "file.txt"},
		CommandDir:  "/tmp/workspace",
	})

	assert.Contains(t, message, "run **/bin/grep** with args **[foo file.txt]**")
	assert.Contains(t, message, "\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.")
}

func TestPluginPermissionPromptMessageWithShellWrappedCommandListsPrograms(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		Path:        "/usr/local/bin/plugin-cli",
		Args:        []string{"run"},
		Permission:  extensionapi.PermissionExecute,
		CommandPath: "/bin/bash",
		CommandArgs: []string{"-c", "grep foo file.txt"},
		Programs: []ProgramSummary{
			{Name: "/bin/bash", ResolvedPath: "/bin/bash"},
			{Name: "grep", ResolvedPath: "/usr/bin/grep"},
		},
	})

	assert.Contains(t, message, "run **/bin/bash** with args **[-c grep foo file.txt]**")
	assert.Contains(t, message, "\n\nRuns `/bin/bash`, `/usr/bin/grep`.")
	assert.Contains(t, message, "\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.")
}

func TestPluginPermissionPromptMessageWithUnknownShellScriptUsesExactScopeNote(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		Path:               "/usr/local/bin/plugin-cli",
		Args:               []string{"run"},
		Permission:         extensionapi.PermissionExecute,
		CommandPath:        "/bin/bash",
		CommandArgs:        []string{"-c", "$CMD foo"},
		CommandDir:         "/tmp",
		Programs:           []ProgramSummary{{Name: "/bin/bash", ResolvedPath: "/bin/bash"}},
		CommandExactOnly:   true,
		CommandExactReason: "it runs no program a scope can describe",
	})

	assert.Equal(t, "Program **/usr/local/bin/plugin-cli** with args **[run]** "+
		"wants to run **/bin/bash** with args **[-c $CMD foo]** in **/tmp**."+
		"\n\nRuns `/bin/bash`."+
		"\n\nChoosing **Always** or **Never** covers only this exact command because "+
		"it runs no program a scope can describe. Any change to it will prompt again.", message)
}

func TestPluginPermissionPromptMessageListsWhatEachProgramRuns(t *testing.T) {
	t.Parallel()

	message := pluginPermissionPromptMessage(PermissionRequest{
		Path:        "/usr/local/bin/plugin-cli",
		Args:        []string{"run"},
		Permission:  extensionapi.PermissionExecute,
		CommandPath: "/bin/bash",
		CommandArgs: []string{"-c", "cd x && ./git status && $CMD && make test && rm file"},
		Programs: []ProgramSummary{
			{Name: "/bin/bash", ResolvedPath: "/bin/bash"},
			{Name: "./git"},
			{Name: "$CMD", Opaque: true},
			{Name: "echo", Builtin: true},
			{Name: "make", ResolvedPath: "/usr/bin/make", Changed: true},
			{Name: "rm", ResolvedPath: "/ws/bin/rm"},
		},
	})

	assert.Contains(t, message, "run **/bin/bash**")
	assert.Contains(t, message,
		"\n\nRuns `/bin/bash`, `./git` (not found, so Rune asks every time), `$CMD` (known only when it runs), "+
			"`echo` (shell builtin), **`/usr/bin/make`** (changed since you approved it), "+
			"`/ws/bin/rm`.")
	assert.Contains(t, message,
		"\n\nChoosing **Always** or **Never** decides for each of these programs with any arguments, as long as it runs the same file.")
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

	status := &capturingStatus{}
	assert.Nil(t, newPermissionPrompter(nil, schedule, status))
	assert.Nil(t, newPermissionPrompter(promptOpener, nil, status))
	assert.NotNil(t, newPermissionPrompter(promptOpener, schedule, status))
}

func TestPermissionPrompterUnscheduledDeniesOnce(t *testing.T) {
	t.Parallel()

	promptOpener := &fakePluginPromptOpener{option: PromptOptionYes}
	status := &capturingStatus{}
	prompter := newPermissionPrompter(promptOpener, func(fn func()) bool {
		return false
	}, status)

	decision, err := prompter.PromptPermission(context.Background(),
		PermissionRequest{
			Path:       "/bin/test",
			Permission: extensionapi.PermissionBrowserWindowManager,
		})
	require.NoError(t, err)
	assert.Equal(t, PermissionDenyOnce, decision)
	assert.Zero(t, promptOpener.calls)
	// No prompt was opened, so no status was reported.
	assert.Empty(t, status.snapshot())
}

func TestPermissionPrompterContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	promptOpener := &fakePluginPromptOpener{option: "not selected"}
	prompter := newPermissionPrompter(promptOpener, func(fn func()) bool {
		return true
	}, &capturingStatus{})

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

type capturingStatus struct {
	mu      sync.Mutex
	reports []capturedStatus
}

type capturedStatus struct {
	uri    string
	report programstatus.Report
}

func (c *capturingStatus) Tab(workspaceapi.URI, rune, string, browserapi.Handler) (browserapi.Handler, error) {
	return nil, errors.New("unimplemented")
}

func (c *capturingStatus) SetTabName(workspaceapi.URI, string, term.Attributes) error {
	return nil
}

func (c *capturingStatus) OnTabExit(workspaceapi.URI) bool { return false }

func (c *capturingStatus) SetTabStatus(uri workspaceapi.URI, report programstatus.Report) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports = append(c.reports, capturedStatus{uri: uri.String(), report: report})
	return nil
}

func (c *capturingStatus) EndTabStatus(workspaceapi.URI) error {
	return errors.New("unimplemented")
}

func (c *capturingStatus) snapshot() []capturedStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.reports)
}

// blocked returns the blocked reports, one per prompt announced.
func (c *capturingStatus) blocked() []programstatus.Report {
	var ret []programstatus.Report
	for _, r := range c.snapshot() {
		if r.report.State == programstatus.Blocked {
			ret = append(ret, r.report)
		}
	}
	return ret
}

func TestPermissionPrompterReportsStatus(t *testing.T) {
	t.Parallel()

	promptOpener := &fakePluginPromptOpener{option: PromptOptionYes}
	status := &capturingStatus{}
	prompter := newPermissionPrompter(promptOpener, func(fn func()) bool {
		fn()
		return true
	}, status)
	var windows []string
	prompter.onWindow = func(uri workspaceapi.URI, win browser.Window) {
		require.NotNil(t, win)
		windows = append(windows, uri.String())
	}
	req := PermissionRequest{
		Path:        "/usr/local/bin/plugin-cli",
		Permission:  extensionapi.PermissionExecute,
		CommandPath: "/usr/bin/make",
		CommandArgs: []string{"test"},
	}

	decision, err := prompter.PromptPermission(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, PermissionAllowOnce, decision)
	assert.Equal(t, []string{"ideauthorizer:///prompt/1"}, windows,
		"the window of the prompt is told under the uri of its status")

	reports := status.snapshot()
	require.Len(t, reports, 2)
	assert.Equal(t, "ideauthorizer:///prompt/1", reports[0].uri)
	assert.Equal(t, programstatus.Report{
		State: programstatus.Blocked, Kind: programstatus.KindPermission, Progress: -1,
		App: programstatus.HostApp, Title: "Authorization",
		Msg: "User authorization required for /usr/bin/make",
	}, reports[0].report)
	assert.Equal(t, reports[0].uri, reports[1].uri, "the prompt is withdrawn under its own uri")
	assert.Equal(t, programstatus.Report{State: programstatus.Clear}, reports[1].report)

	_, err = prompter.PromptPermission(context.Background(), req)
	require.NoError(t, err)
	reports = status.snapshot()
	require.Len(t, reports, 4)
	assert.Equal(t, "ideauthorizer:///prompt/2", reports[2].uri, "every prompt has its own uri")

	t.Run("a cancelled prompt is withdrawn", func(t *testing.T) {
		ticks := make(chan func(), 2)
		status := &capturingStatus{}
		prompter := newPermissionPrompter(promptOpener, func(fn func()) bool {
			ticks <- fn
			return true
		}, status)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, err := prompter.PromptPermission(ctx, req)
			assert.ErrorIs(t, err, context.Canceled)
		}()
		cancel()
		<-done
		(<-ticks)()
		(<-ticks)()
		reports := status.snapshot()
		require.Len(t, reports, 2)
		assert.Equal(t, programstatus.Blocked, reports[0].report.State)
		assert.Equal(t, programstatus.Report{State: programstatus.Clear}, reports[1].report)
	})
}

func TestPermissionPrompterPromptRenewal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		option string
		close  bool
		want   RenewalDecision
	}{
		{option: PromptOptionRenew, want: RenewalRenew},
		{option: PromptOptionOnce, want: RenewalOnce},
		{option: PromptOptionNo, want: RenewalDeny},
		{close: true, want: RenewalDeny},
	}
	for _, tc := range cases {
		promptOpener := &fakePluginPromptOpener{option: tc.option, close: tc.close}
		status := &capturingStatus{}
		prompter := newPermissionPrompter(promptOpener, func(fn func()) bool {
			fn()
			return true
		}, status)

		decision, err := prompter.PromptRenewal(context.Background(), RenewalRequest{
			Requester: "extension Agent",
			Changes: []ExecutableChange{{
				Path:   "/usr/bin/git",
				Scopes: []string{"git *", "git `x` *"},
				Old:    ExecutableFingerprint{SHA256: strings.Repeat("a", 64)},
				New:    ExecutableFingerprint{SHA256: "b1"},
			}},
		})
		require.NoError(t, err)
		assert.Equal(t, tc.want, decision)
		assert.True(t, strings.HasPrefix(promptOpener.message,
			"**`/usr/bin/git`** has changed since you approved `git *`, ``git `x` *`` "+
				"for extension Agent.\n\nsha256 `aaaaaaaaaaaa…` → `b1`, modified "), promptOpener.message)
		blocked := status.blocked()
		require.Len(t, blocked, 1)
		assert.Equal(t, "User authorization required: /usr/bin/git changed", blocked[0].Msg)
	}
}
