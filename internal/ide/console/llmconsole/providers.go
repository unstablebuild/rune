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

package llmconsole

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/inputbox"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/llm/claude"
	"unstable.build/rune/internal/llm/codex"
	"unstable.build/rune/internal/llm/llmrouter"
)

// hostedProviders are the API-key-managed providers under
// `models providers`.
var hostedProviders = []string{
	llmrouter.ProviderOpenAI,
	llmrouter.ProviderAnthropic,
	llmrouter.ProviderGemini,
	llmrouter.ProviderBedrock,
}

// providersConfig bundles the dependencies the providers subtree needs.
type providersConfig struct {
	storage  storageapi.Service
	router   *llmrouter.Router
	wm       browserapi.WindowManager
	notifs   browserapi.Notifications
	schedule func(func()) bool
	prompt   PromptOpener
}

// providersHandler implements the `models providers` subtree.
type providersHandler struct {
	storage  storageapi.Service
	router   *llmrouter.Router
	wm       browserapi.WindowManager
	notifs   browserapi.Notifications
	schedule func(func()) bool
	prompt   PromptOpener
	// verify tests an API key before storing it. It defaults to the
	// router's live verification; tests swap in a fake to avoid network
	// calls. region scopes the probe for region-bound providers (Bedrock)
	// and is empty otherwise.
	verify func(ctx context.Context, provider, key, region string) error
	// spawn runs key verification off the event loop. It defaults to a
	// panic-captured goroutine; tests swap in an inline runner.
	spawn func(func())
}

func newProvidersHandler(cfg providersConfig) *providersHandler {
	h := &providersHandler{
		storage:  cfg.storage,
		router:   cfg.router,
		wm:       cfg.wm,
		notifs:   cfg.notifs,
		schedule: cfg.schedule,
		prompt:   cfg.prompt,
	}
	h.verify = func(ctx context.Context, provider, key, region string) error {
		return h.router.VerifyProviderKey(ctx, provider, key, region)
	}
	h.spawn = func(fn func()) {
		go debug.CapturePanicReport(fn)
	}
	return h
}

func isHostedProvider(name string) bool {
	return slices.Contains(hostedProviders, name)
}

// HandleCommand satisfies repl.CommandHandler for the providers subtree.
// cmd.Args is the remaining args after the `providers` prefix has been
// stripped by the parent dispatcher.
func (h *providersHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return markdownOutput(usageMarkdown(providersManual())), nil
	}
	provider := cmd.Args[0]
	switch {
	case provider == "codex":
		return h.handleCodex(ctx, cmd.Args[1:])
	case provider == "claude":
		return h.handleClaude(ctx, cmd.Args[1:])
	case isHostedProvider(provider):
		return h.handleHostedKeyProvider(ctx, provider, cmd.Args[1:])
	default:
		return markdownOutput(unknownProviderMarkdown(provider)), nil
	}
}

// Complete satisfies repl.CommandHandler for the providers subtree.
func (h *providersHandler) Complete(
	ctx context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	providerNames := append([]string{"codex", "claude"}, hostedProviders...)
	switch len(args) {
	case 0:
		return iterator.FromSlice(providerNames), nil
	case 1:
		return iterator.FromSlice(filterNames(providerNames, args[0])), nil
	case 2:
		switch {
		case args[0] == "codex":
			return iterator.FromSlice(filterNames([]string{"login", "status"}, args[1])), nil
		case args[0] == "claude":
			return iterator.FromSlice(filterNames([]string{"login", "status"}, args[1])), nil
		case isHostedProvider(args[0]):
			return iterator.FromSlice(
				filterNames([]string{"add", "remove", "use", "status"}, args[1])), nil
		default:
			return iterator.FromSlice[string](nil), nil
		}
	case 3:
		if !isHostedProvider(args[0]) {
			return iterator.FromSlice[string](nil), nil
		}
		if args[1] != "remove" && args[1] != "use" {
			return iterator.FromSlice[string](nil), nil
		}
		names, err := h.router.ProviderKeyNames(ctx, args[0])
		if err != nil {
			return iterator.FromSlice[string](nil), nil
		}
		return iterator.FromSlice(filterNames(names, args[2])), nil
	}
	return iterator.FromSlice[string](nil), nil
}

