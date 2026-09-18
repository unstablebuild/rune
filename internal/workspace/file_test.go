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
	"errors"
	"fmt"
	"io"
	"math/rand"
	os "os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	gomock "go.uber.org/mock/gomock"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/workspace/workspaceapitest"
)

const sampleSnippet = `
/*
 * Check if the current buffer should be added to or removed from the list of
 * diff buffers.
 */
	void
diff_buf_adjust(win_T *win)
{
	win_T	*wp;
	int		i;

	if (!win->w_p_diff)
	{
	/* When there is no window showing a diff for this buffer, remove
	 * it from the diffs. */
	FOR_ALL_WINDOWS(wp)
		if (wp->w_buffer == win->w_buffer && wp->w_p_diff)
		break;
	if (wp == NULL)
	{
		i = diff_buf_idx(win->w_buffer);
		if (i != DB_COUNT)
		{
		curtab->tp_diffbuf[i] = NULL;
		curtab->tp_diff_invalid = TRUE;
		diff_redraw(TRUE);
		}
	}
	}
	else
	diff_buf_add(win->w_buffer);
} /* { */ `

// INTEGRATION TESTS

// awaitFlushErr collapses the async (chan, err) result back into a
// single error so existing sync-style tests keep working.
func awaitFlushErr(ch <-chan error, err error) error {
	if err != nil {
		return err
	}
	return <-ch
}

func newIntegrationTestCase(t *testing.T, endsInEOL bool) (
	*cell.Buffer, *os.File,
) {
	buffer := cell.NewBuffer()
	file, err := os.CreateTemp("", "frctl_file_test")
	require.NoError(t, err)

	_, err = file.Write([]byte(sampleSnippet))
	require.NoError(t, err)

	if endsInEOL {
		_, err = file.Write([]byte{'\n'})
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	})
	return buffer, file
}

// refactor shim
func openFile(
	filename string, buf *cell.Buffer, swapDir string, readOnly bool,
) (*file, error) {
	workspaceURI, err := makeLocalURI(filepath.Dir(filename))
	if err != nil {
		return nil, err
	}
	scheme, err := newTestFileScheme(workspaceURI)
	if err != nil {
		return nil, err
	}
	l, err := newFile(scheme, filename, buf, swapDir, readOnly, inlineSchedule)
	if err != nil {
		return nil, err
	}
	return l, nil
}

// refactor shim
func recoverFile(
	filename, recoverFilename string, buf *cell.Buffer, force bool,
) (*file, error) {
	workspaceURI, err := makeLocalURI(filepath.Dir(filename))
	if err != nil {
		return nil, err
	}
	scheme, err := newTestFileScheme(workspaceURI)
	if err != nil {
		return nil, err
	}
	l, err := newFileRecover(scheme, filename, recoverFilename, buf, force, inlineSchedule)
	if err != nil {
		return nil, err
	}
	return l, nil
}

// tests FileBuffer with real os.File's. endsInEOL refers to the original file.
func testFileBufferIntegration(t *testing.T, endsInEOL bool) {
	t.Run("if file does not exist, create it upon Flush", func(t *testing.T) {
		buf := cell.NewBuffer()
		file, err := os.CreateTemp("", "frctl_file_test")
		require.NoError(t, err)

		// secure a random filename in a tmp directory
		filename := file.Name()
		require.NoError(t, os.Remove(filename))

		f, err := openFile(filename, buf, "", false)
		require.NoError(t, err)
		defer f.Close()

		_, err = os.Stat(filename)
		require.Error(t, err)

		require.NoError(t, awaitFlushErr(f.Flush(context.Background())))

		_, err = os.Stat(filename)
		require.NoError(t, err)
	})

	t.Run("if file does not exist and readOnly, error out", func(t *testing.T) {
		buf := cell.NewBuffer()
		file, err := os.CreateTemp("", "frctl_file_test")
		require.NoError(t, err)

		// secure a random filename in a tmp directory
		filename := file.Name()
		require.NoError(t, os.Remove(filename))

		_, err = openFile(filename, buf, "", true)
		require.Error(t, err)

		_, err = os.Stat(filename)
		require.Error(t, err)
	})

	t.Run("if readOnly, error out on flush", func(t *testing.T) {
		buf := cell.NewBuffer()
		file, err := os.CreateTemp("", "frctl_file_test")
		require.NoError(t, err)

		filename := file.Name()
		os.WriteFile(filename, []byte("blah"), 0000)
		require.NoError(t, file.Close())

		f, err := openFile(filename, buf, "", true)
		require.NoError(t, err)
		defer f.Close()

		buf.WriteString("meh")
		assert.Error(t, awaitFlushErr(f.Flush(context.Background())))

		data, err := os.ReadFile(filename)
		require.NoError(t, err)
		assert.Equal(t, "blah", string(data))
	})

	t.Run("if readOnly no swap files are initialized", func(t *testing.T) {
		buf := cell.NewBuffer()
		file, err := os.CreateTemp("", "frctl_file_test")
		require.NoError(t, err)

		filename := file.Name()
		require.NoError(t, file.Close())

		swapDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		f, err := openFile(filename, buf, swapDir, true)
		require.NoError(t, err)
		defer f.Close()

		_, swapFileName := swapFileName(swapDir, file.Name())

		_, err = os.Stat(swapFileName)
		require.Error(t, err)
	})

	t.Run("if readOnly it doesn't error out if file is already open", func(t *testing.T) {
		buf := cell.NewBuffer()
		file, err := os.CreateTemp("", "frctl_file_test")
		require.NoError(t, err)

		filename := file.Name()
		require.NoError(t, file.Close())

		f1, err := openFile(filename, buf, "", false)
		require.NoError(t, err)
		defer f1.Close()

		f2, err := openFile(filename, buf, "", true)
		require.NoError(t, err)
		defer f2.Close()
	})

	t.Run("if swap holding buffer is closed, then NewFileBuffer should NOT error out", func(t *testing.T) {
		buf := cell.NewBuffer()
		file, err := os.CreateTemp("", "frctl_file_test")
		require.NoError(t, err)

		filename := file.Name()
		require.NoError(t, file.Close())

		f1, err := openFile(filename, buf, "", false)
		require.NoError(t, err)

		_, err = openFile(filename, buf, "", false)
		require.Error(t, err)

		assert.NoError(t, f1.Close())

		f2, err := openFile(filename, buf, "", false)
		require.NoError(t, err)
		assert.NoError(t, f2.Close())
	})

	t.Run("respects original file mode", func(t *testing.T) {
		buf := cell.NewBuffer()
		file, err := os.CreateTemp("", "frctl_file_test")
		require.NoError(t, err)

		filename := file.Name()

		err = file.Chmod(0700)
		require.NoError(t, err)
		require.NoError(t, file.Close())

		f, err := openFile(filename, buf, "", false)
		require.NoError(t, err)
		defer f.Close()

		fileInfo, err := os.Stat(filename)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0700), fileInfo.Mode())

		require.NoError(t, awaitFlushErr(f.Flush(context.Background())))

		fileInfo, err = os.Stat(filename)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0700), fileInfo.Mode())
	})

	t.Run("respects symlinks", func(t *testing.T) {
		buf := cell.NewBuffer()
		orig, err := os.CreateTemp("", "frctl_file_test")
		require.NoError(t, err)

		require.NoError(t, orig.Chmod(0700))
		filename := orig.Name() + ".symlink"

		require.NoError(t, os.Symlink(orig.Name(), filename))
		fileInfo, err := os.Lstat(filename)
		require.NoError(t, err)
		assert.True(t, fileInfo.Mode()&os.ModeSymlink == os.ModeSymlink)

		require.NoError(t, orig.Close())

		f, err := openFile(filename, buf, "", false)
		require.NoError(t, err)
		defer f.Close()
		require.NoError(t, awaitFlushErr(f.Flush(context.Background())))
		b, err := os.ReadFile(filename)
		require.NoError(t, err)
		assert.Equal(t, "", string(b))

		buf.InsertString(term.Coordinates{}, "blah\n")

		require.NoError(t, awaitFlushErr(f.Flush(context.Background())))

		fileInfo, err = os.Lstat(filename)
		require.NoError(t, err)
		assert.True(t, fileInfo.Mode()&os.ModeSymlink == os.ModeSymlink)

		b, err = os.ReadFile(filename)
		require.NoError(t, err)
		assert.Equal(t, "blah\n", string(b))

		fileInfo, err = os.Stat(orig.Name())
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0700), fileInfo.Mode())
	})

	t.Run("if file does not exist, if there are errors upon creation, it bubbles up on Flush", func(t *testing.T) {
		buf := cell.NewBuffer()
		rgen := rand.New(rand.NewSource(int64(time.Now().Nanosecond())))
		filename := fmt.Sprintf("/tmp/mpo/tmp/tmp/tmp/tmp/%d.go", rgen.Int())

		f, err := openFile(filename, buf, os.TempDir(), false)
		require.NoError(t, err)
		defer f.Close()

		_, err = os.Stat(filename)
		require.Error(t, err)

		require.Error(t, awaitFlushErr(f.Flush(context.Background())))
	})

	t.Run("if file does not exist, Reload errors", func(t *testing.T) {
		buf := cell.NewBuffer()
		rgen := rand.New(rand.NewSource(int64(time.Now().Nanosecond())))
		filename := fmt.Sprintf("/tmp/%d.go", rgen.Int())

		f, err := openFile(filename, buf, os.TempDir(), false)
		require.NoError(t, err)
		defer f.Close()

		_, err = os.Stat(filename)
		require.Error(t, err)

		require.Error(t, awaitFlushErr(f.Reload(context.Background())))
	})

	t.Run("no swap file is open, creates one; removes on close", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, endsInEOL)

		swapDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		_, swapFileName := swapFileName(swapDir, file.Name())

		_, err = os.Stat(swapFileName)
		require.Error(t, err)

		f, err := openFile(file.Name(), b, swapDir, false)
		require.NoError(t, err)

		_, err = os.Stat(swapFileName)
		require.NoError(t, err, swapFileName)

		f.Close()

		_, err = os.Stat(swapFileName)
		assert.Error(t, err)
	})

	t.Run("fsyncs swap upon update", func(t *testing.T) {
		for _, reload := range []bool{false, true} {
			b, file := newIntegrationTestCase(t, endsInEOL)

			f, err := openFile(file.Name(), b, "", false)
			require.NoError(t, err)

			if reload {
				require.NoError(t, awaitFlushErr(f.Reload(context.Background())))
			}

			buf, err := os.ReadFile(f.swap.Name())
			require.NoError(t, err)
			content := sampleSnippet
			if endsInEOL {
				content += "\n"
			}
			assert.Equal(t, content, string(buf))

			const writeStr = "XXXXXX"
			b.InsertString(term.Coordinates{}, writeStr)

			// wait for updates
			f.wg.Wait()

			buf, err = os.ReadFile(f.swap.Name())
			require.NoError(t, err)

			assert.Equal(t, writeStr+sampleSnippet+"\n", string(buf))
		}
	})

	t.Run("fsyncs file upon Flush", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, endsInEOL)

		f, err := openFile(file.Name(), b, "", false)
		require.NoError(t, err)
		defer f.Close()

		const writeStr = "XXXXXX"
		b.InsertString(term.Coordinates{}, writeStr)
		require.NoError(t, awaitFlushErr(f.Flush(context.Background())))

		buf, err := os.ReadFile(file.Name())
		require.NoError(t, err)

		assert.Equal(t, writeStr+sampleSnippet+"\n", string(buf))
	})

	t.Run("returns error if swap is already open", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, endsInEOL)

		swapDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)

		f, err := openFile(file.Name(), b, swapDir, false)
		require.NoError(t, err)
		defer f.Close()

		_, err = openFile(file.Name(), b, swapDir, false)
		assert.Equal(t, workspaceapi.ErrFileAlreadyOpen, err)
	})

	t.Run("Reload reloads file as it was originally on disk", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, endsInEOL)

		f, err := openFile(file.Name(), b, "", false)
		require.NoError(t, err)

		const writeStr = "XXXXXX"
		b.InsertString(term.Coordinates{}, writeStr)

		require.NoError(t, awaitFlushErr(f.Reload(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), sampleSnippet, endsInEOL)
		require.NoError(t, f.Close())
	})

	t.Run("Reload reloads file as it was on disk, after Flush", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, endsInEOL)

		f, err := openFile(file.Name(), b, "", false)
		require.NoError(t, err)

		const writeStr = "XXXXXX"
		b.InsertString(term.Coordinates{}, writeStr)

		require.NoError(t, awaitFlushErr(f.Flush(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), writeStr+sampleSnippet, true)

		require.NoError(t, awaitFlushErr(f.Reload(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), writeStr+sampleSnippet, true)

		b.InsertString(term.Coordinates{}, writeStr)
		require.NoError(t, awaitFlushErr(f.Reload(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), writeStr+sampleSnippet, true)
	})

	t.Run("Reload reloads originally uncreated file", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, endsInEOL)
		require.NoError(t, file.Close())
		require.NoError(t, os.Remove(file.Name()))

		f, err := openFile(file.Name(), b, "", false)
		require.NoError(t, err)

		require.NoError(t, os.WriteFile(file.Name(), []byte("deep purple\n"), 0666))

		require.NoError(t, awaitFlushErr(f.Reload(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), "deep purple", true)
	})

	t.Run("Reload integration with cell subscribers", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, endsInEOL)

		f, err := openFile(file.Name(), b, "", false)
		require.NoError(t, err)

		buf := cell.NewBuffer()
		b.Subscribe(&testCellSubscriber{buf: buf})

		const writeStr = "XXXXXX"
		b.InsertString(term.Coordinates{}, writeStr)

		require.NoError(t, awaitFlushErr(f.Reload(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), sampleSnippet, endsInEOL)
		assert.Equal(t, sampleSnippet+"\n", buf.String()) // buf doesn't have unix view

		require.NoError(t, f.Close())
	})
}

