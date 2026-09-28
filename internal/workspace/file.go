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
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	multierr "github.com/ernestrc/go-multierror"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/debug"
)

const (
	defaultFileMode os.FileMode = 0644
)

// SwapFileExtensionName is the extension Rune appends to its per-file
// swap files. It is deliberately distinct from Vim's ".swp" so the two
// never collide and mistake each other's swap files for their own.
const SwapFileExtensionName = ".rswp"

var _ FlusherCloser = (*file)(nil)

// file implements the sync (swap file) logic
type file struct {
	// wg tracks in-flight copy-swap work scheduled by edit
	// subscribers (OnWillEdit Add / OnDidEdit or worker Done).
	//
	// flushWG and reloadWG are kept separate so Close can wait for
	// in-flight flush (mid-rename disk writes would corrupt the
	// file) without waiting for reload (whose worker parks on the
	// host event loop via scheduleNextTick and would deadlock
	// Close when both run on the same loop).
	wg       sync.WaitGroup
	flushWG  sync.WaitGroup
	reloadWG sync.WaitGroup

	ch      chan struct{}
	mu      sync.Mutex
	content string
	scheme  schemeapi.Scheme

	buf  *cell.Buffer
	view UnixFileView
	// reloading is flipped by the reload worker and read by
	// OnDidEdit on the host event loop, so it is atomic.
	reloading       atomic.Bool
	swapDir         string
	swapFileName    string
	fileName        string
	readOnly        bool
	infoModTime     time.Time
	swapInfoModTime time.Time
	// swapHash follows the same serialized ownership as swap, not content:
	// edits may update content while a flush owns the saved bytes.
	swapHash     [sha256.Size]byte
	orig, swap   workspaceapi.File
	delayedError error
	unflushed    bool
	lastFlush    time.Time
	flushing     bool
	pendingEdits bool
	// closed and reloadOwnsFiles form the close handoff for the
	// descriptor state (orig/swap/fileName and friends):
	// beginFileSwap hands ownership to the reload worker,
	// endFileSwap returns it. Whoever holds ownership when closed
	// flips runs teardown exactly once — Close never waits for the
	// worker, whose disk I/O can be remote, slow or wedged.
	closed          bool
	reloadOwnsFiles bool
	closedOnce      sync.Once
	closedCh        chan struct{}
	// scheduleNextTick dispatches buffer-mutation work for async
	// operations (reload) back onto the host event loop.
	scheduleNextTick func(func()) bool
}