func (h *providersHandler) handleCodex(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return markdownOutput(providerHelp("codex")), nil
	}
	switch args[0] {
	case "status":
		return h.codexStatus(ctx)
	case "login":
		return h.codexLogin(ctx)
	default:
		return markdownOutput(unknownCodexSubcommandMarkdown(args[0])), nil
	}
}

func (h *providersHandler) codexStatus(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	status, err := codex.Status(ctx, h.storage)
	if err != nil {
		return nil, err
	}
	return markdownOutput(formatCodexStatus(status)), nil
}

func (h *providersHandler) codexLogin(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	session, err := codex.StartLogin(ctx, h.storage)
	if err != nil {
		return nil, err
	}
	started := false
	waited := false
	return iterator.FromFunc(
		func(ctx context.Context) (component.Responsive, bool, error) {
			if !started {
				started = true
				return markdownResponsive(formatCodexLoginStart(session)), true, nil
			}
			if waited {
				return nil, false, nil
			}
			waited = true
			cred, err := session.Wait(ctx)
			if err != nil {
				return nil, false, err
			}
			status := codex.AuthStatus{
				Authenticated:   true,
				Email:           cred.Email,
				AccountID:       cred.AccountID,
				PlanType:        cred.PlanType,
				Expiry:          cred.Expiry,
				LastRefresh:     cred.LastRefresh,
				HasRefreshToken: cred.RefreshToken != "",
			}
			return markdownResponsive(formatCodexStatus(status)), true, nil
		},
		session.Close,
	), nil
}

func formatCodexLoginStart(session *codex.LoginSession) string {
	var b strings.Builder
	b.WriteString("Open this URL in your browser to sign in to ChatGPT Codex:\n\n")
	b.WriteByte('<')
	b.WriteString(session.AuthURL())
	b.WriteString(">\n")
	if err := session.BrowserError(); err != nil {
		fmt.Fprintf(&b, "\nCould not open a browser automatically: `%v`.\n", err)
	}
	return b.String()
}

func formatCodexStatus(status codex.AuthStatus) string {
	var b strings.Builder
	b.WriteString("## Codex Provider\n\n")
	b.WriteString("Runs OpenAI models through your ChatGPT Codex subscription " +
		"(OAuth sign-in, no API key). The separate `openai` provider bills the " +
		"same models by API key instead.\n\n")
	if !status.Authenticated {
		b.WriteString("- **Status**: not authenticated\n\n")
		b.WriteString("Run `models providers codex login` to sign in with your " +
			"ChatGPT subscription.\n")
		return b.String()
	}
	if status.Expired {
		b.WriteString("- **Status**: authenticated credential expired\n")
	} else {
		b.WriteString("- **Status**: authenticated\n")
	}
	if status.Email != "" {
		fmt.Fprintf(&b, "- **Account**: %s\n", status.Email)
	}
	if status.AccountID != "" {
		fmt.Fprintf(&b, "- **Account ID**: `%s`\n", status.AccountID)
	}
	if status.PlanType != "" {
		fmt.Fprintf(&b, "- **Plan**: %s\n", status.PlanType)
	}
	if !status.Expiry.IsZero() {
		fmt.Fprintf(&b, "- **Expires**: %s\n", status.Expiry.Format("2006-01-02T15:04:05Z07:00"))
	}
	if !status.LastRefresh.IsZero() {
		fmt.Fprintf(&b, "- **Last refresh**: %s\n", status.LastRefresh.Format("2006-01-02T15:04:05Z07:00"))
	}
	if !status.HasRefreshToken {
		b.WriteString("- **Refresh token**: missing\n")
	}
	return b.String()
}

func (h *providersHandler) handleClaude(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return markdownOutput(providerHelp("claude")), nil
	}
	switch args[0] {
	case "status":
		return h.claudeStatus(ctx)
	case "login":
		return h.claudeLogin(ctx)
	default:
		return markdownOutput(unknownClaudeSubcommandMarkdown(args[0])), nil
	}
}