type testCellSubscriber struct {
	buf *cell.Buffer
}

func (t *testCellSubscriber) OnWillEdit(
	ctx context.Context, start, end term.Coordinates, str string,
) {
	t.buf.Edit(ctx, start, end, str)
}

func (t *testCellSubscriber) OnDidEdit(
	ctx context.Context, from, to term.Coordinates, old string,
) {
}

func assertFileAndBufferOnDisk(
	t *testing.T, b *cell.Buffer, name, expected string,
	expectedLastEOL bool,
) {
	t.Helper()

	assert.Equal(t, expected, b.String())

	data, err := os.ReadFile(name)
	require.NoError(t, err)

	expectedOnDisk := expected
	if expectedLastEOL {
		expectedOnDisk += "\n"
	}
	assert.Equal(t, expectedOnDisk, string(data))
}

func TestFileBufferIntegrationEOL(t *testing.T) {
	testFileBufferIntegration(t, true)
}

func TestFileBufferIntegrationNOEOL(t *testing.T) {
	testFileBufferIntegration(t, false)
}

func assertRecoverFromSwapFile(t *testing.T, filename, swapname string, b *cell.Buffer) {
	assert.Equal(t, sampleSnippet, b.String())

	buf, err := os.ReadFile(filename)
	require.NoError(t, err)
	assert.Equal(t, sampleSnippet+"\n", string(buf))

	buf, err = os.ReadFile(swapname)
	require.NoError(t, err)
	assert.Equal(t, sampleSnippet+"\n", string(buf))
}

func newRecoveryIntegrationCase(t *testing.T) (
	*cell.Buffer, *os.File, *os.File,
) {
	_, file := newIntegrationTestCase(t, true)
	require.NoError(t, file.Truncate(0))

	b, swap := newIntegrationTestCase(t, true)
	return b, file, swap
}

func TestFileBufferRecover(t *testing.T) {
	t.Run("recovers file from swap", func(t *testing.T) {
		b, file, swap := newRecoveryIntegrationCase(t)

		filepath, swapFilepath := file.Name(), swap.Name()
		f, err := recoverFile(filepath, swapFilepath, b, false)
		require.NoError(t, err)
		defer f.Close()

		assertRecoverFromSwapFile(t, filepath, swapFilepath, b)
	})

	t.Run("recovers file from swap even if file does not exist", func(t *testing.T) {
		b, swap := newIntegrationTestCase(t, true)

		swapFilepath := swap.Name()
		swapFileName := path.Base(swapFilepath)
		filepath := path.Join(path.Dir(swapFilepath), "my_actual_file"+swapFileName)
		f, err := recoverFile(filepath, swapFilepath, b, false)
		require.NoError(t, err, filepath)
		defer f.Close()

		assertRecoverFromSwapFile(t, filepath, swapFilepath, b)
	})

	t.Run("returns error if file was modified after swap and does not remove swap", func(t *testing.T) {
		b, file, swap := newRecoveryIntegrationCase(t)

		filepath, swapFilepath := file.Name(), swap.Name()

		time.Sleep(10 * time.Millisecond)
		_, err := file.WriteString("blah")
		require.NoError(t, err)
		require.NoError(t, file.Sync())

		_, err = recoverFile(filepath, swapFilepath, b, false)
		assert.Equal(t, workspaceapi.ErrStaleData, err)

		_, err = os.Stat(swapFilepath)
		require.NoError(t, err)
	})

	t.Run("recovers if file was modified after swap and recover was called with force=true", func(t *testing.T) {
		b, file, swap := newRecoveryIntegrationCase(t)

		filepath, swapFilepath := file.Name(), swap.Name()

		time.Sleep(10 * time.Millisecond)
		_, err := file.WriteString("Inma")
		require.NoError(t, err)
		require.NoError(t, file.Sync())

		_, err = recoverFile(filepath, swapFilepath, b, true)
		assert.NoError(t, err)

		assertRecoverFromSwapFile(t, filepath, swapFilepath, b)
	})

	t.Run("recovers file and updates it with swap contents if swap is ahead", func(t *testing.T) {
		b, file, swap := newRecoveryIntegrationCase(t)

		filepath, swapFilepath := file.Name(), swap.Name()

		time.Sleep(10 * time.Millisecond)
		_, err := swap.WriteString("blah")
		require.NoError(t, err)
		require.NoError(t, swap.Sync())

		swapContent, err := os.ReadFile(swapFilepath)
		require.NoError(t, err)

		_, err = recoverFile(filepath, swapFilepath, b, false)
		require.NoError(t, err)

		fileContent, err := os.ReadFile(filepath)
		require.NoError(t, err)
		assert.Equal(t, swapContent, fileContent)
	})

	t.Run("if a recover buffer takes over swap, it should now allow for other NewFileBuffer to open it", func(t *testing.T) {
		b, file, _ := newRecoveryIntegrationCase(t)

		swapDir := filepath.Dir(file.Name())

		f1, err := openFile(file.Name(), b, swapDir, false)
		require.NoError(t, err)
		defer f1.Close()

		// checking update time is time based
		time.Sleep(10 * time.Millisecond)

		b2 := cell.NewBuffer()
		f2, err := recoverFile(file.Name(), f1.swapFileName, b2, false)
		require.NoError(t, err)
		defer f2.Close()

		time.Sleep(10 * time.Millisecond)

		require.Equal(t, workspaceapi.ErrStaleData, awaitFlushErr(f1.Flush(context.Background())))

		_, err = openFile(file.Name(), b, swapDir, false)
		require.Error(t, err)
	})
}

func TestForceFlush(t *testing.T) {
	t.Run("flushes to disk", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, true)

		f, err := openFile(file.Name(), b, "", false)
		require.NoError(t, err)

		const writeStr = "XXXXXX"
		b.InsertString(term.Coordinates{}, writeStr)

		require.NoError(t, awaitFlushErr(f.ForceFlush(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), writeStr+sampleSnippet, true)
	})

	t.Run("flushes to disk after a reload", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, true)

		f, err := openFile(file.Name(), b, "", false)
		require.NoError(t, err)

		const writeStr = "XXXXXX"
		b.InsertString(term.Coordinates{}, writeStr)

		require.NoError(t, awaitFlushErr(f.Reload(context.Background())))
		require.NoError(t, awaitFlushErr(f.ForceFlush(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), sampleSnippet, true)
	})

	t.Run("is able to overwrite when a file was modified oob", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, true)

		f, err := openFile(file.Name(), b, "", false)
		require.NoError(t, err)

		const writeStr = "XXXXXX"
		b.InsertString(term.Coordinates{}, writeStr)

		require.NoError(t, awaitFlushErr(f.Flush(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), writeStr+sampleSnippet, true)

		require.NoError(t, os.WriteFile(file.Name(), []byte("abv"), 0))
		data, err := os.ReadFile(file.Name())
		require.NoError(t, err)
		assert.Equal(t, "abv", string(data))

		require.NoError(t, awaitFlushErr(f.ForceFlush(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), writeStr+sampleSnippet, true)
	})

	t.Run("creates file if file is removed oob", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, true)

		f, err := openFile(file.Name(), b, "", false)
		require.NoError(t, err)

		const writeStr = "XXXXXX"
		b.InsertString(term.Coordinates{}, writeStr)

		require.NoError(t, awaitFlushErr(f.Flush(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), writeStr+sampleSnippet, true)

		require.NoError(t, os.Remove(file.Name()))

		require.NoError(t, awaitFlushErr(f.ForceFlush(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), writeStr+sampleSnippet, true)
	})

	t.Run("overwrites read-only", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, true)

		f, err := openFile(file.Name(), b, "", true)
		require.NoError(t, err)

		const writeStr = "XXXXXX"
		b.InsertString(term.Coordinates{}, writeStr)

		require.NoError(t, awaitFlushErr(f.ForceFlush(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), writeStr+sampleSnippet, true)
	})

	t.Run("overwrites read-only no changes, respects original contents", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, true)

		f, err := openFile(file.Name(), b, "", true)
		require.NoError(t, err)

		require.NoError(t, awaitFlushErr(f.ForceFlush(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), sampleSnippet, true)
	})

	t.Run("Flush after ForceFlush a readonly should not error", func(t *testing.T) {
		b, file := newIntegrationTestCase(t, true)

		f, err := openFile(file.Name(), b, "", true)
		require.NoError(t, err)

		const writeStr = "XXXXXX"
		b.InsertString(term.Coordinates{}, writeStr)

		require.NoError(t, awaitFlushErr(f.ForceFlush(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), writeStr+sampleSnippet, true)

		b.InsertString(term.Coordinates{}, writeStr)

		require.NoError(t, awaitFlushErr(f.Flush(context.Background())))
		assertFileAndBufferOnDisk(t, b, file.Name(), writeStr+writeStr+sampleSnippet, true)
	})
}

// UNIT TESTS

// returns an un-initialized (but dep injected) FileBuffer along with the mocked OsFile
func newTestFileBuffer(ctrl *gomock.Controller) (*file, *workspaceapitest.MockFile) {
	schemeIfc, _ := newTestScheme("test")(context.Background(), nil, workspaceapi.URI{})
	scheme := schemeIfc.(*testScheme)
	mock := workspaceapitest.NewMockFile(ctrl)
	scheme.openFunc = func(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
		return mock, nil
	}
	f := new(file)
	f.scheme = scheme
	f.closedCh = make(chan struct{})
	return f, mock
}

