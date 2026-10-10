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

// Package llmconsole exposes the `models` REPL command tree on the rune
// (IDE) side. The shell wraps an llmrouter.Router and surfaces two
// sub-commands:
//
//   - models providers — inspect provider authentication (codex login/status today)
//   - models local — manage the local llama.cpp model cache
//
// The agent loop continues to expose its own `agent` shell from
// rune-agent until migration; this shell is intentionally narrower.
package llmconsole

import (
	"context"
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llmrouter"
)

// CommandName is the top-level REPL command exposed by this shell.
const CommandName = "models"

var commandManual = textapi.CommandManual{
	Name:     CommandName,
	Summary:  "Inspect and manage LLM providers and local models.",
	Synopsis: "<command> [<args>]",
	Commands: []textapi.CommandManual{
		{
			Name: "providers",
			Summary: "Sign in to model providers. `codex` and `claude` use a " +
				"ChatGPT/Claude subscription via browser sign-in; `openai`, " +
				"`anthropic`, `gemini`, and `bedrock` use pay-as-you-go API keys.",
			Synopsis: "(codex|claude|openai|anthropic|gemini|bedrock)",
			Commands: []textapi.CommandManual{
				{
					Name: "codex",
					Summary: "Use OpenAI models through your ChatGPT Codex " +
						"subscription (OAuth sign-in, no API key). The separate " +
						"`openai` provider bills the same models by API key " +
						"instead.",
					Synopsis: "(login|status)",
					Commands: []textapi.CommandManual{
						{
							Name: "login",
							Summary: "Open your browser to sign in with your ChatGPT " +
								"account. Requests then bill against your Codex " +
								"subscription instead of an API key.",
						},
						{
							Name: "status",
							Summary: "Show whether you are signed in with a ChatGPT " +
								"Codex subscription, and which account and plan.",
						},
					},
				},
				{
					Name: "claude",
					Summary: "Use Anthropic models through your Claude Pro/Max " +
						"subscription (OAuth sign-in, no API key). The " +
						"separate `anthropic` provider bills the same models " +
						"by API key instead.",
					Synopsis: "(login|status)",
					Commands: []textapi.CommandManual{
						{
							Name: "login",
							Summary: "Open your browser to sign in with your Claude " +
								"Pro/Max account. Requests then bill against your " +
								"subscription instead of an API key. Subscription " +
								"usage draws from a separate monthly Agent-SDK " +
								"credit; to continue past it, enable usage credits " +
								"(\"extra usage\") in your Claude account under " +
								"Settings > Usage.",
						},
						{
							Name: "status",
							Summary: "Show whether you are signed in with a Claude " +
								"subscription, which account and plan, and how " +
								"subscription usage is billed.",
						},
					},
				},
				{
					Name: "openai",
					Summary: "Use OpenAI models with a pay-as-you-go API key. To " +
						"use a ChatGPT subscription instead, see the `codex` " +
						"provider.",
					Synopsis: "(add|remove|use|status)",
					Commands: hostedProviderManual,
				},
				{
					Name: "anthropic",
					Summary: "Use Anthropic (Claude) models with a pay-as-you-go " +
						"API key. To use a Claude Pro/Max subscription instead, " +
						"see the `claude` provider.",
					Synopsis: "(add|remove|use|status)",
					Commands: hostedProviderManual,
				},
				{
					Name:     "gemini",
					Summary:  "Use Google Gemini models with a pay-as-you-go API key.",
					Synopsis: "(add|remove|use|status)",
					Commands: hostedProviderManual,
				},
				{
					Name: "bedrock",
					Summary: "Use Amazon Bedrock models with a Bedrock API key. " +
						"Keys are bound to one AWS region, so `add` takes the " +
						"region alongside the key name. Without a stored key, " +
						"Rune uses the AWS sign-in already on this machine (run " +
						"`aws configure` or `aws sso login` with the AWS CLI " +
						"to sign in).",
					Synopsis: "(add|remove|use|status)",
					Commands: bedrockProviderManual,
				},
			},
		},
		{
			Name:     "local",
			Summary:  "Manage locally cached GGUF models.",
			Synopsis: "(list|download|delete) [<args>]",
			Commands: []textapi.CommandManual{
				{Name: "list", Summary: "List GGUF models in the local cache."},
				{
					Name:     "download",
					Summary:  "Download a GGUF model from an OCI registry.",
					Synopsis: "[<host>/]<owner>/<repo>[:<tag> | @<digest>]",
				},
				{Name: "delete", Summary: "Delete a locally cached GGUF model.", Synopsis: "<reference>"},
			},
		},
		{
			Name: "alias",
			Summary: "Manage named model aliases. An alias is an arbitrary " +
				"name you choose that resolves to a `provider/name` model " +
				"and can be used anywhere a model is accepted, including the " +
				"model name you pass when opening a chat. An unset alias " +
				"falls back to `default`; an unset `default` resolves to " +
				"the latest authenticated provider's flagship model.",
			Synopsis: "(list|set|remove) [<args>]",
			Commands: []textapi.CommandManual{
				{Name: "list", Summary: "List aliases and the models they resolve to."},
				{
					Name:     "set",
					Summary:  "Point an alias at a model. The bare form `alias <name> <provider/name>` also works.",
					Synopsis: "<name> <provider/name>",
				},
				{Name: "remove", Summary: "Delete a named alias.", Synopsis: "<name>"},
			},
		},
	},
}