func newFile(
	p schemeapi.Scheme, path string, buf *cell.Buffer, swapDir, swapFilePath string,
	readOnly bool, scheduleNextTick func(func()) bool,
) (*file, error) {
	if scheduleNextTick == nil {
		return nil, errors.New("workspace.newFile: scheduleNextTick must not be nil")
	}
	ret := new(file)
	ret.scheme = p
	ret.scheduleNextTick = scheduleNextTick
	ret.closedCh = make(chan struct{})
	ret.swapFileName = swapFilePath
	ret.swapDir = swapDir

	err := ret.init(path, buf, swapDir, readOnly)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func newFileRecover(
	p schemeapi.Scheme, path, swapFilePath string, buf *cell.Buffer,
	force bool, scheduleNextTick func(func()) bool,
) (
	*file, error,
) {
	if scheduleNextTick == nil {
		return nil, errors.New("workspace.newFileRecover: scheduleNextTick must not be nil")
	}
	ret := new(file)
	ret.scheme = p
	ret.scheduleNextTick = scheduleNextTick
	ret.closedCh = make(chan struct{})

	err := ret.initRecover(path, swapFilePath, buf, force)
	if err != nil {
		return nil, err
	}
	return ret, err
}

func (f *file) initSwapFile(orig workspaceapi.File, origPerms os.FileMode) (workspaceapi.File, error) {
	swap, osErr := f.scheme.OpenFile(f.swapFileName, os.O_RDWR|os.O_CREATE|os.O_EXCL, origPerms)
	if osErr != nil {
		if os.IsExist(osErr) {
			return nil, workspaceapi.ErrFileAlreadyOpen
		}
		return nil, osErr
	}

	if orig == nil {
		f.swapHash = sha256.Sum256(nil)
		return swap, nil
	}

	content, err := io.ReadAll(orig)
	if err != nil {
		_ = f.scheme.Remove(f.swapFileName)
		return nil, err
	}

	// not necessary for good scheme implementations, but we should
	// not trust that flags are interpreted correctly
	if err := swap.Truncate(0); err != nil {
		_ = f.scheme.Remove(f.swapFileName)
		return nil, err
	}

	if _, err := swap.Write(content); err != nil {
		_ = f.scheme.Remove(f.swapFileName)
		return nil, err
	}

	if err := swap.Sync(); err != nil {
		_ = f.scheme.Remove(f.swapFileName)
		return nil, err
	}

	if _, err := orig.Seek(0, 0); err != nil {
		_ = f.scheme.Remove(f.swapFileName)
		return nil, err
	}

	f.swapHash = sha256.Sum256(content)
	return swap, nil
}

func validateFileType(file workspaceapi.File) (os.FileInfo, error) {
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if fileInfo.IsDir() {
		return nil, workspaceapi.ErrFileIsNotRegular
	}

	mode := fileInfo.Mode()
	if mode.IsRegular() || mode&os.ModeSymlink != 0 {
		return fileInfo, nil
	}

	return nil, workspaceapi.ErrFileIsNotRegular
}

func (f *file) openFile(filePath string, flag int) (
	workspaceapi.File, os.FileInfo, error,
) {
	file, err := f.scheme.OpenFile(filePath, flag, 0666)
	if err != nil {
		return nil, nil, err
	}

	fileInfo, verr := validateFileType(file)
	if verr != nil {
		return nil, nil, verr
	}

	return file, fileInfo, nil
}

func (f *file) initFiles(filePath, swapDir string, readOnly bool) error {
	flag := os.O_RDWR
	if readOnly {
		flag = os.O_RDONLY
	}
	file, fileInfo, err := f.openFile(filePath, flag)
	switch {
	case os.IsNotExist(err):
		// create unless read-only mode
		if !readOnly {
			err = nil
		}
	case os.IsPermission(err):
		// delegate write error to Flush
		file, fileInfo, err = f.openFile(filePath, os.O_RDONLY)
		readOnly = true
	}
	if err != nil {
		return err
	}

	if !readOnly {
		err := f.initSwap(swapDir, filePath, file, fileInfo)
		if err != nil {
			return err
		}
	}

	f.orig = file
	if fileInfo != nil {
		f.infoModTime = fileInfo.ModTime()
	}
	f.fileName = filePath
	f.readOnly = readOnly

	return nil
}

func (f *file) initSwap(
	swapDir, filePath string, orig workspaceapi.File, fileInfo os.FileInfo,
) error {
	if f.swapFileName == "" {
		var dir string
		dir, f.swapFileName = swapFileName(swapDir, filePath)
		swapDir = sharedSwapDir(dir, filePath)
	}

	if swapDir != "" {
		if err := f.scheme.MkdirAll(swapDir, swapDirMode); err != nil {
			return err
		}
	}

	mode := os.FileMode(defaultFileMode)
	if fileInfo != nil {
		mode = fileInfo.Mode()
	}
	swap, err := f.initSwapFile(orig, mode)
	if err != nil {
		return err
	}
	// store swapInfo so we can check update times at Flush
	swapInfo, err := f.scheme.Stat(f.swapFileName)
	if err != nil {
		_ = f.scheme.Remove(f.swapFileName)
		return err
	}
	f.swap = swap
	f.swapInfoModTime = swapInfo.ModTime()
	f.swapDir = swapDir

	return nil
}

func (f *file) initBuffer(buf *cell.Buffer, file workspaceapi.File) (err error) {
	buf.Reset()
	view := NewUnixFileView(buf.View())

	// file could be not created yet, so initialize from the swap
	// in case some scheme implementations initialize files with
	// a template
	if file == nil {
		file = f.swap
	}
	if file != nil {
		var reader io.Reader = file
		h := sha256.New()
		if file == f.swap {
			reader = io.TeeReader(file, h)
		}
		_, err = buf.ReadFrom(reader)
		if err != nil {
			return
		}
		if file == f.swap {
			f.swapHash = [sha256.Size]byte(h.Sum(nil))
		}

		defer func() {
			_, err = file.Seek(0, 0)
		}()
	}

	if !view.EndsWithEOL() {
		buf.WriteString("\n")
	}

	buf.Subscribe(f)
	buf.WithView(view)
	buf.ResetVersion()

	f.buf = buf
	f.view = view

	return
}

func (f *file) initRecover(filePath, swapFilePath string, buf *cell.Buffer, force bool) error {
	orig, info, err := f.openFile(filePath, os.O_RDWR)
	if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return err
	}
	swap, swapFileInfo, osErr := f.openFile(swapFilePath, os.O_RDWR)
	if osErr != nil {
		return osErr
	}

	f.orig = orig
	f.swap = swap
	if info != nil {
		f.infoModTime = info.ModTime()
	}
	f.swapInfoModTime = swapFileInfo.ModTime()
	f.swapFileName = swapFilePath
	f.swapDir = sharedSwapDir(filepath.Dir(swapFilePath), filePath)
	f.fileName = filePath

	err = f.initBuffer(buf, f.swap)
	if err != nil {
		// do not remove swap if error is that swap is out of date
		// let use decide what to do with it
		if clerr := f.swap.Close(); clerr != nil {
			err = multierr.Append(err, clerr)
		}
		f.swap = nil
		if clerr := f.Close(); clerr != nil {
			err = multierr.Append(err, clerr)
		}
		return err
	}

	err = f.flush(force)
	if err != nil {
		buf.Reset()
		if clerr := f.swap.Close(); clerr != nil {
			err = multierr.Append(err, clerr)
		}
		f.swap = nil
		if clerr := f.Close(); clerr != nil {
			err = multierr.Append(err, clerr)
		}
		return err
	}

	f.setupCopySwapWorker()

	return nil
}