func expectRead(mock *workspaceapitest.MockFile, data []byte) {
	mock.EXPECT().Read(gomock.Any()).DoAndReturn(func(buf []byte) (int, error) {
		if len(buf) < len(data) {
			panic("seriously?")
		}
		n := copy(buf, data)
		return n, io.EOF
	})
}

func expectInitSwap(
	mock *workspaceapitest.MockFile, fileName string, fileInfo os.FileInfo, data []byte,
) {
	// stat original file
	mock.EXPECT().Stat().Return(fileInfo, nil).AnyTimes()
	// read original file
	expectRead(mock, data)

	mock.EXPECT().Name().Return(fileName).AnyTimes()
	// seek original file back to 0
	mock.EXPECT().Seek(gomock.Eq(int64(0)), gomock.Eq(0)).Return(int64(0), nil)

	mock.EXPECT().Truncate(gomock.Any()).Return(nil)

	// write to swap file
	mock.EXPECT().Write(gomock.Any()).DoAndReturn(func(p []byte) (n int, err error) {
		// assert.EqualValues(t, data, p)
		return len(p), nil
	})
	mock.EXPECT().Sync().Return(nil)
}

func expectInitBuffer(mock *workspaceapitest.MockFile, data []byte) {
	expectRead(mock, data)

	// seek original file back to 0
	mock.EXPECT().Seek(gomock.Eq(int64(0)), gomock.Eq(0)).Return(int64(0), nil)
}

func TestFileBufferInit(t *testing.T) {
	t.Run("is able to use a symlink file", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		fileName := "elMeuNom"
		data := []byte("bon dia senyor")
		fileInfo := testFileInfo{mode: os.ModeSymlink}

		f, mock := newTestFileBuffer(ctrl)

		expectInitSwap(mock, fileName, fileInfo, data)
		expectInitBuffer(mock, data)

		assert.NoError(t, f.init(fileName, cell.NewBuffer(), "", false))
	})

	t.Run("is able to use a regular file", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		fileName := "myName"
		data := []byte("good morning sir")
		fileInfo := testFileInfo{}

		f, mock := newTestFileBuffer(ctrl)
		expectInitSwap(mock, fileName, fileInfo, data)
		expectInitBuffer(mock, data)

		assert.NoError(t, f.init(fileName, cell.NewBuffer(), "", false))
	})

	t.Run("masks whether there's a last EOL from clients", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		fileName := "CA"

		for _, data := range [][]byte{[]byte("Oakland\n"), []byte("Oakland")} {
			fileInfo := testFileInfo{}

			f, mock := newTestFileBuffer(ctrl)
			expectInitSwap(mock, fileName, fileInfo, data)
			expectInitBuffer(mock, data)

			buf := cell.NewBuffer()
			assert.NoError(t, f.init(fileName, buf, "", false))
			assert.Equal(t, "Oakland", buf.String(), fmt.Sprintf("%q", string(data)))
			assert.Equal(t, 1, buf.Rows(), fmt.Sprintf("%q", string(data)))

			wait := expectCopyToSwapPrepare(f, mock)
			mock.EXPECT().
				Write(gomock.Any()).
				Return(1, nil)
			mock.EXPECT().Sync().Return(nil)
			buf.WriteString("\n")
			assert.Equal(t, "Oakland\n", buf.String(), fmt.Sprintf("%q", string(data)))
			assert.Equal(t, 2, buf.Rows(), fmt.Sprintf("%q", string(data)))
			wait()
		}
	})

	t.Run("returns error if original file is not regular or symlink file", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock := newTestFileBuffer(ctrl)
		mock.EXPECT().Stat().Return(testFileInfo{mode: os.ModeSocket}, nil)

		assert.Equal(t, workspaceapi.ErrFileIsNotRegular, f.init("fjkelw", cell.NewBuffer(), "", false))
	})

	t.Run("returns error if original file is directory", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock := newTestFileBuffer(ctrl)
		mock.EXPECT().Stat().Return(testFileInfo{isDir: true}, nil)

		assert.Equal(t, workspaceapi.ErrFileIsNotRegular, f.init("fjkelw", cell.NewBuffer(), "", false))
	})

	t.Run("bubble up original file open error", func(t *testing.T) {
		accessDeniedErr := errors.New("access denied")
		f := new(file)
		f.scheme = &testScheme{}
		f.closedCh = make(chan struct{})
		f.scheme.(*testScheme).openFunc = func(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
			return nil, accessDeniedErr
		}
		assert.Equal(t, accessDeniedErr, f.init("fjkelw", cell.NewBuffer(), "", false))
	})

	t.Run("bubble up swap file open error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		accessDeniedErr := errors.New("access denied")
		origFileMock := workspaceapitest.NewMockFile(ctrl)
		f := new(file)
		f.scheme = &testScheme{}
		f.closedCh = make(chan struct{})
		i := 0
		f.scheme.(*testScheme).openFunc = func(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
			i++
			if i == 1 {
				return origFileMock, nil
			}
			return nil, accessDeniedErr
		}

		fileName := "fjklewjflk"

		origFileMock.EXPECT().Stat().Return(testFileInfo{}, nil)
		origFileMock.EXPECT().Name().Return(fileName).AnyTimes()

		assert.Equal(t, accessDeniedErr, f.init(fileName, cell.NewBuffer(), "", false))
	})

	t.Run("bubble up swap file write error, and remove empty file swap", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		accessDeniedErr := errors.New("access denied")
		fileName := "myName"
		data := []byte("good morning sir")
		fileInfo := testFileInfo{}

		f, mock := newTestFileBuffer(ctrl)
		var i int
		f.scheme.(*testScheme).removeFunc = func(name string) error {
			i++
			return nil
		}
		mock.EXPECT().Stat().Return(fileInfo, nil).AnyTimes()
		expectRead(mock, data)

		mock.EXPECT().Name().Return(fileName).AnyTimes()

		mock.EXPECT().Truncate(gomock.Any()).Return(nil)

		mock.EXPECT().Write(gomock.Any()).DoAndReturn(func(p []byte) (n int, err error) {
			return 0, accessDeniedErr
		})

		assert.Error(t, f.init(fileName, cell.NewBuffer(), "", false))
		assert.Equal(t, 1, i)
	})

	t.Run("bubble up swap file read error, and remove empty file swap", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		accessDeniedErr := errors.New("access denied")
		fileName := "myName"
		fileInfo := testFileInfo{}

		f, mock := newTestFileBuffer(ctrl)
		var i int
		f.scheme.(*testScheme).removeFunc = func(name string) error {
			i++
			return nil
		}
		mock.EXPECT().Stat().Return(fileInfo, nil).AnyTimes()
		mock.EXPECT().Read(gomock.Any()).DoAndReturn(func(buf []byte) (int, error) {
			return 0, accessDeniedErr
		})

		assert.Error(t, f.init(fileName, cell.NewBuffer(), "", false))
		assert.Equal(t, 1, i)
	})

	t.Run("bubble up swap file stat error, and remove empty file swap", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		accessDeniedErr := errors.New("access denied")
		fileName := "myName"
		fileInfo := testFileInfo{}

		f, mock := newTestFileBuffer(ctrl)
		var i int
		f.scheme.(*testScheme).removeFunc = func(name string) error {
			i++
			return nil
		}
		var j int
		mock.EXPECT().Stat().Return(fileInfo, nil).DoAndReturn(func() (os.FileInfo, error) {
			j++
			if j == 1 {
				return fileInfo, nil
			}
			return nil, accessDeniedErr
		})
		expectRead(mock, []byte("1234"))

		mock.EXPECT().Name().Return(fileName).AnyTimes()
		mock.EXPECT().Truncate(gomock.Any()).Return(nil)

		mock.EXPECT().Write(gomock.Any()).DoAndReturn(func(p []byte) (n int, err error) {
			return 0, accessDeniedErr
		})

		assert.Error(t, f.init(fileName, cell.NewBuffer(), "", false))
		assert.Equal(t, 1, i)
	})
}

const defaultFileName = "myOhDear.go"

var defaultFileData = []byte("oh, dear")

func newInitializedTestFileBuffer(t *testing.T, ctrl *gomock.Controller) (
	*file, *workspaceapitest.MockFile, *cell.Buffer,
) {
	f, mock := newTestFileBuffer(ctrl)
	expectInitSwap(mock, defaultFileName, testFileInfo{}, defaultFileData)
	expectInitBuffer(mock, defaultFileData)
	buf := cell.NewBuffer()
	require.NoError(t, f.init(defaultFileName, buf, "", false))
	return f, mock, buf
}

type testOsError struct {
	isExistErr      bool
	isNotExistErr   bool
	isPermissionErr bool
}

func (t testOsError) isPermission() bool {
	return t.isPermissionErr
}
func (t testOsError) isExist() bool {
	return t.isExistErr
}
func (t testOsError) isNotExist() bool {
	return t.isNotExistErr
}

func newUninitializedTestFileBuffer(t *testing.T, ctrl *gomock.Controller) (
	*file, *workspaceapitest.MockFile, *cell.Buffer,
) {
	f, mock := newTestFileBuffer(ctrl)
	f.scheme.(*testScheme).openFunc = func(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
		if flag&os.O_CREATE != 0 {
			mock.EXPECT().Read(gomock.Any()).Return(0, io.EOF).Times(1)
			mock.EXPECT().Seek(gomock.Any(), gomock.Any()).Return(int64(0), nil).Times(1)
			return mock, nil
		}
		return nil, os.ErrNotExist
	}

	mock.EXPECT().Name().Return(defaultFileName).AnyTimes()

	buf := cell.NewBuffer()
	require.NoError(t, f.init(defaultFileName, buf, "", false))
	return f, mock, buf
}

func newReadOnlyTestFileBuffer(t *testing.T, ctrl *gomock.Controller) (
	*file, *workspaceapitest.MockFile, *cell.Buffer,
) {
	f, mock := newTestFileBuffer(ctrl)
	f.scheme.(*testScheme).openFunc = func(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
		if flag&os.O_RDWR != 0 || flag&os.O_CREATE != 0 {
			return nil, os.ErrPermission
		}
		return mock, nil
	}

	mock.EXPECT().Name().Return(defaultFileName).AnyTimes()
	mock.EXPECT().Stat().Return(testFileInfo{}, nil).AnyTimes()
	expectRead(mock, defaultFileData)
	mock.EXPECT().Seek(gomock.Eq(int64(0)), gomock.Eq(0)).Return(int64(0), nil)

	buf := cell.NewBuffer()
	require.NoError(t, f.init(defaultFileName, buf, "", false))
	return f, mock, buf
}

func newRecoveredTestFileBuffer(t *testing.T, ctrl *gomock.Controller) (
	*file, *workspaceapitest.MockFile, *cell.Buffer,
) {
	f, mock := newTestFileBuffer(ctrl)
	mock.EXPECT().Stat().Return(testFileInfo{}, nil).AnyTimes()
	mock.EXPECT().Close().Return(nil).Times(2)
	expectInitSwap(mock, defaultFileName, testFileInfo{}, defaultFileData)
	expectInitBuffer(mock, defaultFileData)
	buf := cell.NewBuffer()
	require.NoError(t, f.initRecover(defaultFileName,
		"."+defaultFileName+".rswp", buf, false))
	return f, mock, buf
}

