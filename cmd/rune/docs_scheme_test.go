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
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// newTestConfigPath returns a fresh temp-dir-backed config path. The
// host directory must exist because workspace.NewFileScheme stats its
// workspace root at construction time.
func newTestConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.yaml")
}

func TestDocsSchemePrefill(t *testing.T) {
	cfgPath := newTestConfigPath(t)
	uri, err := workspaceapi.ParseURI("docs:///")
	require.NoError(t, err)

	s, err := newDocsSchemeFunc(cfgPath)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// intro.md is one of the embedded files; opening it should yield non-empty content.
	f, err := s.Open("/intro.md")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	data, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.NotEmpty(t, data, "embedded intro.md should not be empty")
}

func TestDocsSchemeAllFilesNonEmpty(t *testing.T) {
	cfgPath := newTestConfigPath(t)
	uri, err := workspaceapi.ParseURI("docs:///")
	require.NoError(t, err)

	s, err := newDocsSchemeFunc(cfgPath)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	expected := make(map[string][]byte)
	require.NoError(t, fsWalkMD(t, &expected))
	require.NotEmpty(t, expected)

	for rel, want := range expected {
		p := "/" + rel
		f, err := s.Open(p)
		require.NoError(t, err, "open %s", p)

		got, err := io.ReadAll(f)
		_ = f.Close()
		require.NoError(t, err, "read %s", p)

		assert.Equal(t, string(want), string(got), "%s contents mismatch", p)
	}
}

func TestDocsSchemeReadDirOpenRoundTrip(t *testing.T) {
	cfgPath := newTestConfigPath(t)
	uri, err := workspaceapi.ParseURI("docs:///")
	require.NoError(t, err)

	s, err := newDocsSchemeFunc(cfgPath)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	entries, err := s.ReadDir("/")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	rootNames := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		rootNames[name] = true
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		f, err := s.Open("/" + name)
		require.NoError(t, err, "open %s", name)
		data, err := io.ReadAll(f)
		_ = f.Close()
		require.NoError(t, err, "read %s", name)
		assert.NotEmpty(t, data, "%s should not be empty when opened by ReadDir-returned name", name)
	}
	// AGENTS.md must be discoverable at the workspace root so the
	// agent's DiscoverAgentsFiles traversal picks it up as project
	// instructions.
	assert.Truef(t, rootNames["AGENTS.md"], "ReadDir(/) must list AGENTS.md, got %v", rootNames)
}