func (f *file) setupCopySwapWorker() {
	// a buffered channel of 1 guarantees that if worker
	// is busy and the call to copyFlushSwap is skipped
	// we are going to copyFlushSwap at least one final time
	f.ch = make(chan struct{}, 1)

	ch := f.ch
	go debug.CapturePanicReport(func() {
		for range ch {
			f.mu.Lock()
			str := f.content
			f.mu.Unlock()
			f.copyFlushSwapFile(str)
			f.wg.Done()
		}
	})
}

// init instantiates opens the file at filePath and initializes
// buf with the contents of it. swapDir is the shared directory that
// holds the swap entry, or "" when the entry sits next to the file.
func (f *file) init(
	file string, buf *cell.Buffer, swapDir string, readOnly bool,
) error {
	err := f.initFiles(file, swapDir, readOnly)
	if err != nil {
		return err
	}

	err = f.initBuffer(buf, f.orig)
	if err != nil {
		if clerr := f.Close(); clerr != nil {
			err = multierr.Append(err, clerr)
		}
		return err
	}

	f.setupCopySwapWorker()

	return nil
}

func (f *file) delayCopySwapError(err error) {
	f.delayedError = fmt.Errorf("swap file error %s: %s", f.swapFileName, err)
}

// recoverFiles closes the cached orig/swap descriptors and re-opens
// them against the current scheme. It is used when a previous swap
// write failed and may have left us with stale file descriptors —
// for example after an ssh scheme reconnected following a transient
// network drop. The on-disk swap file is removed before re-opening
// because the in-memory buffer is the source of truth at this point
// (a subsequent copyFlushSwapFile will re-stage it).
//
// recoverFiles intentionally does not touch the buffer or set
// f.reloading: the swap-copy worker is quiescent because the caller
// (flush) is already running with f.wg drained.
//
// On error f.orig / f.swap are left nil and f.readOnly stays at the
// pre-recovery value. flush() is expected to retry recovery on the
// next call: see the swap == nil && !readOnly branch.
func (f *file) recoverFiles() error {
	prevReadOnly := f.readOnly
	if f.orig != nil {
		_ = f.orig.Close()
		f.orig = nil
	}
	if f.swap != nil {
		_ = f.swap.Close()
		f.swap = nil
	}
	// Best-effort removal of any leftover swap file from the dead
	// session. If the scheme is still flaky this may fail; that's
	// fine, initFiles → initSwap will surface a clearer error.
	if f.swapFileName != "" {
		_ = f.scheme.Remove(f.swapFileName)
	}
	err := f.initFiles(f.fileName, f.swapDir, prevReadOnly)
	if err != nil {
		// Don't leave the file flagged read-only just because a
		// transient recovery attempt failed; otherwise a later
		// successful recovery would still surface as
		// ErrFileIsNotWritable. readOnly is only meaningful once
		// initFiles has succeeded against a live scheme.
		f.readOnly = prevReadOnly
	}
	return err
}

