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

package workspace

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

func TestSwapFileName(t *testing.T) {
	tsuite := []struct {
		name     string
		swapDir  string
		filePath string
		wantDir  string
		wantPath string
	}{{
		name:     "empty swap directory keeps the swap next to the file",
		filePath: "/Users/x/src/proj/main.go",
		wantDir:  "/Users/x/src/proj",
		wantPath: "/Users/x/src/proj/.main.go.rswp",
	}, {
		name:     "the file's own directory keeps the sibling layout",
		swapDir:  "/Users/x/src/proj",
		filePath: "/Users/x/src/proj/main.go",
		wantDir:  "/Users/x/src/proj",
		wantPath: "/Users/x/src/proj/.main.go.rswp",
	}, {
		name:     "a shared directory mangles the full path",
		swapDir:  "/Users/x/.rune/swap",
		filePath: "/Users/x/src/proj/main.go",
		wantDir:  "/Users/x/.rune/swap",
		wantPath: "/Users/x/.rune/swap/+Users+x+src+proj+main.go.rswp",
	}, {
		name:     "deep nesting stays a single entry",
		swapDir:  "/swap",
		filePath: "/a/b/c/d/e/f.go",
		wantDir:  "/swap",
		wantPath: "/swap/+a+b+c+d+e+f.go.rswp",
	}, {
		name:     "a literal separator in the path is doubled",
		swapDir:  "/swap",
		filePath: "/a+b/c.go",
		wantDir:  "/swap",
		wantPath: "/swap/+a++b+c.go.rswp",
	}, {
		name:     "non-ASCII path components survive verbatim",
		swapDir:  "/swap",
		filePath: "/tmp/ñandú/café.go",
		wantDir:  "/swap",
		wantPath: "/swap/+tmp+ñandú+café.go.rswp",
	}, {
		name:     "the file path is cleaned before mangling",
		swapDir:  "/swap",
		filePath: "/tmp/./sub/../a.go",
		wantDir:  "/swap",
		wantPath: "/swap/+tmp+a.go.rswp",
	}}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			dir, swapPath := swapFileName(tcase.swapDir, tcase.filePath)
			assert.Equal(t, tcase.wantDir, dir)
			assert.Equal(t, tcase.wantPath, swapPath)
		})
	}

	t.Run("distinct paths never share a mangled entry", func(t *testing.T) {
		seen := map[string]string{}
		for _, filePath := range []string{
			"/a/b/c.go", "/a+b/c.go", "/a/b+c.go", "/a%2fb/c.go", "/a++b/c.go",
		} {
			_, swapPath := swapFileName("/swap", filePath)
			prev, dup := seen[swapPath]
			require.False(t, dup, "%q and %q collide on %q", prev, filePath, swapPath)
			seen[swapPath] = filePath
		}
	})
}

// TestSwapFileNameTruncation covers paths that cannot fit in a
// directory entry: the name must stay within NAME_MAX, stay valid
// UTF-8, be deterministic, and still distinguish paths that share the
// truncated part.
func TestSwapFileNameTruncation(t *testing.T) {
	long := "/" + strings.Repeat("ñ", 200) + "/deeply/nested/main.go"
	_, swapPath := swapFileName("/swap", long)
	name := path.Base(swapPath)

	assert.LessOrEqual(t, len(name), swapNameMax)
	assert.True(t, utf8.ValidString(name), "mangled entry %q must be valid UTF-8", name)
	assert.True(t, strings.HasSuffix(name, SwapFileExtensionName))
	assert.Contains(t, name, "main.go", "the tail of the path must survive")

	_, again := swapFileName("/swap", long)
	assert.Equal(t, swapPath, again, "mangling must be deterministic")

	sibling := "/" + strings.Repeat("ñ", 200) + "X/deeply/nested/main.go"
	_, siblingPath := swapFileName("/swap", sibling)
	assert.NotEqual(t, swapPath, siblingPath,
		"the digest must keep truncated paths apart")
	assert.LessOrEqual(t, len(path.Base(siblingPath)), swapNameMax)
}

