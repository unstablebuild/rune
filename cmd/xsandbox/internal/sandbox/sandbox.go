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

// Package sandbox impersonates the Rune editor's side of the
// extension protocol: it hosts the workspace gRPC services on a unix
// socket (reusing the production extensionv2 runner, TLS and per-RPC
// token auth), launches the extension under test, and executes the
// Starlark spec against the recorded RPC traffic.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"unstable.build/rune/cmd/xsandbox/internal/record"
	"unstable.build/rune/cmd/xsandbox/internal/spec"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/textrpc"
)

// Options configure a sandbox run.
type Options struct {
	// SpecSource is the Starlark spec.
	SpecSource []byte
	// SpecFilename appears in spec error positions.
	SpecFilename string
	// ExtensionArgv is the extension command and its arguments.
	ExtensionArgv []string
	// DataDir is the host data directory; a temp dir when empty.
	DataDir string
	// WorkspaceDir is the workspace root; a temp dir when empty.
	WorkspaceDir string
	// InsecureTransport disables TLS (debugging only).
	InsecureTransport bool
	// InsecureAuth disables per-RPC token auth (debugging only).
	InsecureAuth bool
	// Timeout bounds the whole run.
	Timeout time.Duration
	// ExpectTimeout is the default per-expectation timeout.
	ExpectTimeout time.Duration
	// Grace is how long to wait after SIGTERM before SIGKILL.
	Grace time.Duration
	// Verbose additionally streams the extension's stderr to Log.
	Verbose bool
	// Log receives sandbox diagnostics and spec print() output.
	Log io.Writer
}

func (o *Options) applyDefaults() {
	if o.SpecFilename == "" {
		o.SpecFilename = "spec.star"
	}
	if o.Timeout <= 0 {
		o.Timeout = 60 * time.Second
	}
	if o.ExpectTimeout <= 0 {
		o.ExpectTimeout = 10 * time.Second
	}
	if o.Grace <= 0 {
		o.Grace = 5 * time.Second
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
}

// Run executes the spec against the extension and returns the report.
// The returned error is non-nil when the spec failed, the extension
// crashed, or the sandbox could not be set up; the report is non-nil
// whenever the spec started evaluating.
func Run(ctx context.Context, opts Options) (*record.Report, error) {
	opts.applyDefaults()
	if len(opts.ExtensionArgv) == 0 {
		return nil, errors.New("extension command is required")
	}
	s, err := newSandbox(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer s.cleanup()

	start := time.Now()
	specErr := spec.Run(opts.SpecSource, s, spec.Options{
		Filename:       opts.SpecFilename,
		DefaultTimeout: opts.ExpectTimeout,
		Print: func(msg string) {
			fmt.Fprintf(opts.Log, "spec: %s\n", msg)
		},
	})
	crashed, exitDetail := s.shutdown()
	report := s.buildReport(specErr == nil && !crashed, exitDetail, start)
	if specErr != nil {
		return report, specErr
	}
	if crashed {
		return report, fmt.Errorf("extension exited unexpectedly: %s", exitDetail)
	}
	return report, nil
}

type sandbox struct {
	opts Options

	// ctx bounds the entire run; waitCtx additionally ends when the
	// extension exits so blocked expectations fail fast.
	ctx        context.Context
	cancelCtx  context.CancelFunc
	waitCtx    context.Context
	cancelWait context.CancelFunc

	recorder *record.Recorder
	editor   *recordingEditor
	grantor  *recordingGrantor
	locker   *sync.Mutex
	browser  *browserHost

	dataDir      string
	workspaceDir string
	tempDirs     []string
	keepDirs     bool

	mu           sync.Mutex
	cfg          map[string]any
	launched     bool
	launchErr    error
	stopping     bool
	expectations []record.ExpectationResult
	unexpected   []record.Snapshot
	handshake    time.Duration

	extensionID string
	exec        *teeExecutor
	scheme      interface {
		Signal(workspaceapi.Pid, syscall.Signal) error
		io.Closer
	}
	runner     extension.Runner
	extStorage storageapi.Service
	stderrLog  *os.File
}

var _ spec.Host = (*sandbox)(nil)

func newSandbox(ctx context.Context, opts Options) (*sandbox, error) {
	s := &sandbox{
		opts:     opts,
		recorder: record.NewRecorder(),
		editor:   newRecordingEditor(),
		grantor:  &recordingGrantor{},
		locker:   new(sync.Mutex),
		cfg:      map[string]any{},
		browser:  newBrowserHost(defaultRenderWidth, defaultRenderHeight),
	}
	s.ctx, s.cancelCtx = context.WithTimeout(ctx, opts.Timeout)
	s.waitCtx, s.cancelWait = context.WithCancel(s.ctx)

	var err error
	s.dataDir, err = s.ensureDir(opts.DataDir, "xsandbox-data-*")
	if err != nil {
		return nil, err
	}
	s.workspaceDir, err = s.ensureDir(opts.WorkspaceDir, "xsandbox-workspace-*")
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *sandbox) ensureDir(dir, tempPattern string) (string, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", dir, err)
		}
		return dir, nil
	}
	tmp, err := os.MkdirTemp("", tempPattern)
	if err != nil {
		return "", fmt.Errorf("mkdtemp: %w", err)
	}
	s.tempDirs = append(s.tempDirs, tmp)
	return tmp, nil
}