func TestDocsSchemeNestedDirsAreBrowsable(t *testing.T) {
	cfgPath := newTestConfigPath(t)
	uri, err := workspaceapi.ParseURI("docs:///")
	require.NoError(t, err)

	s, err := newDocsSchemeFunc(cfgPath)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	root, err := s.ReadDir("/")
	require.NoError(t, err)

	sub := map[string]bool{}
	for _, e := range root {
		if e.IsDir() {
			sub[e.Name()] = true
		}
	}
	for _, want := range []string{"develop", "learn"} {
		assert.Truef(t, sub[want], "expected docs:/// to expose %q as a subdirectory", want)
	}

	develop, err := s.ReadDir("/develop")
	require.NoError(t, err)
	developFiles := map[string]bool{}
	for _, e := range develop {
		developFiles[e.Name()] = e.IsDir()
	}
	assert.Contains(t, developFiles, "sdk", "/develop must list sdk")
	assert.True(t, developFiles["sdk"], "/develop/sdk must be a directory")

	sdk, err := s.ReadDir("/develop/sdk")
	require.NoError(t, err)
	sdkFiles := map[string]bool{}
	for _, e := range sdk {
		sdkFiles[e.Name()] = e.IsDir()
	}
	assert.Contains(t, sdkFiles, "index.md", "/develop/sdk must list index.md")
	assert.False(t, sdkFiles["index.md"], "/develop/sdk/index.md must be a file")

	f, err := s.Open("/develop/sdk/index.md")
	require.NoError(t, err)
	data, err := io.ReadAll(f)
	_ = f.Close()
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestDocsSchemeAgentsMDPresent(t *testing.T) {
	cfgPath := newTestConfigPath(t)
	uri, err := workspaceapi.ParseURI("docs:///")
	require.NoError(t, err)

	s, err := newDocsSchemeFunc(cfgPath)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	f, err := s.Open(docsAgentsPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	data, err := io.ReadAll(f)
	require.NoError(t, err)
	require.NotEmpty(t, data, "AGENTS.md must not be empty")
	want, err := renderDocsAgentsMD(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(data))
	assert.Contains(t, string(data), cfgPath,
		"AGENTS.md must embed the user's resolved config path")
}

func TestDocsSchemeDefaultsStar(t *testing.T) {
	cfgPath := newTestConfigPath(t)
	uri, err := workspaceapi.ParseURI("docs:///")
	require.NoError(t, err)

	s, err := newDocsSchemeFunc(cfgPath)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	f, err := s.Open(docsDefaultsPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	data, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, docsDefaultStarlarkConfig, string(data),
		"defaults.star must match the embedded rune.star verbatim")
}

func TestDocsSchemeConfigYAML(t *testing.T) {
	cfgPath := newTestConfigPath(t)
	uri, err := workspaceapi.ParseURI("docs:///")
	require.NoError(t, err)

	s, err := newDocsSchemeFunc(cfgPath)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	f, err := s.Open(docsConfigPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	data, err := io.ReadAll(f)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	got := string(data)
	for _, want := range []string{
		"workspace:",
		"notice:",
		"show: always",
		"# Welcome to the Rune docs",
	} {
		assert.Containsf(t, got, want, "docs config must contain %q", want)
	}
}

func TestDocsSchemeConfigYAMLNotRouted(t *testing.T) {
	cfgPath := newTestConfigPath(t)
	uri, err := workspaceapi.ParseURI("docs:///")
	require.NoError(t, err)

	s, err := newDocsSchemeFunc(cfgPath)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	f, err := s.Open(docsConfigPath)
	require.NoError(t, err)
	data, err := io.ReadAll(f)
	_ = f.Close()
	require.NoError(t, err)
	assert.Equal(t, string(docsConfigYAML), string(data),
		"docs:///.rune/config.yaml must return the in-memory baked bytes")
}
func TestDocsSchemeRoutesConfigPathToFileScheme(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	const initial = "agent:\n  provider: openai\n"
	require.NoError(t, os.WriteFile(cfg, []byte(initial), 0o644))

	uri, err := workspaceapi.ParseURI("docs:///")
	require.NoError(t, err)

	s, err := newDocsSchemeFunc(cfg)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	f, err := s.Open(cfg)
	require.NoError(t, err)
	got, err := io.ReadAll(f)
	_ = f.Close()
	require.NoError(t, err)
	assert.Equal(t, initial, string(got))

	info, err := s.Stat(cfg)
	require.NoError(t, err)
	assert.Equal(t, int64(len(initial)), info.Size())

	const updated = "agent:\n  provider: anthropic\n"
	w, err := s.Create(cfg)
	require.NoError(t, err)
	_, err = w.Write([]byte(updated))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	onDisk, err := os.ReadFile(cfg)
	require.NoError(t, err)
	assert.Equal(t, updated, string(onDisk))

	// A sibling host path must not be routed: only the exact
	// configPath crosses into the file scheme.
	sibling := filepath.Join(dir, "other.yaml")
	require.NoError(t, os.WriteFile(sibling, []byte("x"), 0o644))
	_, err = s.Open(sibling)
	assert.Error(t, err, "non-config host paths must not be routed to the file scheme")
}

func TestDocsSchemeNewFileRoutesConfigPath(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	const initial = "agent:\n  provider: openai\n"
	require.NoError(t, os.WriteFile(cfg, []byte(initial), 0o644))

	uri, err := workspaceapi.ParseURI("docs:///")
	require.NoError(t, err)

	s, err := newDocsSchemeFunc(cfg)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	f, err := s.Open(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	g := s.NewFile(f.Fd(), cfg)
	require.NotNil(t, g, "NewFile must return the host file descriptor for the config path")

	got, err := io.ReadAll(g)
	require.NoError(t, err)
	assert.Equal(t, initial, string(got))
}

func fsWalkMD(t *testing.T, out *map[string][]byte) error {
	t.Helper()
	entries, err := docsFS.ReadDir(docsSchemeRoot)
	if err != nil {
		return err
	}
	var walk func(dir string) error
	walk = func(dir string) error {
		es, err := docsFS.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range es {
			p := dir + "/" + e.Name()
			if e.IsDir() {
				if err := walk(p); err != nil {
					return err
				}
				continue
			}
			if !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			data, err := docsFS.ReadFile(p)
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(p, docsSchemeRoot+"/")
			(*out)[rel] = data
		}
		return nil
	}
	_ = entries
	return walk(docsSchemeRoot)
}
