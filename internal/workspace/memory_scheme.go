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
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/coreutils"
)

const (
	// MemoryScheme represents an in-memory URI scheme.
	MemoryScheme = "memory"
	// DocsScheme serves Rune's bundled documentation from memory.
	DocsScheme = "docs"
)

// vshBanner greets an interactive shell in a memory workspace.
const vshBanner = "Rune's built-in in-memory shell — type 'help' for the available commands."

// IsInMemoryScheme reports whether workspaces under scheme live only in
// the IDE's memory. Their commands run in-process in the Rune shell,
// so no host toolchain or language server can serve them. It is keyed
// on the scheme name because that is all an extension, running in its
// own process, can see of its workspace.
func IsInMemoryScheme(scheme string) bool {
	return scheme == MemoryScheme || scheme == DocsScheme
}

// NewMemoryScheme returns a Scheme that manages resources in a temporary
// in-memory file system. It does not enforce O_RDONLY, O_WRONLY, O_RDWR Open flags
// and rejects O_SYNC. O_APPEND positions the file's offset at its end, which
// every handle of the file shares.
func NewMemoryScheme(
	ctx context.Context, cfg config.Config, workspace workspaceapi.URI,
) (schemeapi.Scheme, error) {
	ret := new(memoryScheme)
	err := ret.init(MemoryScheme, workspace)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// NewInMemorySchemeFunc returns a schemeapi.SchemeFunc that constructs an
// in-memory backed Scheme for URIs that use the given scheme name. The
// returned scheme behaves like NewMemoryScheme but is bound to scheme
// rather than the built-in "memory" scheme. This is useful for embedding
// virtual filesystems (e.g. bundled documentation) under a custom URI
// scheme.
func NewInMemorySchemeFunc(scheme string) schemeapi.SchemeFunc {
	return func(
		ctx context.Context, cfg config.Config, workspace workspaceapi.URI,
	) (schemeapi.Scheme, error) {
		ret := new(memoryScheme)
		err := ret.init(scheme, workspace)
		if err != nil {
			return nil, err
		}
		return ret, nil
	}
}

type memoryScheme struct {
	scheme         string
	workspace      workspaceapi.URI
	mu             sync.Locker
	closed         bool
	closedCh       chan struct{}
	senders        sync.WaitGroup
	files          map[string]*memFile
	fd             *atomic.Uint64 // next fd
	watchpoints    map[schemeapi.Event][]chan<- schemeapi.EventInfo
	watchpointIDs  map[int64]chan<- schemeapi.EventInfo
	nextWatchpoint *atomic.Int64

	// vshMu guards vsh apart from mu: installing the shell's /bin
	// calls back into the filesystem.
	vshMu sync.Mutex
	vsh   *vsh.Executor
}

func (m *memoryScheme) init(scheme string, workspace workspaceapi.URI) error {
	if workspace.Host() != "" || workspace.User() != "" || workspace.Scheme() != scheme {
		return errors.New("invalid memory URI")
	}
	m.scheme = scheme
	m.workspace = workspace
	m.closedCh = make(chan struct{})
	m.watchpoints = make(map[schemeapi.Event][]chan<- schemeapi.EventInfo)
	m.watchpointIDs = make(map[int64]chan<- schemeapi.EventInfo)
	m.files = make(map[string]*memFile)
	m.mu = new(sync.Mutex)
	m.nextWatchpoint = new(atomic.Int64)
	m.fd = new(atomic.Uint64)
	return nil
}

// copyWatchpoints snapshots the channels subscribed to event and
// registers an in-flight delivery with m.senders so Close can wait
// for it before closing the watcher channels. Must be called with
// m.mu held; a nil return means no delivery was registered.
func (m *memoryScheme) copyWatchpoints(
	event schemeapi.Event,
) []chan<- schemeapi.EventInfo {
	watchpoints := m.watchpoints[event]
	if len(watchpoints) == 0 {
		return nil
	}
	copied := make([]chan<- schemeapi.EventInfo, len(watchpoints))
	copy(copied, watchpoints)
	m.senders.Add(1)
	return copied
}

// sendWatchEvents delivers fis to the channels snapshotted by
// copyWatchpoints. It must be called exactly once after releasing
// m.mu whenever copyWatchpoints returned non-nil. Blocked sends are
// aborted when the scheme closes: Close closes closedCh, waits for
// senders, and only then closes the watcher channels.
func (m *memoryScheme) sendWatchEvents(
	copied []chan<- schemeapi.EventInfo, fis ...watchFileInfo,
) {
	if copied == nil {
		return
	}
	defer m.senders.Done()
	for _, wp := range copied {
		for _, fi := range fis {
			select {
			case wp <- fi:
			case <-m.closedCh:
				return
			}
		}
	}
}

func (m *memoryScheme) NewFile(fd uintptr, filename string) workspaceapi.File {
	if f := m.ptyFile(fd, filename); f != nil {
		return f
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, f := range m.files {
		// The filename must match: descriptor numbers are recycled, so
		// a stale request naming a closed file would otherwise resolve
		// to whatever now owns the number.
		if f.fd == fd && f.filename == filename {
			return f
		}
	}

	return InvalidFile(fd, filename, errors.New("invalid file descriptor"))
}

// ptyFile resolves a descriptor the shell handed out for a pty, or
// nil. It never starts the shell: a pty needs one to exist first.
func (m *memoryScheme) ptyFile(fd uintptr, filename string) workspaceapi.File {
	m.vshMu.Lock()
	e := m.vsh
	m.vshMu.Unlock()
	if e == nil {
		return nil
	}
	return e.NewFile(fd, filename)
}

// executor returns the Rune shell serving this workspace, starting it
// on first use so that workspaces never opening a terminal pay
// nothing.
func (m *memoryScheme) executor() (*vsh.Executor, error) {
	m.vshMu.Lock()
	defer m.vshMu.Unlock()
	if m.vsh != nil {
		return m.vsh, nil
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, os.ErrClosed
	}
	e, err := vsh.NewExecutor(vsh.Config{
		Scheme:   m.scheme,
		Root:     m.workspace.Path(),
		FS:       m,
		AllocFd:  func() uintptr { return uintptr(m.fd.Add(1)) },
		Banner:   vshBanner,
		Commands: coreutils.Commands(),
	})
	if err != nil {
		return nil, err
	}
	m.vsh = e
	return e, nil
}

func getUserError() (*user.User, error) {
	return nil, errors.New("cannot determine user in memory scheme")
}

func (m *memoryScheme) OpenFile(path string, flag int, mode os.FileMode) (
	workspaceapi.File, error,
) {
	path = filepath.Clean(path)
	if path == "" {
		return nil, errors.New("invalid file")
	}

	uri, err := m.URI(path)
	if err != nil {
		return nil, err
	}
	uriStr := uri.String()

	m.mu.Lock()
	f, ok := m.files[uriStr]
	m.mu.Unlock()
	if !ok && flag&os.O_CREATE == 0 {
		return nil, os.ErrNotExist
	}
	if ok && f.mode.IsDir() {
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EISDIR}
	}
	if ok && flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0 {
		return nil, os.ErrExist
	}
	if ok && flag&os.O_TRUNC != 0 {
		ok = false // force re-create
	}
	if flag&os.O_SYNC != 0 {
		return nil, errors.New("unsupported Open flag")
	}

	if !ok {
		data := make([]byte, 0)
		var filename string
		rel, err := filepath.Rel(m.workspace.Path(), path)
		if err != nil {
			filename = filepath.Join(m.workspace.Path(), path)
		} else {
			filename = filepath.Join(m.workspace.Path(), rel)
		}
		fd := m.fd.Add(1)
		f = NewMemoryFile(filename, uintptr(fd), mode, data, m.mu).(*memFile)
		f.m = m
		m.mu.Lock()
		// The async workspace teardown closes the scheme off the
		// event loop, so calls can race Close; the maps are nil then.
		if m.closed {
			m.mu.Unlock()
			return nil, os.ErrClosed
		}
		m.files[uriStr] = f
		copied := m.copyWatchpoints(schemeapi.Create)
		m.mu.Unlock()
		m.sendWatchEvents(copied, watchFileInfo{
			event: schemeapi.Create,
			uri:   uri,
		})
	} else if flag&os.O_APPEND != 0 {
		_, _ = f.Seek(0, io.SeekEnd)
	} else {
		_, _ = f.Seek(0, 0)
	}

	return f, nil
}

func (m *memoryScheme) Remove(path string) error {
	uri, err := m.URI(path)
	if err != nil {
		return err
	}

	m.mu.Lock()

	uriStr := uri.String()
	f, ok := m.files[uriStr]
	if (!ok || f.mode.IsDir()) && m.hasChildrenLocked(uriStr) {
		m.mu.Unlock()
		return &os.PathError{Op: "remove", Path: path, Err: syscall.ENOTEMPTY}
	}
	if !ok {
		m.mu.Unlock()
		return os.ErrNotExist
	}

	delete(m.files, uriStr)
	copied := m.copyWatchpoints(schemeapi.Remove)
	m.mu.Unlock()

	m.sendWatchEvents(copied, watchFileInfo{
		event: schemeapi.Remove,
		uri:   uri,
		isDir: f.mode.IsDir(),
	})
	return nil
}

// hasChildrenLocked reports whether any entry is stored beneath the
// directory at uriStr. It must be called with m.mu held.
func (m *memoryScheme) hasChildrenLocked(uriStr string) bool {
	prefix := uriStr + "/"
	for other := range m.files {
		if strings.HasPrefix(other, prefix) {
			return true
		}
	}
	return false
}

func (m *memoryScheme) Rename(old, new string) error {
	oldURI, err := m.URI(old)
	if err != nil {
		return err
	}
	newURI, err := m.URI(new)
	if err != nil {
		return err
	}
	oldURIStr := oldURI.String()
	newURIStr := newURI.String()

	m.mu.Lock()

	f, ok := m.files[oldURIStr]
	if !ok {
		m.mu.Unlock()
		return os.ErrNotExist
	}
	if f.mode.IsDir() {
		// Renaming a directory would have to move every entry stored
		// beneath it; the scheme only renames files.
		m.mu.Unlock()
		return &os.PathError{Op: "rename", Path: old, Err: syscall.EISDIR}
	}
	delete(m.files, oldURIStr)
	f.filename = filepath.Base(new)
	m.files[newURIStr] = f
	copied := m.copyWatchpoints(schemeapi.Rename)
	m.mu.Unlock()

	m.sendWatchEvents(copied,
		watchFileInfo{
			event: schemeapi.Rename,
			uri:   oldURI,
		},
		watchFileInfo{
			event: schemeapi.Rename,
			uri:   newURI,
		})
	return nil
}

func (m *memoryScheme) Lstat(path string) (os.FileInfo, error) {
	uri, err := m.URI(path)
	if err != nil {
		return nil, err
	}

	// special cases, should always be a dir
	if uri.Path() == "/" {
		return memFileInfo{
			filename: "/",
			isDir:    true,
		}, nil
	}

	if uri.Equal(m.workspace) {
		return memFileInfo{
			filename: m.workspace.Path(),
			isDir:    true,
		}, nil
	}

	uriStr := uri.String()
	m.mu.Lock()
	f, ok := m.files[uriStr]
	if ok {
		m.mu.Unlock()
		return f.Stat()
	}
	// No file exists at this exact URI. The path may still describe an
	// implicit directory: a directory exists iff at least one file URI
	// is stored beneath it. We hold the lock while scanning to keep
	// observation consistent with concurrent Create/Remove.
	if m.hasChildrenLocked(uriStr) {
		m.mu.Unlock()
		return memFileInfo{
			filename: filepath.Base(uri.Path()),
			isDir:    true,
		}, nil
	}
	m.mu.Unlock()
	return nil, os.ErrNotExist
}

func (m *memoryScheme) Stat(path string) (os.FileInfo, error) {
	target, err := m.Readlink(path)
	if err != nil {
		return m.Lstat(path)
	}
	finfo, err := m.Lstat(target)
	if err != nil {
		return nil, err
	}
	mfi := finfo.(memFileInfo)
	mfi.filename = path
	return mfi, nil
}

func (m *memoryScheme) Readlink(path string) (string, error) {
	uri, err := m.URI(path)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	uriStr := uri.String()
	f, ok := m.files[uriStr]
	if !ok {
		m.mu.Unlock()
		return "", os.ErrNotExist
	}
	link := f.link
	m.mu.Unlock()
	if link == "" {
		return path, os.ErrInvalid
	}
	return link, nil
}

func (m *memoryScheme) URI(path string) (workspaceapi.URI, error) {
	absPath, err := workspaceapi.ExpandPath(path, getUserError, func() (string, error) {
		return m.workspace.Path(), nil
	})
	if err != nil {
		return workspaceapi.URI{}, err
	}
	uriStr := m.scheme + "://" + absPath
	return workspaceapi.ParseURI(uriStr)
}

func (m *memoryScheme) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (
	workspaceapi.Pid, error,
) {
	e, err := m.executor()
	if err != nil {
		return 0, err
	}
	return e.StartCommand(ctx, cmd)
}

func (m *memoryScheme) Signal(pid workspaceapi.Pid, signal syscall.Signal) error {
	m.vshMu.Lock()
	e := m.vsh
	m.vshMu.Unlock()
	if e == nil {
		return os.ErrProcessDone
	}
	return e.Signal(pid, signal)
}

// LookPath resolves name among the Rune shell's programs.
func (m *memoryScheme) LookPath(ctx context.Context, name, dir string) (string, error) {
	e, err := m.executor()
	if err != nil {
		return "", err
	}
	return e.LookPath(ctx, name, dir)
}

func (m *memoryScheme) NewPty(ctx context.Context) (workspaceapi.Pty, error) {
	e, err := m.executor()
	if err != nil {
		return workspaceapi.Pty{}, err
	}
	return e.NewPty(ctx)
}

func (m *memoryScheme) SetPtySize(pty workspaceapi.Pty, size workspaceapi.PtySize) error {
	m.vshMu.Lock()
	e := m.vsh
	m.vshMu.Unlock()
	if e == nil {
		return ErrInvalidMasterPtyFd
	}
	if err := e.SetPtySize(pty, size); err != nil {
		if errors.Is(err, vsh.ErrPtyClosed) {
			return ErrInvalidMasterPtyFd
		}
		return err
	}
	return nil
}

func (m *memoryScheme) ForegroundProcessGroup(workspaceapi.Pty) (int, error) {
	// The Rune shell has no job control, so its ptys have no foreground
	// group to report.
	return 0, fmt.Errorf("memory scheme: no job control: %w", errors.ErrUnsupported)
}

// Chroot returns a scheme rooted at path over the same files. Its
// shell starts in and treats path as home; the /bin it installs is the
// shared one.
func (m *memoryScheme) Chroot(path string) (schemeapi.Scheme, error) {
	uri, err := m.URI(path)
	if err != nil {
		return nil, err
	}
	nm := new(memoryScheme)
	err = nm.init(m.scheme, uri)
	if err != nil {
		return nil, err
	}
	// share locker, fds, files, watchpoints, etc.
	nm.mu = m.mu
	nm.fd = m.fd
	nm.files = m.files
	return nm, nil
}

func (m *memoryScheme) Root() string {
	return m.workspace.Path()
}

func (m *memoryScheme) Symlink(oldname, newname string) error {
	olduri, err := m.URI(oldname)
	if err != nil {
		return err
	}
	newuri, err := m.URI(newname)
	if err != nil {
		return err
	}

	m.mu.Lock()
	oldfile, ok := m.files[olduri.String()]
	if !ok {
		m.mu.Unlock()
		return os.ErrNotExist
	}
	_, ok = m.files[newuri.String()]
	if ok {
		m.mu.Unlock()
		return os.ErrExist
	}
	fd := m.fd.Add(1)
	const mode = 0755 | os.ModeSymlink
	mf := &memFile{
		filename: newname,
		fd:       uintptr(fd),
		mode:     mode,
		// share the pointer to the slice, and the locker
		data:   oldfile.data,
		reader: memReader{s: oldfile.data},
		locker: oldfile.locker,
		link:   oldname,
		m:      m,
	}
	m.files[newuri.String()] = mf
	copied := m.copyWatchpoints(schemeapi.Create)
	m.mu.Unlock()
	m.sendWatchEvents(copied, watchFileInfo{
		event: schemeapi.Create,
		uri:   newuri,
	})
	return err
}

func (m *memoryScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	return CreateTemp(m, dir, prefix)
}

func (m *memoryScheme) Join(elem ...string) string {
	return filepath.Join(elem...)
}

func (m *memoryScheme) Create(filename string) (workspaceapi.File, error) {
	return m.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
}

func (m *memoryScheme) Open(filename string) (workspaceapi.File, error) {
	return m.OpenFile(filename, os.O_RDONLY, 0)
}

func (m *memoryScheme) ReadDir(name string) (
	[]os.DirEntry, error,
) {
	info, err := m.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("not a directory")
	}

	// Resolve name to its canonical absolute URI path. info.Name() is
	// not enough for nested directories because it is just the basename
	// (an intermediate dir has no stored URI of its own); we need the
	// full prefix to slice file URIs against.
	dirURI, err := m.URI(name)
	if err != nil {
		return nil, err
	}
	prefix := dirURI.Path()
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Walk every file URI and bucket immediate children. A grandchild
	// like prefix + "a/b" contributes an immediate-child directory
	// entry "a"; a direct child like prefix + "leaf" contributes a
	// file or directory entry according to its mode. seen dedupes when
	// multiple files live under the same immediate child directory.
	seen := make(map[string]bool)
	var ret []os.DirEntry
	for uri, f := range m.files {
		u, err := workspaceapi.ParseURI(uri)
		if err != nil {
			panic("could not parse internal uri")
		}
		p := u.Path()
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := p[len(prefix):]
		if rest == "" {
			continue
		}
		childName, _, isDir := strings.Cut(rest, "/")
		if !isDir {
			isDir = f.mode.IsDir()
		}
		if seen[childName] {
			continue
		}
		seen[childName] = true
		ret = append(ret, memFileInfo{
			filename: childName,
			isDir:    isDir,
		})
	}
	// deterministic output
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].(memFileInfo).filename > ret[j].(memFileInfo).filename
	})
	return ret, nil
}

