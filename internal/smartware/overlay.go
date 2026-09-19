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

// Package smartware shells out to the local Smartware CLI.
// Rune does not embed Smartware. Overlay drafts only; it does not run jackets.
package smartware

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OverlayArgs is one draft run against an installed jacket.
type OverlayArgs struct {
	CoreDir string
	Jacket  string
	Job     string
	Node    string
}

// OverlayCmd is the argv Rune should spawn. Cwd must be CoreDir.
type OverlayCmd struct {
	Name string
	Args []string
	Dir  string
}

// BuildOverlayCmd returns node --import tsx src/cli.ts overlay run ...
func BuildOverlayCmd(a OverlayArgs) (OverlayCmd, error) {
	core := strings.TrimSpace(a.CoreDir)
	jacket := strings.TrimSpace(a.Jacket)
	job := strings.TrimSpace(a.Job)
	if core == "" {
		return OverlayCmd{}, errors.New("smartware core dir required")
	}
	if strings.Contains(core, "://") {
		return OverlayCmd{}, errors.New("smartware core dir must be local")
	}
	if jacket == "" {
		return OverlayCmd{}, errors.New("jacket id required")
	}
	if job == "" {
		return OverlayCmd{}, errors.New("job required")
	}
	cli := filepath.Join(core, "src", "cli.ts")
	if _, err := os.Stat(cli); err != nil {
		return OverlayCmd{}, fmt.Errorf("smartware cli missing: %w", err)
	}
	node := strings.TrimSpace(a.Node)
	if node == "" {
		node = "node"
	}
	return OverlayCmd{
		Name: node,
		Args: []string{"--import", "tsx", cli, "overlay", "run", "--jacket", jacket, "--job", job},
		Dir:  core,
	}, nil
}
