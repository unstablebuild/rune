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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/debug"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"golang.org/x/mod/modfile"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// modCommandHandler creates a handler for a module management command
// that executes goplsCommand against the go.mod nearest to the open file.
// buildArgs returns the command's argument for that go.mod: gopls does
// not validate it, and panics on a struct missing its URI.
func modCommandHandler(
	lsp semanticapi.LSP,
	notify browserapi.Notifications,
	goplsCommand string,
	buildArgs func(goModURI string) (any, error),
) textapi.CommandHandler {
	return &modCmd{
		lsp: lsp, notify: notify, goplsCommand: goplsCommand, buildArgs: buildArgs,
	}
}

// tidyArgs builds gopls.tidy's command.URIArgs.
func tidyArgs(goModURI string) (any, error) {
	return map[string]any{"URIs": []string{goModURI}}, nil
}

// vendorArgs builds gopls.vendor's command.URIArg.
func vendorArgs(goModURI string) (any, error) {
	return map[string]any{"URI": goModURI}, nil
}

// checkUpgradesArgs builds gopls.check_upgrades's command.CheckUpgradesArgs.
// gopls only checks the modules it is given, so it is given every
// requirement in go.mod, as gopls's own "Check for upgrades" code lens does.
func checkUpgradesArgs(fs workspaceapi.FileSystem) func(string) (any, error) {
	return func(goModURI string) (any, error) {
		path := strings.TrimPrefix(goModURI, "file://")
		f, err := fs.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		mod, err := modfile.ParseLax(path, data, nil)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		modules := make([]string, 0, len(mod.Require))
		for _, req := range mod.Require {
			modules = append(modules, req.Mod.Path)
		}
		return map[string]any{"URI": goModURI, "Modules": modules}, nil
	}
}

// vulncheckHandler creates a handler for gopls.vulncheck (synchronous).
// Unlike the deprecated gopls.run_govulncheck which returned only a
// progress token, gopls.vulncheck blocks until the scan completes and
// returns the actual vulnerability results. Progress is still reported
// via $/progress notifications during execution.
func vulncheckHandler(
	lsp semanticapi.LSP,
	notify browserapi.Notifications,
) textapi.CommandHandler {
	return &vulncheckCmd{lsp: lsp, notify: notify}
}

var _ textapi.CommandHandler = (*vulncheckCmd)(nil)

type vulncheckCmd struct {
	lsp    semanticapi.LSP
	notify browserapi.Notifications
}

func (h *vulncheckCmd) HandleCommand(
	ctx context.Context, cmd textapi.Command,
) error {
	if cmd.Resource == nil {
		return fmt.Errorf("no file open; open a file first")
	}

	fileURI := lspcmd.URIToLSP(cmd.URI)
	goModURI := findGoModURI(fileURI)

	argsData, err := json.Marshal(map[string]any{"URI": goModURI, "Pattern": "./..."})
	if err != nil {
		return fmt.Errorf("marshal args: %w", err)
	}

	// The request ctx is canceled when HandleCommand returns, but the scan
	// takes much longer.
	ctx = context.WithoutCancel(ctx)
	go debug.CapturePanicReport(func() {
		_, err := h.lsp.ExecuteCommand(ctx, semanticapi.ExecuteCommandParams{
			Command:   "gopls.vulncheck",
			Arguments: []json.RawMessage{argsData},
		})
		if err != nil {
			_, _ = h.notify.Notify(browserapi.LevelError, "vulncheck: %s", err)
			return
		}
	})
	return nil
}

func (h *vulncheckCmd) Complete(
	_ context.Context, _ string, _ []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

var _ textapi.CommandHandler = (*modCmd)(nil)

type modCmd struct {
	lsp          semanticapi.LSP
	notify       browserapi.Notifications
	goplsCommand string
	buildArgs    func(goModURI string) (any, error)
}

func (h *modCmd) HandleCommand(
	ctx context.Context, cmd textapi.Command,
) error {
	if cmd.Resource == nil {
		return fmt.Errorf("no file open; open a file first")
	}

	fileURI := lspcmd.URIToLSP(cmd.URI)
	goModURI := findGoModURI(fileURI)

	args, err := h.buildArgs(goModURI)
	if err != nil {
		return fmt.Errorf("%s: %w", h.goplsCommand, err)
	}
	argsData, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal args: %w", err)
	}

	// The request ctx is canceled when HandleCommand returns, before the
	// command completes.
	ctx = context.WithoutCancel(ctx)
	go debug.CapturePanicReport(func() {
		result, err := h.lsp.ExecuteCommand(ctx, semanticapi.ExecuteCommandParams{
			Command:   h.goplsCommand,
			Arguments: []json.RawMessage{argsData},
		})
		if err != nil {
			_, _ = h.notify.Notify(browserapi.LevelError, "execute %s: %s", h.goplsCommand, err)
			return
		}

		if result != "" {
			_, _ = h.notify.Notify(browserapi.LevelInfo, "%s: %s", h.goplsCommand, result)
		} else {
			_, _ = h.notify.Notify(browserapi.LevelInfo, "%s completed", h.goplsCommand)
		}
	})
	return nil
}