// writeThroughSwap is the fallback for a Rename that cannot cross from
// the swap directory to the file's filesystem.
func (f *file) writeThroughSwap(origTarget string) error {
	swap, err := f.scheme.OpenFile(f.swapFileName, os.O_RDONLY, defaultFileMode)
	if err != nil {
		return err
	}
	defer func() { _ = swap.Close() }()

	mode := defaultFileMode
	if info, serr := swap.Stat(); serr == nil {
		mode = info.Mode()
	}
	orig, err := f.scheme.OpenFile(origTarget, os.O_WRONLY|os.O_CREATE, mode)
	if err != nil {
		return err
	}
	err = func() error {
		if err := orig.Truncate(0); err != nil {
			return err
		}
		if _, err := io.Copy(orig, swap); err != nil {
			return err
		}
		return orig.Sync()
	}()
	return errors.Join(err, orig.Close())
}

// reopenFiles stands in for initFiles after writeThroughSwap: the swap
// entry survived the save, so re-creating it with O_EXCL would fail.
func (f *file) reopenFiles(readOnly bool) error {
	flag := os.O_RDWR
	if readOnly {
		flag = os.O_RDONLY
	}
	orig, origInfo, err := f.openFile(f.fileName, flag)
	if err != nil {
		return err
	}
	swap, _, err := f.openFile(f.swapFileName, os.O_RDWR)
	if err != nil {
		_ = orig.Close()
		return err
	}
	swapInfo, err := swap.Stat()
	if err != nil {
		return errors.Join(err, orig.Close(), swap.Close())
	}

	f.orig = orig
	if origInfo != nil {
		f.infoModTime = origInfo.ModTime()
	}
	f.swap = swap
	f.swapInfoModTime = swapInfo.ModTime()
	f.readOnly = readOnly
	return nil
}

func (f *file) copyFlushSwapFile(str string) (ok bool) {
	if f.swap == nil {
		return
	}

	f.unflushed = true
	err := f.swap.Truncate(0)
	if err != nil {
		f.delayCopySwapError(err)
		return
	}
	_, err = f.swap.Seek(0, 0)
	if err != nil {
		f.delayCopySwapError(err)
		return
	}

	// files must end in EOL
	if !strings.HasSuffix(str, "\n") {
		str += "\n"
	}

	data := []byte(str)
	if _, err = f.swap.Write(data); err != nil {
		f.delayCopySwapError(err)
		return
	}

	if err := f.swap.Sync(); err != nil {
		f.delayCopySwapError(err)
		return
	}

	finfo, err := f.swap.Stat()
	if err != nil {
		f.delayCopySwapError(err)
		return
	}

	// we have to always use what the file is reporting as mod time
	// because we cannot use the host's clock or a skew on a remote
	// workspace would introduce all sorts of bugs
	f.swapInfoModTime = finfo.ModTime()
	f.swapHash = sha256.Sum256(data)
	ok = true
	return
}

func fileContentHash(file workspaceapi.File) ([sha256.Size]byte, error) {
	h := sha256.New()
	_, err := io.Copy(h, file)
	_, seekErr := file.Seek(0, io.SeekStart)
	return [sha256.Size]byte(h.Sum(nil)), errors.Join(err, seekErr)
}

// reopenedMatchesSave reports whether the file reopened after the save
// still holds the bytes this flush wrote. staged spares it a read: the
// swap was re-staged from the reopened file, so its digest already
// identifies the content.
func (f *file) reopenedMatchesSave(
	savedHash [sha256.Size]byte, staged bool,
) (bool, error) {
	if f.orig == nil {
		return false, nil
	}
	if staged && !f.readOnly {
		return f.swapHash == savedHash, nil
	}
	hash, err := fileContentHash(f.orig)
	if err != nil {
		return false, err
	}
	return hash == savedHash, nil
}

