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

package vctrltest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"

	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/ide/vctrl/testgit"
)

func TestCmdWorkingDiff(t *testing.T) {
	tests := []struct {
		name string
		// unborn skips the base commit, leaving a repository whose
		// first commit has not landed yet.
		unborn bool
		setup  func(t *testing.T, repo string)
		// subdir is the path WorkingDiff is called with, relative to
		// the repository root.
		subdir       string
		contextLines int
		want         []vctrl.FileDiff
		wantErr      bool
	}{
		{
			name:  "clean repository has nothing to review",
			setup: func(t *testing.T, repo string) {},
		},
		{
			name: "unstaged edit",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "alpha.txt", "a1\na2\nA3\na4\na5\n")
			},
			want: []vctrl.FileDiff{{
				OrigName: "alpha.txt", NewName: "alpha.txt",
				Hunks: []vctrl.Hunk{{
					OrigStartLine: 3, OrigLines: 1,
					NewStartLine: 3, NewLines: 1,
					Section: "a2",
					Body:    "-a3\n+A3\n",
				}},
			}},
		},
		{
			name: "staged edit is reviewed like an unstaged one",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "alpha.txt", "a1\na2\nA3\na4\na5\n")
				testgit.Run(t, repo, "add", "alpha.txt")
			},
			want: []vctrl.FileDiff{{
				OrigName: "alpha.txt", NewName: "alpha.txt",
				Hunks: []vctrl.Hunk{{
					OrigStartLine: 3, OrigLines: 1,
					NewStartLine: 3, NewLines: 1,
					Section: "a2",
					Body:    "-a3\n+A3\n",
				}},
			}},
		},
		{
			name: "staged and unstaged edits of one file combine",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "alpha.txt", "a1\na2\nA3\na4\na5\n")
				testgit.Run(t, repo, "add", "alpha.txt")
				writeFile(t, repo, "alpha.txt", "a1\na2\nA3\na4\nA5\n")
			},
			want: []vctrl.FileDiff{{
				OrigName: "alpha.txt", NewName: "alpha.txt",
				Hunks: []vctrl.Hunk{
					{
						OrigStartLine: 3, OrigLines: 1,
						NewStartLine: 3, NewLines: 1,
						Section: "a2",
						Body:    "-a3\n+A3\n",
					},
					{
						OrigStartLine: 5, OrigLines: 1,
						NewStartLine: 5, NewLines: 1,
						Section: "a4",
						Body:    "-a5\n+A5\n",
					},
				},
			}},
		},
		{
			name: "several files are reviewed together",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "alpha.txt", "a1\na2\nA3\na4\na5\n")
				writeFile(t, repo, "beta.txt", "b1\nB2\n")
			},
			want: []vctrl.FileDiff{
				{
					OrigName: "alpha.txt", NewName: "alpha.txt",
					Hunks: []vctrl.Hunk{{
						OrigStartLine: 3, OrigLines: 1,
						NewStartLine: 3, NewLines: 1,
						Section: "a2",
						Body:    "-a3\n+A3\n",
					}},
				},
				{
					OrigName: "beta.txt", NewName: "beta.txt",
					Hunks: []vctrl.Hunk{{
						OrigStartLine: 2, OrigLines: 1,
						NewStartLine: 2, NewLines: 1,
						Section: "b1",
						Body:    "-b2\n+B2\n",
					}},
				},
			},
		},
		{
			name: "distant edits of one file are separate hunks",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "alpha.txt", "A1\na2\na3\na4\nA5\n")
			},
			want: []vctrl.FileDiff{{
				OrigName: "alpha.txt", NewName: "alpha.txt",
				Hunks: []vctrl.Hunk{
					{
						OrigStartLine: 1, OrigLines: 1,
						NewStartLine: 1, NewLines: 1,
						Body: "-a1\n+A1\n",
					},
					{
						OrigStartLine: 5, OrigLines: 1,
						NewStartLine: 5, NewLines: 1,
						Section: "a4",
						Body:    "-a5\n+A5\n",
					},
				},
			}},
		},
		{
			name: "context lines surround the change",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "alpha.txt", "a1\na2\nA3\na4\na5\n")
			},
			contextLines: 2,
			want: []vctrl.FileDiff{{
				OrigName: "alpha.txt", NewName: "alpha.txt",
				Hunks: []vctrl.Hunk{{
					OrigStartLine: 1, OrigLines: 5,
					NewStartLine: 1, NewLines: 5,
					Body: " a1\n a2\n-a3\n+A3\n a4\n a5\n",
				}},
			}},
		},
		{
			name: "staged new file",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "new.txt", "n1\nn2\n")
				testgit.Run(t, repo, "add", "new.txt")
			},
			want: []vctrl.FileDiff{{
				OrigName: "/dev/null", NewName: "new.txt",
				Hunks: []vctrl.Hunk{{
					NewStartLine: 1, NewLines: 2,
					Body: "+n1\n+n2\n",
				}},
			}},
		},
		{
			name: "untracked file is reviewed as an addition",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "note.txt", "u1\nu2\n")
			},
			want: []vctrl.FileDiff{{
				OrigName: "/dev/null", NewName: "note.txt",
				Hunks: []vctrl.Hunk{{
					NewStartLine: 1, NewLines: 2,
					Body: "+u1\n+u2\n",
				}},
			}},
		},
		{
			name: "untracked file in an untracked directory",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "fresh/inside.txt", "i1\n")
			},
			want: []vctrl.FileDiff{{
				OrigName: "/dev/null", NewName: "fresh/inside.txt",
				Hunks: []vctrl.Hunk{{
					NewStartLine: 1, NewLines: 1,
					Body: "+i1\n",
				}},
			}},
		},
		{
			name: "ignored files stay out of the review",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "debug.log", "noise\n")
				writeFile(t, repo, "ignored/thing.txt", "noise\n")
			},
		},
		{
			name: "empty untracked file has nothing to show",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "empty.txt", "")
			},
		},
		{
			name: "binary untracked file is skipped",
			setup: func(t *testing.T, repo string) {
				require.NoError(t, os.WriteFile(
					filepath.Join(repo, "blob.bin"),
					[]byte{0x1, 0x0, 0x2, 0xff}, 0o644))
			},
		},
		{
			name: "deleted file",
			setup: func(t *testing.T, repo string) {
				require.NoError(t, os.Remove(filepath.Join(repo, "beta.txt")))
			},
			want: []vctrl.FileDiff{{
				OrigName: "beta.txt", NewName: "/dev/null",
				Hunks: []vctrl.Hunk{{
					OrigStartLine: 1, OrigLines: 2,
					Body: "-b1\n-b2\n",
				}},
			}},
		},
		{
			name: "staged delete",
			setup: func(t *testing.T, repo string) {
				testgit.Run(t, repo, "rm", "-q", "beta.txt")
			},
			want: []vctrl.FileDiff{{
				OrigName: "beta.txt", NewName: "/dev/null",
				Hunks: []vctrl.Hunk{{
					OrigStartLine: 1, OrigLines: 2,
					Body: "-b1\n-b2\n",
				}},
			}},
		},
		{
			name: "pure rename carries both names and no hunk",
			setup: func(t *testing.T, repo string) {
				testgit.Run(t, repo, "mv", "beta.txt", "delta.txt")
			},
			want: []vctrl.FileDiff{{
				OrigName: "beta.txt", NewName: "delta.txt",
			}},
		},
		{
			name: "rename with an edit",
			setup: func(t *testing.T, repo string) {
				testgit.Run(t, repo, "mv", "alpha.txt", "renamed.txt")
				writeFile(t, repo, "renamed.txt", "a1\na2\nA3\na4\na5\n")
			},
			want: []vctrl.FileDiff{{
				OrigName: "alpha.txt", NewName: "renamed.txt",
				Hunks: []vctrl.Hunk{{
					OrigStartLine: 3, OrigLines: 1,
					NewStartLine: 3, NewLines: 1,
					Section: "a2",
					Body:    "-a3\n+A3\n",
				}},
			}},
		},
		{
			// A mode-only change carries no hunks. A patch review has
			// no way to show a permission bit anyway.
			name: "mode change alone is not reviewable",
			setup: func(t *testing.T, repo string) {
				require.NoError(t, os.Chmod(
					filepath.Join(repo, "alpha.txt"), 0o755))
			},
		},
		{
			name: "path with a space",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "with space.txt", "S1\n")
			},
			want: []vctrl.FileDiff{{
				OrigName: "with space.txt", NewName: "with space.txt",
				Hunks: []vctrl.Hunk{{
					OrigStartLine: 1, OrigLines: 1,
					NewStartLine: 1, NewLines: 1,
					Body: "-s1\n+S1\n",
				}},
			}},
		},
		{
			name: "non-ascii path is not octal escaped",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "café.txt", "C1\n")
			},
			want: []vctrl.FileDiff{{
				OrigName: "café.txt", NewName: "café.txt",
				Hunks: []vctrl.Hunk{{
					OrigStartLine: 1, OrigLines: 1,
					NewStartLine: 1, NewLines: 1,
					Body: "-c1\n+C1\n",
				}},
			}},
		},
		{
			name: "untracked non-ascii path",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "räksmörgås.txt", "r1\n")
			},
			want: []vctrl.FileDiff{{
				OrigName: "/dev/null", NewName: "räksmörgås.txt",
				Hunks: []vctrl.Hunk{{
					NewStartLine: 1, NewLines: 1,
					Body: "+r1\n",
				}},
			}},
		},
		{
			// git marks it with "\ No newline at end of file", which the
			// parser folds into a body that simply does not end in a
			// newline.
			name: "missing trailing newline",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "beta.txt", "b1\nB2")
			},
			want: []vctrl.FileDiff{{
				OrigName: "beta.txt", NewName: "beta.txt",
				Hunks: []vctrl.Hunk{{
					OrigStartLine: 2, OrigLines: 1,
					NewStartLine: 2, NewLines: 1,
					Section: "b1",
					Body:    "-b2\n+B2",
				}},
			}},
		},
		{
			name: "tracked and untracked changes are reviewed together",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "alpha.txt", "a1\na2\nA3\na4\na5\n")
				writeFile(t, repo, "note.txt", "u1\n")
			},
			want: []vctrl.FileDiff{
				{
					OrigName: "alpha.txt", NewName: "alpha.txt",
					Hunks: []vctrl.Hunk{{
						OrigStartLine: 3, OrigLines: 1,
						NewStartLine: 3, NewLines: 1,
						Section: "a2",
						Body:    "-a3\n+A3\n",
					}},
				},
				{
					OrigName: "/dev/null", NewName: "note.txt",
					Hunks: []vctrl.Hunk{{
						NewStartLine: 1, NewLines: 1,
						Body: "+u1\n",
					}},
				},
			},
		},
		{
			name: "a path inside the repository still reviews the whole tree",
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "alpha.txt", "a1\na2\nA3\na4\na5\n")
				writeFile(t, repo, "dir/gamma.txt", "G1\n")
			},
			subdir: "dir",
			want: []vctrl.FileDiff{
				{
					OrigName: "alpha.txt", NewName: "alpha.txt",
					Hunks: []vctrl.Hunk{{
						OrigStartLine: 3, OrigLines: 1,
						NewStartLine: 3, NewLines: 1,
						Section: "a2",
						Body:    "-a3\n+A3\n",
					}},
				},
				{
					OrigName: "dir/gamma.txt", NewName: "dir/gamma.txt",
					Hunks: []vctrl.Hunk{{
						OrigStartLine: 1, OrigLines: 1,
						NewStartLine: 1, NewLines: 1,
						Body: "-g1\n+G1\n",
					}},
				},
			},
		},
		{
			name:   "repository without commits reviews staged and untracked work",
			unborn: true,
			setup: func(t *testing.T, repo string) {
				writeFile(t, repo, "staged.txt", "s1\n")
				testgit.Run(t, repo, "add", "staged.txt")
				writeFile(t, repo, "loose.txt", "l1\n")
			},
			want: []vctrl.FileDiff{
				{
					OrigName: "/dev/null", NewName: "staged.txt",
					Hunks: []vctrl.Hunk{{
						NewStartLine: 1, NewLines: 1,
						Body: "+s1\n",
					}},
				},
				{
					OrigName: "/dev/null", NewName: "loose.txt",
					Hunks: []vctrl.Hunk{{
						NewStartLine: 1, NewLines: 1,
						Body: "+l1\n",
					}},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepo(t, tc.unborn)
			tc.setup(t, repo)

			path := repo
			if tc.subdir != "" {
				path = filepath.Join(repo, tc.subdir)
			}
			cwd, err := workspaceapi.ParseURI("file://" + path)
			require.NoError(t, err)

			diffs, err := setupGitCmdService(t, cwd).
				WorkingDiff(context.Background(), cwd, tc.contextLines)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, diffs)
		})
	}
}

