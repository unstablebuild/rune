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

package extension

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"mvdan.cc/sh/v3/syntax"
	runemcp "unstable.build/rune/cmd/rune-agent/mcp"
)

const mcpTrustPartition = "mcp-trust"

const (
	trustOpt     = "  Trust  "
	trustOnceOpt = "  Trust once  "
	distrustOpt  = "  Don't connect  "
)

// mcpTrust asks the user before Rune connects to an MCP server a
// workspace declares. A cloned repository would otherwise decide which
// commands Rune starts and which hosts it sends requests, headers and
// environment values to. The command authorization a stdio launch goes
// through does not stand in for it: it trusts the extension asking, not
// the workspace content the extension was told to run, and may be set
// to grant every command a verified extension runs.
type mcpTrust struct {
	storage  storageapi.Service
	prompter browserapi.Prompter

	// mu serializes the prompts of servers connecting concurrently.
	mu sync.Mutex
	// session maps a server to the config hash trusted until Rune
	// exits.
	session map[string]string
}

func newMCPTrust(storage storageapi.Service, prompter browserapi.Prompter) *mcpTrust {
	return &mcpTrust{
		storage:  storageapi.WithPartition(storage, mcpTrustPartition),
		prompter: prompter,
		session:  make(map[string]string),
	}
}

// mcpTrustDoc records that the user trusts a server for as long as its
// config hashes to Hash.
type mcpTrustDoc struct {
	Hash string
}

// Allow reports whether Rune may connect to the server name that the
// workspace at root declares with cfg, as written, before expansion.
// exp lists the environment variables cfg refers to. Servers from the
// user's own configuration are always allowed. Workspace servers need
// the user's trust, asked for once per config: any change to how the
// server starts or where Rune connects, and what it sends there, asks
// again. Without a prompter, untrusted servers are not allowed.
func (t *mcpTrust) Allow(
	ctx context.Context, root workspaceapi.URI, name string,
	cfg runemcp.ServerConfig, exp runemcp.Expansion,
) (bool, error) {
	kind, err := cfg.Transport()
	if err != nil {
		return false, err
	}
	if !cfg.FromWorkspace() {
		return true, nil
	}
	hash, err := mcpTrustHash(kind, cfg)
	if err != nil {
		return false, err
	}
	id := mcpTrustID(root, name)

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.session[id] == hash {
		return true, nil
	}
	var doc mcpTrustDoc
	if err := t.storage.Get(ctx, id, &doc); err == nil && doc.Hash == hash {
		return true, nil
	} else if err != nil && !errors.Is(err, storageapi.ErrNotFound) {
		slog.Warn("mcp: read trust", "server", name, "error", err)
	}
	if t.prompter == nil {
		return false, nil
	}

	answer, err := t.ask(ctx, name, cfg, exp)
	if err != nil {
		return false, err
	}
	switch answer {
	case trustOpt:
		t.session[id] = hash
		if err := t.storage.Set(ctx, id, mcpTrustDoc{Hash: hash}); err != nil {
			slog.Warn("mcp: save trust", "server", name, "error", err)
		}
		return true, nil
	case trustOnceOpt:
		t.session[id] = hash
		return true, nil
	default:
		return false, nil
	}
}