func (f *file) OnWillEdit(ctx context.Context, start, end term.Coordinates, str string) {
	f.wg.Add(1)
}

func (f *file) OnDidEdit(ctx context.Context, from, to term.Coordinates, old string) {
	if f.reloading.Load() {
		f.wg.Done()
		return
	}
	// store the latest version of the buffer so the last
	// copyFlushSwap to run uses the up-to-date version.
	f.mu.Lock()
	f.content = f.buf.String()
	flushing := f.flushing
	if flushing {
		f.pendingEdits = true
	}
	f.mu.Unlock()
	if flushing {
		// async Flush/ForceFlush/Reload owns the swap file right now;
		// the goroutine will enqueue a catch-up swap write upon
		// completion.
		f.wg.Done()
		return
	}
	select {
	case f.ch <- struct{}{}:
	default:
		f.wg.Done()
	}
}

func (f *file) touchFile() (isExist bool) {
	var err error
	f.orig, err = f.scheme.OpenFile(f.fileName, os.O_RDWR|os.O_CREATE|os.O_EXCL, defaultFileMode)
	if err != nil {
		// file was not created when instantiating this file, but now
		// file seems to be there so file must be stale.
		if os.IsExist(err) {
			return true
		}
		// ignore other errors
	}
	return false
}

// Flush saves the contents of the buffer to disk asynchronously. If
// the file was modified by some other process, the returned channel
// will receive workspaceapi.ErrStaleData. See FlusherCloser for the
// full channel contract.
func (f *file) Flush(ctx context.Context) (<-chan error, error) {
	return f.startAsync(ctx, &f.flushWG, false, func() error { return f.flush(false) })
}

// ForceFlush forces saving the contents of the buffer to disk,
// overwriting any changes if the file was modified by another
// process. See Flush for the channel contract.
func (f *file) ForceFlush(ctx context.Context) (<-chan error, error) {
	return f.startAsync(ctx, &f.flushWG, false, func() error { return f.flush(true) })
}

// LastFlush returns the last time this file was flushed or a zero value time
// if this file has not been flushed yet.
func (f *file) LastFlush() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastFlush
}

// Reload reloads the contents of the buffer from disk asynchronously.
// See Flush for the channel contract.
func (f *file) Reload(ctx context.Context) (<-chan error, error) {
	return f.startAsync(ctx, &f.reloadWG, true, f.reload)
}

// reload replaces the in-memory buffer with the on-disk contents of
// f.orig. It is the worker half of Reload and must be invoked from
// the async goroutine spawned by startAsync.
//
// # Concurrency contract
//
// reload competes with three other actors over f's state:
//
//   - the copy-swap worker goroutine (setupCopySwapWorker), which
//     consumes f.ch and writes the swap file;
//   - the subscriber callbacks OnWillEdit/OnDidEdit, which run on
//     the host event-loop goroutine and enqueue copy-swap work;
//   - the host event-loop goroutine itself, which owns f.buf.
//
// reload sets f.reloading = true so OnDidEdit short-circuits any
// edit that lands during the reload (the buffer is about to be
// overwritten from disk; intervening edits are by definition
// stale). It then f.wg.Wait()s for any in-flight OnWillEdit/Add to
// drain and for the copy-swap worker to finish its current pass, so
// that the swap descriptor we are about to close is not being
// written to.
//
// Unlike flush, reload does not use the swap file as a staging
// buffer: it closes the old swap (and removes it from disk), then
// reopens orig/swap via initFiles. Because reload destroys the swap
// rather than coordinating writes against it, the post-work
// catch-up copy-swap pass is suppressed (suppressCopySwap=true in
// Reload) — there is no pending copy-swap work to run.
//
// Disk I/O (open + read) runs on this worker goroutine. The
// resulting cell.Buffer mutations and the f.lastFlush publish are
// scheduled onto the host event loop via f.scheduleNextTick so
// buffer subscribers (notably text.editorFlusherCloser, which
// touches UI-owned state from OnDidEdit) run on the event-loop
// goroutine. The worker blocks on the done channel before returning,
// preserving the startAsync invariant that the result channel only
// fires once the buffer is fully reloaded.
func (f *file) reload() error {
	f.reloading.Store(true)
	defer func() {
		f.reloading.Store(false)
	}()

	f.wg.Wait()

	if !f.beginFileSwap() {
		return nil
	}
	contents, err := f.reloadFiles()
	if f.endFileSwap() {
		// Close ran mid-swap and handed the teardown to us; its
		// caller already returned, so errors are unobservable.
		_ = f.teardown()
		return err
	}
	if err != nil {
		return err
	}

	done := make(chan struct{})
	infoModTime := f.infoModTime
	scheduled := f.scheduleNextTick(func() {
		defer close(done)
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return
		}
		f.lastFlush = infoModTime
		f.mu.Unlock()
		f.buf.ReloadContents(context.Background(), contents)
		if !f.view.EndsWithEOL() {
			f.buf.WriteString("\n")
		}
	})
	if !scheduled {
		return errors.New("reload: scheduleNextTick rejected callback")
	}
	// closeCh unblocks this wait when the host loop will no longer
	// pump the scheduled callback (e.g. Close ran on that loop).
	select {
	case <-done:
	case <-f.closedCh:
	}
	return nil
}

