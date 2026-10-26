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
	options []string
	// floatingErr fails the scope pickers opened; otherwise they confirm
	// what is preselected.
	floatingErr error
	floatings   int
}

func (f *fakePluginPromptOpener) Prompt(
	message string, options []string,
	bindings []term.KeyComb,
	promptHandler handler.PromptHandler,
) browser.Window {
	f.calls++
	f.message = message
	f.options = options
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

func (f *fakePluginPromptOpener) Floating(
	h browser.Floating, _ browserapi.FloatingConfig,
) (browser.Window, error) {
	f.floatings++
	if f.floatingErr != nil {
		return nil, f.floatingErr
	}
	h.(*scopePicker).Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	return browsertest.NopWindow(), nil
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

func TestPermissionPrompterOffersTheDecisionsForGoodACommandCanHave(t *testing.T) {
	t.Parallel()

	cases := []struct {
		remember CommandRemember
		want     []string
	}{
		{remember: RememberScopes, want: []string{
			PromptOptionYes, PromptOptionYesAlways, PromptOptionNo, PromptOptionNoNever}},
		{remember: RememberPrograms, want: []string{
			PromptOptionYes, PromptOptionYesAlways, PromptOptionNo}},
		{remember: RememberNothing, want: []string{PromptOptionYes, PromptOptionNo}},
	}
	for _, tc := range cases {
		opener := &fakePluginPromptOpener{option: PromptOptionYes}
		prompter := newPermissionPrompter(opener, func(fn func()) bool {
			fn()
			return true
		}, &capturingStatus{})

		_, err := prompter.PromptPermission(context.Background(), PermissionRequest{
			Permission:      extensionapi.PermissionExecute,
			CommandPath:     "/bin/bash",
			CommandArgs:     []string{"-c", "git status"},
			CommandRemember: tc.remember,
		})
		require.NoError(t, err)
		assert.Equal(t, tc.want, opener.options, tc.remember)
	}
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
				"\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.",
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
				"\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.",
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
				"\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.",
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
				"\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.",
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
				"\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.",
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
				"\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.",
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
				"\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.",
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
				"\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.",
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
	assert.Contains(t, message, "\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.")
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
		"\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.")
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
	assert.Contains(t, message, "\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.")
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
	assert.Contains(t, message, "\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.")
}

func TestPluginPermissionPromptMessageTellsWhatDecidingForGoodDoes(t *testing.T) {
	t.Parallel()

	const intro = "Program **/usr/local/bin/plugin-cli** with args **[run]** " +
		"wants to run **/bin/bash** with args **[-c $CMD foo]** in **/tmp**."
	cases := []struct {
		name     string
		programs []ProgramSummary
		remember CommandRemember
		reason   string
		want     string
	}{
		{name: "nothing, with a reason", remember: RememberNothing,
			programs: []ProgramSummary{{Name: "/bin/bash", ResolvedPath: "/bin/bash"}},
			reason:   "it runs no program a scope can describe",
			want: intro + "\n\nRuns `/bin/bash`." +
				"\n\nRune asks every time because it runs no program a scope can describe."},
		{name: "nothing, which the programs tell", remember: RememberNothing,
			programs: []ProgramSummary{{Name: "./build"}},
			want:     intro + "\n\nRuns `./build` (not found, so Rune asks every time)."},
		{name: "nothing, since the directory a program runs in is not known",
			remember: RememberNothing,
			programs: []ProgramSummary{{Name: "./build", DirUnknown: "the script changes directory"}},
			want: intro + "\n\nRuns `./build` (the script changes directory, so Rune asks " +
				"every time)."},
		{name: "programs", remember: RememberPrograms,
			programs: []ProgramSummary{
				{Name: "timeout", ResolvedPath: "/usr/bin/timeout"},
				{Name: "git", ResolvedPath: "/usr/bin/git", Changed: true},
			},
			want: intro + "\n\nRuns `/usr/bin/timeout`, **`/usr/bin/git`** (changed since you " +
				"approved it).\n\nChoosing **Always** approves `/usr/bin/timeout`, " +
				"`/usr/bin/git` from now on."},
		{name: "programs, one of which is not found", remember: RememberPrograms,
			programs: []ProgramSummary{
				{Name: "sh", ResolvedPath: "/bin/sh"},
				{Name: "gtimeout", Absent: true},
			},
			want: intro + "\n\nRuns `/bin/sh`, `gtimeout` (not found).\n\nChoosing **Always** " +
				"approves `/bin/sh`, `gtimeout` (as long as it is not found) from now on."},
		{name: "a program that runs arbitrary code", remember: RememberScopes,
			programs: []ProgramSummary{{Name: "sed", ResolvedPath: "/usr/bin/sed", RunsCode: true}},
			want: intro + "\n\nRuns `/usr/bin/sed`." +
				"\n\nChoosing **Always** or **Never** lets you pick which commands and " +
				"subcommands the decision covers. ⚠️ Be careful: `sed` can run arbitrary code."},
		{name: "programs that run arbitrary code", remember: RememberScopes,
			programs: []ProgramSummary{
				{Name: "sed", ResolvedPath: "/usr/bin/sed", RunsCode: true},
				{Name: "head", ResolvedPath: "/usr/bin/head"},
				{Name: "python3", ResolvedPath: "/usr/bin/python3", RunsCode: true},
			},
			want: intro + "\n\nRuns `/usr/bin/sed`, `/usr/bin/head`, `/usr/bin/python3`." +
				"\n\nChoosing **Always** or **Never** lets you pick which commands and " +
				"subcommands the decision covers. ⚠️ Be careful: `sed`, `python3` can run " +
				"arbitrary code."},
	}
	for _, tc := range cases {
		message := pluginPermissionPromptMessage(PermissionRequest{
			Path:              "/usr/local/bin/plugin-cli",
			Args:              []string{"run"},
			Permission:        extensionapi.PermissionExecute,
			CommandPath:       "/bin/bash",
			CommandArgs:       []string{"-c", "$CMD foo"},
			CommandDir:        "/tmp",
			Programs:          tc.programs,
			CommandRemember:   tc.remember,
			CommandOnceReason: tc.reason,
		})
		assert.Equal(t, tc.want, message, tc.name)
	}
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
		"\n\nChoosing **Always** or **Never** lets you pick which commands and subcommands the decision covers.")
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

func TestPermissionPrompterPickScopes(t *testing.T) {
	t.Parallel()

	errNoWindow := errors.New("no window")
	ladder := []commandScope{{Label: "git *"}, {Label: "git status *"}, {Label: "git status"}}
	cases := []struct {
		name          string
		scopes        []commandScope
		floatingErr   error
		wantSelected  []int
		wantOK        bool
		wantErr       error
		wantFloatings int
	}{
		{name: "ladder is picked", scopes: ladder,
			wantSelected: []int{1}, wantOK: true, wantFloatings: 1},
		{name: "single scope wider than the command is picked", scopes: ladder[:1],
			wantOK: true, wantFloatings: 1},
		{name: "picker that cannot open fails", scopes: ladder, floatingErr: errNoWindow,
			wantErr: errNoWindow, wantFloatings: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opener := &fakePluginPromptOpener{floatingErr: tc.floatingErr}
			prompter := newPermissionPrompter(opener, func(fn func()) bool {
				fn()
				return true
			}, &capturingStatus{})
			var preselected []int
			if len(tc.scopes) > 1 {
				preselected = []int{1}
			}

			selected, ok, err := prompter.PickScopes(context.Background(), scopePickerRequest{
				Requester: "extension Agent", Scopes: tc.scopes, Selected: preselected,
			})

			assert.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantSelected, selected)
			assert.Equal(t, tc.wantFloatings, opener.floatings)
		})
	}
}