// ask shows the trust prompt and waits for the chosen option, which is
// empty when the user dismisses the prompt.
func (t *mcpTrust) ask(
	ctx context.Context, name string, cfg runemcp.ServerConfig, exp runemcp.Expansion,
) (string, error) {
	options := []string{trustOpt, trustOnceOpt, distrustOpt}
	// Selecting an option and the dismissal that follows both answer, so
	// only the first is kept.
	ch := make(chan string, 1)
	answer := func(option string) {
		select {
		case ch <- option:
		default:
		}
	}
	_, err := t.prompter.Prompt(browserapi.PromptConfig{
		Message:  mcpTrustMessage(name, cfg, exp),
		Options:  options,
		Bindings: []term.KeyComb{{Ch: 't'}, {Ch: 'o'}, {Ch: 'n'}},
	}, handler.FuncPromptHandler(
		func(idx int, _ string) { answer(options[idx]) },
		func() error {
			answer("")
			return nil
		}))
	if err != nil {
		return "", fmt.Errorf("ask to trust MCP server %q: %w", name, err)
	}
	select {
	case a := <-ch:
		return a, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func mcpTrustMessage(name string, cfg runemcp.ServerConfig, exp runemcp.Expansion) string {
	var b strings.Builder
	if kind, _ := cfg.Transport(); kind == runemcp.TransportStdio {
		fmt.Fprintf(&b, "This workspace's .mcp.json asks Rune to start the MCP server %s "+
			"on the workspace host, in the workspace root, with:\n\n%s\n\n",
			codeSpan(name), codeBlock(commandLine(cfg.Command, cfg.Args)))
		if len(cfg.Env) > 0 {
			fmt.Fprintf(&b, "- Environment variables it sets: %s\n", codeSpans(slices.Sorted(maps.Keys(cfg.Env))))
		}
	} else {
		fmt.Fprintf(&b, "This workspace's .mcp.json asks Rune to connect to the MCP server %s at:\n\n",
			codeSpan(name))
		b.WriteString(emphasizeHost(cfg.URL))
		b.WriteString("\n\n")
		if len(cfg.Headers) > 0 {
			fmt.Fprintf(&b, "- Headers sent: %s\n", codeSpans(slices.Sorted(maps.Keys(cfg.Headers))))
		}
	}
	if len(exp.Referenced) > 0 {
		fmt.Fprintf(&b, "- Your environment variables it receives: %s\n", codeSpans(exp.Referenced))
	}
	if cfg.HeadersHelper != "" {
		fmt.Fprintf(&b, "- Command run for headers before each connection:\n\n%s\n",
			codeBlock(cfg.HeadersHelper))
	}
	b.WriteString("\nOnly trust servers you recognize. Rune asks again if the workspace changes this server.")
	return b.String()
}

func codeSpans(names []string) string {
	spans := make([]string, len(names))
	for i, n := range names {
		spans[i] = codeSpan(n)
	}
	return strings.Join(spans, ", ")
}

// commandLine renders a command and its arguments as one shell line, so
// arguments holding spaces or quotes read as the separate words they are.
func commandLine(command string, args []string) string {
	words := make([]string, 0, 1+len(args))
	for _, w := range append([]string{command}, args...) {
		quoted, err := syntax.Quote(w, syntax.LangBash)
		if err != nil {
			quoted = strconv.Quote(w)
		}
		words = append(words, quoted)
	}
	return strings.Join(words, " ")
}

// emphasizeHost renders rawURL with its host in bold, so a look-alike
// path cannot pass for the destination.
func emphasizeHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return escapeMarkdown(rawURL)
	}
	i := strings.Index(rawURL, u.Host)
	if i < 0 {
		return escapeMarkdown(rawURL)
	}
	return escapeMarkdown(rawURL[:i]) + "**" + escapeMarkdown(u.Host) + "**" +
		escapeMarkdown(rawURL[i+len(u.Host):])
}

// escapeMarkdown backslash-escapes every ASCII punctuation character,
// so workspace-controlled text renders literally.
func escapeMarkdown(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 && strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func codeBlock(s string) string {
	fence := "```"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	return fence + "sh\n" + s + "\n" + fence
}

// mcpTrustID identifies a server of the workspace at root.
func mcpTrustID(root workspaceapi.URI, name string) string {
	sum := sha256.Sum256([]byte(root.String() + "\x00" + name))
	return hex.EncodeToString(sum[:])
}

// mcpTrustHash hashes the parts of cfg that decide what Rune starts or
// where it connects, and what it sends there.
func mcpTrustHash(kind string, cfg runemcp.ServerConfig) (string, error) {
	data, err := json.Marshal(struct {
		Type          string
		Command       string
		Args          []string
		Env           map[string]string
		URL           string
		Headers       map[string]string
		HeadersHelper string
		OAuth         *runemcp.OAuthConfig
	}{kind, cfg.Command, cfg.Args, cfg.Env, cfg.URL, cfg.Headers, cfg.HeadersHelper, cfg.OAuth})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
