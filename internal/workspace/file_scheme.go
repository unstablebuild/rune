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
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ernestrc/go-multierror"
	"github.com/ernestrc/sensible/find"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/bluectx"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/notify"
	"github.com/unstablebuild/pty"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/gitenv"
	"unstable.build/rune/internal/procattr"
)

const (
	// FileScheme represents the local file URL scheme.
	FileScheme         = "file"
	watcherWaitTimeout = 2 * time.Minute
)

var (
	errProcNotFound = errors.New("process not found")
)

// ErrInvalidMasterPtyFd reports a SetPtySize whose master descriptor
// is no longer registered with the scheme, i.e. the pty was closed.
// The message is a wire contract: the workspacerpc server returns it
// verbatim, gRPC flattens it to an untyped status error, and the ide
// resize worker matches on the text to drop the resize silently.
var ErrInvalidMasterPtyFd = errors.New("invalid master pty fd")

// NewFileScheme returns a Scheme that manages resources
// on the local file system.
func NewFileScheme(
	ctx context.Context, cfg config.Config, workspace workspaceapi.URI,
) (schemeapi.Scheme, error) {
	ret := new(fileScheme)
	ret.getUser = user.Current
	ret.osStat = os.Stat
	ret.lookupUser = user.Lookup
	err := ret.init(cfg, workspace)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// OpenFile opens the given filename using the default current working
// directory's file scheme. This is preferrable over os.OpenFile, because
// it does expansion of paths (i.e. ~ is expanded to the current user's home directory).
func OpenFile(filename string, flag int, perm os.FileMode) (workspaceapi.File, error) {
	cwdURI, _ := workspaceapi.CurrentUserHostURI(".")
	fs, err := NewFileScheme(context.Background(), config.NopConfig(), cwdURI)
	if err != nil {
		return nil, fmt.Errorf("file scheme: %v", err)
	}
	f, err := fs.OpenFile(filename, flag, perm)
	if err != nil {
		_ = fs.Close()
		return nil, err
	}
	return &ownedSchemeFile{File: f, scheme: fs}, nil
}

// ReadFile reads the file named by filename and returns the contents.
// See os.ReadFile for more details.
func ReadFile(filename string) ([]byte, error) {
	f, err := OpenFile(filename, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	defer f.Close() //nolint:errcheck
	return io.ReadAll(f)
}

type fileScheme struct {
	getUser    func() (*user.User, error)
	osStat     func(string) (os.FileInfo, error)
	lookupUser func(string) (*user.User, error)
	workspace  workspaceapi.URI
	ctx        context.Context
	cancelCtx  func()
	cmds       sync.Map // map[workspaceapi.Pid]struct{}

	// zdotDir, when non-empty, is exported as ZDOTDIR to the shell
	// processes started via the empty-cmd-Path protocol contract (see
	// StartCommand). Resolved from the scheme's config at init time so
	// it always reflects the executor's host: for SSH workspaces the
	// remote `rune -x` server reads its own config, so the local IDE's
	// zdotdir (which points at a host-specific path) doesn't leak into
	// the remote shell's environment.
	zdotDir string

	watchpoints    sync.Map
	nextWatchPoint atomic.Int64

	// This is important to prevent runtime finalizers
	// running on files that are garbage collected on host
	// but that clients hold references to.
	//
	// Technically we could leak files if clients
	// never close files, but once Server is garbage
	// collected, all files that are orhpaned will be closed.
	//
	// A pointer is used so a chrooted view can share the same map
	// with its parent — see Chroot below.
	files *sync.Map // map[uintptr]workspaceapi.File

	// execMu serializes StartCommand's unwrap-to-fork window
	// against Close force-closing the tracked files: os/exec reads
	// the unwrapped *os.Files' fds during StartProcess, and closing
	// them concurrently is a data race on the fd state. The window
	// is bounded (fork/exec, not the process lifetime), so Close
	// only ever waits momentarily. Shared across chrooted views
	// like files.
	execMu *sync.RWMutex
}

func (p *fileScheme) init(
	cfg config.Config, workspace workspaceapi.URI,
) error {
	if workspace.Host() != "" || workspace.User() != "" || workspace.Scheme() != FileScheme {
		return errors.New("invalid file URI")
	}
	workspacewd := workspace.Path()
	fs, err := p.osStat(workspacewd)
	if err != nil {
		return err
	}
	if !fs.IsDir() {
		return fmt.Errorf("workspaceapi.URI does not refer to a directory: %s", workspace.String())
	}
	p.workspace = workspace
	p.nextWatchPoint.Add(1)
	p.ctx, p.cancelCtx = context.WithCancel(context.Background())
	if p.files == nil {
		p.files = new(sync.Map)
	}
	if p.execMu == nil {
		p.execMu = new(sync.RWMutex)
	}
	if cfg != nil {
		// zdotdir is optional; ErrNotFound just means "not configured".
		if z, err := cfg.GetString("zdotdir"); err == nil {
			p.zdotDir = z
		}
	}
	return nil
}

func (p *fileScheme) Root() string {
	return p.workspace.Path()
}

func (p *fileScheme) OpenFile(path string, flag int, perm os.FileMode) (workspaceapi.File, error) {
	var err error
	path, err = workspaceapi.ExpandPath(path, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}

	ret := &fileSchemeFile{File: f, p: p, fd: f.Fd()}
	p.files.Store(f.Fd(), ret)

	return ret, nil
}

func (p *fileScheme) Symlink(target, link string) error {
	var err error
	target, err = workspaceapi.ExpandPath(target, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return err
	}
	link, err = workspaceapi.ExpandPath(link, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return err
	}
	return os.Symlink(target, link)
}

func (p *fileScheme) Join(elems ...string) string {
	return filepath.Join(elems...)
}

func (p *fileScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	f, err := os.CreateTemp(dir, prefix)
	if err != nil {
		return nil, err
	}

	ret := &fileSchemeFile{File: f, p: p, fd: f.Fd()}
	p.files.Store(f.Fd(), ret)

	return ret, nil
}

func (p *fileScheme) Create(filename string) (workspaceapi.File, error) {
	return p.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
}

func (p *fileScheme) Open(filename string) (workspaceapi.File, error) {
	var err error
	filename, err = workspaceapi.ExpandPath(filename, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}

	ret := &fileSchemeFile{File: f, p: p, fd: f.Fd()}
	p.files.Store(f.Fd(), ret)

	return ret, nil
}

func (p *fileScheme) NewFile(fd uintptr, filename string) workspaceapi.File {
	f, ok := p.files.Load(fd)
	if !ok {
		return nil
	}
	ret := f.(*fileSchemeFile)
	// The OS recycles descriptor numbers, so a stale request naming a
	// file that has since been closed would otherwise resolve to
	// whatever now owns the number (e.g. a task pty).
	if ret.Name() != filename {
		return nil
	}
	return ret
}

func (p *fileScheme) Remove(path string) error {
	var err error
	path, err = workspaceapi.ExpandPath(path, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func (p *fileScheme) Rename(old, new string) error {
	var err error
	old, err = workspaceapi.ExpandPath(old, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return err
	}
	new, err = workspaceapi.ExpandPath(new, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return err
	}
	return os.Rename(old, new)
}

func (p *fileScheme) Stat(path string) (os.FileInfo, error) {
	var err error
	path, err = workspaceapi.ExpandPath(path, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return nil, err
	}
	return os.Stat(path)
}

func (p *fileScheme) ReadDir(name string) ([]os.DirEntry, error) {
	var err error
	name, err = workspaceapi.ExpandPath(name, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return nil, err
	}
	return os.ReadDir(name)
}

func (p *fileScheme) Lstat(path string) (os.FileInfo, error) {
	var err error
	path, err = workspaceapi.ExpandPath(path, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return nil, err
	}
	return os.Lstat(path)
}

func (p *fileScheme) Readlink(path string) (string, error) {
	var err error
	path, err = workspaceapi.ExpandPath(path, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return "", err
	}
	return os.Readlink(path)
}

func (p *fileScheme) getUserOrLookup() (*user.User, error) {
	if p.workspace.User() == "" {
		return p.getUser()
	}
	username := p.workspace.User()
	return p.lookupUser(username)
}

func (p *fileScheme) URI(path string) (workspaceapi.URI, error) {
	absPath, err := workspaceapi.ExpandPath(path, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return workspaceapi.URI{}, fmt.Errorf("expand path: %w", err)
	}
	return makeLocalURI(absPath)
}

func (p *fileScheme) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "fileScheme").
		WithField("URI", p.workspace.String()).
		Logf(level, msg, args...)
}

func (p *fileScheme) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (
	workspaceapi.Pid, error,
) {
	var err error
	// Empty Path is the protocol contract for "run the user's login
	// shell on the executor's host".
	if cmd.Path == "" {
		cmd.Path = resolveLoginShell()
		if len(cmd.Args) == 0 {
			cmd.Args = []string{"--login", "-i"}
		}
		if filepath.Base(cmd.Path) == "zsh" && p.zdotDir != "" {
			cmd.Env = append(cmd.Env, fmt.Sprintf("ZDOTDIR=%s", p.zdotDir))
		}
	}
	cmd.Path, err = workspaceapi.ExpandPath(
		cmd.Path, p.getUserOrLookup,
		func() (string, error) { return "", nil })
	if err != nil {
		return 0, fmt.Errorf("expand cmd.Path: %w", err)
	}
	path := cmd.Path
	if filepath.Base(cmd.Path) == cmd.Path {
		path, err = find.Executable(cmd.Path)
		if err != nil {
			p.log(log.WarnLevel,
				"find.Executable: could not find executable of '%s' in path. "+
					"Falling back to shell expanding it: %v", cmd.Path, err)
			path = cmd.Path
		}
	}
	// ensure that if file scheme is closed, all commands are cleaned up
	var cancelFn func()
	ctx, cancelFn = bluectx.First(p.ctx, ctx)
	stdcmd := exec.CommandContext(ctx, path, cmd.Args...)

	if cmd.Dir != "" {
		stdcmd.Dir = cmd.Dir
	} else {
		stdcmd.Dir = p.workspace.Path()
	}

	// Strip git's per-repository overrides (GIT_DIR and friends)
	// from the inherited base: when this process runs under a git
	// hook they pin every spawned git command to the hook's
	// repository instead of the workspace. Caller-provided cmd.Env
	// is appended after and wins on duplicates.
	stdcmd.Env = gitenv.Sanitize(stdcmd.Environ())
	stdcmd.Env = append(stdcmd.Env, cmd.Env...)
	stdcmd.SysProcAttr = cmd.SysProcAttr
	// A caller that asks to head a new process group is asking for its
	// descendants to be terminated with it: intermediaries like
	// `go run` exec the real program as a grandchild that SIGKILL
	// cannot be forwarded to. Joining an existing Pgid is someone
	// else's group and not ours to signal.
	leadsGroup := procattr.LeadsGroup(cmd.SysProcAttr)
	if leadsGroup {
		stdcmd.Cancel = func() error { return procattr.KillGroup(stdcmd.Process) }
	}

	p.execMu.RLock()
	if p.ctx.Err() != nil {
		p.execMu.RUnlock()
		cancelFn()
		return 0, fmt.Errorf("start command: %w", p.ctx.Err())
	}
	stdcmd.Stdout = p.tryUnwrapFileWriter(cmd.Stdout)
	stdcmd.Stderr = p.tryUnwrapFileWriter(cmd.Stderr)
	stdcmd.Stdin = p.tryUnwrapFileReader(cmd.Stdin)

	err = stdcmd.Start()
	p.execMu.RUnlock()
	if err != nil {
		cancelFn()
		return 0, err
	}

	pid := workspaceapi.Pid(stdcmd.Process.Pid)
	go debug.CapturePanicReport(func() {
		defer cancelFn()
		defer func() {
			p.cmds.Delete(pid)
		}()

		p.log(log.TraceLevel, "exec.Command: Wait: pid=%d", stdcmd.Process.Pid)
		err := stdcmd.Wait()
		p.log(log.DebugLevel, "exec.Command: Wait returned: cmd=%v pid=%d, err=%v",
			stdcmd.Args, stdcmd.Process.Pid, err)

		if leadsGroup {
			// Cancellation is not the only way a tree is left
			// behind: a program that exits on its own can leave
			// helpers running. The group's id stays reserved while
			// any member of it is alive, so this cannot reach the
			// group of a process that reused the pid.
			_ = procattr.KillGroup(stdcmd.Process)
		}

		ctx, cancel := context.WithTimeout(
			context.Background(), watcherWaitTimeout)
		defer cancel()

		if cmd.Watcher != nil && cmd.Watcher.WatchProcess() != nil {
			select {
			case cmd.Watcher.WatchProcess() <- err:
			case <-ctx.Done():
				p.log(log.WarnLevel, "could not deliver error to watcher chan: "+
					"watcher not ready for too long")
			}
		}
	})

	p.log(log.DebugLevel, "exec.Command: (pid=%d)", stdcmd.Process.Pid)

	p.cmds.Store(pid, struct{}{})

	return pid, nil
}

func (p *fileScheme) Chroot(path string) (schemeapi.Scheme, error) {
	uri, err := p.URI(path)
	if err != nil {
		return nil, err
	}
	child := new(fileScheme)
	child.getUser = p.getUser
	child.osStat = p.osStat
	child.lookupUser = p.lookupUser
	child.files = p.files
	child.execMu = p.execMu
	child.zdotDir = p.zdotDir
	if err := child.init(config.NopConfig(), uri); err != nil {
		return nil, err
	}
	return child, nil
}

func (p *fileScheme) Signal(pid workspaceapi.Pid, signal syscall.Signal) error {
	_, ok := p.cmds.Load(pid)
	if !ok {
		return errProcNotFound
	}

	err := procattr.Signal(int(pid), signal)
	if err != nil {
		return fmt.Errorf("syscall kill: %w", err)
	}
	return nil
}

func (p *fileScheme) NewPty(ctx context.Context) (workspaceapi.Pty, error) {
	// open master/slave files
	pty, tty, err := pty.Open()
	if err != nil {
		return workspaceapi.Pty{}, fmt.Errorf("open pty: %v", err)
	}

	master := &fileSchemeFile{File: pty, p: p, fd: pty.Fd()}
	p.files.Store(master.fd, master)

	slave := &fileSchemeFile{File: tty, p: p, fd: tty.Fd()}
	p.files.Store(slave.fd, slave)

	return workspaceapi.Pty{
		Master: master,
		Slave:  slave,
	}, nil
}

func (p *fileScheme) SetPtySize(pp workspaceapi.Pty, width, height int) error {
	ptyFile, ok := pp.Master.(*fileSchemeFile)
	if !ok {
		return fmt.Errorf("extraneous pty: %+v", pp)
	}

	// Resizes are queued off the event loop, so one can race the
	// terminal teardown and dequeue after Close released the master.
	// Close deregisters the wrapper, so a missing registration means
	// the cached descriptor number is dead or already recycled by an
	// unrelated file, which must not be ioctl'd.
	if !p.registered(ptyFile) {
		return ErrInvalidMasterPtyFd
	}
	err := pty.Setsize(ptyFile.Fd(), &pty.Winsize{
		Rows: uint16(height),
		Cols: uint16(width),
	})
	if err != nil {
		// Close may have landed between the check above and the
		// ioctl; report that as the pty being gone, not as EBADF.
		if !p.registered(ptyFile) {
			return ErrInvalidMasterPtyFd
		}
		return fmt.Errorf("set pty size: %v", err)
	}
	return nil
}

// registered reports whether f is still the file registered under its
// descriptor number, i.e. it has not been closed.
func (p *fileScheme) registered(f *fileSchemeFile) bool {
	cur, ok := p.files.Load(f.fd)
	return ok && cur == f
}

func (p *fileScheme) MkdirAll(path string, perm os.FileMode) error {
	var err error
	path, err = workspaceapi.ExpandPath(path, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return err
	}
	return os.MkdirAll(path, perm)
}

func (p *fileScheme) Watch(
	path string, c chan<- schemeapi.EventInfo, events ...schemeapi.Event,
) (int, error) {
	path, err := workspaceapi.ExpandPath(path, p.getUserOrLookup, func() (string, error) {
		return p.workspace.Path(), nil
	})
	if err != nil {
		return 0, err
	}
	// NOTE: We cannot simply re-use notify.Event values, because we need
	// this values to remain stable across platforms: i.e. a value is sent
	// across the wire from a linux to a macos system.
	var notifyEvents []notify.Event
	for _, ev := range events {
		var nev notify.Event
		switch ev {
		case schemeapi.Create:
			nev = notify.Create
		case schemeapi.Write:
			nev = notify.Write
		case schemeapi.Rename:
			nev = notify.Rename
		case schemeapi.Remove:
			nev = notify.Remove
		}
		notifyEvents = append(notifyEvents, nev)
	}
	ch := make(chan notify.EventInfo, 8192)
	w, err := notify.Watch(path, ch, notifyEvents...)
	if err != nil {
		return 0, fmt.Errorf("notify: %v", err)
	}
	discardWatchBootstrap(ch)
	go debug.CapturePanicReport(func() {
		for {
			select {
			case <-p.ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				uri, err := p.URI(ev.Path())
				if err != nil {
					p.log(log.WarnLevel, "watched file uri %q: %v", ev.Path(), err)
					continue
				}
				ei := newEventInfo(ev, uri)
				select {
				case c <- ei:
				case <-p.ctx.Done():
					return
				}
			}
		}
	})
	id := p.nextWatchPoint.Add(1)
	p.watchpoints.Store(int(id), w)
	return int(id), nil
}

// discardWatchBootstrap drops the events notify.Watch queues while
// establishing the watch. Backends without a native recursive watch
// (inotify) walk the tree to install one watch per directory and
// report every entry they pass as a Create, so a fresh watch would
// otherwise announce the whole workspace as newly created. Those
// events are all enqueued before notify.Watch returns, so a
// non-blocking drain here separates them from real activity without
// resorting to a settling delay.
//
// A file genuinely created while the walk is still running can be
// swallowed too, but that is inherent to watch establishment: until
// the walk reaches a directory, changes in it are not observable
// either.
func discardWatchBootstrap(ch <-chan notify.EventInfo) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func (p *fileScheme) StopWatch(ID int) error {
	w, ok := p.watchpoints.LoadAndDelete(ID)
	if !ok {
		return nil
	}
	closer := w.(io.Closer)
	return closer.Close()
}

func (p *fileScheme) Close() (ret error) {
	p.cancelCtx()
	p.watchpoints.Range(func(id any, value any) bool {
		if err := p.StopWatch(id.(int)); err != nil {
			ret = multierror.Append(ret, err)
		}
		return true
	})
	// Exclude in-flight StartCommand fork/exec windows before
	// force-closing the files they may be handing to the child.
	// p.ctx is already cancelled, so late StartCommand callers fail
	// fast instead of racing this teardown.
	p.execMu.Lock()
	defer p.execMu.Unlock()
	p.files.Range(func(_ any, value any) bool {
		if err := value.(*fileSchemeFile).Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
		return true
	})
	return
}

// enables overriding Close to delete from map.
type fileSchemeFile struct {
	workspaceapi.File
	p *fileScheme
	// cache fd so pty.SetSize doesn't cause races on fd destroy (on reads)
	fd        uintptr
	closeOnce sync.Once
	closeErr  error
}

func (f *fileSchemeFile) Fd() uintptr {
	return f.fd
}

func (f *fileSchemeFile) Close() error {
	f.closeOnce.Do(func() {
		// Delete only our own registration: the OS recycles
		// descriptor numbers, so a plain Delete could remove the
		// entry of whichever file now owns the number.
		f.p.files.CompareAndDelete(f.fd, f)
		f.closeErr = f.File.Close()
	})
	return f.closeErr
}

type ownedSchemeFile struct {
	workspaceapi.File
	scheme    io.Closer
	closeOnce sync.Once
	closeErr  error
}

func (f *ownedSchemeFile) Close() error {
	f.closeOnce.Do(func() {
		if err := f.File.Close(); err != nil {
			f.closeErr = err
		}
		if err := f.scheme.Close(); err != nil {
			f.closeErr = multierror.Append(f.closeErr, err)
		}
	})
	return f.closeErr
}

func makeLocalURI(path string) (workspaceapi.URI, error) {
	uriStr := "file://" + path
	return workspaceapi.ParseURI(uriStr)
}

func (p *fileScheme) tryUnwrapFileWriter(f io.Writer) io.Writer {
	// A local caller hands us the wrapper itself; unwrap by identity
	// so descriptor-number recycling cannot alias it to another file.
	if f, ok := f.(*fileSchemeFile); ok {
		return f.File
	}
	if f, ok := f.(workspaceapi.File); ok {
		v, ok := p.files.Load(f.Fd())
		if ok {
			return v.(*fileSchemeFile).File
		}
	}
	return f
}

func (p *fileScheme) tryUnwrapFileReader(f io.Reader) io.Reader {
	if f, ok := f.(*fileSchemeFile); ok {
		return f.File
	}
	if f, ok := f.(workspaceapi.File); ok {
		v, ok := p.files.Load(f.Fd())
		if ok {
			return v.(*fileSchemeFile).File
		}
	}
	return f
}

type eventInfo struct {
	uri workspaceapi.URI
	e   schemeapi.Event
	d   bool
}

func (e eventInfo) Event() schemeapi.Event {
	return e.e
}

func (e eventInfo) URI() workspaceapi.URI {
	return e.uri
}

func (e eventInfo) IsDir() (bool, error) {
	return e.d, nil
}

func newEventInfo(ei notify.EventInfo, uri workspaceapi.URI) eventInfo {
	var nev schemeapi.Event
	switch ei.Event() {
	case notify.Create:
		nev = schemeapi.Create
	case notify.Write:
		nev = schemeapi.Write
	case notify.Rename:
		nev = schemeapi.Rename
	case notify.Remove:
		nev = schemeapi.Remove
	}
	// this can be an error only in windows
	isDir, _ := ei.IsDir()
	return eventInfo{
		d:   isDir,
		uri: uri,
		e:   nev,
	}
}

func resolveLoginShell() string {
	const fallback = "/bin/sh"
	candidates := []string{
		"/bin/bash",
		"/usr/bin/bash",
		"/bin/zsh",
		"/usr/bin/zsh",
		fallback,
	}
	if sh := strings.TrimSpace(os.Getenv("SHELL")); sh != "" && filepath.IsAbs(sh) {
		if isExecutableFile(sh) {
			return sh
		}
		log.WithField(logging.KeyClass, "fileScheme").Warnf(
			"resolveLoginShell: $SHELL=%q is not an executable file on this "+
				"host; falling back to a well-known shell", sh)
	}
	for _, c := range candidates {
		if isExecutableFile(c) {
			return c
		}
	}
	return fallback
}

// isExecutableFile reports whether path refers to a regular,
// executable file. Symlinks are followed (os.Stat).
func isExecutableFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !st.Mode().IsRegular() {
		return false
	}
	// Any execute bit is enough; the kernel will tell us later if we
	// can't actually run it as the current user.
	return st.Mode().Perm()&0o111 != 0
}
