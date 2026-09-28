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

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// Permission prompt option labels shown in permission prompts.
const (
	PromptOptionYes       = "   Yes   "
	PromptOptionYesAlways = "   Always   "
	PromptOptionNo        = "   No   "
	PromptOptionNoNever   = "   Never   "
)

type permissionPrompter struct {
	promptOpener     PromptOpener
	scheduleNextTick func(func()) bool
	notifications    browserapi.Notifications
}

func newPermissionPrompter(
	promptOpener PromptOpener,
	scheduleNextTick func(func()) bool,
	notifications browserapi.Notifications,
) *permissionPrompter {
	if promptOpener == nil || scheduleNextTick == nil {
		return nil
	}
	if notifications == nil {
		notifications = nopPromptNotifications{}
	}
	return &permissionPrompter{
		promptOpener:     promptOpener,
		scheduleNextTick: scheduleNextTick,
		notifications:    notifications,
	}
}

func (p *permissionPrompter) PromptPermission(
	ctx context.Context, req PermissionRequest,
) (PermissionDecision, error) {
	result := make(chan PermissionDecision, 1)
	message := pluginPermissionPromptMessage(req)
	warning := permissionPromptWarningMessage(req)
	options := []string{
		PromptOptionYes,
		PromptOptionYesAlways,
		PromptOptionNo,
		PromptOptionNoNever,
	}
	bindings := []term.KeyComb{{Ch: 'Y'}, {Ch: 'A'}, {Ch: 'N'}, {Ch: 'V'}}
	scheduled := p.scheduleNextTick(func() {
		_, _ = p.notifications.Notify(browserapi.LevelWarn, warning)
		p.promptOpener.Prompt(message, options, bindings, handler.FuncPromptHandler(
			func(i int, opt string) {
				select {
				case result <- pluginPromptDecision(opt):
				default:
				}
			},
			func() error {
				select {
				case result <- PermissionDenyOnce:
				default:
				}
				return nil
			}))
	})
	if !scheduled {
		return PermissionDenyOnce, nil
	}

	select {
	case decision := <-result:
		return decision, nil
	case <-ctx.Done():
		return PermissionDenyOnce, ctx.Err()
	}
}

func permissionPromptWarningMessage(req PermissionRequest) string {
	labels := displayScopeLabels(req)
	if len(labels) > 0 {
		return fmt.Sprintf("User authorization required for %s", strings.Join(labels, ", "))
	}
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

func displayScopeLabels(req PermissionRequest) []string {
	labels := req.CommandScopeLabels
	if len(labels) == 0 && req.CommandScopeLabel != "" {
		labels = []string{req.CommandScopeLabel}
	}
	if len(labels) == 0 {
		return nil
	}
	ret := make([]string, len(labels))
	for i, l := range labels {
		ret[i] = strings.TrimSpace(strings.ReplaceAll(l, "*", ""))
	}
	return ret
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

// commandScopeNote renders the scope-approval suffix shown after a
// command-scoped permission request.
// for a command-scoped permission prompt. When multiple scope labels are
// provided, each is rendered bold and separated by commas so the user
// sees which commands will be broadened.
func commandScopeNote(req PermissionRequest) string {
	if req.CommandScopeExact {
		return "\n\nChoosing **Always** approves only this exact command. " +
			"Any change to its arguments or working directory will prompt again."
	}
	labels := displayScopeLabels(req)
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, len(labels))
	for i, l := range labels {
		parts[i] = fmt.Sprintf("**%s**", l)
	}
	return fmt.Sprintf(
		"\n\nChoosing **Always** approves %s for all future authorization requests, for any argument combination.",
		strings.Join(parts, ", "),
	)
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

// nopPromptNotifications is used when no notifications implementation is
// provided to newPermissionPrompter; it silently drops all notifications.
type nopPromptNotifications struct{}

func (nopPromptNotifications) Notify(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	return "", nil
}

func (nopPromptNotifications) NotifyOnce(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	return "", nil
}

func (nopPromptNotifications) UpdateNotificationProgress(
	string, string, int64, int64,
) error {
	return nil
}
