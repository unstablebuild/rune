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

package extension

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"

	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/ide/vctrl/testgit"
	"unstable.build/rune/internal/workspace"
)

func TestReviewWorkingTree(t *testing.T) {
	tests := []struct {
		name        string
		diffs       []vctrl.FileDiff
		want        string
		wantHeaders []string
		wantErr     error
	}{
		{
			name: "modified file keeps hunk section and context",
			diffs: []vctrl.FileDiff{{
				OrigName: "foo.go", NewName: "foo.go",
				Hunks: []vctrl.Hunk{{
					Section: "func main",
					Body:    " ctx\n-old\n+new\n",
				}},
			}},
			want: "*** Update File: foo.go\n" +
				"@@ func main\n" +
				" ctx\n" +
				"-old\n" +
				"+new\n",
			wantHeaders: []string{"*** Update File: foo.go"},
		},
		{
			name: "several hunks of one file render in order",
			diffs: []vctrl.FileDiff{{
				OrigName: "foo.go", NewName: "foo.go",
				Hunks: []vctrl.Hunk{
					{Body: "+first\n"},
					{Body: "-second\n"},
				},
			}},
			want: "*** Update File: foo.go\n" +
				"@@\n" +
				"+first\n" +
				"@@\n" +
				"-second\n",
		},
		{
			name: "untracked file renders as an add",
			diffs: []vctrl.FileDiff{{
				OrigName: devNullPath, NewName: "new.go",
				Hunks: []vctrl.Hunk{{Body: "+package main\n+\n"}},
			}},
			want: "*** Add File: new.go\n" +
				"+package main\n" +
				"+\n",
			wantHeaders: []string{"*** Add File: new.go"},
		},
		{
			name: "deleted file is reported without a body",
			diffs: []vctrl.FileDiff{{
				OrigName: "gone.go", NewName: devNullPath,
				Hunks: []vctrl.Hunk{{Body: "-package main\n"}},
			}},
			want:        "*** Delete File: gone.go\n",
			wantHeaders: []string{"*** Delete File: gone.go"},
		},
		{
			name: "rename carries the destination path",
			diffs: []vctrl.FileDiff{{
				OrigName: "old.go", NewName: "new.go",
				Hunks: []vctrl.Hunk{{Body: " same\n"}},
			}},
			want: "*** Update File: old.go\n" +
				"*** Move to: new.go\n" +
				"@@\n" +
				" same\n",
		},
		{
			name: "files are separated by a blank line",
			diffs: []vctrl.FileDiff{
				{
					OrigName: "a.go", NewName: "a.go",
					Hunks: []vctrl.Hunk{{Body: "+one\n"}},
				},
				{
					OrigName: "b.go", NewName: "b.go",
					Hunks: []vctrl.Hunk{{Body: "+two\n"}},
				},
			},
			want: "*** Update File: a.go\n" +
				"@@\n" +
				"+one\n" +
				"\n" +
				"*** Update File: b.go\n" +
				"@@\n" +
				"+two\n",
			wantHeaders: []string{
				"*** Update File: a.go", "*** Update File: b.go",
			},
		},
		{
			name: "no newline marker is dropped",
			diffs: []vctrl.FileDiff{{
				OrigName: "foo.go", NewName: "foo.go",
				Hunks: []vctrl.Hunk{{
					Body: "-old\n\\ No newline at end of file\n+new\n",
				}},
			}},
			want: "*** Update File: foo.go\n" +
				"@@\n" +
				"-old\n" +
				"+new\n",
		},
		{
			name:    "no diffs",
			wantErr: errNoWorkingChanges,
		},
		{
			name: "file with no renderable hunk is skipped",
			diffs: []vctrl.FileDiff{{
				OrigName: "foo.go", NewName: "foo.go",
			}},
			wantErr: errNoWorkingChanges,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			review, err := reviewWorkingTree(tc.diffs)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, review.diffs)
				return
			}
			require.NoError(t, err)

			rendered := vctrl.DiffString(review.diffs)
			assert.Equal(t, tc.want, rendered)
			if tc.wantHeaders != nil {
				got := make([]string, 0, len(review.headers))
				for _, h := range review.headers {
					got = append(got, h.text)
				}
				assert.Equal(t, tc.wantHeaders, got)
			}
			assertRowsAddressRenderedLines(t, review, rendered)
		})
	}
}

func TestReviewWorkingTreeE2E(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}

	write("main.go", "package main\n\nfunc main() {\n\tprintln(\"one\")\n\tprintln(\"two\")\n}\n")
	write("gone.go", "package main\n\nfunc gone() {}\n")
	testgit.Run(t, dir, "init", "-q")
	testgit.Run(t, dir, "add", "-A")
	testgit.Run(t, dir, "commit", "-qm", "baseline")

	write("main.go", "package main\n\nfunc main() {\n\tprintln(\"one\")\n\tprintln(\"2\")\n}\n")
	require.NoError(t, os.Remove(filepath.Join(dir, "gone.go")))
	write("extra.go", "package main\n")

	root, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(t.Context(), config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, scheme.Close()) })
	cwd, err := scheme.URI(".")
	require.NoError(t, err)

	git := vctrl.NewGitCommand(cwd, schemeExecutor{scheme}, scheme)
	diffs, err := git.WorkingDiff(context.Background(), cwd, 0)
	require.NoError(t, err)

	review, err := reviewWorkingTree(diffs)
	require.NoError(t, err)

	rendered := vctrl.DiffString(review.diffs)
	assert.Equal(t, "*** Delete File: gone.go\n"+
		"\n"+
		"*** Update File: main.go\n"+
		"@@ func main() {\n"+
		"-\tprintln(\"two\")\n"+
		"+\tprintln(\"2\")\n"+
		"\n"+
		"*** Add File: extra.go\n"+
		"+package main\n", rendered)
	assert.Equal(t, []string{
		"*** Delete File: gone.go",
		"*** Update File: main.go",
		"*** Add File: extra.go",
	}, headerTexts(review))
	assertRowsAddressRenderedLines(t, review, rendered)
}

func headerTexts(review changesReview) []string {
	ret := make([]string, 0, len(review.headers))
	for _, h := range review.headers {
		ret = append(ret, h.text)
	}
	return ret
}

// schemeExecutor adapts a scheme to the executor the git service runs
// its subprocesses through.
type schemeExecutor struct{ scheme schemeapi.Scheme }

func (e schemeExecutor) Start(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	return e.scheme.StartCommand(ctx, cmd)
}

func (e schemeExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }

func (e schemeExecutor) Close() error { return nil }
