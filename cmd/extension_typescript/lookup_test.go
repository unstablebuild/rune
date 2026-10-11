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
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/extension/langext/langexttest"
)

// tsTools is the lookup bring-up gets for a typescript package holding
// inst's files.
func tsTools(t *testing.T, inst *langexttest.Installer) *langext.Tools {
	return langext.NewInitializer(t.Context(), nil, nil, inst, langext.ProjectConfig{
		LanguageID: tsLanguageID, Tools: []string{"tsgo"},
	}).Tools()
}

func TestReadLspPath(t *testing.T) {
	tests := []struct {
		name   string
		cfg    stubConfig
		want   string
		wantOK bool
	}{
		{"absent", stubConfig{}, "", false},
		{"empty override ignored", stubConfig{strings: map[string]string{"lsp_path": ""}}, "", false},
		{"override set", stubConfig{strings: map[string]string{"lsp_path": "/opt/tsgo"}}, "/opt/tsgo", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := readLspPath(tt.cfg, &fakeNotifications{})
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
	got, ok := readLspPath(nil, &fakeNotifications{})
	assert.Empty(t, got)
	assert.False(t, ok)
}

func TestIsNativeVersion(t *testing.T) {
	tests := []struct {
		out  string
		want bool
	}{
		{"Version 7.0.2\n", true},
		{"Version 7.1.0-dev.20260101.1\n", true},
		{"Version 10.0.0\n", true},
		{"Version 5.9.3\n", false},
		{"Version 6.0.0\n", false},
		{"", false},
		{"garbage", false},
	}
	for _, tt := range tests {
		t.Run(tt.out, func(t *testing.T) {
			assert.Equal(t, tt.want, isNativeVersion(tt.out))
		})
	}
}

func TestResolveServer(t *testing.T) {
	const ws = "/ws"
	const npmBin = "/ws/node_modules/@typescript/typescript-darwin-arm64/lib/tsc"
	notInstalled := &langexttest.Installer{Err: fmt.Errorf("typescript: %w", pkgapi.ErrNotInstalled)}
	packaged := &langexttest.Installer{Files: []string{"/data/typescript/bin/tsgo"}}
	hostTsgo := func() *fakeExecutor {
		return newFakeExecutor().
			respond("sh -lc command -v tsgo", scriptedCmd{stdout: "/usr/local/bin/tsgo\n"}).
			respond("/usr/local/bin/tsgo --version", scriptedCmd{stdout: "Version 7.0.2\n"})
	}

	tests := []struct {
		name       string
		cfg        stubConfig
		fs         func() *fakeFS
		exec       func() *fakeExecutor
		inst       *langexttest.Installer
		rootDir    string
		want       string
		wantNotify string
	}{
		{
			name:    "lsp_path override wins",
			cfg:     stubConfig{strings: map[string]string{"lsp_path": "/opt/tsgo"}},
			fs:      func() *fakeFS { return newFakeFS(ws).addFile(npmBin) },
			exec:    hostTsgo,
			inst:    packaged,
			rootDir: ws,
			want:    "/opt/tsgo",
		},
		{
			name:    "project npm install beats the package",
			fs:      func() *fakeFS { return newFakeFS(ws).addFile(npmBin) },
			exec:    hostTsgo,
			inst:    packaged,
			rootDir: ws,
			want:    npmBin,
		},
		{
			name: "project pnpm install",
			fs: func() *fakeFS {
				return newFakeFS(ws).addFile(
					"/ws/node_modules/.pnpm/node_modules/@typescript/typescript-linux-x64/lib/tsc")
			},
			exec:    newFakeExecutor,
			inst:    notInstalled,
			rootDir: ws,
			want:    "/ws/node_modules/.pnpm/node_modules/@typescript/typescript-linux-x64/lib/tsc",
		},
		{
			name: "project bun isolated install",
			fs: func() *fakeFS {
				return newFakeFS(ws).addFile(
					"/ws/node_modules/.bun/node_modules/@typescript/typescript-linux-arm64/lib/tsc")
			},
			exec:    newFakeExecutor,
			inst:    notInstalled,
			rootDir: ws,
			want:    "/ws/node_modules/.bun/node_modules/@typescript/typescript-linux-arm64/lib/tsc",
		},
		{
			name:    "hoisted install found from a nested root",
			fs:      func() *fakeFS { return newFakeFS(ws).addFile(npmBin).addDir("/ws/packages/app") },
			exec:    newFakeExecutor,
			inst:    notInstalled,
			rootDir: "/ws/packages/app",
			want:    npmBin,
		},
		{
			name: "install above the workspace is ignored",
			fs: func() *fakeFS {
				return newFakeFS(ws).
					addFile("/node_modules/@typescript/typescript-darwin-arm64/lib/tsc")
			},
			exec:    newFakeExecutor,
			inst:    packaged,
			rootDir: ws,
			want:    "/data/typescript/bin/tsgo",
		},
		{
			name: "native-preview scope entries are not the compiler",
			fs: func() *fakeFS {
				return newFakeFS(ws).
					addFile("/ws/node_modules/@typescript/native-preview-darwin-arm64/lib/tsgo")
			},
			exec:    newFakeExecutor,
			inst:    packaged,
			rootDir: ws,
			want:    "/data/typescript/bin/tsgo",
		},
		{
			name:    "host tsgo when the package is not installed",
			fs:      func() *fakeFS { return newFakeFS(ws) },
			exec:    hostTsgo,
			inst:    notInstalled,
			rootDir: ws,
			want:    "/usr/local/bin/tsgo",
		},
		{
			name: "host tsc 7 when there is no tsgo",
			fs:   func() *fakeFS { return newFakeFS(ws) },
			exec: func() *fakeExecutor {
				return newFakeExecutor().
					respond("sh -lc command -v tsc", scriptedCmd{stdout: "/usr/bin/tsc\n"}).
					respond("/usr/bin/tsc --version", scriptedCmd{stdout: "Version 7.0.2\n"})
			},
			inst:    notInstalled,
			rootDir: ws,
			want:    "/usr/bin/tsc",
		},
		{
			name: "host tsc 5 is rejected",
			fs:   func() *fakeFS { return newFakeFS(ws) },
			exec: func() *fakeExecutor {
				return newFakeExecutor().
					respond("sh -lc command -v tsc", scriptedCmd{stdout: "/usr/bin/tsc\n"}).
					respond("/usr/bin/tsc --version", scriptedCmd{stdout: "Version 5.9.3\n"})
			},
			inst:       notInstalled,
			rootDir:    ws,
			want:       "",
			wantNotify: "We could not locate a TypeScript 7 language server",
		},
		{
			name:       "package without tsgo warns and probes the host",
			fs:         func() *fakeFS { return newFakeFS(ws) },
			exec:       hostTsgo,
			inst:       &langexttest.Installer{Files: []string{"/data/typescript/lib/tsc"}},
			rootDir:    ws,
			want:       "/usr/local/bin/tsgo",
			wantNotify: "Could not find tsgo in the typescript package",
		},
		{
			name:       "failed package lookup warns and probes the host",
			fs:         func() *fakeFS { return newFakeFS(ws) },
			exec:       hostTsgo,
			inst:       &langexttest.Installer{Err: errors.New("rpc error: code = PermissionDenied")},
			rootDir:    ws,
			want:       "/usr/local/bin/tsgo",
			wantNotify: "rpc error: code = PermissionDenied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notify := &fakeNotifications{}
			got := resolveServer(t.Context(), tt.cfg, notify, tt.fs(), tt.exec(),
				tsTools(t, tt.inst), ws, tt.rootDir)
			assert.Equal(t, tt.want, got)
			if tt.wantNotify == "" {
				assert.Empty(t, notify.messages())
				return
			}
			if assert.Len(t, notify.messages(), 1) {
				assert.Contains(t, notify.messages()[0], tt.wantNotify)
			}
		})
	}
}

func TestResolveServerStopsAtProjectCompiler(t *testing.T) {
	inst := &langexttest.Installer{Files: []string{"/data/typescript/bin/tsgo"}}
	fs := newFakeFS("/ws").addFile("/ws/node_modules/@typescript/typescript-linux-arm64/lib/tsc")
	exec := newFakeExecutor()
	resolveServer(t.Context(), stubConfig{}, &fakeNotifications{}, fs, exec,
		tsTools(t, inst), "/ws", "/ws")
	assert.Zero(t, inst.Lookups(), "a project compiler must not prompt a package install")
	assert.Empty(t, exec.callsSnapshot(), "a project compiler must not probe the host")
}