func (h *providersHandler) claudeStatus(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	status, err := claude.Status(ctx, h.storage)
	if err != nil {
		return nil, err
	}
	return markdownOutput(formatClaudeStatus(status)), nil
}

func (h *providersHandler) claudeLogin(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	session, err := claude.StartLogin(ctx, h.storage)
	if err != nil {
		return nil, err
	}
	started := false
	waited := false
	return iterator.FromFunc(
		func(ctx context.Context) (component.Responsive, bool, error) {
			if !started {
				started = true
				return markdownResponsive(formatClaudeLoginStart(session)), true, nil
			}
			if waited {
				return nil, false, nil
			}
			waited = true
			cred, err := session.Wait(ctx)
			if err != nil {
				return nil, false, err
			}
			status := claude.AuthStatus{
				Authenticated:   true,
				Email:           cred.Email,
				AccountID:       cred.AccountID,
				PlanType:        cred.PlanType,
				Expiry:          cred.Expiry,
				LastRefresh:     cred.LastRefresh,
				HasRefreshToken: cred.RefreshToken != "",
			}
			return markdownResponsive(formatClaudeStatus(status)), true, nil
		},
		session.Close,
	), nil
}

func formatClaudeLoginStart(session *claude.LoginSession) string {
	var b strings.Builder
	b.WriteString("Sign in with your Claude Pro/Max subscription to use Anthropic " +
		"models without an API key. Open this URL in your browser to continue:\n\n")
	b.WriteByte('<')
	b.WriteString(session.AuthURL())
	b.WriteString(">\n")
	if err := session.BrowserError(); err != nil {
		fmt.Fprintf(&b, "\nCould not open a browser automatically: `%v`.\n", err)
	}
	return b.String()
}

func formatClaudeStatus(status claude.AuthStatus) string {
	var b strings.Builder
	b.WriteString("## Claude Provider\n\n")
	b.WriteString("Runs Anthropic models through your Claude Pro/Max subscription " +
		"(OAuth sign-in, no API key). The separate `anthropic` provider bills the " +
		"same models by API key instead.\n\n")
	if !status.Authenticated {
		b.WriteString("- **Status**: not authenticated\n\n")
		b.WriteString("Run `models providers claude login` to sign in with your " +
			"Claude subscription.\n")
		return b.String()
	}
	if status.Expired {
		b.WriteString("- **Status**: authenticated credential expired\n")
	} else {
		b.WriteString("- **Status**: authenticated\n")
	}
	if status.Email != "" {
		fmt.Fprintf(&b, "- **Account**: %s\n", status.Email)
	}
	if status.AccountID != "" {
		fmt.Fprintf(&b, "- **Account ID**: `%s`\n", status.AccountID)
	}
	if status.PlanType != "" {
		fmt.Fprintf(&b, "- **Plan**: %s\n", status.PlanType)
	}
	if !status.Expiry.IsZero() {
		fmt.Fprintf(&b, "- **Expires**: %s\n", status.Expiry.Format("2006-01-02T15:04:05Z07:00"))
	}
	if !status.LastRefresh.IsZero() {
		fmt.Fprintf(&b, "- **Last refresh**: %s\n", status.LastRefresh.Format("2006-01-02T15:04:05Z07:00"))
	}
	if !status.HasRefreshToken {
		b.WriteString("- **Refresh token**: missing\n")
	}
	b.WriteString("\nSubscription usage here draws from a separate monthly " +
		"Agent-SDK credit (distinct from your Claude.ai and official Claude Code " +
		"limits). To continue past that credit, enable usage credits " +
		"(\"extra usage\") in your Claude account under Settings > Usage.\n")
	return b.String()
}