func (h *modCmd) Complete(
	_ context.Context, _ string, _ []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// addImportHandler creates a handler for the "add-import" subcommand
// that adds an import path to the current file. It completes the path
// from the packages the last focused Go file can import, so it subscribes
// to the editor's focus events.
func addImportHandler(
	lsp semanticapi.LSP,
	editor textapi.Editor,
	notify browserapi.Notifications,
) (textapi.CommandHandler, error) {
	h := &addImportCmd{lsp: lsp, notify: notify}
	if err := editor.SubscribeEvents([]textapi.EventType{textapi.EventTypeFocus}, h); err != nil {
		return nil, fmt.Errorf("subscribe focus events: %w", err)
	}
	return h, nil
}

var (
	_ textapi.CommandHandler = (*addImportCmd)(nil)
	_ textapi.EventHandler   = (*addImportCmd)(nil)
)

type addImportCmd struct {
	lsp    semanticapi.LSP
	notify browserapi.Notifications

	mu sync.Mutex
	// focused is the Go file focused last. Focusing the console or a
	// non-Go file does not clear it, since completion runs from the
	// console.
	focused workspaceapi.URI
}

func (h *addImportCmd) Handle(_ context.Context, ev textapi.Event) bool {
	if ev.Type == textapi.EventTypeFocus && strings.HasSuffix(ev.URI.Path(), ".go") {
		h.mu.Lock()
		h.focused = ev.URI
		h.mu.Unlock()
	}
	return false
}

func (h *addImportCmd) HandleCommand(
	ctx context.Context, cmd textapi.Command,
) error {
	if cmd.Resource == nil {
		return fmt.Errorf("no file open; open a file first")
	}
	if len(cmd.Args) == 0 {
		_, _ = h.notify.Notify(browserapi.LevelError,
			"add-import requires a package path argument")
		return nil
	}
	importPath := cmd.Args[0]
	fileURI := lspcmd.URIToLSP(cmd.URI)

	args := map[string]any{
		"URI":        fileURI,
		"ImportPath": importPath,
	}
	argsData, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal args: %w", err)
	}

	_, err = h.lsp.ExecuteCommand(ctx, semanticapi.ExecuteCommandParams{
		Command:   "gopls.add_import",
		Arguments: []json.RawMessage{argsData},
	})
	if err != nil {
		return fmt.Errorf("add import %s: %w", importPath, err)
	}

	_, _ = h.notify.Notify(browserapi.LevelInfo, "Added import: %s", importPath)
	return nil
}

func (h *addImportCmd) Complete(
	ctx context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	// Only the package argument completes; with no args the subcommand
	// name itself is the word being completed.
	if len(args) != 1 {
		return iterator.Empty[string](), nil
	}
	h.mu.Lock()
	focused := h.focused
	h.mu.Unlock()
	if focused.String() == "" {
		return iterator.Empty[string](), nil
	}
	argsData, err := json.Marshal(map[string]string{"URI": lspcmd.URIToLSP(focused)})
	if err != nil {
		return nil, fmt.Errorf("marshal args: %w", err)
	}
	result, err := h.lsp.ExecuteCommand(ctx, semanticapi.ExecuteCommandParams{
		Command:   "gopls.list_known_packages",
		Arguments: []json.RawMessage{argsData},
	})
	if err != nil {
		return nil, fmt.Errorf("list known packages: %w", err)
	}
	var resp struct {
		Packages []string `json:"Packages"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		return nil, fmt.Errorf("decode known packages: %w", err)
	}
	prefix := args[0]
	matches := make([]string, 0, len(resp.Packages))
	for _, pkg := range resp.Packages {
		if strings.HasPrefix(pkg, prefix) {
			matches = append(matches, pkg)
		}
	}
	return iterator.FromSlice(matches), nil
}

// findGoModURI walks up from the given file URI to find the nearest
// directory containing a go.mod file. It strips the filename first,
// then walks up parent directories. If no go.mod is found, it falls
// back to the immediate parent directory of the file.
func findGoModURI(fileURI string) string {
	path := strings.TrimPrefix(fileURI, "file://")
	// Strip the filename to get the directory.
	idx := strings.LastIndex(path, "/")
	if idx > 0 {
		path = path[:idx]
	}
	dir := path
	for {
		candidate := dir + "/go.mod"
		if _, err := os.Stat(candidate); err == nil {
			return "file://" + candidate
		}
		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == dir || parent == "" {
			break
		}
		dir = parent
	}
	// Fallback: assume go.mod is in the starting directory.
	return "file://" + path + "/go.mod"
}