func TestSwapDirectory(t *testing.T) {
	tsuite := []struct {
		dir  string
		file string
		want string
	}{
		{"", "file:///Users/x/src/proj/main.go", "file:///Users/x/src/proj"},
		{"/Users/x/.rune/swap", "file:///Users/x/src/proj/main.go", "/Users/x/.rune/swap"},
		// A remote file keeps the path literal so the remote host, not
		// this one, expands "~".
		{"~/.rune/swap", "ssh://user@my_host/srv/a.go", "~/.rune/swap"},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.file+" in "+tcase.dir, func(t *testing.T) {
			file, err := workspaceapi.ParseURI(tcase.file)
			require.NoError(t, err)

			swapDir, err := SwapDirectory(tcase.dir, file)
			require.NoError(t, err)

			if tcase.dir == "" {
				assert.Equal(t, tcase.want, swapDir.String())
			} else {
				assert.Equal(t, tcase.want, swapDir.Path())
			}
			assert.Equal(t, file.Scheme(), swapDir.Scheme())
			assert.Equal(t, file.Host(), swapDir.Host())
			assert.Equal(t, file.User(), swapDir.User())
		})
	}
} // TestFlushWritesThroughWhenRenameFails covers a swap directory on
// another volume, where the rename that publishes the swap onto the
// original cannot work.
func TestFlushWritesThroughWhenRenameFails(t *testing.T) {
	dir, swapDir := t.TempDir(), t.TempDir()
	target := filepath.Join(dir, "main.go")
	require.NoError(t, os.WriteFile(target, []byte("original\n"), 0640))

	link := filepath.Join(dir, "hard.go")
	require.NoError(t, os.Link(target, link))

	before, err := os.Lstat(target)
	require.NoError(t, err)

	scheme := renameFailScheme{Scheme: newLocalScheme(t, dir)}
	buf := cell.NewBuffer()
	f, swapPath := openInSwapDir(t, scheme, target, swapDir, buf)
	defer f.Close()

	buf.InsertString(term.Coordinates{}, "edited ")
	require.NoError(t, awaitFlushErr(f.Flush(context.Background())))

	saved, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "edited original\n", string(saved))

	// The swap is the journal, not a staging file the save consumes:
	// keeping it means the O_EXCL lock is never dropped mid-save.
	_, err = os.Stat(swapPath)
	assert.NoError(t, err, "swap %s must survive the save", swapPath)

	after, err := os.Lstat(target)
	require.NoError(t, err)
	assert.Equal(t, before.Mode(), after.Mode())
	assert.True(t, os.SameFile(before, after),
		"the original must keep its inode so ownership and ACLs survive")

	linked, err := os.ReadFile(link)
	require.NoError(t, err)
	assert.Equal(t, string(saved), string(linked),
		"a hard link must still alias the saved file")

	// The save must remain repeatable against the surviving swap.
	buf.InsertString(term.Coordinates{}, "re")
	require.NoError(t, awaitFlushErr(f.Flush(context.Background())))
	saved, err = os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "reedited original\n", string(saved))
}

// TestFlushWritesThroughSymlinkTarget checks that the write-through
// save follows the symlink instead of replacing it, the same way the
// rename path resolves origTarget through Readlink.
func TestFlushWritesThroughSymlinkTarget(t *testing.T) {
	dir, swapDir := t.TempDir(), t.TempDir()
	target := filepath.Join(dir, "target.go")
	require.NoError(t, os.WriteFile(target, []byte("original\n"), 0644))

	link := filepath.Join(dir, "link.go")
	require.NoError(t, os.Symlink(target, link))

	scheme := renameFailScheme{Scheme: newLocalScheme(t, dir)}
	buf := cell.NewBuffer()
	f, _ := openInSwapDir(t, scheme, link, swapDir, buf)
	defer f.Close()

	buf.InsertString(term.Coordinates{}, "edited ")
	require.NoError(t, awaitFlushErr(f.Flush(context.Background())))

	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "link must still be a symlink")

	saved, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "edited original\n", string(saved))
}