// handleHostedKeyProvider dispatches the add/remove/use/status subcommands
// for the openai/anthropic/gemini/bedrock providers.
func (h *providersHandler) handleHostedKeyProvider(
	ctx context.Context, provider string, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return markdownOutput(providerHelp(provider)), nil
	}
	switch args[0] {
	case "add":
		return h.providerAdd(ctx, provider, args[1:])
	case "remove":
		return h.providerRemove(ctx, provider, args[1:])
	case "use":
		return h.providerUse(ctx, provider, args[1:])
	case "status":
		return h.providerStatus(ctx, provider)
	default:
		return markdownOutput(unknownSubcommandMarkdown(provider, args[0])), nil
	}
}

func (h *providersHandler) providerRemove(
	ctx context.Context, provider string, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return markdownOutput(providerHelp(provider)), nil
	}
	name := args[0]
	if err := h.router.RemoveProviderKey(ctx, provider, name); err != nil {
		return nil, err
	}
	return markdownOutput(fmt.Sprintf(
		"Removed `%s` API key **%s**.", provider, name)), nil
}

func (h *providersHandler) providerUse(
	ctx context.Context, provider string, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return markdownOutput(providerHelp(provider)), nil
	}
	name := args[0]
	if err := h.router.UseProviderKey(ctx, provider, name); err != nil {
		return nil, err
	}
	return markdownOutput(fmt.Sprintf(
		"`%s` API key **%s** is now active.", provider, name)), nil
}

func (h *providersHandler) providerStatus(
	ctx context.Context, provider string,
) (iterator.Iterator[component.Responsive], error) {
	names, err := h.router.ProviderKeyNames(ctx, provider)
	if err != nil {
		return nil, err
	}
	active, err := h.router.ProviderKeyActiveName(ctx, provider)
	if err != nil {
		return nil, err
	}
	var regions map[string]string
	if provider == llmrouter.ProviderBedrock {
		if regions, err = h.router.ProviderKeyRegions(ctx, provider); err != nil {
			return nil, err
		}
	}
	status := formatHostedStatus(provider, names, active, regions)
	if provider == llmrouter.ProviderBedrock {
		status += h.bedrockChainNote(ctx, len(names) > 0)
	}
	return markdownOutput(status), nil
}

// bedrockChainNote explains how Bedrock authenticates when no API key is
// stored: Rune falls back to the ambient AWS credentials.
func (h *providersHandler) bedrockChainNote(ctx context.Context, hasKeys bool) string {
	source, ok := h.router.BedrockCredentialChain(ctx)
	switch {
	case ok && hasKeys:
		return fmt.Sprintf("\nWithout a stored key, Rune would sign requests "+
			"with your AWS credentials from %s.\n", source)
	case ok:
		return fmt.Sprintf("\nRune is signing Bedrock requests with your AWS "+
			"credentials from %s. The region comes from your AWS "+
			"configuration (`AWS_REGION` or the profile's region).\n", source)
	case hasKeys:
		return ""
	default:
		return "\nNo AWS credentials were found either. Add an API key with " +
			"`models providers bedrock add <name> <region>`, or sign in with " +
			"the AWS CLI (`aws configure` for access keys, `aws sso login` " +
			"for single sign-on) and Rune will use that sign-in.\n"
	}
}

// formatHostedStatus renders the stored-key listing. regions, when
// non-nil, annotates each key with the region it is scoped to.
func formatHostedStatus(provider string, names []string, active string, regions map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s API keys\n\n", providerLabel(provider))
	if len(names) == 0 {
		fmt.Fprintf(&b, "No API keys stored yet.\n\n"+
			"Add one with `models providers %s add <name>` — you'll be "+
			"prompted to paste the key, and it's verified before being saved.\n",
			provider)
		return b.String()
	}
	fmt.Fprintf(&b, "%d stored key(s). The **active** key is the one Rune "+
		"uses for %s requests.\n\n", len(names), providerLabel(provider))
	for _, n := range names {
		label := n
		if region := regions[n]; region != "" {
			label = fmt.Sprintf("%s (%s)", n, region)
		}
		if n == active {
			fmt.Fprintf(&b, "- **%s** — active\n", label)
		} else {
			fmt.Fprintf(&b, "- %s\n", label)
		}
	}
	fmt.Fprintf(&b, "\nSwitch with `models providers %s use <name>`.\n", provider)
	return b.String()
}

