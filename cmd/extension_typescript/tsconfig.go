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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tailscale/hujson"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace/walkdir"
)

// tsconfigName is the config `tsc -p <dir>` reads.
const tsconfigName = "tsconfig.json"

// hasProjectReferences reports whether the tsconfig at path lists project
// references. tsconfig is JSON with comments and trailing commas.
func hasProjectReferences(fs workspaceapi.FileSystem, path string) (bool, error) {
	f, err := fs.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes))
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	data, err = hujson.Standardize(data)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	var cfg struct {
		References []json.RawMessage `json:"references"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	return len(cfg.References) > 0, nil
}

// checkArgs returns the tsgo arguments that type-check the project
// config at path, named rel on the command line. A project with
// references is built as its own `tsc -b` builds it: `-p` checks none of
// a solution's references, and `-b --noEmit` is rejected for references
// whose dependents read their declarations. Any other project is checked
// with --noEmit, so the check writes nothing.
func checkArgs(fs workspaceapi.FileSystem, path, rel string) []string {
	if refs, err := hasProjectReferences(fs, path); err == nil && refs {
		return []string{"-b", "--pretty", "false", rel}
	}
	return []string{"--noEmit", "--pretty", "false", "-p", rel}
}

// relFilter matches paths relative to a workspace subdirectory against a
// filter of workspace-relative paths.
type relFilter struct {
	filter walkdir.Filter
	prefix string
}

func (f relFilter) MatchRelPath(rel string, isDir bool) bool {
	return f.filter.MatchRelPath(filepath.Join(f.prefix, rel), isDir)
}