// TestSharedSwapDirectoryIsCreated covers a configured swap directory
// that does not exist yet: the O_EXCL open of the entry has no
// MkdirAll of its own.
func TestSharedSwapDirectoryIsCreated(t *testing.T) {
	dir := t.TempDir()
	swapDir := filepath.Join(t.TempDir(), "rune", "swap")
	target := filepath.Join(dir, "main.go")
	require.NoError(t, os.WriteFile(target, []byte("original\n"), 0644))

	f, swapPath := openInSwapDir(t, newLocalScheme(t, dir), target, swapDir,
		cell.NewBuffer())
	defer f.Close()

	info, err := os.Stat(swapDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	_, err = os.Stat(swapPath)
	assert.NoError(t, err)
}

// TestSharedSwapCrashRecoveryRoundTrip guards the agreement between
// the entry Load opens and the one the recovery prompts recompute
// from the file URI alone, which is what makes a crash recoverable.
func TestSharedSwapCrashRecoveryRoundTrip(t *testing.T) {
	dir, swapDir := t.TempDir(), t.TempDir()
	target := filepath.Join(dir, "main.go")
	require.NoError(t, os.WriteFile(target, []byte("original\n"), 0644))

	scheme := newLocalScheme(t, dir)
	buf := cell.NewBuffer()
	// Deliberately never closed: this session is the one that crashes.
	f, swapPath := openInSwapDir(t, scheme, target, swapDir, buf)

	buf.InsertString(term.Coordinates{}, "edited ")
	f.wg.Wait()

	onDisk, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "original\n", string(onDisk),
		"the original must stay untouched until a flush")

	staged, err := os.ReadFile(swapPath)
	require.NoError(t, err)
	assert.Equal(t, "edited original\n", string(staged),
		"the swap must hold the full buffer before the original is written")

	// The recovery prompts recompute the entry from the file URI
	// alone, so it has to match the one Load opened.
	fileURI, err := makeLocalURI(target)
	require.NoError(t, err)
	swapDirURI, err := SwapDirectory(swapDir, fileURI)
	require.NoError(t, err)
	swapFileURI, err := DefaultSwapFile(swapDirURI, fileURI)
	require.NoError(t, err)
	assert.Equal(t, swapPath, swapFileURI.Path())

	_, err = newFile(scheme, target, cell.NewBuffer(), swapDir, swapPath,
		false, inlineSchedule)
	assert.Equal(t, workspaceapi.ErrFileAlreadyOpen, err,
		"the surviving swap must lock out a second session")

	recovered := cell.NewBuffer()
	rf, err := newFileRecover(scheme, target, swapPath, recovered, true, inlineSchedule)
	require.NoError(t, err)
	assert.Equal(t, "edited original", recovered.String())
	require.NoError(t, rf.Close())

	saved, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "edited original\n", string(saved))

	_ = os.Remove(swapPath)
	reopened, _ := openInSwapDir(t, scheme, target, swapDir, cell.NewBuffer())
	require.NoError(t, reopened.Close())
}

// TestSharedSwapDetectsSameFileAcrossWorkspaces covers the upside of
// naming entries after the absolute path: two workspace roots that
// reach the same file now collide on one entry, so the second open is
// reported as already open instead of silently getting its own swap.
func TestSharedSwapDetectsSameFileAcrossWorkspaces(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.MkdirAll(nested, 0755))

	target := filepath.Join(nested, "main.go")
	require.NoError(t, os.WriteFile(target, []byte("original\n"), 0644))

	swapDir := filepath.Join(t.TempDir(), "swap")
	fileURI, err := makeLocalURI(target)
	require.NoError(t, err)
	swapDirURI, err := SwapDirectory(swapDir, fileURI)
	require.NoError(t, err)

	open := func(workspaceRoot string) (FlusherCloser, error) {
		uri, err := makeLocalURI(workspaceRoot)
		require.NoError(t, err)
		scheme, err := newTestFileScheme(uri)
		require.NoError(t, err)
		w := NewSchemeWorkspace(uri, scheme, inlineSchedule)
		return w.Load(fileURI, cell.NewBuffer(), swapDirURI, false)
	}

	first, err := open(root)
	require.NoError(t, err)
	defer first.Close()

	_, err = open(nested)
	assert.Equal(t, workspaceapi.ErrFileAlreadyOpen, err)
}

type renameFailScheme struct {
	schemeapi.Scheme
}

func (renameFailScheme) Rename(oldpath, newpath string) error {
	return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
}

// openInSwapDir mirrors how schemeWorkspace.Load resolves the entry.
func openInSwapDir(
	t *testing.T, scheme schemeapi.Scheme, filePath, swapDir string, buf *cell.Buffer,
) (*file, string) {
	t.Helper()
	dir, swapPath := swapFileName(swapDir, filePath)
	f, err := newFile(scheme, filePath, buf, sharedSwapDir(dir, filePath),
		swapPath, false, inlineSchedule)
	require.NoError(t, err)
	return f, swapPath
}

func newLocalScheme(t *testing.T, root string) schemeapi.Scheme {
	t.Helper()
	uri, err := makeLocalURI(root)
	require.NoError(t, err)
	scheme, err := newTestFileScheme(uri)
	require.NoError(t, err)
	return scheme
}
