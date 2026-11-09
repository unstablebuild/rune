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
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/docs"
	"unstable.build/rune/internal/workspace"
)

// docsAgentsMDTmpl lives next to this file rather than in the docs
// tree so we can change agent behavior independently of the
// published documentation. It is parameterised on the user's resolved
// config path so the agent can name the exact file to edit.
//
//go:embed docs_agents.md.tmpl
var docsAgentsMDTmpl string

// docsAgentsPath must sit at the workspace root because
// agent.DiscoverAgentsFiles only consults AGENTS.md from there.
const docsAgentsPath = "/AGENTS.md"

// docsConfigYAML is the baked workspace overlay shipped with the
// docs:/// scheme. It sets workspace.notice so opening the docs
// always greets the user. It is not parameterised: the
// configRoutedScheme keeps this file in-memory and routes only the
// user's real host configPath.
//
//go:embed docs_config.yaml.tmpl
var docsConfigYAML []byte

// docsConfigPath must match cmd/rune/main.go:workspaceConfigFilename
// (".rune/config.yaml") so loadWorkspaceConfig overlays this file
// onto the IDE config when docs:/// opens.
const docsConfigPath = "/.rune/config.yaml"

// docsDefaultsPath surfaces the shipped Starlark default config
// (cmd/rune/rune.star, embedded as docsDefaultStarlarkConfig) inside
// the docs workspace so the agent can read Rune's effective defaults
// when answering configuration questions. The theme catalog lives in
// themes.star and is omitted here to keep the reference concise.
const docsDefaultsPath = "/defaults.star"

func renderDocsAgentsMD(configPath string) ([]byte, error) {
	t, err := template.New("docs_agents.md").Parse(docsAgentsMDTmpl)
	if err != nil {
		return nil, fmt.Errorf("parse docs AGENTS.md template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, struct{ ConfigPath string }{ConfigPath: configPath}); err != nil {
		return nil, fmt.Errorf("render docs AGENTS.md template: %w", err)
	}
	return buf.Bytes(), nil
}

// newDocsSchemeFunc builds the docs scheme. configPath is used both as
// the AGENTS.md template value and as a routing key: filesystem
// operations targeting that exact path are forwarded to a host file
// scheme so the agent can apply_patch the user's config from inside
// docs:///.
func newDocsSchemeFunc(configPath string) schemeapi.SchemeFunc {
	inner := workspace.NewInMemorySchemeFunc(workspace.DocsScheme)
	agentsMD, err := renderDocsAgentsMD(configPath)
	return func(
		ctx context.Context, cfg config.Config, uri workspaceapi.URI,
	) (schemeapi.Scheme, error) {
		if err != nil {
			return nil, err
		}
		mem, err := inner(ctx, cfg, uri)
		if err != nil {
			return nil, err
		}
		if err := prefillDocsScheme(mem, agentsMD, docsConfigYAML); err != nil {
			_ = mem.Close()
			return nil, fmt.Errorf("prefill docs scheme: %w", err)
		}
		if configPath == "" {
			return mem, nil
		}
		fileURI, ferr := workspaceapi.CurrentUserHostURI(filepath.Dir(configPath))
		if ferr != nil {
			_ = mem.Close()
			return nil, fmt.Errorf("build file scheme URI for %q: %w", configPath, ferr)
		}
		file, ferr := workspace.NewFileScheme(ctx, cfg, fileURI)
		if ferr != nil {
			_ = mem.Close()
			return nil, fmt.Errorf("file scheme for config %q: %w", configPath, ferr)
		}
		return &configRoutedScheme{
			Scheme:     mem,
			file:       file,
			configPath: filepath.Clean(configPath),
		}, nil
	}
}

// prefillDocsScheme walks docs.FS (which always uses forward slashes,
// even on Windows) and writes agentsMD and configYAML after the walk
// so a stray AGENTS.md or .rune/config.yaml in the docs tree
// cannot win.
func prefillDocsScheme(s schemeapi.Scheme, agentsMD, configYAML []byte) error {
	if err := fs.WalkDir(docs.FS, docs.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".md") {
			return nil
		}
		data, err := docs.FS.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read embedded %q: %w", p, err)
		}
		rel := strings.TrimPrefix(p, docs.Root+"/")
		dst := "/" + rel
		return writeDocsFile(s, dst, data)
	}); err != nil {
		return err
	}
	if err := writeDocsFile(s, docsAgentsPath, agentsMD); err != nil {
		return err
	}
	if err := writeDocsFile(s, docsDefaultsPath, []byte(docsDefaultStarlarkConfig)); err != nil {
		return err
	}
	return writeDocsFile(s, docsConfigPath, configYAML)
}