// Manual returns the parent REPL command manual.
func Manual() textapi.CommandManual { return commandManual }

// hostedProviderManual is the shared subcommand manual for the
// openai/anthropic/gemini providers, which all manage named API keys.
var hostedProviderManual = []textapi.CommandManual{
	{
		Name:     "add",
		Summary:  "Add a new API key under a name. Opens a hidden prompt to paste the key, tests it against the provider, then stores it. The first key you add becomes the active one.",
		Synopsis: "<name>",
	},
	{
		Name:     "remove",
		Summary:  "Delete a stored API key by name. If it was the active key, another stored key is promoted automatically.",
		Synopsis: "<name>",
	},
	{
		Name:     "use",
		Summary:  "Switch the active API key to a stored one by name. Takes effect immediately, no restart needed.",
		Synopsis: "<name>",
	},
	{
		Name:    "status",
		Summary: "List the names of stored API keys and show which one is currently active.",
	},
}

// bedrockProviderManual mirrors hostedProviderManual with a region-aware
// `add`: Bedrock API keys only work in the AWS region they were minted
// in, so the region is stored with the key.
var bedrockProviderManual = []textapi.CommandManual{
	{
		Name: "add",
		Summary: "Add a new API key under a name, scoped to the AWS region " +
			"it was generated in. Opens a hidden prompt to paste the key, " +
			"tests it against Bedrock in that region, then stores both " +
			"together. The first key you add becomes the active one.",
		Synopsis: "<name> <region>",
	},
	{
		Name:     "remove",
		Summary:  "Delete a stored API key by name. If it was the active key, another stored key is promoted automatically.",
		Synopsis: "<name>",
	},
	{
		Name:     "use",
		Summary:  "Switch the active API key to a stored one by name. Takes effect immediately, no restart needed.",
		Synopsis: "<name>",
	},
	{
		Name:    "status",
		Summary: "List the stored API keys with their regions and show which one is currently active.",
	},
}

// Config configures a Handler. All fields are mandatory: the router
// is the dispatcher used by the providers subtree to advertise the
// installed clients, the local registry backs the `local` subtree,
// and storage backs codex auth state.
type Config struct {
	// Service is the LLM service from which available models are read.
	Service llmapi.Service
	// Router is the concrete router that backs the hosted-provider key
	// management subcommands (add/remove/use/status).
	Router *llmrouter.Router
	// LocalRegistry is the llama.cpp cache registry that the local
	// subcommand operates on.
	LocalRegistry *llamacpp.Registry
	// Storage is the persistent storage service used by the codex
	// provider for auth state.
	Storage storageapi.Service
	// WindowManager opens the floating redacted prompt and confirmation
	// prompt used by the hosted-provider `add` flow.
	WindowManager browserapi.WindowManager
	// Notifications surfaces success/failure of key operations.
	Notifications browserapi.Notifications
	// ScheduleNextTick marshals UI work back onto the host event loop
	// from the REPL goroutine.
	ScheduleNextTick func(func()) bool
	// PromptOpener opens yes/no confirmation prompts using the IDE's
	// shared prompt machinery, so they render markdown and use the
	// configured button styling exactly like core IDE prompts.
	PromptOpener PromptOpener
}

// PromptOpener opens a browser prompt for host-side decisions. It mirrors
// the core IDE prompt entry point (text.Component.Prompt) so the provider
// flows render markdown messages and styled option buttons consistently
// with the rest of the IDE.
type PromptOpener interface {
	Prompt(
		message string, options []string,
		bindings []term.KeyComb,
		promptHandler handler.PromptHandler,
	) browser.Window
}