func testFileBufferClose(t *testing.T, newBuffer newBufferFunc) {
	t.Run("Close should remove and close all resources such that Init can be called again", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, _ := newBuffer(t, ctrl)

		mock.EXPECT().Close().Return(nil).Times(2)
		assert.NoError(t, f.Close())

		expectInitSwap(mock, defaultFileName, testFileInfo{}, defaultFileData)
		expectInitBuffer(mock, defaultFileData)
		assert.NoError(t, f.init(defaultFileName, cell.NewBuffer(), "", false))
	})

	t.Run("two consecutive calls to Close should return an error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, _ := newBuffer(t, ctrl)

		mock.EXPECT().Close().Return(nil).Times(2)
		assert.NoError(t, f.Close())
		assert.Error(t, f.Close())
	})

	t.Run("Close should remove the swap file", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, _ := newBuffer(t, ctrl)
		called := false
		f.scheme.(*testScheme).removeFunc = func(name string) error {
			called = true
			return nil
		}

		mock.EXPECT().Close().Return(nil).Times(2)
		assert.NoError(t, f.Close())
		assert.True(t, called)
	})

	t.Run("Close returns no errors if file was opened in read-only", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, _ := newReadOnlyTestFileBuffer(t, ctrl)
		mock.EXPECT().Close().Return(nil).Times(1)
		assert.NoError(t, f.Close())
	})
}

func TestNewFileBufferClose(t *testing.T) {
	testFileBufferClose(t, newInitializedTestFileBuffer)
}

func TestRecoverFileBufferClose(t *testing.T) {
	testFileBufferClose(t, newRecoveredTestFileBuffer)
}

func TestFileNotCreatedBufferClose(t *testing.T) {
	t.Run("Close should remove and close all resources such that Init can be called again", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, _ := newUninitializedTestFileBuffer(t, ctrl)

		mock.EXPECT().Close().Return(nil).Times(1)
		assert.NoError(t, f.Close())
		assert.NoError(t, f.init(defaultFileName, cell.NewBuffer(), "", false))
	})

	t.Run("two consecutive calls to Close should return an error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, _ := newUninitializedTestFileBuffer(t, ctrl)

		mock.EXPECT().Close().Return(nil).Times(1)
		assert.NoError(t, f.Close())
		assert.Error(t, f.Close())
	})
}

func testFileBufferFlush(t *testing.T, newBuffer newBufferFunc) {
	t.Run("Flush bubbles up Stat errors", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, _, _ := newBuffer(t, ctrl)

		myErr := errors.New("what?")
		f.scheme.(*testScheme).statFunc = func(name string) (os.FileInfo, error) {
			return nil, myErr
		}
		assert.Equal(t, myErr, awaitFlushErr(f.Flush(context.Background())))
	})

	t.Run("flush syncs the contents of the buffer to disk", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, _ := newBuffer(t, ctrl)
		called := false
		f.scheme.(*testScheme).renameFunc = func(oldName, newName string) error {
			called = true
			return nil
		}

		mock.EXPECT().Close().Return(nil).Times(2)
		expectInitSwap(mock, defaultFileName, testFileInfo{}, defaultFileData)

		assert.NoError(t, awaitFlushErr(f.Flush(context.Background())))
		assert.True(t, called)
	})

	t.Run("flush bubbles up rename errors", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, _ := newBuffer(t, ctrl)
		myErr := errors.New("wtf")
		f.scheme.(*testScheme).renameFunc = func(oldName, newName string) error {
			return myErr
		}

		mock.EXPECT().Close().Return(nil).Times(2)
		assert.Equal(t, myErr, awaitFlushErr(f.Flush(context.Background())))
	})

	t.Run("flush returns error if file was modified", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, _, _ := newBuffer(t, ctrl)
		f.scheme.(*testScheme).statFunc = func(name string) (os.FileInfo, error) {
			return testFileInfo{modTime: time.Now()}, nil
		}
		assert.Error(t, workspaceapi.ErrStaleData, awaitFlushErr(f.Flush(context.Background())))
	})
}

func TestNewFileBufferFlush(t *testing.T) {
	testFileBufferFlush(t, newInitializedTestFileBuffer)

	t.Run("Flush returns ErrFileIsNotWritable if file was opened in read-only", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, _, _ := newReadOnlyTestFileBuffer(t, ctrl)
		assert.Equal(t, workspaceapi.ErrFileIsNotWritable, awaitFlushErr(f.Flush(context.Background())))
	})

	t.Run("if file is created after NewFileBuffer is called returns error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, _, _ := newUninitializedTestFileBuffer(t, ctrl)
		require.Nil(t, f.orig)

		f.scheme.(*testScheme).openFunc = func(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
			assert.NotZero(t, flag&os.O_CREATE)
			return nil, os.ErrExist
		}
		assert.Equal(t, workspaceapi.ErrStaleData, awaitFlushErr(f.Flush(context.Background())))
	})
}

func TestRecoverFileBufferFlush(t *testing.T) {
	testFileBufferFlush(t, newRecoveredTestFileBuffer)
}

func expectCopyToSwapPrepare(f *file, mock *workspaceapitest.MockFile) func() {
	mock.EXPECT().Truncate(gomock.Eq(int64(0))).Return(nil)
	mock.EXPECT().Seek(gomock.Eq(int64(0)), gomock.Eq(0)).Return(int64(0), nil)
	return f.wg.Wait
}

func expectCopyToSwap(f *file, mock *workspaceapitest.MockFile, newData string) func() {
	expectedContent := newData + string(defaultFileData)
	clean := expectCopyToSwapPrepare(f, mock)
	mock.EXPECT().
		Write(gomock.Eq([]byte(expectedContent+"\n"))).
		Return(len(expectedContent)+1, nil)
	mock.EXPECT().Sync().Return(nil)
	mock.EXPECT().Stat().Return(testFileInfo{}, nil).AnyTimes()
	return clean
}

type newBufferFunc func(*testing.T, *gomock.Controller) (*file, *workspaceapitest.MockFile, *cell.Buffer)

// TODO debug why it's failing sometimes
func testFileBufferInsert(
	t *testing.T,
	newBuffer newBufferFunc,
) {

	t.Run("if copy to swap fails, retries on next insert", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, buf := newBuffer(t, ctrl)
		myString := "my string\n"
		myError := errors.New("I feel clammy")

		mock.EXPECT().Truncate(gomock.Eq(int64(0))).Return(myError)
		buf.InsertString(term.Coordinates{}, myString)
		f.wg.Wait()

		wait := expectCopyToSwap(f, mock, myString+myString)
		defer wait()

		buf.InsertString(term.Coordinates{}, myString)
	})

	t.Run("if copy to swap fails, retries on next Flush", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, buf := newBuffer(t, ctrl)
		myString := "Sant Hipòlit de Voltregà"
		myError := errors.New("No té capità")

		mock.EXPECT().Truncate(gomock.Eq(int64(0))).Return(nil)
		mock.EXPECT().Seek(gomock.Eq(int64(0)), gomock.Eq(0)).Return(int64(0), myError)
		buf.InsertString(term.Coordinates{}, myString)
		f.wg.Wait()

		wait := expectCopyToSwap(f, mock, myString)
		defer wait()

		mock.EXPECT().Close().Return(nil).Times(2)
		expectInitSwap(mock, defaultFileName, testFileInfo{}, defaultFileData)
		assert.NoError(t, awaitFlushErr(f.Flush(context.Background())))
	})

	t.Run("if copy to swap fails, retries on next Flush and fails, bubbles up error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, buf := newBuffer(t, ctrl)
		myString := "Ballz"
		myError := errors.New("rounder")

		mock.EXPECT().Truncate(gomock.Eq(int64(0))).Return(myError)
		buf.InsertString(term.Coordinates{}, myString)
		f.wg.Wait()

		// Replay on Flush fails the same way (e.g. fd genuinely
		// broken, not just stale). recoverFiles then attempts to
		// re-open against the scheme; here the re-open also fails,
		// which is what bubbles up to the caller.
		mock.EXPECT().Truncate(gomock.Eq(int64(0))).Return(myError)
		mock.EXPECT().Close().Return(nil).Times(2)
		recoverErr := errors.New("scheme down")
		f.scheme.(*testScheme).openFunc = func(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
			return nil, recoverErr
		}
		assert.Error(t, awaitFlushErr(f.Flush(context.Background())))
	})

	// TestFlushRecoversAfterStaleSwapFD reproduces the post-reconnect
	// "invalid file descriptor" symptom: the background swap worker
	// failed while the scheme was disconnected, then the scheme
	// reconnected, so the in-memory swap fd is stale. The first
	// retry on :write fails with the same stale-fd error; flush
	// must recover by re-opening orig/swap and re-staging the
	// buffer, then complete the rename normally.
	t.Run("if copy to swap fails after replay, recovers fds and retries", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, buf := newBuffer(t, ctrl)
		myString := "post-reconnect content\n"
		dropErr := errors.New("connection reset by peer")
		staleFd := errors.New("rpc error: code = Unknown desc = invalid file descriptor")

		// Background swap write that fails while the transport is down.
		mock.EXPECT().Truncate(gomock.Eq(int64(0))).Return(dropErr)
		buf.InsertString(term.Coordinates{}, myString)
		f.wg.Wait()

		// First flush attempt after reconnect: the cached swap fd is
		// still wired to the dead session, so copyFlushSwapFile fails
		// again with the gRPC "invalid file descriptor" surface error.
		mock.EXPECT().Truncate(gomock.Eq(int64(0))).Return(staleFd)

		// recoverFiles: close cached orig + swap, remove the on-disk
		// swap, then re-open both against the now-live scheme.
		mock.EXPECT().Close().Return(nil).Times(2)
		f.scheme.(*testScheme).removeFunc = func(name string) error { return nil }
		expectInitSwap(mock, defaultFileName, testFileInfo{}, defaultFileData)

		// Successful re-staging of the buffer into the freshly
		// opened swap, followed by the rest of the flush path.
		wait := expectCopyToSwap(f, mock, myString)
		defer wait()

		// flush's tail: close orig + swap before rename, then reopen
		// the files for continued editing.
		mock.EXPECT().Close().Return(nil).Times(2)
		expectInitSwap(mock, defaultFileName, testFileInfo{}, defaultFileData)

		assert.NoError(t, awaitFlushErr(f.Flush(context.Background())))
	})

	// If a recoverFiles attempt fails partway (e.g. the scheme is
	// still flaky on the user's first post-reconnect :write so
	// initFiles errors), the file used to get stuck reporting
	// ErrFileIsNotWritable on every subsequent :write because
	// f.swap was left nil. Verify that a later :write retries
	// recovery once the scheme is healthy and succeeds.
	t.Run("retries recovery on next Flush after partial recovery failure", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, buf := newBuffer(t, ctrl)
		myString := "edited under flaky link\n"
		dropErr := errors.New("connection reset by peer")

		// Worker fails while transport is down, sets delayedError.
		mock.EXPECT().Truncate(gomock.Eq(int64(0))).Return(dropErr)
		buf.InsertString(term.Coordinates{}, myString)
		f.wg.Wait()

		// First flush after a flaky reconnect: the replay swap copy
		// fails again (stale fd), recoverFiles tries to re-open and
		// also fails because the scheme is still partially down.
		mock.EXPECT().Truncate(gomock.Eq(int64(0))).Return(dropErr)
		mock.EXPECT().Close().Return(nil).Times(2)

		stillDown := errors.New("connection refused")
		ts := f.scheme.(*testScheme)
		ts.openFunc = func(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
			return nil, stillDown
		}

		err := awaitFlushErr(f.Flush(context.Background()))
		require.Error(t, err)
		// State after partial recovery: swap is nil, readOnly is
		// false (we never observed a permission denied), so the
		// file is in the "stuck" state we want to test.
		require.Nil(t, f.swap)
		require.False(t, f.readOnly)

		// Second flush after the scheme is fully back up: must
		// retry recovery rather than short-circuit on
		// ErrFileIsNotWritable. Restore openFunc so initFiles
		// works, expect the recover-init swap setup, and then the
		// rest of the flush path.
		ts.openFunc = func(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
			return mock, nil
		}
		ts.removeFunc = func(name string) error { return nil }
		// recoverFiles → initFiles → initSwap. f.orig was nil after
		// the failed recovery so no Close calls precede this run.
		expectInitSwap(mock, defaultFileName, testFileInfo{}, defaultFileData)

		// Replay of delayedError-cleared copyFlushSwapFile happens
		// on the freshly opened swap.
		wait := expectCopyToSwap(f, mock, myString)
		defer wait()

		// flush tail: close orig + swap, rename, reopen.
		mock.EXPECT().Close().Return(nil).Times(2)
		expectInitSwap(mock, defaultFileName, testFileInfo{}, defaultFileData)

		assert.NoError(t, awaitFlushErr(f.Flush(context.Background())))
	})

	t.Run("if copy to swap fails, errors contains details of swap file", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, buf := newBuffer(t, ctrl)
		myString := "a"
		myError := errors.New("access super-denied")

		mock.EXPECT().Truncate(gomock.Eq(int64(0))).Return(myError)
		buf.InsertString(term.Coordinates{}, myString)
		f.wg.Wait()

		mock.EXPECT().Truncate(gomock.Eq(int64(0))).Return(myError)
		// Replay fails; recoverFiles attempts to re-open and fails
		// as well — the error returned to the user must still
		// reference the swap file name.
		mock.EXPECT().Close().Return(nil).Times(2)
		f.scheme.(*testScheme).openFunc = func(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
			return nil, myError
		}
		err := awaitFlushErr(f.Flush(context.Background()))
		require.Error(t, err)
		assert.Contains(t, err.Error(), defaultFileName)
	})

	t.Run("Undo/Redo should be captured and therefore copied to swap file", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, buf := newBuffer(t, ctrl)
		myString := "my string\n"

		// wait for all of them otherwise if test goroutine is slow
		// file could chose to skip one of the syncs
		wait := expectCopyToSwap(f, mock, myString)
		buf.InsertString(term.Coordinates{}, myString)
		wait()

		wait = expectCopyToSwap(f, mock, "")
		buf.Undo()
		wait()

		wait = expectCopyToSwap(f, mock, myString)
		buf.Redo()
		wait()
	})

	t.Run("upon Insert, it copies content to swap file", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, buf := newBuffer(t, ctrl)
		require.Equal(t, string(defaultFileData), buf.String())

		myString := "my string\n"
		wait := expectCopyToSwap(f, mock, myString)
		defer wait()

		buf.InsertString(term.Coordinates{}, myString)
	})

	t.Run("upon Insert, if file is not writeable, it does nothing", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		fbuf, _, buf := newBuffer(t, ctrl)
		fbuf.swap = nil
		buf.InsertString(term.Coordinates{}, "blah")
	})
}