func writeDocsFile(s schemeapi.Scheme, dst string, data []byte) error {
	f, err := s.Create(dst)
	if err != nil {
		return fmt.Errorf("create %q: %w", dst, err)
	}
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		return fmt.Errorf("write %q: %w", dst, werr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("close %q: %w", dst, cerr)
	}
	return nil
}

// configRoutedScheme forwards filesystem operations on the user's real
// config path to a host file scheme; every other path falls through to
// the embedded in-memory scheme. Rename/Symlink route when either side
// matches because the two endpoints would otherwise straddle schemes.
type configRoutedScheme struct {
	schemeapi.Scheme
	file       schemeapi.Scheme
	configPath string
}

func (s *configRoutedScheme) isConfig(p string) bool {
	return filepath.Clean(p) == s.configPath
}

func (s *configRoutedScheme) Close() error {
	memErr := s.Scheme.Close()
	fileErr := s.file.Close()
	if memErr != nil {
		return memErr
	}
	return fileErr
}

func (s *configRoutedScheme) Create(filename string) (workspaceapi.File, error) {
	if s.isConfig(filename) {
		return s.file.Create(filename)
	}
	return s.Scheme.Create(filename)
}

func (s *configRoutedScheme) Open(filename string) (workspaceapi.File, error) {
	if s.isConfig(filename) {
		return s.file.Open(filename)
	}
	return s.Scheme.Open(filename)
}

func (s *configRoutedScheme) OpenFile(
	filename string, flag int, perm fs.FileMode,
) (workspaceapi.File, error) {
	if s.isConfig(filename) {
		return s.file.OpenFile(filename, flag, perm)
	}
	return s.Scheme.OpenFile(filename, flag, perm)
}

func (s *configRoutedScheme) Stat(filename string) (fs.FileInfo, error) {
	if s.isConfig(filename) {
		return s.file.Stat(filename)
	}
	return s.Scheme.Stat(filename)
}

func (s *configRoutedScheme) Lstat(filename string) (fs.FileInfo, error) {
	if s.isConfig(filename) {
		return s.file.Lstat(filename)
	}
	return s.Scheme.Lstat(filename)
}

func (s *configRoutedScheme) Remove(filename string) error {
	if s.isConfig(filename) {
		return s.file.Remove(filename)
	}
	return s.Scheme.Remove(filename)
}

func (s *configRoutedScheme) Rename(oldpath, newpath string) error {
	if s.isConfig(oldpath) || s.isConfig(newpath) {
		return s.file.Rename(oldpath, newpath)
	}
	return s.Scheme.Rename(oldpath, newpath)
}

func (s *configRoutedScheme) Symlink(oldname, newname string) error {
	if s.isConfig(oldname) || s.isConfig(newname) {
		return s.file.Symlink(oldname, newname)
	}
	return s.Scheme.Symlink(oldname, newname)
}

func (s *configRoutedScheme) Readlink(link string) (string, error) {
	if s.isConfig(link) {
		return s.file.Readlink(link)
	}
	return s.Scheme.Readlink(link)
}

// NewFile must route by filename because the workspace RPC server
// reconstitutes file handles on every Read/Write/Close/Seek via
// NewFile(fd, name). Without this, fds opened against the file scheme
// get looked up in the in-memory scheme and fail with
// "invalid file descriptor".
func (s *configRoutedScheme) NewFile(fd uintptr, name string) workspaceapi.File {
	if s.isConfig(name) {
		return s.file.NewFile(fd, name)
	}
	return s.Scheme.NewFile(fd, name)
}