// providerHelp renders the full help page for a hosted provider node
// (e.g. `models providers openai`), falling back to the providers help
// when the provider is unknown.
func providerHelp(provider string) string {
	man, ok := providerManual(provider)
	if !ok {
		return usageMarkdown(providersManual())
	}
	return usageMarkdown(man)
}

// unknownProviderMarkdown explains that a provider name is not
// recognised and shows the providers help so the user can pick a valid
// one.
func unknownProviderMarkdown(provider string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unknown provider `%s`. Choose one of `codex`, `claude`, "+
		"`openai`, `anthropic`, `gemini`, or `bedrock`.\n\n", provider)
	b.WriteString(usageMarkdown(providersManual()))
	return b.String()
}

// unknownSubcommandMarkdown explains that a hosted-provider subcommand
// is not recognised and shows that provider's help.
func unknownSubcommandMarkdown(provider, sub string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unknown `%s` subcommand `%s`. Choose one of `add`, "+
		"`remove`, `use`, or `status`.\n\n", provider, sub)
	b.WriteString(providerHelp(provider))
	return b.String()
}

// unknownCodexSubcommandMarkdown explains that a codex subcommand is not
// recognised and shows the codex help.
func unknownCodexSubcommandMarkdown(sub string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unknown `codex` subcommand `%s`. Choose one of `login` "+
		"or `status`.\n\n", sub)
	b.WriteString(providerHelp("codex"))
	return b.String()
}

// unknownClaudeSubcommandMarkdown explains that a claude subcommand is not
// recognised and shows the claude help.
func unknownClaudeSubcommandMarkdown(sub string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unknown `claude` subcommand `%s`. Choose one of `login` "+
		"or `status`.\n\n", sub)
	b.WriteString(providerHelp("claude"))
	return b.String()
}

// completionIter is a blocking output iterator that returns no rows and
// completes only when signalDone is called. providerAdd returns one so
// the companion shell (and the tutorial observing it) treats the `add`
// command as running until the asynchronous key prompt, verification, and
// storage have reached a terminal outcome. signalDone is safe to call from
// both the event loop and verification goroutine, and more than once.
type completionIter struct {
	done chan struct{}
	once sync.Once
}

func newCompletionIter() *completionIter {
	return &completionIter{done: make(chan struct{})}
}

func (c *completionIter) signalDone() { c.once.Do(func() { close(c.done) }) }

func (c *completionIter) Next(ctx context.Context) (component.Responsive, bool) {
	select {
	case <-c.done:
	case <-ctx.Done():
	}
	return nil, false
}

func (c *completionIter) Err() error { return nil }

// Close unblocks Next so a closed shell tab does not leak the iterator.
func (c *completionIter) Close() error {
	c.signalDone()
	return nil
}

// providerAdd opens a redacted floating prompt to capture an API key for
// the given provider, verifies it live, and stores it on success. On a
// verification failure it opens a yes/no confirmation prompt carrying the
// provider's error. The work is asynchronous; the returned iterator stays
// open until the prompt resolves (key stored, declined, or aborted) so
// callers can observe true completion.
func (h *providersHandler) providerAdd(
	ctx context.Context, provider string, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return markdownOutput(addKeyHelp(provider)), nil
	}
	name := args[0]
	region := ""
	if provider == llmrouter.ProviderBedrock {
		// Bedrock keys only work in the region they were minted in, so the
		// region is recorded with the key instead of living in config.
		if len(args) < 2 {
			return markdownOutput(
				"Bedrock API keys are bound to one AWS region, so name it " +
					"when adding the key:\n\n```\nmodels providers bedrock " +
					"add " + name + " <region>\n```\n\nUse the region shown " +
					"in the AWS console where you generated the key, for " +
					"example `us-east-1`.\n"), nil
		}
		region = args[1]
	}
	ci := newCompletionIter()
	h.openKeyPrompt(ctx, provider, name, region, ci)
	return ci, nil
}

