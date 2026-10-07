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
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/extension/langext/langexttest"
)

func TestNewExtensionMetadata(t *testing.T) {
	_, meta := NewExtension()
	assert.Equal(t, "typescript", meta.ExtensionID)
	for _, p := range []extensionapi.Permission{
		extensionapi.PermissionLSP,
		extensionapi.PermissionPackages,
		extensionapi.PermissionCommands,
		extensionapi.PermissionBrowserWindowManager,
		extensionapi.PermissionBrowserResourceOpener,
	} {
		assert.Contains(t, meta.Permissions, p)
	}
}

func TestIsTSFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/ws/a.ts", true},
		{"/ws/a.tsx", true},
		{"/ws/a.mts", true},
		{"/ws/a.cts", true},
		{"/ws/a.js", true},
		{"/ws/a.jsx", true},
		{"/ws/a.mjs", true},
		{"/ws/a.cjs", true},
		{"/ws/a.d.ts", true},
		{"/ws/package.json", false},
		{"/ws/a.go", false},
		{"/ws/ts", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			uri, err := workspaceapi.ParseURI("file://" + tt.path)
			require.NoError(t, err)
			assert.Equal(t, tt.want, isTSFile(uri))
		})
	}
}

func TestDetectTSProject(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeFS)
		want  bool
	}{
		{"empty", func(*fakeFS) {}, false},
		{"package.json", func(f *fakeFS) { f.addFile("package.json") }, true},
		{"tsconfig.json", func(f *fakeFS) { f.addFile("tsconfig.json") }, true},
		{"jsconfig.json", func(f *fakeFS) { f.addFile("jsconfig.json") }, true},
		{"top-level script", func(f *fakeFS) { f.addFile("build.mjs") }, true},
		{"marker dir is not a manifest", func(f *fakeFS) { f.addDir("package.json") }, false},
		{"nested sources only", func(f *fakeFS) { f.addFile("web/src/app.ts") }, false},
		{"other languages only", func(f *fakeFS) { f.addFile("main.go") }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newFakeFS("/ws")
			tt.setup(fs)
			assert.Equal(t, tt.want, detectTSProject(fs))
		})
	}
}

// tsExtensionEnv is the extension brought up against a fake workspace
// at /ws whose typescript package ships tsgo.
type tsExtensionEnv struct {
	lsp     *captureLSP
	editor  *fakeEditor
	notify  *fakeNotifications
	console textapi.CommandManual
	prompt  textapi.CommandManual
	err     error
}

func runTSExtension(t *testing.T, fs *fakeFS, inst *langexttest.Installer) tsExtensionEnv {
	t.Helper()
	return runTSExtensionWith(t, fs, inst, stubConfig{})
}

func runTSExtensionWith(
	t *testing.T, fs *fakeFS, inst *langexttest.Installer, cfg stubConfig,
) tsExtensionEnv {
	t.Helper()
	env := tsExtensionEnv{lsp: newCaptureLSP(), editor: &fakeEditor{}, notify: &fakeNotifications{}}
	env.err = (&tsExtension{}).extendWorkspaceWith(context.Background(),
		fs, newFakeExecutor(), env.notify, env.lsp, env.editor, &fakeWM{}, &recordingOpener{}, inst, cfg,
		func(m textapi.CommandManual, _ textapi.REPLHandler) error {
			env.console = m
			return nil
		},
		func(m textapi.CommandManual, _ textapi.CommandHandler) error {
			env.prompt = m
			return nil
		})
	return env
}

func rootURIs(env tsExtensionEnv) []string {
	var uris []string
	for _, p := range env.lsp.captured() {
		uris = append(uris, p.RootURI)
	}
	return uris
}

func TestExtendWorkspaceInitializesRootProject(t *testing.T) {
	inst := &langexttest.Installer{Files: []string{"/data/typescript/bin/tsgo"}}
	env := runTSExtension(t, newFakeFS("/ws").addFile("package.json"), inst)
	require.NoError(t, env.err)

	inits := env.lsp.captured()
	require.Len(t, inits, 1)
	assert.Equal(t, "file:///ws", inits[0].RootURI)
	var opts map[string]any
	require.NoError(t, json.Unmarshal(inits[0].InitializeOptions, &opts))
	assert.Equal(t, "typescript", opts["langID"])
	assert.Equal(t, "/data/typescript/bin/tsgo --lsp --stdio", opts["command"])
}

func TestExtendWorkspaceFailsWithoutServer(t *testing.T) {
	env := runTSExtension(t, newFakeFS("/ws").addFile("tsconfig.json"), &langexttest.Installer{})
	require.ErrorContains(t, env.err, "no TypeScript 7 language server found")
	assert.Empty(t, env.lsp.captured())
}

func TestExtendWorkspaceDiscovery(t *testing.T) {
	inst := &langexttest.Installer{Files: []string{"/data/typescript/bin/tsgo"}}
	tests := []struct {
		name     string
		fs       func() *fakeFS
		open     string
		wantRoot string
	}{
		{
			name: "nested project initializes at its marker",
			fs: func() *fakeFS {
				return newFakeFS("/ws").addFile("web/package.json").addFile("web/src/app.tsx")
			},
			open:     "/ws/web/src/app.tsx",
			wantRoot: "file:///ws/web",
		},
		{
			name: "a package shares its monorepo's root",
			fs: func() *fakeFS {
				return newFakeFS("/ws").addFile("web/package.json").
					addFile("web/packages/client/package.json").addFile("web/packages/client/tsconfig.json").
					addFile("web/packages/client/src/main.ts")
			},
			open:     "/ws/web/packages/client/src/main.ts",
			wantRoot: "file:///ws/web",
		},
		{
			name:     "marker-less script falls back to the workspace root",
			fs:       func() *fakeFS { return newFakeFS("/ws").addFile("tools/gen/run.js") },
			open:     "/ws/tools/gen/run.js",
			wantRoot: "file:///ws",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := runTSExtension(t, tt.fs(), inst)
			require.NoError(t, env.err)
			require.Empty(t, env.lsp.captured(), "no root project to bring up eagerly")

			env.editor.open(t, tt.open)
			env.lsp.waitForInit(t)
			assert.Equal(t, []string{tt.wantRoot}, rootURIs(env))
		})
	}
}

