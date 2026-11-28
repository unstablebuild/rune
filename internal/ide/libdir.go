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

package ide

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/syntax"
)

// libDirFailureTTL is how long sharedLibDir answers a failed lookup of a
// package that is not installed with the same failure.
const libDirFailureTTL = time.Minute

// sharedLibDir lists packages through next, the package manager that may
// prompt, install or contact the release server. Concurrent lookups of one
// package share a single call to next and its outcome. Listings of
// installed packages are reused until a directory holding them changes,
// and a failed lookup is not retried for failureTTL. A package installed
// meanwhile is always found: installed, which never prompts or contacts
// the server, is checked before a remembered failure is returned.
type sharedLibDir struct {
	installed, next syntax.PkgManager
	// cacheListings is only set for this machine's manager: the paths a
	// host lists are on another machine.
	cacheListings bool
	failureTTL    time.Duration
	now           func() time.Time

	mu       sync.Mutex
	inflight map[string]*lookup
	listings map[string]*cachedListing
	failures map[string]failure
}

var _ syntax.PkgManager = (*sharedLibDir)(nil)

func newSharedLibDir(installed, next syntax.PkgManager, cacheListings bool) *sharedLibDir {
	return &sharedLibDir{
		installed:     installed,
		next:          next,
		cacheListings: cacheListings,
		failureTTL:    libDirFailureTTL,
		now:           time.Now,
		inflight:      make(map[string]*lookup),
		listings:      make(map[string]*cachedListing),
		failures:      make(map[string]failure),
	}
}

// lookup is a lookup of a package shared by every caller asking for it
// until it ends: when it fails, when its listing completes, or when every
// caller gave up on it before listing it.
type lookup struct {
	pkgID string
	// done is closed once it and err are set.
	done chan struct{}
	it   sdkiterator.Iterator[string]
	err  error

	// The fields below are guarded by sharedLibDir.mu. refs counts the
	// callers waiting for the lookup or holding an iterator over it.
	found   bool
	refs    int
	listing *listing
}

// listing is the listing of a lookup's iterator, shared by every iterator
// over the lookup.
type listing struct {
	// done is closed once files and err are set.
	done  chan struct{}
	files []string
	err   error
}

type failure struct {
	err error
	at  time.Time
}

// LibDir implements syntax.PkgManager. A caller cancelling ctx gives up on
// the lookup without ending it for the others.
func (s *sharedLibDir) LibDir(ctx context.Context, pkgID string) (
	sdkiterator.Iterator[string], error,
) {
	if files, ok := s.cachedFiles(pkgID); ok {
		return sdkiterator.FromSlice(files), nil
	}

	s.mu.Lock()
	l, ok := s.inflight[pkgID]
	if !ok {
		l = &lookup{pkgID: pkgID, done: make(chan struct{})}
		s.inflight[pkgID] = l
		lctx := context.WithoutCancel(ctx)
		go debug.CapturePanicReport(func() { s.find(lctx, l) })
	}
	l.refs++
	s.mu.Unlock()

	select {
	case <-l.done:
	case <-ctx.Done():
		s.release(l)
		return nil, ctx.Err()
	}
	if l.err != nil {
		return nil, l.err
	}
	return &sharedLibDirIterator{s: s, l: l}, nil
}

// find looks l's package up: among the installed packages, else as a
// remembered failure, else through next.
func (s *sharedLibDir) find(ctx context.Context, l *lookup) {
	it, err := s.installed.LibDir(ctx, l.pkgID)
	remembered := false
	if err != nil {
		if err, remembered = s.failed(l.pkgID); !remembered {
			it, err = s.next.LibDir(ctx, l.pkgID)
		}
	}

	s.mu.Lock()
	l.it, l.err, l.found = it, err, true
	abandoned := false
	switch {
	case err != nil:
		s.endLocked(l)
		if !remembered {
			s.rememberLocked(l.pkgID, err)
		}
	case l.refs == 0:
		s.endLocked(l)
		abandoned = true
	}
	close(l.done)
	s.mu.Unlock()
	if abandoned {
		_ = it.Close()
	}
}

