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
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/term/programstatus"
)

// Permission prompt option labels shown in permission prompts.
const (
	PromptOptionYes       = "   Yes   "
	PromptOptionYesAlways = "   Always   "
	PromptOptionNo        = "   No   "
	PromptOptionNoNever   = "   Never   "
	PromptOptionRenew     = "   Renew   "
	PromptOptionOnce      = "   Once   "
)

// fingerprintPreviewLength is how many hex digits of an executable's
// SHA-256 prompts show.
const fingerprintPreviewLength = 12

type permissionPrompter struct {
	promptOpener     PromptOpener
	scheduleNextTick func(func()) bool
	// status is told that a prompt awaits the user, under a uri of its
	// own per prompt.
	status    browser.TabManager
	promptSeq atomic.Uint64
	// onWindow, when set, is told of the window of each prompt.
	onWindow func(uri workspaceapi.URI, win browser.Window)
}

func newPermissionPrompter(
	promptOpener PromptOpener,
	scheduleNextTick func(func()) bool,
	status browser.TabManager,
) *permissionPrompter {
	if promptOpener == nil || scheduleNextTick == nil {
		return nil
	}
	return &permissionPrompter{
		promptOpener:     promptOpener,
		scheduleNextTick: scheduleNextTick,
		status:           status,
	}
}

func (p *permissionPrompter) PromptPermission(
	ctx context.Context, req PermissionRequest,
) (PermissionDecision, error) {
	options := []string{
		PromptOptionYes,
		PromptOptionYesAlways,
		PromptOptionNo,
		PromptOptionNoNever,
	}
	bindings := []term.KeyComb{{Ch: 'Y'}, {Ch: 'A'}, {Ch: 'N'}, {Ch: 'V'}}
	switch req.CommandRemember {
	case RememberPrograms:
		options, bindings = options[:3], bindings[:3]
	case RememberNothing:
		options = []string{PromptOptionYes, PromptOptionNo}
		bindings = []term.KeyComb{{Ch: 'Y'}, {Ch: 'N'}}
	}
	opt, err := p.ask(ctx, pluginPermissionPromptMessage(req),
		permissionPromptWarningMessage(req), options, bindings)
	return pluginPromptDecision(opt), err
}

// PromptRenewal asks the user whether approvals still hold after the
// executables they run changed.
func (p *permissionPrompter) PromptRenewal(
	ctx context.Context, req RenewalRequest,
) (RenewalDecision, error) {
	options := []string{PromptOptionRenew, PromptOptionOnce, PromptOptionNo}
	bindings := []term.KeyComb{{Ch: 'R'}, {Ch: 'O'}, {Ch: 'N'}}
	paths := make([]string, len(req.Changes))
	for i, c := range req.Changes {
		paths[i] = c.Path
	}
	warning := "User authorization required: " + strings.Join(paths, ", ") + " changed"
	opt, err := p.ask(ctx, renewalPromptMessage(req), warning, options, bindings)
	switch opt {
	case PromptOptionRenew:
		return RenewalRenew, err
	case PromptOptionOnce:
		return RenewalOnce, err
	default:
		return RenewalDeny, err
	}
}