// beginFileSwap hands descriptor ownership (orig/swap/fileName) to
// the reload worker. It refuses when the file is already closed.
func (f *file) beginFileSwap() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.reloadOwnsFiles = true
	return true
}

// endFileSwap returns descriptor ownership. It reports whether Close
// ran mid-swap, in which case the worker inherited the teardown.
func (f *file) endFileSwap() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloadOwnsFiles = false
	return f.closed
}

// reloadFiles is the disk half of reload: it swaps the old
// orig/swap descriptors for freshly opened ones and reads the new
// on-disk contents. Callers must own the descriptors via
// beginFileSwap/endFileSwap so Close cannot tear them down mid-swap.
func (f *file) reloadFiles() (contents string, err error) {
	if f.orig != nil {
		_ = f.orig.Close()
	}
	if f.swap != nil {
		_ = f.swap.Close()
		_ = f.scheme.Remove(f.swapFileName)
	}

	if err := f.initFiles(f.fileName, f.swapDir, f.readOnly); err != nil {
		return "", err
	}

	if f.orig == nil {
		return "", errors.New("cannot reload a file that doesn't exist on disk")
	}

	data, err := io.ReadAll(f.orig)
	if err != nil {
		_, _ = f.orig.Seek(0, 0)
		return "", fmt.Errorf("read from file: %w", err)
	}
	if _, err := f.orig.Seek(0, 0); err != nil {
		return "", fmt.Errorf("seek: %w", err)
	}

	return string(data), nil
}

func (f *file) startAsync(
	ctx context.Context, wg *sync.WaitGroup, suppressCopySwap bool,
	work func() error,
) (<-chan error, error) {
	f.mu.Lock()
	if f.flushing {
		f.mu.Unlock()
		return nil, ErrFlushInProgress
	}
	f.flushing = true
	f.pendingEdits = false
	f.mu.Unlock()

	ch := make(chan error, 1)
	wg.Add(1)
	go debug.CapturePanicReport(func() {
		defer wg.Done()

		err := work()

		f.mu.Lock()
		f.flushing = false
		runCopySwap := !suppressCopySwap && f.pendingEdits && f.swap != nil
		f.pendingEdits = false
		f.mu.Unlock()

		if runCopySwap {
			f.wg.Add(1)
			select {
			case f.ch <- struct{}{}:
			default:
				f.wg.Done()
			}
		}

		// Honor ctx cancellation: deliver ctx.Err() instead of the real
		// result so callers see a uniform "cancelled" signal.
		if ctxErr := ctx.Err(); ctxErr != nil {
			ch <- ctxErr
		} else {
			ch <- err
		}
		close(ch)
	})
	return ch, nil
}