func providerLabel(provider string) string {
	if provider == "" {
		return provider
	}
	return strings.ToUpper(provider[:1]) + provider[1:]
}

// addKeyHelp renders the help page for `models providers <p> add`,
// shown when the user runs `add` without supplying a key name.
func addKeyHelp(provider string) string {
	man, ok := providerManual(provider)
	if !ok {
		return providerHelp(provider)
	}
	add, ok := findSubcommand(man, "add")
	if !ok {
		return usageMarkdown(man)
	}
	add.Name = "models providers " + provider + " add"
	return usageMarkdown(add)
}

// openKeyPrompt schedules the redacted inputbox onto the event loop. ci is
// signaled on every terminal outcome so the add command's iterator
// completes only once the prompt has fully resolved.
func (h *providersHandler) openKeyPrompt(
	ctx context.Context, provider, name, region string, ci *completionIter,
) {
	h.schedule(func() {
		ib := inputbox.New(
			inputbox.WithPrompt(
				fmt.Sprintf("Paste your %s API key: ", providerLabel(provider))),
			inputbox.WithRedact(true),
			inputbox.WithCtrlCAborts(),
		)
		fh := &keyInputFloating{
			ib:    ib,
			label: fmt.Sprintf("Paste your %s API key: ", providerLabel(provider)),
			onDone: func(key string, aborted bool) {
				key = strings.TrimSpace(key)
				if aborted || key == "" {
					ci.signalDone()
					return
				}
				h.verifyAndStore(ctx, provider, name, key, region, ci)
			},
		}
		win, err := h.wm.Floating(fh, browserapi.FloatingConfig{
			Alignment: component.AlignmentCentered,
		})
		if err != nil {
			_, _ = h.notifs.Notify(browserapi.LevelError,
				"open %s api key prompt: %v", provider, err)
			ci.signalDone()
			return
		}
		fh.win = win
	})
}

// verifyAndStore runs key verification off the event loop and marshals the
// result back via schedule. On success it stores the key; on failure it
// opens a yes/no confirmation prompt. ci is propagated so completion is
// signaled from whichever terminal branch runs.
func (h *providersHandler) verifyAndStore(
	ctx context.Context, provider, name, key, region string, ci *completionIter,
) {
	h.spawn(func() {
		verifyErr := h.verify(ctx, provider, key, region)
		h.schedule(func() {
			if verifyErr == nil {
				h.storeKey(ctx, provider, name, key, region, ci)
				return
			}
			h.openConfirmPrompt(ctx, provider, name, key, region, verifyErr, ci)
		})
	})
}

func (h *providersHandler) storeKey(
	ctx context.Context, provider, name, key, region string, ci *completionIter,
) {
	defer ci.signalDone()
	if err := h.router.AddProviderKey(ctx, provider, name, key, region); err != nil {
		_, _ = h.notifs.Notify(browserapi.LevelError,
			"store %s api key: %v", provider, err)
		return
	}
	_, _ = h.notifs.Notify(browserapi.LevelSuccess,
		"%s api key '%s' added", provider, name)
}

// openConfirmPrompt asks the user whether to store a key that failed
// verification, showing the provider's error.
func (h *providersHandler) openConfirmPrompt(
	ctx context.Context, provider, name, key, region string, verifyErr error, ci *completionIter,
) {
	const (
		yesOpt = " yes "
		noOpt  = " no "
	)
	message := fmt.Sprintf(
		"This api key was tested and `%s` returned the following error:\n\n```\n%s\n```\n\nDo you still want to add it?",
		provider, verifyErr.Error())
	h.prompt.Prompt(
		message,
		[]string{yesOpt, noOpt},
		[]term.KeyComb{{Ch: 'y'}, {Ch: 'n'}},
		handler.FuncPromptHandler(func(idx int, _ string) {
			if idx == 0 {
				h.storeKey(ctx, provider, name, key, region, ci)
				return
			}
			_, _ = h.notifs.Notify(browserapi.LevelInfo,
				"%s api key '%s' not added", provider, name)
			ci.signalDone()
		}, func() error { ci.signalDone(); return nil }),
	)
}