func (s *sandbox) cleanup() {
	s.cancelCtx()
	if s.keepDirs {
		for _, dir := range s.tempDirs {
			fmt.Fprintf(s.opts.Log, "keeping %s for inspection\n", dir)
		}
		return
	}
	for _, dir := range s.tempDirs {
		_ = os.RemoveAll(dir)
	}
}

// ExpectMetadata satisfies spec.Host.
func (s *sandbox) ExpectMetadata(id string, permissions []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.launched {
		return errors.New("expect_metadata must be called before any expectation or action")
	}
	s.extensionID = id
	s.grantor.expect(id, permissions)
	return nil
}

// SetConfig satisfies spec.Host.
func (s *sandbox) SetConfig(cfg map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.launched {
		return errors.New("config must be called before any expectation or action")
	}
	s.cfg = cfg
	return nil
}

// FSWrite satisfies spec.Host.
func (s *sandbox) FSWrite(path, content string) error {
	if !filepath.IsLocal(path) {
		return fmt.Errorf("fs.write path %q must be relative to the workspace root", path)
	}
	dst := filepath.Join(s.workspaceDir, path)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("fs.write %s: %w", path, err)
	}
	if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
		return fmt.Errorf("fs.write %s: %w", path, err)
	}
	return nil
}

// StubRPC satisfies spec.Host.
func (s *sandbox) StubRPC(method string, where *record.Matcher, resp *record.Response) error {
	s.recorder.AddResponder(method, where, resp)
	return nil
}

// ExpectRPC satisfies spec.Host.
func (s *sandbox) ExpectRPC(exp spec.RPCExpectation) error {
	name := "expect_rpc " + record.NormalizeMethod(exp.Method)
	if exp.Respond != nil {
		s.recorder.AddResponder(exp.Method, exp.Where, exp.Respond)
	}
	if err := s.ensureLaunched(); err != nil {
		return err
	}
	start := time.Now()
	_, err := s.recorder.WaitMatch(s.waitCtx, exp.Method, exp.Where, exp.Timeout)
	return s.finishExpectation(name, start, err)
}

// ExpectCommand satisfies spec.Host.
func (s *sandbox) ExpectCommand(name string, repl bool, timeout time.Duration) error {
	expName := "expect_command " + name
	if repl {
		expName = "expect_repl_command " + name
	}
	if err := s.ensureLaunched(); err != nil {
		return err
	}
	start := time.Now()
	_, err := s.editor.waitRegistered(name, repl, timeout, s.waitCtx.Done())
	return s.finishExpectation(expName, start, err)
}