// flush stages the current buffer contents into the swap file and
// then renames swap → orig, atomically replacing the on-disk file.
// It is the worker half of Flush/ForceFlush and must be invoked
// from the async goroutine spawned by startAsync.
//
// # Concurrency contract
//
// flush competes with the copy-swap worker goroutine and the
// OnWillEdit/OnDidEdit subscribers for the swap file descriptor:
//
//   - the worker writes f.buf.String() into f.swap whenever an
//     edit signals f.ch;
//   - flush also writes f.swap (via copyFlushSwapFile) and then
//     publishes it onto f.orig, so it must be the sole writer for
//     the duration of the save.
//
// startAsync set f.flushing = true before invoking flush. Edits
// that land while flushing is true do not enqueue copy-swap work;
// instead OnDidEdit sets f.pendingEdits = true and the post-work
// runCopySwap branch in startAsync re-enqueues a single catch-up
// copy-swap pass (suppressCopySwap=false in Flush/ForceFlush) so
// those edits reach the next swap file after the rename. flush
// itself then waits on f.wg.Wait() to make sure no copy-swap pass
// is mid-write when it takes over the descriptor.
//
// Unlike reload, flush keeps and reuses the swap file: it is the
// staging buffer that makes the disk write atomic. Stale-data
// detection (ErrStaleData) compares on-disk mod times against
// f.swapInfoModTime / f.infoModTime so concurrent external writes
// are surfaced unless the caller passed force=true.
func (f *file) flush(force bool) error {
	f.wg.Wait()

	if f.swap == nil && !force && !f.readOnly && f.fileName != "" {
		if rerr := f.recoverFiles(); rerr != nil {
			return workspaceapi.ErrFileIsNotWritable
		}
		if f.swap == nil {
			return workspaceapi.ErrFileIsNotWritable
		}
	} else if f.swap == nil && !force {
		return workspaceapi.ErrFileIsNotWritable
	}

	err := f.delayedError
	if err != nil {
		f.delayedError = nil
		if !f.copyFlushSwapFile(f.buf.String()) {
			replayErr := f.delayedError
			if replayErr == nil {
				replayErr = err
			}
			if rerr := f.recoverFiles(); rerr != nil {
				return replayErr
			}
			f.delayedError = nil
			if !f.copyFlushSwapFile(f.buf.String()) {
				retErr := f.delayedError
				if retErr == nil {
					retErr = replayErr
				}
				return retErr
			}
		}
	}

	// used to override with symlink target if applicable
	origTarget := f.fileName

	var newFileInfo os.FileInfo

	// create file if it didn't exist before
	if f.orig == nil {
		isExist := f.touchFile()
		if isExist {
			return workspaceapi.ErrStaleData
		}
		// The touch is the first observable FS event for a brand-new
		// file and it fires long before the swap rename publishes
		// lastFlush below. Publish the touched file's mtime right away
		// so an FS-watcher goroutine dispatching the Create event
		// mid-flush sees lastFlush == mtime (our own write) instead of
		// a zero lastFlush with a dirty buffer, which it would surface
		// as a "file was just created on disk" conflict prompt.
		if f.orig != nil {
			if info, statErr := f.orig.Stat(); statErr == nil {
				f.mu.Lock()
				f.lastFlush = info.ModTime()
				f.mu.Unlock()
			}
		}
	} else if f.readOnly && force {
		// if it exists, but created readonly and want to force flush
		// overwrite f.orig with correct flags
		f.orig, newFileInfo, err = f.openFile(f.fileName, os.O_CREATE|os.O_RDWR)
		if err != nil {
			return err
		}
	} else {
		newFileInfo, err = f.scheme.Stat(f.fileName)
		if err != nil && force && os.IsNotExist(err) {
			// file was might have been removed, create it if in force mode
			f.orig, newFileInfo, err = f.openFile(f.fileName, os.O_CREATE|os.O_RDWR)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if !force && (newFileInfo.ModTime().After(f.swapInfoModTime) ||
				newFileInfo.ModTime().After(f.infoModTime)) {
				return workspaceapi.ErrStaleData
			}

			newFileInfo, err = f.scheme.Lstat(f.fileName)
			if err != nil {
				return err
			}
			if newFileInfo.Mode()&os.ModeSymlink != 0 {
				origTarget, err = f.scheme.Readlink(origTarget)
				if err != nil {
					return err
				}
			}
		}
	}

	newSwapInfo, err := f.scheme.Stat(f.swapFileName)
	if (err != nil && force) || f.swap == nil {
		err := f.initSwap(f.swapDir, f.fileName, f.orig, newFileInfo)
		if err != nil {
			return err
		}
		// write happens in the default goroutine so there's no need to sync
		f.content = f.buf.String()
		f.copyFlushSwapFile(f.content)
	} else if err != nil {
		return err
	}

	if !force && newSwapInfo.ModTime().After(f.swapInfoModTime) {
		return workspaceapi.ErrStaleData
	}

	savedModTime, savedHash := f.swapInfoModTime, f.swapHash
	f.mu.Lock()
	f.lastFlush = savedModTime
	f.mu.Unlock()

	if f.orig != nil {
		_ = f.orig.Close()
	}
	_ = f.swap.Close()

	readOnly := f.readOnly
	if readOnly && force {
		readOnly = false
	}

	// The rename is an optimisation: the swap is already a complete,
	// fsynced copy, so the original is recoverable either way.
	var staged bool
	if err = f.scheme.Rename(f.swapFileName, origTarget); err != nil {
		if err = f.writeThroughSwap(origTarget); err != nil {
			return err
		}
		err = f.reopenFiles(readOnly)
	} else {
		staged = true
		err = f.initFiles(f.fileName, f.swapDir, readOnly)
	}
	if err != nil {
		return err
	}

	if !f.infoModTime.Equal(savedModTime) {
		ours, err := f.reopenedMatchesSave(savedHash, staged)
		if err != nil {
			return err
		}
		if !ours {
			// A save bumps mtime, but an external write that landed in
			// this window must stay pending for the watcher and the next
			// stale-data check instead of becoming our own baseline.
			f.infoModTime = savedModTime
		}
	}

	f.unflushed = false
	f.mu.Lock()
	f.lastFlush = f.infoModTime
	f.mu.Unlock()

	return nil
}