func testFileBufferDelete(t *testing.T, newBuffer newBufferFunc) {
	t.Run("upon Delete, it copies content to swap file", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		f, mock, buf := newBuffer(t, ctrl)
		wait := expectCopyToSwapPrepare(f, mock)
		defer wait()

		mock.EXPECT().
			Write(gomock.Eq([]byte("\n"))).
			Return(1, nil)
		mock.EXPECT().Sync().Return(nil)
		buf.DeleteRow(0)
	})

	t.Run("upon Delete, if file is not writeable, it does nothing", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		fbuf, _, buf := newBuffer(t, ctrl)
		fbuf.swap = nil
		buf.DeleteRow(0)
	})
}

func TestNewFileBufferDelete(t *testing.T) {
	testFileBufferDelete(t, newInitializedTestFileBuffer)
}

func TestNewFileBufferInsert(t *testing.T) {
	testFileBufferInsert(t, newInitializedTestFileBuffer)
}

func TestRecoverFileBufferDelete(t *testing.T) {
	testFileBufferDelete(t, newRecoveredTestFileBuffer)
}

func TestRecoverFileBufferInsert(t *testing.T) {
	testFileBufferInsert(t, newRecoveredTestFileBuffer)
}

func TestFileMissingLastCopySwap(t *testing.T) {
	buf, file := newIntegrationTestCase(t, true)

	f, err := openFile(file.Name(), buf, "", false)
	require.NoError(t, err)
	defer f.Close()

	var builder strings.Builder
	builder.Write([]byte(sampleSnippet))

	for range 1000 {
		buf.WriteString("a")
		builder.WriteString("a")
	}
	require.NoError(t, awaitFlushErr(f.Flush(context.Background())))

	builder.Write([]byte("\n"))
	want := builder.String()
	actual, err := os.ReadFile(file.Name())
	require.NoError(t, awaitFlushErr(f.Flush(context.Background())))
	assert.Equal(t, want, string(actual))
}

// TestFileFlushConcurrentRejected verifies that a second Flush
// invoked while the first is in flight returns ErrFlushInProgress.
func TestFileFlushConcurrentRejected(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	f, err := openFile(fileObj.Name(), buf, "", false)
	require.NoError(t, err)
	defer f.Close()

	// Hold the file's mutex via a long-running Flush by simulating
	// an in-flight goroutine. We can't easily intercept the scheme,
	// so we manually set the flushing flag and verify Flush returns
	// ErrFlushInProgress.
	f.mu.Lock()
	f.flushing = true
	f.mu.Unlock()

	ch, err := f.Flush(context.Background())
	assert.Nil(t, ch)
	assert.ErrorIs(t, err, ErrFlushInProgress)

	// Clean up so deferred Close doesn't deadlock.
	f.mu.Lock()
	f.flushing = false
	f.mu.Unlock()
}

// TestFileFlushCtxCancel verifies that cancelling ctx after Flush
// is started delivers context.Canceled on the result channel even
// if the underlying work completes successfully.
func TestFileFlushCtxCancel(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	f, err := openFile(fileObj.Name(), buf, "", false)
	require.NoError(t, err)
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so the goroutine observes ctx.Err() when it
	// finishes its work.
	cancel()
	ch, err := f.Flush(ctx)
	require.NoError(t, err)
	res := <-ch
	assert.ErrorIs(t, res, context.Canceled)
}

// TestFileFlushAsyncReturnsImmediately verifies that the (chan, err)
// pair is returned promptly. This is the freeze-regression guard: a
// slow scheme must not block the caller of Flush.
func TestFileFlushAsyncReturnsImmediately(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	f, err := openFile(fileObj.Name(), buf, "", false)
	require.NoError(t, err)
	defer f.Close()

	// Real local FS is fast, so we cannot directly observe the
	// async-ness — but we can at least verify the API contract:
	// Flush must return (non-nil ch, nil err) without blocking on
	// the channel.
	ch, err := f.Flush(context.Background())
	require.NoError(t, err)
	require.NotNil(t, ch)
	// Drain the channel so the goroutine cleans up.
	res := <-ch
	require.NoError(t, res)
}

// TestFileReloadAsync verifies that Reload follows the same async
// contract as Flush.
func TestFileReloadAsync(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	f, err := openFile(fileObj.Name(), buf, "", false)
	require.NoError(t, err)
	defer f.Close()

	ch, err := f.Reload(context.Background())
	require.NoError(t, err)
	require.NotNil(t, ch)
	res := <-ch
	require.NoError(t, res)
}

// TestFileFlushPublishesLastFlushBeforeRename verifies that by the
// time the underlying scheme observes the Rename call (which triggers
// the FS Write event), f.LastFlush() already returns the post-rename
// mtime. Otherwise an FS-watcher goroutine that wakes up during the
// rename can see lastFlush at its pre-flush value while Stat already
// reports the new mtime — falsely concluding the file changed
// externally and showing the "Discard your changes / Discard external
// changes" prompt for our own write.
func TestFileFlushPublishesLastFlushBeforeRename(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	workspaceURI, err := makeLocalURI(filepath.Dir(fileObj.Name()))
	require.NoError(t, err)
	inner, err := newTestFileScheme(workspaceURI)
	require.NoError(t, err)

	var (
		observed    time.Time
		swapPreRen  time.Time
		renameCount int
	)
	hook := &renameHookScheme{Scheme: inner}

	f, err := newFile(hook, fileObj.Name(), buf, "", false, inlineSchedule)
	require.NoError(t, err)
	defer f.Close()

	// First flush to establish a baseline lastFlush so we can
	// distinguish it from the post-rename mtime captured below.
	require.NoError(t, awaitFlushErr(f.Flush(context.Background())))
	prevLastFlush := f.LastFlush()

	// Sleep enough that the second flush's swap mtime is strictly
	// after the first flush's. Filesystems vary in mtime
	// granularity; 20ms is conservative for typical local FSes.
	time.Sleep(20 * time.Millisecond)
	buf.WriteString("more data")

	hook.onRename = func(oldpath, newpath string) {
		renameCount++
		// Capture the swap's mtime right before the underlying
		// Rename runs. After rename this is the orig file's mtime.
		if info, statErr := inner.Stat(oldpath); statErr == nil {
			swapPreRen = info.ModTime()
		}
		// LastFlush must already reflect the post-rename mtime.
		observed = f.LastFlush()
	}

	ch, err := f.Flush(context.Background())
	require.NoError(t, err)
	require.NoError(t, <-ch)

	require.Equal(t, 1, renameCount, "Rename hook must have fired")
	require.False(t, swapPreRen.IsZero(), "swap stat before rename failed")
	assert.True(t, observed.Equal(swapPreRen),
		"LastFlush observed at Rename time (%s) must match the swap's "+
			"pre-rename mtime (%s) so concurrent FS event handlers do "+
			"not see a stale lastFlush",
		observed, swapPreRen)
	assert.False(t, observed.Equal(prevLastFlush),
		"observed lastFlush %s must differ from the pre-flush value %s",
		observed, prevLastFlush)
}

// renameHookScheme wraps a schemeapi.Scheme so a test can observe and
// inject behaviour around Rename. All other methods delegate
// transparently.
type renameHookScheme struct {
	schemeapi.Scheme
	onRename    func(oldpath, newpath string)
	afterRename func(oldpath, newpath string) error
}

func (s *renameHookScheme) Rename(oldpath, newpath string) error {
	if s.onRename != nil {
		s.onRename(oldpath, newpath)
	}
	if err := s.Scheme.Rename(oldpath, newpath); err != nil {
		return err
	}
	if s.afterRename != nil {
		return s.afterRename(oldpath, newpath)
	}
	return nil
}