// PickScopes asks the user which of req.Scopes a decision covers. ok is
// false when the user cancelled or the picker could not be shown.
func (p *permissionPrompter) PickScopes(
	ctx context.Context, req scopePickerRequest,
) (selected []int, ok bool, err error) {
	type pick struct {
		selected []int
		ok       bool
		err      error
	}
	result := make(chan pick, 1)
	send := func(r pick) {
		select {
		case result <- r:
		default:
		}
	}
	statusURI := p.promptStatusURI()
	scheduled := p.scheduleNextTick(func() {
		p.announce(statusURI, "User authorization required: pick the commands "+
			req.Requester+" may run")
		picker, err := newScopePicker(req, func(selected []int, ok bool) {
			send(pick{selected: selected, ok: ok})
		})
		if err != nil {
			send(pick{err: err})
			return
		}
		win, err := p.promptOpener.Floating(picker, browserapi.FloatingConfig{
			Alignment: component.AlignmentCentered,
			Title:     picker.title(),
		})
		if err != nil {
			send(pick{err: fmt.Errorf("open command scope picker: %w", err)})
			return
		}
		if p.onWindow != nil && win != nil {
			p.onWindow(statusURI, win)
		}
	})
	if !scheduled {
		return nil, false, nil
	}
	defer p.withdraw(statusURI)
	select {
	case r := <-result:
		return r.selected, r.ok, r.err
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// ask shows a prompt and returns the option the user picked, or "" when
// they dismissed it.
func (p *permissionPrompter) ask(
	ctx context.Context, message, warning string, options []string, bindings []term.KeyComb,
) (string, error) {
	result := make(chan string, 1)
	statusURI := p.promptStatusURI()
	scheduled := p.scheduleNextTick(func() {
		p.announce(statusURI, warning)
		win := p.promptOpener.Prompt(message, options, bindings, handler.FuncPromptHandler(
			func(i int, opt string) {
				select {
				case result <- opt:
				default:
				}
			},
			func() error {
				select {
				case result <- "":
				default:
				}
				return nil
			}))
		if p.onWindow != nil && win != nil {
			p.onWindow(statusURI, win)
		}
	})
	if !scheduled {
		return "", nil
	}

	defer p.withdraw(statusURI)
	select {
	case opt := <-result:
		return opt, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (p *permissionPrompter) promptStatusURI() workspaceapi.URI {
	uri, err := workspaceapi.ParseURI(fmt.Sprintf("ideauthorizer:///prompt/%d", p.promptSeq.Add(1)))
	if err != nil {
		panic(err)
	}
	return uri
}

// announce tells the user a prompt awaits them as a blocked status.
func (p *permissionPrompter) announce(uri workspaceapi.URI, warning string) {
	report := programstatus.Report{
		State: programstatus.Blocked, Kind: programstatus.KindPermission, Progress: -1,
		App: programstatus.HostApp, Title: "Authorization", Msg: warning,
	}
	if normalized, err := programstatus.Normalize(report); err == nil {
		report = normalized
	} else {
		report.Msg = ""
	}
	_ = p.status.SetTabStatus(uri, report)
}

// withdraw forgets the status announced under uri. It goes through the
// tick so that it runs after the announcement, which may not have run
// yet when the prompt is cancelled.
func (p *permissionPrompter) withdraw(uri workspaceapi.URI) {
	p.scheduleNextTick(func() {
		_ = p.status.SetTabStatus(uri, programstatus.Report{State: programstatus.Clear})
	})
}

func permissionPromptWarningMessage(req PermissionRequest) string {
	if req.CommandPath != "" {
		return fmt.Sprintf("User authorization required for %s", req.CommandPath)
	}
	if req.ExtensionName != "" {
		return fmt.Sprintf("User authorization required for %s", req.ExtensionName)
	}
	if req.Path != "" {
		return fmt.Sprintf("User authorization required for %s", req.Path)
	}
	return "User authorization required"
}

func pluginPermissionPromptMessage(req PermissionRequest) string {
	action := fmt.Sprintf("**%s**", PermissionActionText(req.Permission))
	commandScopeNote := commandScopeNote(req)
	if req.CommandPath != "" && req.ExtensionID != "" && req.ExtensionName != "" {
		command := commandPromptFragment(req)
		message := fmt.Sprintf("Extension %s by %s wants to %s",
			req.ExtensionName, req.DeveloperID, command.text)
		if !command.block {
			message += "."
		}
		return message + commandScopeNote
	}
	if req.ExtensionID != "" && req.ExtensionName != "" {
		return fmt.Sprintf("Extension %s by %s wants to %s.",
			req.ExtensionName, req.DeveloperID, action)
	}
	if req.CommandPath != "" {
		command := commandPromptFragment(req)
		program := programPromptFragment(req.Path, req.Args, true, true)
		if req.LauncherPath != "" {
			program = appendLauncherPromptFragment(program, req.LauncherPath,
				req.LauncherArgs, true, true)
		}
		message := joinPromptFragments(program, "wants to", command)
		if !command.block {
			message += "."
		}
		return message + commandScopeNote
	}
	program := programPromptFragment(req.Path, req.Args, false, false)
	if req.LauncherPath != "" {
		program = appendLauncherPromptFragment(program, req.LauncherPath,
			req.LauncherArgs, false, false)
	}
	return joinPromptFragments(program, "wants to", promptFragment{text: action}) + "."
}

type promptFragment struct {
	text  string
	block bool
}

func commandPromptFragment(req PermissionRequest) promptFragment {
	args := promptCommandArgsFragment(req.CommandPath, req.CommandArgs, true)
	if args.block {
		command := fmt.Sprintf("run **%s** with args:\n\n%s", req.CommandPath, args.text)
		if req.CommandDir != "" {
			command = fmt.Sprintf("%s\n\nin **%s**", command, req.CommandDir)
		}
		return promptFragment{text: command, block: true}
	}
	command := fmt.Sprintf("run **%s** with args %s", req.CommandPath, args.text)
	if req.CommandDir != "" {
		command = fmt.Sprintf("%s in **%s**", command, req.CommandDir)
	}
	return promptFragment{text: command}
}

func programPromptFragment(path string, args []string, boldPath bool, boldArgs bool) promptFragment {
	pathText := path
	if boldPath {
		pathText = fmt.Sprintf("**%s**", path)
	}
	argText := promptArgsFragment(args, boldArgs)
	if argText.block {
		return promptFragment{
			text:  fmt.Sprintf("Program %s with args:\n\n%s", pathText, argText.text),
			block: true,
		}
	}
	return promptFragment{text: fmt.Sprintf("Program %s with args %s", pathText, argText.text)}
}

func appendLauncherPromptFragment(
	program promptFragment, launcherPath string, launcherArgs []string,
	boldPath bool, boldArgs bool,
) promptFragment {
	pathText := launcherPath
	if boldPath {
		pathText = fmt.Sprintf("**%s**", launcherPath)
	}
	args := promptArgsFragment(launcherArgs, boldArgs)
	var launcher promptFragment
	if args.block {
		launcher = promptFragment{
			text:  fmt.Sprintf("running inside %s with args:\n\n%s", pathText, args.text),
			block: true,
		}
	} else {
		launcher = promptFragment{text: fmt.Sprintf("running inside %s %s", pathText, args.text)}
	}
	return joinAdjacentPromptFragments(program, launcher)
}

func promptArgsFragment(args []string, bold bool) promptFragment {
	return promptArgsFragmentWithBlock(args, bold, promptArgsNeedCodeBlock(args))
}

func promptCommandArgsFragment(path string, args []string, bold bool) promptFragment {
	return promptArgsFragmentWithBlock(args, bold,
		promptArgsNeedCodeBlock(args) || promptCommandArgsNeedCodeBlock(path, args))
}

func promptArgsFragmentWithBlock(args []string, bold bool, block bool) promptFragment {
	if block {
		content := strings.Join(args, "\n")
		return promptFragment{text: fencedPromptCodeBlock(content), block: true}
	}
	text := fmt.Sprintf("%v", args)
	if bold {
		text = fmt.Sprintf("**%s**", text)
	}
	return promptFragment{text: text}
}

func promptCommandArgsNeedCodeBlock(path string, args []string) bool {
	base := filepath.Base(path)
	if len(args) < 2 {
		return false
	}
	scriptFlags := map[string]bool{
		"-c": true, "-lc": true,
		"-e": true, "-E": true,
	}
	for i := 0; i < len(args)-1; i++ {
		if !scriptFlags[args[i]] {
			continue
		}
		switch base {
		case "python", "python3", "perl", "ruby", "node":
			return true
		}
	}
	return false
}

func promptArgsNeedCodeBlock(args []string) bool {
	const longArgThreshold = 80
	if len(args) == 0 {
		return false
	}
	content := strings.Join(args, "\n")
	if len(content) > longArgThreshold {
		return true
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, "\n\r`") || containsMarkdownMeta(arg) {
			return true
		}
	}
	return false
}

func containsMarkdownMeta(s string) bool {
	markers := []string{"**", "__", "[", "](", "#", "<", ">"}
	for _, marker := range markers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func fencedPromptCodeBlock(content string) string {
	fence := strings.Repeat("`", maxConsecutiveBackticks(content)+1)
	if len(fence) < 3 {
		fence = "```"
	}
	return fmt.Sprintf("%s\n%s\n%s", fence, content, fence)
}

func maxConsecutiveBackticks(s string) int {
	maxRun := 0
	currentRun := 0
	for _, r := range s {
		if r == '`' {
			currentRun++
			if currentRun > maxRun {
				maxRun = currentRun
			}
			continue
		}
		currentRun = 0
	}
	return maxRun
}

func joinPromptFragments(left promptFragment, connector string, right promptFragment) string {
	separator := " "
	if left.block {
		separator = "\n\n"
	}
	return fmt.Sprintf("%s%s%s %s", left.text, separator, connector, right.text)
}

func joinAdjacentPromptFragments(left promptFragment, right promptFragment) promptFragment {
	separator := " "
	if left.block {
		separator = "\n\n"
	}
	return promptFragment{text: left.text + separator + right.text, block: right.block}
}

// commandScopeNote tells the user which files a command runs, so that a
// program shadowing another stands out, and what deciding for good does.
func commandScopeNote(req PermissionRequest) string {
	var note strings.Builder
	var code []string
	if len(req.Programs) > 0 {
		parts := make([]string, len(req.Programs))
		for i, p := range req.Programs {
			parts[i] = programNote(p)
			if p.RunsCode {
				code = append(code, inlineCode(p.Name))
			}
		}
		fmt.Fprintf(&note, "\n\nRuns %s.", strings.Join(parts, ", "))
	}
	switch req.CommandRemember {
	case RememberNothing:
		if req.CommandOnceReason != "" {
			fmt.Fprintf(&note, "\n\nRune asks every time because %s.", req.CommandOnceReason)
		}
	case RememberPrograms:
		var approved []string
		for _, p := range req.Programs {
			switch {
			case p.Absent:
				approved = append(approved, inlineCode(p.Name)+" (as long as it is not found)")
			case p.ResolvedPath != "":
				approved = append(approved, inlineCode(p.ResolvedPath))
			}
		}
		fmt.Fprintf(&note, "\n\nChoosing **Always** approves %s from now on.",
			strings.Join(approved, ", "))
	default:
		note.WriteString("\n\nChoosing **Always** or **Never** lets you pick which commands " +
			"and subcommands the decision covers.")
		// The warning is about what deciding for good covers: this
		// command is shown in full above.
		if len(code) > 0 {
			fmt.Fprintf(&note, " ⚠️ Be careful: %s can run arbitrary code.",
				strings.Join(code, ", "))
		}
	}
	return note.String()
}

func programNote(p ProgramSummary) string {
	switch {
	case p.Opaque:
		return inlineCode(p.Name) + " (known only when it runs)"
	case p.Builtin:
		return inlineCode(p.Name) + " (shell builtin)"
	case p.Absent:
		return inlineCode(p.Name) + " (not found)"
	case p.ResolvedPath == "" && p.DirUnknown != "":
		return inlineCode(p.Name) + " (" + p.DirUnknown + ", so Rune asks every time)"
	case p.ResolvedPath == "":
		return inlineCode(p.Name) + " (not found, so Rune asks every time)"
	case p.Changed:
		return "**" + inlineCode(p.ResolvedPath) + "** (changed since you approved it)"
	default:
		return inlineCode(p.ResolvedPath)
	}
}

func renewalPromptMessage(req RenewalRequest) string {
	var msg strings.Builder
	for _, c := range req.Changes {
		scopes := make([]string, len(c.Scopes))
		for i, s := range c.Scopes {
			scopes[i] = inlineCode(s)
		}
		approved := "commands that run it"
		if len(scopes) > 0 {
			approved = strings.Join(scopes, ", ")
		}
		fmt.Fprintf(&msg, "**%s** has changed since you approved %s for %s.\n\n",
			inlineCode(c.Path), approved, req.Requester)
		fmt.Fprintf(&msg, "sha256 %s → %s, modified %s.\n\n",
			inlineCode(fingerprintPreview(c.Old.SHA256)),
			inlineCode(fingerprintPreview(c.New.SHA256)),
			c.New.ModTime.Local().Format("2006-01-02 15:04"))
	}
	msg.WriteString("Renew authorization?")
	return msg.String()
}

func fingerprintPreview(sha string) string {
	if len(sha) <= fingerprintPreviewLength {
		return sha
	}
	return sha[:fingerprintPreviewLength] + "…"
}

// inlineCode renders s as a markdown code span.
func inlineCode(s string) string {
	fence := strings.Repeat("`", maxConsecutiveBackticks(s)+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}

func pluginPromptDecision(opt string) PermissionDecision {
	switch opt {
	case PromptOptionYes:
		return PermissionAllowOnce
	case PromptOptionYesAlways:
		return PermissionAllowAlways
	case PromptOptionNoNever:
		return PermissionDenyAlways
	case PromptOptionNo:
		fallthrough
	default:
		return PermissionDenyOnce
	}
}