// list returns the files of l's package, listing them on the first call.
func (s *sharedLibDir) list(ctx context.Context, l *lookup) ([]string, error) {
	s.mu.Lock()
	ls := l.listing
	if ls == nil {
		ls = &listing{done: make(chan struct{})}
		l.listing = ls
		lctx := context.WithoutCancel(ctx)
		go debug.CapturePanicReport(func() { s.drain(lctx, l, ls) })
	}
	s.mu.Unlock()

	select {
	case <-ls.done:
		return ls.files, ls.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// drain lists l's iterator into ls, which may wait for the user to answer
// an install prompt, and ends l.
func (s *sharedLibDir) drain(ctx context.Context, l *lookup, ls *listing) {
	files, err := sdkiterator.ToSlice(ctx, l.it)
	_ = l.it.Close()
	var cached *cachedListing
	if err == nil && s.cacheListings {
		cached = newCachedListing(files)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ls.files, ls.err = files, err
	s.endLocked(l)
	if err != nil {
		s.rememberLocked(l.pkgID, err)
	}
	if cached != nil {
		s.listings[l.pkgID] = cached
	}
	close(ls.done)
}

// release drops a caller's reference to l, ending l once nobody waits for
// it or holds an iterator over it that was not listed.
func (s *sharedLibDir) release(l *lookup) {
	s.mu.Lock()
	l.refs--
	abandoned := l.refs == 0 && l.found && l.err == nil && l.listing == nil
	if abandoned {
		s.endLocked(l)
	}
	s.mu.Unlock()
	if abandoned {
		_ = l.it.Close()
	}
}

// endLocked makes later lookups of l's package start afresh. s.mu must be
// held.
func (s *sharedLibDir) endLocked(l *lookup) {
	if s.inflight[l.pkgID] == l {
		delete(s.inflight, l.pkgID)
	}
}

// failed returns the failure remembered for pkgID within failureTTL.
func (s *sharedLibDir) failed(pkgID string) (error, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.failures[pkgID]
	if !ok {
		return nil, false
	}
	if s.now().Sub(f.at) >= s.failureTTL {
		delete(s.failures, pkgID)
		return nil, false
	}
	return f.err, true
}

// rememberLocked remembers err as the failure of pkgID, unless a caller's
// context ended the lookup. s.mu must be held.
func (s *sharedLibDir) rememberLocked(pkgID string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	s.failures[pkgID] = failure{err: err, at: s.now()}
}

// cachedFiles returns the listing of pkgID cached while it is current.
func (s *sharedLibDir) cachedFiles(pkgID string) ([]string, bool) {
	s.mu.Lock()
	c, ok := s.listings[pkgID]
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	if c.current() {
		return c.files, true
	}
	s.mu.Lock()
	if s.listings[pkgID] == c {
		delete(s.listings, pkgID)
	}
	s.mu.Unlock()
	return nil, false
}

// sharedLibDirIterator iterates over the shared listing of a lookup.
type sharedLibDirIterator struct {
	s      *sharedLibDir
	l      *lookup
	listed bool
	files  []string
	err    error
	closed bool
}

func (it *sharedLibDirIterator) Next(ctx context.Context) (string, bool) {
	if !it.listed && it.err == nil {
		it.files, it.err = it.s.list(ctx, it.l)
		it.listed = it.err == nil
	}
	if it.err != nil || len(it.files) == 0 {
		return "", false
	}
	file := it.files[0]
	it.files = it.files[1:]
	return file, true
}

// Err reports only what Next encountered, so it is nil before the first
// Next.
func (it *sharedLibDirIterator) Err() error {
	return it.err
}

// Close never waits for the listing.
func (it *sharedLibDirIterator) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	it.s.release(it.l)
	return nil
}

// cachedListing is the listing of a package and the directories holding
// its files, as they were when it was listed. Installing or switching to
// another version points the package's lib dir at another directory, and
// adding or removing a file changes the modification time of its
// directory.
type cachedListing struct {
	files []string
	dirs  []cachedDir
}

type cachedDir struct {
	path string
	info os.FileInfo
}

// newCachedListing returns nil when files cannot be told apart from a
// listing of another version: when there are none, or a directory holding
// them is gone.
func newCachedListing(files []string) *cachedListing {
	if len(files) == 0 {
		return nil
	}
	var dirs []cachedDir
	for _, file := range files {
		dir := filepath.Dir(file)
		if slices.ContainsFunc(dirs, func(d cachedDir) bool { return d.path == dir }) {
			continue
		}
		info, err := os.Stat(dir)
		if err != nil {
			return nil
		}
		dirs = append(dirs, cachedDir{path: dir, info: info})
	}
	return &cachedListing{files: slices.Clone(files), dirs: dirs}
}

// current reports whether every directory holding the files is the one
// they were listed from, unchanged.
func (c *cachedListing) current() bool {
	for _, d := range c.dirs {
		info, err := os.Stat(d.path)
		if err != nil || !os.SameFile(info, d.info) || !info.ModTime().Equal(d.info.ModTime()) {
			return false
		}
	}
	return true
}