func TestFileFlushPostRenameModification(t *testing.T) {
	for _, tc := range []struct {
		name         string
		externalEdit bool
		pendingEdit  bool
	}{
		{name: "rename_changes_mtime_only"},
		{name: "external_edit_before_rename_returns", externalEdit: true},
		{name: "rename_with_pending_edit", pendingEdit: true},
		{name: "external_and_pending_edit", externalEdit: true, pendingEdit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf, fileObj := newIntegrationTestCase(t, true)
			workspaceURI, err := makeLocalURI(filepath.Dir(fileObj.Name()))
			require.NoError(t, err)
			inner, err := newTestFileScheme(workspaceURI)
			require.NoError(t, err)
			hook := &renameHookScheme{Scheme: inner}
			f, err := newFile(hook, fileObj.Name(), buf, "", false, inlineSchedule)
			require.NoError(t, err)
			defer f.Close()
			buf.WriteString("editor change")
			saved := buf.String()
			modified := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
			var duringRename time.Time
			hook.afterRename = func(_, path string) error {
				if tc.pendingEdit {
					buf.WriteString("pending user edit")
				}
				if tc.externalEdit {
					if err := os.WriteFile(path, []byte("external change"), 0600); err != nil {
						return err
					}
				}
				if err := os.Chtimes(path, modified, modified); err != nil {
					return err
				}
				duringRename = f.LastFlush()
				return nil
			}
			require.NoError(t, awaitFlushErr(f.Flush(context.Background())))
			info, err := os.Stat(fileObj.Name())
			require.NoError(t, err)
			require.True(t, info.ModTime().Equal(modified))
			data, err := os.ReadFile(fileObj.Name())
			require.NoError(t, err)
			wantBuffer := saved
			if tc.pendingEdit {
				wantBuffer += "pending user edit"
			}
			assert.Equal(t, wantBuffer, buf.String())
			assert.False(t, duringRename.Equal(modified))
			if tc.externalEdit {
				assert.Equal(t, "external change", string(data))
				assert.False(t, f.LastFlush().Equal(info.ModTime()),
					"external edit must not be published as our saved timestamp")
				assert.True(t, f.infoModTime.Equal(duringRename))
				buf.WriteString("pending edit")
				require.ErrorIs(t, awaitFlushErr(f.Flush(context.Background())), workspaceapi.ErrStaleData)
			} else {
				assert.Equal(t, saved+"\n", string(data))
				assert.True(t, f.LastFlush().Equal(info.ModTime()),
					"rename-only mtime change must still be recognized as our save")
			}
		})
	}
}

// statHookScheme wraps a schemeapi.Scheme so a test can observe Stat
// calls issued by the flush worker. All other methods delegate
// transparently.
type statHookScheme struct {
	schemeapi.Scheme
	onStat func(path string)
}

type readCountingFile struct {
	workspaceapi.File
	read *int
}

func (f readCountingFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	*f.read += n
	return n, err
}

// readCountingScheme totals the bytes read from one path so a test can assert
// how much of the saved file a flush pulls back off the wire.
type readCountingScheme struct {
	schemeapi.Scheme
	path string
	read int
}

func (s *readCountingScheme) OpenFile(
	name string, flag int, perm os.FileMode,
) (workspaceapi.File, error) {
	file, err := s.Scheme.OpenFile(name, flag, perm)
	if err != nil || name != s.path {
		return file, err
	}
	return readCountingFile{File: file, read: &s.read}, nil
}

// TestFileFlushReadsSavedFileOncePerSave pins the I/O cost of verifying that
// the reopened file is still ours. Re-staging the swap already reads the file,
// so a second verification pass would double the transfer of every save on a
// remote workspace.
func TestFileFlushReadsSavedFileOncePerSave(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	workspaceURI, err := makeLocalURI(filepath.Dir(fileObj.Name()))
	require.NoError(t, err)
	inner, err := newTestFileScheme(workspaceURI)
	require.NoError(t, err)
	counter := &readCountingScheme{Scheme: inner, path: fileObj.Name()}
	hook := &renameHookScheme{Scheme: counter}
	f, err := newFile(hook, fileObj.Name(), buf, "", false, inlineSchedule)
	require.NoError(t, err)
	defer f.Close()

	buf.WriteString("editor change")
	modified := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	// A rename that moves mtime is the case that has to be verified; without
	// the change there is nothing to check and no read to count.
	hook.afterRename = func(_, path string) error {
		return os.Chtimes(path, modified, modified)
	}

	counter.read = 0
	require.NoError(t, awaitFlushErr(f.Flush(context.Background())))

	info, err := os.Stat(fileObj.Name())
	require.NoError(t, err)
	require.True(t, info.ModTime().Equal(modified))
	require.True(t, f.LastFlush().Equal(modified),
		"an unchanged file must still be recognized as our save")
	assert.Equal(t, int(info.Size()), counter.read,
		"verifying the reopened file must reuse the read that re-stages the swap")
}

func (s *statHookScheme) Stat(path string) (os.FileInfo, error) {
	if s.onStat != nil {
		s.onStat(path)
	}
	return s.Scheme.Stat(path)
}

// TestFileFlushNewFilePublishesLastFlushAfterTouch verifies that when
// flush creates a brand-new file on disk (the O_CREATE|O_EXCL touch),
// lastFlush is published with the touched file's mtime before the
// flush proceeds to stage and rename the swap. Otherwise the FS
// watcher can dispatch the touch's Create event during the rest of
// the flush, observe a zero lastFlush with a dirty buffer, and show
// the "file was just created on disk / discard your changes" prompt
// for our own write.
func TestFileFlushNewFilePublishesLastFlushAfterTouch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "newfile.txt")
	workspaceURI, err := makeLocalURI(dir)
	require.NoError(t, err)
	inner, err := newTestFileScheme(workspaceURI)
	require.NoError(t, err)

	buf := cell.NewBuffer()
	buf.WriteString("data\n")

	hook := &statHookScheme{Scheme: inner}
	f, err := newFile(hook, target, buf, "", false, inlineSchedule)
	require.NoError(t, err)
	defer f.Close()

	var (
		captured   bool
		observed   time.Time
		touchMtime time.Time
	)
	// The touch is the first moment target exists on disk; the first
	// scheme call afterwards is a Stat (of the swap file). Capture
	// LastFlush there — the window in which a watcher could already
	// be dispatching the Create event.
	hook.onStat = func(string) {
		if captured {
			return
		}
		info, statErr := inner.Stat(target)
		if statErr != nil {
			return
		}
		captured = true
		touchMtime = info.ModTime()
		observed = f.LastFlush()
	}

	require.NoError(t, awaitFlushErr(f.Flush(context.Background())))

	require.True(t, captured,
		"no scheme.Stat observed after the touch created %s", target)
	require.False(t, observed.IsZero(),
		"lastFlush must be published as soon as the touch makes the "+
			"file observable on disk")
	assert.True(t, observed.Equal(touchMtime),
		"lastFlush at first post-touch Stat (%s) must match the touched "+
			"file's mtime (%s) so the watcher's self-write suppression "+
			"covers the Create event", observed, touchMtime)
}

// TestFileReloadBufferMutationOnEventLoop guards the invariant
// that file.reload's cell.Buffer mutations and the lastFlush
// update run on the host event loop, not on the async worker
// goroutine. Buffer subscribers (notably text.editorFlusherCloser)
// touch UI-owned state from OnDidEdit; if reload mutates the
// buffer from a background goroutine those callbacks race with
// the event loop.
func TestFileReloadBufferMutationOnEventLoop(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	workspaceURI, err := makeLocalURI(filepath.Dir(fileObj.Name()))
	require.NoError(t, err)
	scheme, err := newTestFileScheme(workspaceURI)
	require.NoError(t, err)

	// Worker goroutine ID is captured the moment the worker enters
	// scheduleNextTick; the scheduler asserts every buffer mutation
	// runs on a different goroutine (i.e. the simulated event loop).
	var (
		mu             sync.Mutex
		workerGID      uint64
		schedulerGID   uint64
		mutationGIDs   []uint64
		schedulerCalls int
	)

	loopCh := make(chan func(), 4)
	loopDone := make(chan struct{})
	go func() {
		schedulerGID = goroutineID()
		close(loopDone)
		for fn := range loopCh {
			fn()
		}
	}()
	<-loopDone

	sched := func(fn func()) bool {
		mu.Lock()
		workerGID = goroutineID()
		schedulerCalls++
		mu.Unlock()
		loopCh <- fn
		return true
	}

	f, err := newFile(scheme, fileObj.Name(), buf, "", false, sched)
	require.NoError(t, err)
	defer func() {
		close(loopCh)
		_ = f.Close()
	}()

	sub := &reloadGIDSubscriber{
		onEdit: func() {
			mu.Lock()
			mutationGIDs = append(mutationGIDs, goroutineID())
			mu.Unlock()
		},
	}
	buf.Subscribe(sub)

	// First reload kicks the buffer-mutation scheduler at least once.
	ch, err := f.Reload(context.Background())
	require.NoError(t, err)
	require.NoError(t, <-ch)

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, schedulerCalls, 1,
		"reload must schedule buffer mutations onto the event loop")
	require.NotEmpty(t, mutationGIDs,
		"reload should emit at least one OnWillEdit/OnDidEdit")
	for _, gid := range mutationGIDs {
		require.NotEqual(t, workerGID, gid,
			"buffer mutation ran on the async worker goroutine "+
				"instead of the scheduled event-loop goroutine; "+
				"subscribers race with the host event loop")
		require.Equal(t, schedulerGID, gid,
			"buffer mutation ran on an unexpected goroutine "+
				"(expected the scheduler/event-loop goroutine)")
	}
}

type reloadGIDSubscriber struct {
	onEdit func()
}

func (s *reloadGIDSubscriber) OnWillEdit(
	_ context.Context, _, _ term.Coordinates, _ string,
) {
	s.onEdit()
}

func (s *reloadGIDSubscriber) OnDidEdit(
	_ context.Context, _, _ term.Coordinates, _ string,
) {
	s.onEdit()
}

// TestFileCloseDoesNotWaitForInFlightReload guards the host
// event-loop close path: Close must not block waiting on a Reload
// worker that is itself parked on the host loop via scheduleNextTick.
// Production hit this when a filesystem rename event reached
// RemoveTab from ide/events.go handleFSChange.
func TestFileCloseDoesNotWaitForInFlightReload(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	workspaceURI, err := makeLocalURI(filepath.Dir(fileObj.Name()))
	require.NoError(t, err)
	scheme, err := newTestFileScheme(workspaceURI)
	require.NoError(t, err)

	// The queue is never pumped, simulating a host loop that has
	// stopped pumping because it is blocked entering Close.
	queue := make(chan func(), 8)
	queueSchedule := func(fn func()) bool {
		queue <- fn
		return true
	}

	f, err := newFile(scheme, fileObj.Name(), buf, "", false, queueSchedule)
	require.NoError(t, err)

	ch, err := f.Reload(context.Background())
	require.NoError(t, err)

	// Wait until the worker has reached scheduleNextTick so Close
	// races against a parked worker, not one still doing disk I/O.
	select {
	case <-queue:
	case <-time.After(2 * time.Second):
		t.Fatal("reload never reached scheduleNextTick")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- f.Close()
	}()

	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("file.Close blocked waiting for in-flight reload " +
			"worker that needs the host event loop")
	}

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("reload result channel never closed after Close")
	}
}

