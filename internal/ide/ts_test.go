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

//go:build e2e

// ide-package tests that specifically test TypeScript and its
// integration with tsgo through the editor/workspace LSP wiring.
package ide

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/vi"
)

func TestIDETypeScriptTsconfigDiagnosticsReachTheOpenTab(t *testing.T) {
	tsgo := findTsgoForIDE(t)

	// tsgo names tsconfig.json with its path case-folded on a
	// case-insensitive host, so the workspace path carries uppercase
	// letters. Symlinks are resolved so that case is the only
	// difference between tsgo's spelling and the editor's.
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	dir := filepath.Join(base, "App")
	for name, content := range map[string]string{
		"tsconfig.json": `{"compilerOptions": {"baseUrl": ".", "strict": true}, "include": ["src"]}`,
		"src/index.ts":  "export const one: number = 1;\n",
	} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	x, ws := newExForLSPIntegrationAt(t, dir, vi.Editor())
	rootURI, err := ws.URI(".")
	require.NoError(t, err)

	ui := newQueuedScheduler()
	callbacks := idelsp.NewCallbackHandler(nopNotifications{}, nil, nil,
		newEditorAdapter(&x.comp), ws, rootURI.String(),
		idelsp.CallbackHandlerConfig{ScheduleNextTick: ui.ScheduleNextTick})
	mgr := idelsp.New(rootURI, schemeForLSP(t, dir), schemeForLSP(t, dir),
		&goplsPkgManager{bin: tsgo}, nil, nil,
		idelsp.Config{
			Callback:          callbacks,
			MaxRetries:        1,
			InitializeTimeout: 30 * time.Second,
		})
	t.Cleanup(func() { _ = mgr.Close() })

	initOpts, err := json.Marshal(map[string]any{
		"langID":  "typescript",
		"command": tsgo + " --lsp --stdio",
	})
	require.NoError(t, err)
	// Like the TypeScript extension, advertise pull diagnostics: tsgo
	// then pushes only tsconfig diagnostics.
	_, err = mgr.Initialize(context.Background(), semanticapi.InitializeParams{
		RootURI:           rootURI.String(),
		InitializeOptions: initOpts,
		Capabilities: json.RawMessage(
			`{"textDocument": {"diagnostic": {}, "publishDiagnostics": {}}}`),
	})
	require.NoError(t, err)
	require.NoError(t, x.comp.SubscribeEvents(idelsp.EditorEvents(), mgr))

	tsconfigURI, err := ws.URI("tsconfig.json")
	require.NoError(t, err)
	indexURI, err := ws.URI("src/index.ts")
	require.NoError(t, err)
	require.NoError(t, x.editFiles(bgctx, tsconfigURI.String(), indexURI.String()))
	x.waitInflight()
	tsconfig, err := x.comp.Editor(tsconfigURI)
	require.NoError(t, err)

	const want = "Option 'baseUrl' has been removed."
	deadline := time.Now().Add(60 * time.Second)
	for !hasLocationMessage(tsconfig, "lsp-diagnostics", want) {
		if time.Now().After(deadline) {
			t.Fatalf("%q never reached the tsconfig.json tab; diagnostics by URI: %v",
				want, callbacks.Diagnostics())
		}
		time.Sleep(100 * time.Millisecond)
		ui.Flush(nil)
	}
}

func hasLocationMessage(h text.Handler, id, msg string) bool {
	for _, set := range h.LocationLists() {
		if set.ID != id {
			continue
		}
		for _, loc := range set.Locations {
			if strings.Contains(loc.Message, msg) {
				return true
			}
		}
	}
	return false
}

// findTsgoForIDE locates a TypeScript 7 native compiler, which serves
// LSP, or skips. An older tsc on PATH is not a candidate.
func findTsgoForIDE(t *testing.T) string {
	t.Helper()
	var candidates []string
	for _, name := range []string{"tsgo", "tsc"} {
		if bin, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, bin)
		}
	}
	candidates = append(candidates,
		filepath.Join(os.Getenv("HOME"), ".rune", "lib", "typescript", "bin", "tsgo"),
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "tsgo"),
	)
	for _, bin := range candidates {
		out, err := exec.Command(bin, "--version").Output()
		if err != nil {
			continue
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			continue
		}
		major, _, _ := strings.Cut(fields[len(fields)-1], ".")
		if n, err := strconv.Atoi(major); err == nil && n >= 7 {
			return bin
		}
	}
	t.Skip("TypeScript 7 (tsgo) not found, skipping typescript e2e test")
	return ""
}