func TestExtendWorkspaceIgnoresUnrelatedOpens(t *testing.T) {
	inst := &langexttest.Installer{Files: []string{"/data/typescript/bin/tsgo"}}
	fs := newFakeFS("/ws").addFile("main.go").addFile("README.md")
	env := runTSExtension(t, fs, inst)
	require.NoError(t, env.err)

	env.editor.open(t, "/ws/main.go")
	env.editor.open(t, "/other/outside.ts")
	select {
	case <-env.lsp.ch:
		t.Fatalf("unexpected bring-up: %v", rootURIs(env))
	case <-time.After(200 * time.Millisecond):
	}
}

func TestExtendWorkspaceRegistersCommands(t *testing.T) {
	names := func(m textapi.CommandManual) []string {
		var out []string
		for _, c := range m.Commands {
			out = append(out, c.Name)
		}
		return out
	}
	tests := []struct {
		name          string
		cfg           stubConfig
		wantProfiling bool
	}{
		{"profiling commands are off by default", stubConfig{}, false},
		{"developer registers the profiling commands", stubConfig{bools: map[string]bool{"developer": true}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := runTSExtensionWith(t, newFakeFS("/ws").addFile("main.go"), &langexttest.Installer{}, tt.cfg)
			require.NoError(t, env.err)
			assert.Equal(t, "ts", env.console.Name)
			assert.Equal(t, "ts", env.prompt.Name)
			assert.Contains(t, names(env.prompt), "organize-imports")
			for _, profiling := range []string{"cpu-profile", "heap-profile", "alloc-profile", "gc"} {
				assert.Equal(t, tt.wantProfiling, slices.Contains(names(env.prompt), profiling), profiling)
			}
		})
	}
}

func TestHintMissingDependencies(t *testing.T) {
	const deps = `{"dependencies": {"react": "^19.0.0"}}`
	tests := []struct {
		name string
		fs   func() *fakeFS
		root string
		want []string
	}{
		{
			name: "workspace root points at the console command",
			fs:   func() *fakeFS { return newFakeFS("/ws").writeFile("package.json", deps) },
			root: "/ws",
			want: []string{"The dependencies of /ws are not installed, so imports from them are " +
				"unresolved. Run `ts install`."},
		},
		{
			name: "nested project names its package manager",
			fs: func() *fakeFS {
				return newFakeFS("/ws").writeFile("web/package.json", `{"devDependencies": {"vite": "6"}}`).
					addFile("web/pnpm-lock.yaml")
			},
			root: "/ws/web",
			want: []string{"The dependencies of /ws/web are not installed, so imports from them are " +
				"unresolved. Run `pnpm install` in /ws/web."},
		},
		{
			name: "installed dependencies",
			fs:   func() *fakeFS { return newFakeFS("/ws").writeFile("package.json", deps).addDir("node_modules") },
			root: "/ws",
		},
		{
			name: "hoisted install above the workspace",
			fs: func() *fakeFS {
				return newFakeFS("/repo/ws").writeFile("web/package.json", deps).addDir("/repo/node_modules")
			},
			root: "/repo/ws/web",
		},
		{
			name: "no dependencies to install",
			fs:   func() *fakeFS { return newFakeFS("/ws").writeFile("package.json", `{"name": "app"}`) },
			root: "/ws",
		},
		{
			name: "no package.json",
			fs:   func() *fakeFS { return newFakeFS("/ws").addFile("tsconfig.json") },
			root: "/ws",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := tt.fs()
			notify := &fakeNotifications{}
			hintMissingDependencies(fs, notify, fs.root, tt.root)
			assert.Equal(t, tt.want, notify.messages())
		})
	}
}

func TestHintOlderTypeScript(t *testing.T) {
	const warning = " installs TypeScript 5.9.3, but Rune's language server is TypeScript 7, which " +
		"removed tsconfig options older releases accept, such as baseUrl, target ES5 and " +
		"moduleResolution node. `ts check` names the ones to change."
	installed := func(dir, version string) func() *fakeFS {
		return func() *fakeFS {
			return newFakeFS("/ws").writeFile(dir+"/node_modules/typescript/package.json",
				`{"name": "typescript", "version": "`+version+`"}`)
		}
	}
	tests := []struct {
		name string
		fs   func() *fakeFS
		root string
		want []string
	}{
		{"older typescript", installed("/ws", "5.9.3"), "/ws", []string{"/ws" + warning}},
		{"hoisted above the root", installed("/ws", "5.9.3"), "/ws/web", []string{"/ws/web" + warning}},
		{"typescript 7", installed("/ws", "7.0.2"), "/ws", nil},
		{"typescript 7 prerelease", installed("/ws", "7.1.0-dev.20261001.1"), "/ws", nil},
		{"no typescript", func() *fakeFS { return newFakeFS("/ws") }, "/ws", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notify := &fakeNotifications{}
			hintOlderTypeScript(tt.fs(), notify, tt.root)
			assert.Equal(t, tt.want, notify.messages())
		})
	}
}
