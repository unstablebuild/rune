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

package symbolresolve_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"

	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/walkdir"
)

func TestDetectSpecs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []string
		want  []*symbolresolve.Spec
	}{
		{
			name:  "go only",
			files: []string{"main.go", "pkg/util.go"},
			want:  []*symbolresolve.Spec{symbolresolve.Go},
		},
		{
			name:  "python only",
			files: []string{"app.py", "lib/helpers.py"},
			want:  []*symbolresolve.Spec{symbolresolve.Python},
		},
		{
			name:  "rust only",
			files: []string{"main.rs", "src/lib.rs"},
			want:  []*symbolresolve.Spec{symbolresolve.Rust},
		},
		{
			name:  "zig only",
			files: []string{"main.zig", "src/root.zig"},
			want:  []*symbolresolve.Spec{symbolresolve.Zig},
		},
		{
			name:  "typescript, tsx and javascript",
			files: []string{"src/main.ts", "src/App.tsx", "lib/util.cjs"},
			want: []*symbolresolve.Spec{
				symbolresolve.TypeScript, symbolresolve.TSX, symbolresolve.JavaScript,
			},
		},
		{
			name:  "declaration files alone select typescript",
			files: []string{"types/index.d.ts"},
			want:  []*symbolresolve.Spec{symbolresolve.TypeScript},
		},
		{
			name:  "mixed go and python",
			files: []string{"main.go", "app.py"},
			want:  []*symbolresolve.Spec{symbolresolve.Go, symbolresolve.Python},
		},
		{
			name:  "no recognized languages",
			files: []string{"README.md", "data.json"},
			want:  nil,
		},
		{
			name:  "empty workspace",
			files: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fs := newDetectFS(t, tt.files)
			got, err := iterator.ToSlice(
				context.Background(),
				symbolresolve.DetectSpecs(context.Background(), fs),
			)
			require.NoError(t, err)
			wantIDs := make([]string, len(tt.want))
			for i, spec := range tt.want {
				wantIDs[i] = spec.LangID
			}
			gotIDs := make([]string, len(got))
			for i := range got {
				gotIDs[i] = got[i].LangID
			}
			if len(wantIDs) == 0 {
				assert.Empty(t, gotIDs)
				return
			}
			assert.ElementsMatch(t, wantIDs, gotIDs)
		})
	}
}

func TestDetectSpecsHonorsContextFilter(t *testing.T) {
	t.Parallel()

	fs := newDetectFS(t, []string{
		"src/main.go",
		".venv/lib/app.py",
		"target/debug/build.rs",
	})

	matcher, err := vctrl.LoadGitignore(fs)
	require.NoError(t, err)
	ctx := walkdir.WithContextFilter(context.Background(), matcher)

	got, err := iterator.ToSlice(ctx, symbolresolve.DetectSpecs(ctx, fs))
	require.NoError(t, err)

	gotIDs := make([]string, len(got))
	for i := range got {
		gotIDs[i] = got[i].LangID
	}
	assert.ElementsMatch(t, []string{symbolresolve.Go.LangID}, gotIDs)
}

func newDetectFS(t *testing.T, files []string) workspaceapi.FileSystem {
	t.Helper()
	root := t.TempDir()
	for _, rel := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("// content\n"), 0o644))
	}
	uri, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	return scheme
}
