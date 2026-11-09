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

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"

	acp "github.com/coder/acp-go-sdk"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi/acpstdio"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"unstable.build/rune/cmd/rune-agent/acpagent"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/extension"
	"unstable.build/rune/cmd/rune-agent/headless"
	"unstable.build/rune/internal/debug"
)

var (
	// Tag is a compile-time variable
	Tag = "development"
	// Commit is a compile-time variable
	Commit = "HEAD"
	// Version is injected at compile time.
	Version string
)

func init() {
	Version = fmt.Sprintf("%s (HEAD is %s)", Tag, Commit)
}

func main() {
	debug.StartPProfOnSignal()

	if len(os.Args) > 1 && os.Args[1] == acpCommand {
		os.Exit(runACP(os.Args[2:]))
	}
	if slices.Contains(os.Args[1:], headlessFlag) {
		os.Exit(runHeadless(os.Args[1:]))
	}

	ext, metadata := extension.NewExtension()
	err := extensionapi.ServeWorkspaceExtension(ext, metadata)
	if err != nil {
		slog.Error("serve extension", "error", err)
		os.Exit(1)
	}
}

const acpCommand = "acp"

// acpExtensionID keeps the storage of ACP sessions apart from the
// extension's chats and headless runs.
const acpExtensionID = "rune-agent-acp"

// runACP serves rune-agent as an ACP agent on stdin and stdout until the
// client disconnects, and returns the process exit code: 0 the client
// disconnected or a signal stopped it, 1 it could not start or the
// connection failed.
func runACP(args []string) int {
	// stdout carries the protocol, so nothing else may write to it.
	stdout := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = stdout }()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "rune-agent: unexpected arguments %q\nusage: rune-agent acp\n", args)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	w, err := headless.Connect(acpExtensionID)
	if err != nil {
		slog.Error("connect to rune", "error", err)
		return 1
	}
	host := headless.NewHost(ctx, w)
	store := dialoguemanager.NewStore(host.Storage, filepath.Join(host.DataDir, "acp"))
	a := acpagent.New(acpagent.Config{
		Info:  acp.Implementation{Name: "rune-agent", Title: new("Rune Agent"), Version: debug.Tag},
		LLM:   host.LLM,
		Store: store,
		NewAgent: func(_ context.Context, req acpagent.AgentRequest) (*agent.Agent, *skills.SkillRegistry, error) {
			cwd, err := host.FS.URI(req.Cwd)
			if err != nil {
				return nil, nil, fmt.Errorf("resolve cwd %q: %w", req.Cwd, err)
			}
			built := host.NewAgent(headless.AgentConfig{
				Cwd:        cwd,
				Store:      store,
				AgentID:    acpExtensionID,
				SessionKey: req.ID,
				Model:      req.Model,
				Prompter:   req.Prompter,
				Progress:   req.Progress,
			})
			return built.Agent, built.Skills, nil
		},
	})
	err = acpstdio.Serve(ctx, a, os.Stdin, stdout)
	_ = a.Close()
	if err != nil && ctx.Err() == nil {
		slog.Error("serve acp", "error", err)
		return 1
	}
	return 0
}

const headlessFlag = "--headless"

const headlessUsage = "usage: rune-agent --headless <instructions-file> " +
	"[--model <provider>/<model>] [--effort <effort>]"

// runHeadless drives a non-interactive run and returns the process exit
// code: 0 ran to completion, 1 could not start, 2 the agent loop
// terminated with an error, 130/143 interrupted by SIGINT/SIGTERM.
func runHeadless(args []string) int {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	opts, err := parseHeadlessArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rune-agent: %s\n", err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigch)

	// A runner timeout must still yield a partial trajectory, so a
	// signal only cancels the context and lets Run flush.
	var signalCode atomic.Int32
	go debug.CapturePanicReport(func() {
		select {
		case sig := <-sigch:
			if sig == syscall.SIGTERM {
				signalCode.Store(143)
			} else {
				signalCode.Store(130)
			}
			cancel()
		case <-ctx.Done():
		}
	})

	res, err := headless.Run(ctx, opts)
	if err != nil {
		slog.Error("headless run", "error", err)
		return 1
	}
	if code := signalCode.Load(); code != 0 {
		return int(code)
	}
	if res.AgentError != nil {
		slog.Error("agent loop", "error", res.AgentError)
		return 2
	}
	return 0
}

// parseHeadlessArgs parses the --headless invocation. The instructions
// file is mandatory and positional, immediately after --headless.
func parseHeadlessArgs(args []string) (headless.Options, error) {
	var opts headless.Options
	if len(args) < 2 || args[0] != headlessFlag {
		return opts, errors.New(headlessUsage)
	}
	opts.InstructionsFile = args[1]
	if strings.HasPrefix(opts.InstructionsFile, "-") {
		return headless.Options{}, errors.New(headlessUsage)
	}
	rest := args[2:]
	for i := 0; i < len(rest); i++ {
		var target *string
		switch rest[i] {
		case "--model":
			target = &opts.Model
		case "--effort":
			target = &opts.Effort
		default:
			return headless.Options{}, fmt.Errorf(
				"unknown argument %q\n%s", rest[i], headlessUsage)
		}
		if i+1 >= len(rest) {
			return headless.Options{}, fmt.Errorf(
				"%s requires a value\n%s", rest[i], headlessUsage)
		}
		*target = rest[i+1]
		i++
	}
	return opts, nil
}