func (m *memoryScheme) MkdirAll(path string, perm os.FileMode) error {
	uri, err := m.URI(path)
	if err != nil {
		return err
	}
	// "/" and the workspace root always exist (Lstat special-cases
	// them), so entries are stored only for the components below "/".
	var components []string
	for p := uri.Path(); p != "/" && p != "."; p = filepath.Dir(p) {
		components = append(components, p)
	}

	var created []watchFileInfo
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return os.ErrClosed
	}
	for _, p := range slices.Backward(components) {
		if p == m.workspace.Path() {
			continue
		}
		uriStr := m.scheme + "://" + p
		if f, ok := m.files[uriStr]; ok {
			if !f.mode.IsDir() {
				m.mu.Unlock()
				return &os.PathError{Op: "mkdir", Path: path, Err: syscall.ENOTDIR}
			}
			continue
		}
		dir := NewMemoryFile(p, uintptr(m.fd.Add(1)), perm|os.ModeDir, nil, m.mu).(*memFile)
		dir.m = m
		dir.modTime = time.Now()
		m.files[uriStr] = dir
		dirURI, err := workspaceapi.ParseURI(uriStr)
		if err != nil {
			panic("could not parse internal uri")
		}
		created = append(created, watchFileInfo{
			event: schemeapi.Create,
			uri:   dirURI,
			isDir: true,
		})
	}
	var copied []chan<- schemeapi.EventInfo
	if len(created) > 0 {
		copied = m.copyWatchpoints(schemeapi.Create)
	}
	m.mu.Unlock()
	m.sendWatchEvents(copied, created...)
	return nil
}