// Handler is the parent dispatcher for the `models` command tree.
type Handler struct {
	service       llmapi.Service
	localRegistry *llamacpp.Registry
	storage       storageapi.Service

	providers *providersHandler
	local     *localHandler
	alias     *aliasHandler
}

// New returns a Handler configured with cfg. It panics if any
// dependency is nil — the rune-side wiring constructs every collaborator
// at workspace boot, so a missing one indicates a programming error.
func New(cfg Config) *Handler {
	if cfg.Service == nil {
		panic("llmconsole: Config.Router must not be nil")
	}
	if cfg.LocalRegistry == nil {
		panic("llmconsole: Config.LocalRegistry must not be nil")
	}
	if cfg.Storage == nil {
		panic("llmconsole: Config.Storage must not be nil")
	}
	if cfg.Router == nil {
		panic("llmconsole: Config.Router must not be nil")
	}
	if cfg.WindowManager == nil {
		panic("llmconsole: Config.WindowManager must not be nil")
	}
	if cfg.Notifications == nil {
		panic("llmconsole: Config.Notifications must not be nil")
	}
	if cfg.ScheduleNextTick == nil {
		panic("llmconsole: Config.ScheduleNextTick must not be nil")
	}
	if cfg.PromptOpener == nil {
		panic("llmconsole: Config.PromptOpener must not be nil")
	}
	return &Handler{
		service:       cfg.Service,
		localRegistry: cfg.LocalRegistry,
		storage:       cfg.Storage,
		providers: newProvidersHandler(providersConfig{
			storage:  cfg.Storage,
			router:   cfg.Router,
			wm:       cfg.WindowManager,
			notifs:   cfg.Notifications,
			schedule: cfg.ScheduleNextTick,
			prompt:   cfg.PromptOpener,
		}),
		local: newLocalHandler(cfg.LocalRegistry),
		alias: newAliasHandler(cfg.Router),
	}
}

// HandleCommand satisfies repl.CommandHandler. The parent shell splits
// the first arg and routes to the matching sub-handler.
func (h *Handler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return markdownOutput(usageMarkdown(commandManual)), nil
	}
	sub := cmd.Args[0]
	rest := repl.Command{Name: cmd.Name + " " + sub, Args: cmd.Args[1:]}
	switch sub {
	case "providers":
		return h.providers.HandleCommand(ctx, rest, pw)
	case "local":
		return h.local.HandleCommand(ctx, rest, pw)
	case "alias":
		return h.alias.HandleCommand(ctx, rest, pw)
	case "help":
		return markdownOutput(usageMarkdown(commandManual)), nil
	default:
		return nil, fmt.Errorf("unknown command: %s", sub)
	}
}

// Complete satisfies repl.CommandHandler.
func (h *Handler) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if len(args) <= 1 {
		filter := ""
		if len(args) == 1 {
			filter = args[0]
		}
		return iterator.FromSlice(filterNames([]string{"providers", "local", "alias", "help"}, filter)), nil
	}
	switch args[0] {
	case "providers":
		return h.providers.Complete(ctx, cmd, args[1:])
	case "local":
		return h.local.Complete(ctx, cmd, args[1:])
	case "alias":
		return h.alias.Complete(ctx, cmd, args[1:])
	}
	return iterator.FromSlice[string](nil), nil
}

// Help satisfies textapi.REPLHandler. The shell renders the manual
// for the `models` tree (or a subcommand when args points at one).
func (h *Handler) Help(
	_ context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	man := commandManual
	for _, a := range args {
		sub, ok := findSubcommand(man, a)
		if !ok {
			break
		}
		man = sub
	}
	return markdownOutput(usageMarkdown(man)), nil
}

// findSubcommand looks up a child of man whose Name matches name.
func findSubcommand(man textapi.CommandManual, name string) (textapi.CommandManual, bool) {
	for _, c := range man.Commands {
		if c.Name == name {
			return c, true
		}
	}
	return textapi.CommandManual{}, false
}

// providerManual returns the manual node for `models providers <name>`
// with its Name rewritten to the full command path, so usageMarkdown
// renders an accurate title, usage line, and example block. ok is false
// for unknown providers.
func providerManual(provider string) (textapi.CommandManual, bool) {
	providers, ok := findSubcommand(commandManual, "providers")
	if !ok {
		return textapi.CommandManual{}, false
	}
	sub, ok := findSubcommand(providers, provider)
	if !ok {
		return textapi.CommandManual{}, false
	}
	sub.Name = "models providers " + provider
	return sub, true
}