// InvokeCommand satisfies spec.Host.
func (s *sandbox) InvokeCommand(
	name string, args []string, repl bool, timeout time.Duration,
) error {
	expName := fmt.Sprintf("invoke_command %s %v", name, args)
	if repl {
		expName = fmt.Sprintf("invoke_repl_command %s %v", name, args)
	}
	if err := s.ensureLaunched(); err != nil {
		return err
	}
	start := time.Now()
	err := s.invoke(name, args, repl, timeout)
	return s.finishExpectation(expName, start, err)
}

func (s *sandbox) invoke(name string, args []string, repl bool, timeout time.Duration) error {
	h, ok := s.editor.lookup(name, repl)
	if !ok {
		return fmt.Errorf("command %q is not registered (registered: %v)",
			name, s.editor.registeredNames(repl))
	}
	ctx, cancel := context.WithTimeout(s.waitCtx, timeout)
	defer cancel()
	if repl {
		return s.invokeREPL(ctx, h.(textapi.REPLHandler), name, args)
	}
	handler := h.(text.CommandHandler)
	waiter := &textrpc.Waiter{Ch: make(chan error, 1)}
	ctx = textrpc.ContextWithWaiter(ctx, waiter)
	s.locker.Lock()
	err := handler.HandleCommand(ctx, textapi.Command{
		Name: name, Args: args, Window: s.browser.focusedWindow(),
	})
	s.locker.Unlock()
	if err != nil {
		return fmt.Errorf("dispatch command %q: %w", name, err)
	}
	if !waiter.Claimed {
		return nil
	}
	select {
	case err := <-waiter.Ch:
		if err != nil {
			return fmt.Errorf("command %q failed: %w", name, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("command %q did not complete: %w", name, ctx.Err())
	}
}

func (s *sandbox) invokeREPL(
	ctx context.Context, h textapi.REPLHandler, name string, args []string,
) error {
	iter, err := h.HandleCommand(ctx, repl.Command{Name: name, Args: args}, nopProgress{})
	if err != nil {
		return fmt.Errorf("dispatch repl command %q: %w", name, err)
	}
	if iter == nil {
		return nil
	}
	for {
		_, ok := iter.Next(ctx)
		if !ok {
			break
		}
	}
	return ctx.Err()
}

// ExpectResourceOpener satisfies spec.Host.
func (s *sandbox) ExpectResourceOpener(scheme string, timeout time.Duration) error {
	if err := s.ensureLaunched(); err != nil {
		return err
	}
	start := time.Now()
	_, err := s.editor.waitOpener(scheme, timeout, s.waitCtx.Done())
	return s.finishExpectation("expect_resource_opener "+scheme, start, err)
}

// OpenResource satisfies spec.Host.
func (s *sandbox) OpenResource(scheme, uri string, timeout time.Duration) error {
	if err := s.ensureLaunched(); err != nil {
		return err
	}
	start := time.Now()
	err := s.openResource(scheme, uri, timeout)
	return s.finishExpectation("open_resource "+uri, start, err)
}

func (s *sandbox) openResource(scheme, rawURI string, timeout time.Duration) error {
	uri, err := workspaceapi.ParseURI(rawURI)
	if err != nil {
		return fmt.Errorf("parse uri: %w", err)
	}
	if uri.Scheme() != scheme {
		return fmt.Errorf("uri %q does not have scheme %q", rawURI, scheme)
	}
	h, ok := s.editor.lookupOpener(scheme)
	if !ok {
		return fmt.Errorf("no resource opener for %q is registered (registered: %v)",
			scheme, s.editor.openerSchemes())
	}
	ctx, cancel := context.WithTimeout(s.waitCtx, timeout)
	defer cancel()
	// Unlike a command dispatch, this waits for the extension without
	// s.locker held, as Rune does: the opener runs off the event loop.
	content, err := h.OpenResource(ctx, uri)
	if err != nil {
		return fmt.Errorf("open %s: %w", rawURI, err)
	}
	if err := s.browser.showResource(uri, content); err != nil {
		_ = content.Close()
		return fmt.Errorf("show %s: %w", rawURI, err)
	}
	return nil
}

// PublishEvent satisfies spec.Host.
func (s *sandbox) PublishEvent(evType, uri, content string) error {
	if err := s.ensureLaunched(); err != nil {
		return err
	}
	t, ok := eventTypes[evType]
	if !ok {
		return fmt.Errorf("unknown event type %q (known: %v)", evType, knownEventTypes())
	}
	ev := textapi.Event{Type: t, Content: content}
	if uri != "" {
		parsed, err := workspaceapi.ParseURI(uri)
		if err != nil {
			return fmt.Errorf("publish_event uri: %w", err)
		}
		ev.URI = parsed
	}
	s.locker.Lock()
	s.editor.Handle(s.waitCtx, ev)
	s.locker.Unlock()
	return nil
}

// WaitIdle satisfies spec.Host.
func (s *sandbox) WaitIdle(d time.Duration) error {
	if err := s.ensureLaunched(); err != nil {
		return err
	}
	start := time.Now()
	err := s.recorder.WaitIdle(s.waitCtx, d)
	return s.finishExpectation("wait_idle "+d.String(), start, err)
}

// AssertNoUnexpectedRPCs satisfies spec.Host.
func (s *sandbox) AssertNoUnexpectedRPCs(ignore []string) error {
	if err := s.ensureLaunched(); err != nil {
		return err
	}
	start := time.Now()
	leftover := s.recorder.Unconsumed(ignore)
	var err error
	if len(leftover) > 0 {
		s.mu.Lock()
		s.unexpected = leftover
		s.mu.Unlock()
		var b strings.Builder
		fmt.Fprintf(&b, "%d unexpected rpc(s):", len(leftover))
		for _, ev := range leftover {
			fmt.Fprintf(&b, "\n  %s", ev.Method)
		}
		err = errors.New(b.String())
	}
	return s.finishExpectation("assert_no_unexpected_rpcs", start, err)
}

// ExpectWindow satisfies spec.Host. It blocks until an install RPC for
// method (Split, Bar, Tab, Floating, Open) is recorded, guaranteeing
// the handler is installed into the headless browser before render or
// send_key drive it.
func (s *sandbox) ExpectWindow(method string, timeout time.Duration) (string, error) {
	name := "expect_window " + record.NormalizeMethod(method)
	if err := s.ensureLaunched(); err != nil {
		return "", err
	}
	start := time.Now()
	err := s.recorder.WaitObserved(s.waitCtx, method, timeout)
	if err := s.finishExpectation(name, start, err); err != nil {
		return "", err
	}
	return record.NormalizeMethod(method), nil
}

// RenderWindow satisfies spec.Host.
func (s *sandbox) RenderWindow(_ string, width, height int) (string, error) {
	if err := s.ensureLaunched(); err != nil {
		return "", err
	}
	return s.browser.render(width, height), nil
}

// SendKey satisfies spec.Host.
func (s *sandbox) SendKey(_, keys string) (handled, quit bool, err error) {
	if err := s.ensureLaunched(); err != nil {
		return false, false, err
	}
	return s.browser.sendKeys(keys)
}

// finishExpectation records the expectation outcome and decorates
// failures with the extension's exit status when it died mid-wait.
func (s *sandbox) finishExpectation(name string, start time.Time, err error) error {
	if err != nil && s.exec != nil {
		if exitErr, exited := s.exec.exitStatus(); exited {
			err = fmt.Errorf("%w (extension exited: %s)", err, exitString(exitErr))
		}
	}
	res := record.ExpectationResult{
		Name:   name,
		Pass:   err == nil,
		WaitMS: float64(time.Since(start)) / float64(time.Millisecond),
	}
	if err != nil {
		res.Detail = err.Error()
	}
	s.mu.Lock()
	s.expectations = append(s.expectations, res)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func exitString(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}