// TestFileCloseReloadOrdering exercises the Close/Reload ownership
// handoff across the interleavings of the two operations. reload
// closes and reopens f.orig/f.swap and rewrites f.fileName via
// initFiles while Close tears the same fields down, so ownership
// must be handed off, never shared: Close returns immediately while
// the worker owns the descriptors (its I/O can be remote and slow or
// wedged, and Close may run on the event loop) and the worker
// inherits the teardown; when Close wins instead, the worker must
// refuse the swap and leave the torn-down state alone.
//
// Every case ends with the same postconditions: the teardown ran
// exactly once (swap file removed, extra Close reports the file
// gone), no copy-swap catch-up survives, and reload contents only
// reach the buffer when the reload completed before the close.
func TestFileCloseReloadOrdering(t *testing.T) {
	tests := []fileCloseReloadCase{
		{
			name:            "reload completes then close tears down",
			closeAt:         closeAfterReload,
			wantBufReloaded: true,
		},
		{
			// Reload must not resurrect descriptors or recreate
			// the swap file after teardown (beginFileSwap refuses),
			// and a later flush must keep reporting the file as
			// not writable.
			name:          "close before reload refuses the swap",
			closeAt:       closeBeforeReload,
			flushAfterAll: true,
		},
		{
			// The old descriptors are already closed but the old
			// swap file is still on disk when Close lands.
			name:    "close while reload removes the old swap",
			gate:    gateRemoveOldSwap,
			closeAt: closeDuringGate,
		},
		{
			// Close lands inside initFiles, right before f.orig,
			// f.swap and f.fileName are rewritten.
			name:    "close while reload reopens orig",
			gate:    gateReopenOrig,
			closeAt: closeDuringGate,
		},
		{
			// Close lands after orig was reopened but before the
			// swap file exists again.
			name:    "close while reload recreates the swap",
			gate:    gateReopenSwap,
			closeAt: closeDuringGate,
		},
		{
			// The swap fails after Close already handed the
			// teardown to the worker: the error must still reach
			// the caller and the fresh swap file must not leak.
			name:          "close while reload fails on a deleted file",
			gate:          gateReopenOrig,
			closeAt:       closeDuringGate,
			deleteOnDisk:  true,
			wantReloadErr: "doesn't exist on disk",
		},
		{
			// The error path of reloadFiles must return descriptor
			// ownership so a later Close tears down inline.
			name:          "reload fails then close tears down",
			closeAt:       closeAfterReload,
			deleteOnDisk:  true,
			wantReloadErr: "doesn't exist on disk",
		},
		{
			// The swap completed and the worker parked on
			// scheduleNextTick with a host loop that stopped
			// pumping (it is blocked entering Close): Close must
			// tear down inline and unpark the worker via closedCh.
			name:    "close while worker parked on the host loop",
			gate:    gateParkedOnLoop,
			closeAt: closeDuringGate,
		},
		{
			// The host loop pumps the parked callback only after
			// Close already ran: the callback must observe closed
			// and leave the buffer alone.
			name:           "loop pumps the stale callback after close",
			gate:           gateParkedOnLoop,
			closeAt:        closeDuringGate,
			pumpAfterClose: true,
		},
		{
			// A second Close while the worker still owns the
			// descriptors must not block or tear down twice.
			name:             "double close while the worker owns the swap",
			gate:             gateReopenOrig,
			closeAt:          closeDuringGate,
			doubleCloseGated: true,
		},
		{
			// An edit landing after Close, mid-swap, must be
			// discarded (reloading=true) and must not enqueue a
			// copy-swap catch-up that the teardown would race.
			name:           "edit after close is discarded mid-swap",
			gate:           gateReopenOrig,
			closeAt:        closeDuringGate,
			editWhileGated: true,
		},
		{
			// The startAsync gate stays held for the whole swap:
			// concurrent flushes short-circuit instead of racing
			// the worker for the descriptors.
			name:            "flush rejected while the worker owns the swap",
			gate:            gateReopenOrig,
			closeAt:         closeDuringGate,
			flushWhileGated: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runFileCloseReloadCase(t, tc)
		})
	}
}

// openFileHookScheme wraps a schemeapi.Scheme so a test can observe
// and inject behaviour around OpenFile. All other methods delegate
// transparently.
type openFileHookScheme struct {
	schemeapi.Scheme
	onOpenFile func(name string)
}

func (s *openFileHookScheme) OpenFile(
	name string, flag int, perm os.FileMode,
) (workspaceapi.File, error) {
	if s.onOpenFile != nil {
		s.onOpenFile(name)
	}
	return s.Scheme.OpenFile(name, flag, perm)
}

// TestFileCloseWaitsForInFlightFlush guards the complementary
// invariant to TestFileCloseDoesNotWaitForInFlightReload: tearing
// down f.orig / f.swap mid-rename would leave the on-disk file
// half-rewritten, so Close must wait for an in-flight flush.
func TestFileCloseWaitsForInFlightFlush(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	workspaceURI, err := makeLocalURI(filepath.Dir(fileObj.Name()))
	require.NoError(t, err)
	inner, err := newTestFileScheme(workspaceURI)
	require.NoError(t, err)

	renameEntered := make(chan struct{})
	renameGate := make(chan struct{})
	hook := &renameHookScheme{Scheme: inner, onRename: func(string, string) {
		close(renameEntered)
		<-renameGate
	}}

	f, err := newFile(hook, fileObj.Name(), buf, "", false, inlineSchedule)
	require.NoError(t, err)

	flushCh, err := f.Flush(context.Background())
	require.NoError(t, err)

	// Only issue Close once the flush is provably mid-rename, so the
	// Close/Flush overlap is guaranteed by construction regardless of
	// goroutine scheduling.
	select {
	case <-renameEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("flush never reached the scheme Rename")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- f.Close()
	}()

	select {
	case <-closeDone:
		t.Fatal("file.Close returned while Flush was still running; " +
			"flush may have been left mid-rename and the on-disk " +
			"file could be in a half-written state")
	case <-time.After(100 * time.Millisecond):
	}

	close(renameGate)

	select {
	case <-flushCh:
	case <-time.After(2 * time.Second):
		t.Fatal("flush never completed after rename gate released")
	}
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked indefinitely after flush completed")
	}
}

// TestFileFlushRejectedDuringInFlightReload guards the shared
// startAsync gate: while a Reload is in flight (f.flushing=true),
// any concurrent Flush/ForceFlush/Reload must short-circuit with
// ErrFlushInProgress instead of racing the worker.
func TestFileFlushRejectedDuringInFlightReload(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	f, err := openFile(fileObj.Name(), buf, "", false)
	require.NoError(t, err)
	defer f.Close()

	f.mu.Lock()
	f.flushing = true
	f.mu.Unlock()

	ch, err := f.Flush(context.Background())
	assert.Nil(t, ch)
	assert.ErrorIs(t, err, ErrFlushInProgress)

	ch, err = f.ForceFlush(context.Background())
	assert.Nil(t, ch)
	assert.ErrorIs(t, err, ErrFlushInProgress)

	ch, err = f.Reload(context.Background())
	assert.Nil(t, ch)
	assert.ErrorIs(t, err, ErrFlushInProgress)

	f.mu.Lock()
	f.flushing = false
	f.mu.Unlock()
}

// TestFileEditsDuringFlushReachDiskViaCatchUp guards the
// suppressCopySwap=false branch of startAsync: edits that land
// while Flush owns the swap file must be captured by pendingEdits
// and re-staged via the post-work runCopySwap pass so they reach
// the next swap file. Without this catch-up an edit racing with
// the rename would be silently dropped.
func TestFileEditsDuringFlushReachDiskViaCatchUp(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	workspaceURI, err := makeLocalURI(filepath.Dir(fileObj.Name()))
	require.NoError(t, err)
	inner, err := newTestFileScheme(workspaceURI)
	require.NoError(t, err)

	// renameGate blocks the underlying Rename until the test has
	// applied a buffer edit. That edit lands while f.flushing is
	// true, so OnDidEdit must record it in pendingEdits and the
	// post-work catch-up must stage it onto a fresh swap file.
	renameGate := make(chan struct{})
	editApplied := make(chan struct{})
	hook := &renameHookScheme{Scheme: inner}

	f, err := newFile(hook, fileObj.Name(), buf, "", false, inlineSchedule)
	require.NoError(t, err)
	defer f.Close()

	// First Flush establishes a clean baseline on disk; no hook
	// installed yet so the rename runs to completion immediately.
	require.NoError(t, awaitFlushErr(f.Flush(context.Background())))
	hook.onRename = func(_, _ string) {
		<-editApplied
		<-renameGate
	}

	// Replace the buffer with a known initial value, then start
	// the Flush that will be intercepted at Rename.
	buf.Reset()
	buf.WriteString("baseline")
	flushCh, err := f.Flush(context.Background())
	require.NoError(t, err)

	// Spin until startAsync has marked flushing=true; from this
	// point any edit must be recorded in pendingEdits.
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.flushing
	}, time.Second, time.Millisecond, "Flush goroutine never set flushing=true")

	// Apply a concurrent edit. OnDidEdit must observe flushing=true
	// and stash the edit in pendingEdits without enqueuing a
	// copy-swap (the worker is locked out of the swap file).
	buf.WriteString(" + concurrent edit")
	close(editApplied)

	// Allow the Rename to proceed and the Flush goroutine to
	// observe pendingEdits and enqueue the catch-up.
	close(renameGate)
	require.NoError(t, <-flushCh)

	// Block until the catch-up copy-swap pass has finished writing
	// the swap file.
	f.wg.Wait()

	// A subsequent successful Flush renames the catch-up swap onto
	// orig so we can read the final on-disk contents.
	require.NoError(t, awaitFlushErr(f.Flush(context.Background())))

	got, err := os.ReadFile(fileObj.Name())
	require.NoError(t, err)
	assert.Equal(t, "baseline + concurrent edit\n", string(got),
		"edit applied during in-flight Flush must reach disk via "+
			"the post-flush catch-up copy-swap pass")
}

// TestFileEditsDuringReloadAreDiscarded guards the
// suppressCopySwap=true branch of startAsync and reload's
// f.reloading short-circuit: edits arriving during a reload must
// be dropped (reload is overwriting the buffer from disk, so any
// intervening user edit is by definition stale), and the
// pendingEdits-driven catch-up must NOT run because reload
// destroys the swap file rather than coordinating writes against
// it.
func TestFileEditsDuringReloadAreDiscarded(t *testing.T) {
	buf, fileObj := newIntegrationTestCase(t, true)
	workspaceURI, err := makeLocalURI(filepath.Dir(fileObj.Name()))
	require.NoError(t, err)
	inner, err := newTestFileScheme(workspaceURI)
	require.NoError(t, err)

	// reload's first interaction with the scheme is to Remove the
	// old swap file. We use that as the "reload has engaged"
	// signal: reloadEntered fires when reload's goroutine reaches
	// Remove (and therefore has already set f.reloading=true).
	reloadEntered := make(chan struct{})
	removeGate := make(chan struct{})
	hook := &removeHookScheme{
		Scheme: inner,
		onRemove: func(string) {
			select {
			case <-reloadEntered:
			default:
				close(reloadEntered)
			}
			<-removeGate
		},
	}

	f, err := newFile(hook, fileObj.Name(), buf, "", false, inlineSchedule)
	require.NoError(t, err)
	defer f.Close()

	// Establish a known on-disk baseline that reload will restore.
	require.NoError(t, os.WriteFile(fileObj.Name(),
		[]byte("on-disk baseline\n"), 0o600))

	// Drive a Reload in the background.
	reloadCh, err := f.Reload(context.Background())
	require.NoError(t, err)

	// Wait for reload to enter the swap-Remove scheme call;
	// f.reloading=true was set before that, on the same goroutine,
	// so the following edit is guaranteed to be observed by
	// OnDidEdit with reloading=true.
	select {
	case <-reloadEntered:
	case <-time.After(time.Second):
		t.Fatal("reload goroutine never reached the swap-Remove hook")
	}

	// Apply an edit mid-reload. OnDidEdit sees f.reloading=true,
	// balances the WaitGroup, and returns without setting
	// pendingEdits or enqueuing copy-swap work.
	buf.WriteString(" + stale edit racing reload")

	// Allow reload to finish.
	close(removeGate)
	require.NoError(t, <-reloadCh)

	// Reload completed; pendingEdits must be false (no catch-up to
	// run) and the buffer must reflect the on-disk contents.
	f.mu.Lock()
	pendingEdits := f.pendingEdits
	f.mu.Unlock()
	assert.False(t, pendingEdits,
		"reload must not flag pendingEdits; suppressCopySwap=true "+
			"means there is no catch-up copy-swap pass")
	// cell.Buffer.String() may omit a trailing newline depending on
	// the view; the key invariant is that the racing edit must NOT
	// be present in the buffer.
	assert.Equal(t, "on-disk baseline",
		strings.TrimRight(buf.String(), "\n"),
		"reload must overwrite the buffer with on-disk contents, "+
			"discarding any concurrent edits")
}