// Close waits for in-flight Flush/ForceFlush (interrupting a rename
// would corrupt the on-disk file) but never for in-flight Reload:
// the reload worker parks on the host event loop (deadlock when
// Close runs on that loop) and its disk I/O can be remote and slow
// or wedged (UI freeze / wedged workspace teardown). If the worker
// owns the descriptors it inherits the teardown when its swap
// finishes (see endFileSwap); otherwise Close tears down inline.
func (f *file) Close() (ret error) {
	f.mu.Lock()
	wasClosed := f.closed
	f.closed = true
	reloadOwns := f.reloadOwnsFiles
	f.mu.Unlock()
	f.closedOnce.Do(func() { close(f.closedCh) })

	f.flushWG.Wait()

	if wasClosed {
		return errors.New("trying to Close an uninitialized file")
	}
	if reloadOwns {
		// The worker may be mutating the descriptors right now;
		// do not even read them here.
		return nil
	}
	if f.fileName == "" {
		return errors.New("trying to Close an uninitialized file")
	}

	return f.teardown()
}

// teardown releases the copy-swap worker, the buffer subscription
// and the orig/swap descriptors. It runs exactly once: from Close
// when no reload worker owns the descriptors, or from the reload
// worker that inherited the close (see endFileSwap).
func (f *file) teardown() (ret error) {
	f.fileName = ""

	f.wg.Wait()

	if f.ch != nil {
		close(f.ch)
	}

	if f.buf != nil {
		f.buf.Unsubscribe(f)
	}

	if f.swap != nil {
		if err := f.swap.Close(); err != nil {
			ret = multierr.Append(ret, err)
		}
		newSwapInfo, err := f.scheme.Stat(f.swapFileName)
		if err != nil {
			ret = multierr.Append(ret, err)
		} else {
			// do not remove a swap from another process, in case
			// another process took over ownership after we did
			if !newSwapInfo.ModTime().After(f.swapInfoModTime) {
				if err := f.scheme.Remove(f.swapFileName); err != nil {
					ret = multierr.Append(ret, err)
				}
			}
		}

		f.swap = nil
	}

	if f.orig != nil {
		if err := f.orig.Close(); err != nil {
			ret = multierr.Append(ret, err)
		}
		f.orig = nil
	}

	return ret
}
