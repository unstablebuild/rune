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

package smartware

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildOverlayCmd(t *testing.T) {
	core := t.TempDir()
	if err := os.MkdirAll(filepath.Join(core, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(core, "src", "cli.ts"), []byte("// test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd, err := BuildOverlayCmd(OverlayArgs{
		CoreDir: core,
		Jacket:  "jacket.fixture.inspect",
		Job:     "summarize readiness",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != core {
		t.Fatalf("cwd %s", cmd.Dir)
	}
	if cmd.Name != "node" {
		t.Fatalf("bin %s", cmd.Name)
	}

	_, err = BuildOverlayCmd(OverlayArgs{CoreDir: "https://example.invalid", Jacket: "j", Job: "x"})
	if err == nil {
		t.Fatal("expected remote core denied")
	}
}