// providersManual returns the `models providers` manual node with its
// Name rewritten to the full command path.
func providersManual() textapi.CommandManual {
	man, ok := findSubcommand(commandManual, "providers")
	if !ok {
		return textapi.CommandManual{}
	}
	man.Name = "models providers"
	return man
}

// markdownOutput wraps a markdown string into a single-shot iterator
// suitable for returning from HandleCommand. Falls back to a plain
// responsive string when the markdown parser rejects the content.
func markdownOutput(content string) iterator.Iterator[component.Responsive] {
	md, err := markdown.New(content)
	if err != nil {
		r := component.NewResponsiveString(content, component.StringResponsiveConfig{})
		return iterator.FromSlice([]component.Responsive{r})
	}
	return iterator.FromSlice([]component.Responsive{md})
}

// markdownResponsive returns a single Responsive value wrapping the
// given markdown content.
func markdownResponsive(content string) component.Responsive {
	it := markdownOutput(content)
	v, _ := it.Next(context.Background())
	return v
}

func filterNames(names []string, prefix string) []string {
	if prefix == "" {
		return names
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

// usageMarkdown renders a full, user-friendly help page for a command
// manual node: a title, the summary as prose, a copy-pasteable usage
// line, a sub-command reference with each child's own synopsis, and a
// worked example block when one is registered for the node.
func usageMarkdown(man textapi.CommandManual) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## `%s`\n\n", man.Name)
	if man.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", man.Summary)
	}
	if man.Synopsis != "" {
		fmt.Fprintf(&b, "**Usage:** `%s %s`\n\n", man.Name, man.Synopsis)
	}
	if len(man.Commands) > 0 {
		b.WriteString("### Subcommands\n\n")
		for _, c := range man.Commands {
			invocation := c.Name
			if c.Synopsis != "" {
				invocation = fmt.Sprintf("%s %s", c.Name, c.Synopsis)
			}
			fmt.Fprintf(&b, "- `%s`\n  %s\n", invocation, c.Summary)
		}
		b.WriteByte('\n')
	}
	if ex := commandExamples[man.Name]; ex != "" {
		b.WriteString("### Examples\n\n")
		b.WriteString(ex)
		if !strings.HasSuffix(ex, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// commandExamples holds worked-example blocks keyed by command-manual
// node name. The key is the node's Name (e.g. "models providers
// openai"); only nodes worth illustrating need an entry.
var commandExamples = map[string]string{
	"models providers": "```\n" +
		"models providers codex login        # sign in to ChatGPT Codex\n" +
		"models providers claude login       # sign in with a Claude subscription\n" +
		"models providers openai add work    # store an OpenAI key named 'work'\n" +
		"models providers anthropic status   # see which Anthropic key is active\n" +
		"```",
	"models providers openai": "```\n" +
		"models providers openai add work    # paste a key, store it as 'work'\n" +
		"models providers openai use work    # make 'work' the active key\n" +
		"models providers openai status      # list keys and the active one\n" +
		"models providers openai remove work # delete the 'work' key\n" +
		"```",
	"models providers anthropic": "```\n" +
		"models providers anthropic add work    # paste a key, store it as 'work'\n" +
		"models providers anthropic use work    # make 'work' the active key\n" +
		"models providers anthropic status      # list keys and the active one\n" +
		"models providers anthropic remove work # delete the 'work' key\n" +
		"```",
	"models providers gemini": "```\n" +
		"models providers gemini add work    # paste a key, store it as 'work'\n" +
		"models providers gemini use work    # make 'work' the active key\n" +
		"models providers gemini status      # list keys and the active one\n" +
		"models providers gemini remove work # delete the 'work' key\n" +
		"```",
	"models providers bedrock": "```\n" +
		"models providers bedrock add work us-east-1 # paste a key minted in us-east-1, store it as 'work'\n" +
		"models providers bedrock use work           # make 'work' the active key\n" +
		"models providers bedrock status             # list keys, regions, and the active one\n" +
		"models providers bedrock remove work        # delete the 'work' key\n" +
		"```",
	"alias": "```\n" +
		"models alias                          # list aliases and what they resolve to\n" +
		"models alias set default openai/gpt-5.5  # point 'default' at a model\n" +
		"models alias default openai/gpt-5.5      # shorthand for set\n" +
		"models alias remove default           # clear 'default' (back to auto)\n" +
		"```",
}