func (m *memoryScheme) Watch(
	path string, c chan<- schemeapi.EventInfo, events ...schemeapi.Event,
) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0, os.ErrClosed
	}
	next := m.nextWatchpoint.Add(1)
	m.watchpointIDs[next] = c
	for _, event := range events {
		m.watchpoints[event] = append(m.watchpoints[event], c)
	}
	return int(next), nil
}

func (m *memoryScheme) StopWatch(id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	c, ok := m.watchpointIDs[int64(id)]
	if !ok {
		return errors.New("watchpoint not found")
	}
	delete(m.watchpointIDs, int64(id))

	for event, chs := range m.watchpoints {
		for i, ch := range chs {
			if ch == c {
				// remove channel from list of channels
				chs[i] = chs[len(chs)-1]
				chs = chs[:len(chs)-1]
				break
			}
		}
		m.watchpoints[event] = chs
	}
	return nil
}

func (m *memoryScheme) Close() error {
	m.vshMu.Lock()
	e := m.vsh
	m.vsh = nil
	m.vshMu.Unlock()
	if e != nil {
		// Processes write through the filesystem as they wind down,
		// so it must still be there.
		_ = e.Close()
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.closedCh)
	watchpointIDs := m.watchpointIDs
	m.files = nil
	m.watchpoints = nil
	m.watchpointIDs = nil
	m.mu.Unlock()

	// In-flight event deliveries may be parked on an unconsumed
	// watcher channel; closing it mid-send panics. closedCh aborts
	// those sends, then wait for them to drain.
	m.senders.Wait()

	// Close each watcher channel exactly once. Watch appends the same channel
	// into one bucket per subscribed event, so iterating m.watchpoints here
	// would try to close the same channel multiple times. watchpointIDs is
	// keyed by watchpoint ID and holds each channel exactly once.
	for _, ch := range watchpointIDs {
		close(ch)
	}
	return nil
}

type watchFileInfo struct {
	event schemeapi.Event
	uri   workspaceapi.URI
	isDir bool
}

func (w watchFileInfo) Event() schemeapi.Event {
	return w.event
}

func (w watchFileInfo) URI() workspaceapi.URI {
	return w.uri
}

func (w watchFileInfo) Sys() any {
	return nil
}

func (w watchFileInfo) IsDir() (bool, error) { return w.isDir, nil }