func TestCmdWorkingDiffOutsideRepository(t *testing.T) {
	dir := canonicalTempDir(t)
	cwd, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	_, err = setupGitCmdService(t, cwd).WorkingDiff(context.Background(), cwd, 3)
	require.Error(t, err)
}

// newTestRepo builds a repository holding the fixture files every case
// starts from. With unborn set the first commit is left unmade.
func newTestRepo(t *testing.T, unborn bool) string {
	t.Helper()
	repo := filepath.Join(canonicalTempDir(t), "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	testgit.Run(t, repo, "init", "-q", "-b", "main")
	if unborn {
		return repo
	}

	writeFile(t, repo, ".gitignore", "*.log\nignored/\n")
	writeFile(t, repo, "alpha.txt", "a1\na2\na3\na4\na5\n")
	writeFile(t, repo, "beta.txt", "b1\nb2\n")
	writeFile(t, repo, "dir/gamma.txt", "g1\n")
	writeFile(t, repo, "with space.txt", "s1\n")
	writeFile(t, repo, "café.txt", "c1\n")
	testgit.Run(t, repo, "add", "-A")
	testgit.Run(t, repo, "commit", "-q", "-m", "base")
	return repo
}

// canonicalTempDir resolves symlinks so the path matches what git
// reports as the repository root, which on macOS is under /private.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

func writeFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	path := filepath.Join(repo, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