// TestFileFlushReloadStress exercises the startAsync gate under
// real concurrency: many goroutines race Flush, ForceFlush and
// Reload against a single file. Edits are deliberately not driven
// concurrently because cell.Buffer expects a single writer (the
// host event loop) and the file's worker reads the buffer from a
// background goroutine; racing edits with flush would surface a
// real but orthogonal cell.Buffer access pattern bug, not the
// startAsync gate we are exercising here. Failure modes the
// -race detector should catch include:
//   - simultaneous Flush+Reload mutating swap/orig without the
//     flushing gate;
//   - reload short-circuiting copy-swap work that flush expected
//     to drain via f.wg.Wait.
//
// The test asserts no panics, no data races (under -race), and
// that every concurrent attempt either succeeds or fails with the
// flushing-gate sentinel ErrFlushInProgress — never an unexpected
// error.
func TestFileFlushReloadStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test skipped in -short mode")
	}
	buf, fileObj := newIntegrationTestCase(t, true)
	f, err := openFile(fileObj.Name(), buf, "", false)
	require.NoError(t, err)
	defer f.Close()

	const (
		workers    = 8
		iterations = 25
	)

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(seed) + 1))
			for range iterations {
				switch rng.Intn(3) {
				case 0:
					ch, ferr := f.Flush(context.Background())
					if ferr == nil {
						_ = <-ch
					} else if !errors.Is(ferr, ErrFlushInProgress) {
						t.Errorf("unexpected Flush error: %v", ferr)
						return
					}
				case 1:
					ch, ferr := f.ForceFlush(context.Background())
					if ferr == nil {
						_ = <-ch
					} else if !errors.Is(ferr, ErrFlushInProgress) {
						t.Errorf("unexpected ForceFlush error: %v", ferr)
						return
					}
				case 2:
					ch, ferr := f.Reload(context.Background())
					if ferr == nil {
						_ = <-ch
					} else if !errors.Is(ferr, ErrFlushInProgress) {
						t.Errorf("unexpected Reload error: %v", ferr)
						return
					}
				}
			}
		}(w)
	}
	wg.Wait()

	// Drain any pending copy-swap pass that the last edit kicked
	// off so Close does not race with the worker.
	f.wg.Wait()
}

// removeHookScheme wraps a schemeapi.Scheme so a test can observe
// and inject behaviour around Remove. All other methods delegate
// transparently.
type removeHookScheme struct {
	schemeapi.Scheme
	onRemove func(name string)
}

func (s *removeHookScheme) Remove(name string) error {
	if s.onRemove != nil {
		s.onRemove(name)
	}
	return s.Scheme.Remove(name)
}

// goroutineID returns the calling goroutine's ID by parsing the
// runtime stack header. Test-only; the format is stable in modern
// Go releases.
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	line := buf[:n]
	// "goroutine 12345 [running]:\n..."
	const prefix = "goroutine "
	if !strings.HasPrefix(string(line), prefix) {
		return 0
	}
	rest := string(line[len(prefix):])
	end := strings.IndexByte(rest, ' ')
	if end < 0 {
		return 0
	}
	id, err := strconv.ParseUint(rest[:end], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// inlineSchedule is a synchronous workspace.ScheduleNextTick stub
// that runs fn on the calling goroutine. Test-only: production code
// must use the host event-loop scheduler so reload's buffer
// mutations do not run on a worker goroutine.
func inlineSchedule(fn func()) bool {
	fn()
	return true
}

// reloadGatePoint selects where the reload worker is trapped so a
// close/reload ordering test can interleave Close deterministically.
type reloadGatePoint int

const (
	// gateNone lets the reload run to completion ungated.
	gateNone reloadGatePoint = iota
	// gateRemoveOldSwap traps the worker at the scheme.Remove of
	// the old swap file, after the old descriptors were closed but
	// before initFiles reopens anything.
	gateRemoveOldSwap
	// gateReopenOrig traps the worker inside initFiles at the
	// OpenFile that reopens f.orig.
	gateReopenOrig
	// gateReopenSwap traps the worker inside initSwap at the
	// OpenFile that recreates the swap file, after orig was already
	// reopened.
	gateReopenSwap
	// gateParkedOnLoop lets the descriptor swap complete and traps
	// the worker parked on scheduleNextTick with a host loop that
	// never pumps the callback.
	gateParkedOnLoop
)

// closePoint selects when Close runs relative to the Reload.
type closePoint int

const (
	closeDuringGate closePoint = iota
	closeBeforeReload
	closeAfterReload
)

type fileCloseReloadCase struct {
	name    string
	gate    reloadGatePoint
	closeAt closePoint
	// deleteOnDisk removes the on-disk file before the Reload so
	// reloadFiles fails after the old descriptors are gone.
	deleteOnDisk bool
	// doubleCloseGated issues a second Close while the worker still
	// owns the descriptors.
	doubleCloseGated bool
	// editWhileGated applies a buffer edit after Close, while the
	// worker is still gated mid-swap.
	editWhileGated bool
	// flushWhileGated attempts a Flush while the worker owns the
	// descriptors; it must be rejected by the startAsync gate.
	flushWhileGated bool
	// pumpAfterClose runs the parked scheduleNextTick callback
	// after Close returned (gateParkedOnLoop only).
	pumpAfterClose bool
	// flushAfterAll attempts a Flush once everything is closed.
	flushAfterAll   bool
	wantReloadErr   string
	wantBufReloaded bool
}

// reloadSentinel is written to disk before the Reload so the final
// buffer assertion can tell whether the reload contents ever reached
// the buffer.
const reloadSentinel = "reloaded from disk"

func runFileCloseReloadCase(t *testing.T, tc fileCloseReloadCase) {
	buf, fileObj := newIntegrationTestCase(t, true)
	workspaceURI, err := makeLocalURI(filepath.Dir(fileObj.Name()))
	require.NoError(t, err)
	inner, err := newTestFileScheme(workspaceURI)
	require.NoError(t, err)

	// The gate traps the reload worker at the chosen scheme call.
	// remaining counts armed calls: the gate fires when the counter
	// reaches zero and stays disarmed afterwards, because the
	// inherited teardown re-enters the same scheme methods.
	var (
		remaining atomic.Int32
		entered   = make(chan struct{})
		released  = make(chan struct{})
	)
	gateFn := func() {
		if remaining.Load() <= 0 {
			return
		}
		if remaining.Add(-1) != 0 {
			return
		}
		close(entered)
		<-released
	}

	var scheme schemeapi.Scheme = inner
	armCount := int32(0)
	switch tc.gate {
	case gateRemoveOldSwap:
		scheme = &removeHookScheme{Scheme: inner,
			onRemove: func(string) { gateFn() }}
		armCount = 1
	case gateReopenOrig:
		scheme = &openFileHookScheme{Scheme: inner,
			onOpenFile: func(string) { gateFn() }}
		armCount = 1
	case gateReopenSwap:
		// The first armed OpenFile reopens orig and passes through;
		// the second recreates the swap file and gates.
		scheme = &openFileHookScheme{Scheme: inner,
			onOpenFile: func(string) { gateFn() }}
		armCount = 2
	}

	sched := inlineSchedule
	loopQueue := make(chan func(), 8)
	if tc.gate == gateParkedOnLoop {
		sched = func(fn func()) bool {
			loopQueue <- fn
			return true
		}
	}

	f, err := newFile(scheme, fileObj.Name(), buf, "", false, sched)
	require.NoError(t, err)

	if tc.deleteOnDisk {
		require.NoError(t, os.Remove(fileObj.Name()))
	} else {
		require.NoError(t, os.WriteFile(
			fileObj.Name(), []byte(reloadSentinel+"\n"), 0o600))
	}

	if tc.closeAt == closeBeforeReload {
		require.NoError(t, closeFileWithin(t, f,
			"Close before reload must not block"))
	}

	remaining.Store(armCount)
	ch, err := f.Reload(context.Background())
	require.NoError(t, err)

	var parked func()
	switch tc.gate {
	case gateNone:
	case gateParkedOnLoop:
		select {
		case parked = <-loopQueue:
		case <-time.After(2 * time.Second):
			t.Fatal("reload never reached scheduleNextTick")
		}
	default:
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("reload never reached the gated scheme call")
		}
	}

	if tc.closeAt == closeDuringGate {
		// Blocking here would freeze the event loop behind remote
		// reload I/O or wedge the off-loop workspace teardown.
		require.NoError(t, closeFileWithin(t, f,
			"Close blocked behind an in-flight reload swap"))
		if tc.doubleCloseGated {
			err := closeFileWithin(t, f,
				"second Close blocked behind an in-flight reload swap")
			require.ErrorContains(t, err, "uninitialized")
		}
		if tc.editWhileGated {
			// reloading=true is still set on the worker: the edit
			// must be discarded, not staged for a catch-up.
			buf.WriteString(" + edit racing close")
		}
		if tc.flushWhileGated {
			fch, ferr := f.Flush(context.Background())
			assert.Nil(t, fch)
			assert.ErrorIs(t, ferr, ErrFlushInProgress)
		}
	}

	if armCount > 0 {
		close(released)
	}

	select {
	case res := <-ch:
		if tc.wantReloadErr == "" {
			require.NoError(t, res)
		} else {
			require.ErrorContains(t, res, tc.wantReloadErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload result never delivered")
	}

	if tc.pumpAfterClose {
		parked()
	}

	if tc.closeAt == closeAfterReload {
		require.NoError(t, closeFileWithin(t, f,
			"Close after reload completed must not block"))
	}

	if tc.wantBufReloaded {
		assert.Equal(t, reloadSentinel,
			strings.TrimRight(buf.String(), "\n"),
			"reload contents must reach the buffer")
	} else {
		assert.NotContains(t, buf.String(), reloadSentinel,
			"reload contents must not reach a closed buffer")
	}

	// Whoever ended up owning the descriptors must have run the
	// teardown exactly once: the swap file is gone, no copy-swap
	// catch-up survives, and an extra Close reports the file gone.
	_, swapPath := swapFileName("", fileObj.Name())
	_, serr := inner.Stat(swapPath)
	require.True(t, os.IsNotExist(serr),
		"swap file must be removed by the teardown, got %v", serr)

	f.mu.Lock()
	pendingEdits := f.pendingEdits
	f.mu.Unlock()
	assert.False(t, pendingEdits,
		"no copy-swap catch-up may survive the close/reload ordering")

	err = closeFileWithin(t, f, "extra Close must not block")
	require.ErrorContains(t, err, "uninitialized",
		"extra Close must report the file already closed")

	if tc.flushAfterAll {
		assert.ErrorIs(t,
			awaitFlushErr(f.Flush(context.Background())),
			workspaceapi.ErrFileIsNotWritable,
			"flush after close must report the file as not writable")
	}
}

// closeFileWithin runs f.Close on its own goroutine and fails the
// test if it does not return within 2s: a blocked Close would freeze
// the event loop or wedge the workspace teardown.
func closeFileWithin(t *testing.T, f *file, msg string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f.Close() }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
		return nil
	}
}
