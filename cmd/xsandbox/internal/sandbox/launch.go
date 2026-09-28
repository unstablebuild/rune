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

package sandbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	sdkhandler "github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/xsandbox/internal/record"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/extension/extensionv2"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/localstorage"
	"unstable.build/rune/internal/workspace"
)

// ensureLaunched builds the extension host and starts the extension
// the first time a run-phase spec builtin executes. All later calls
// return the first launch outcome.
func (s *sandbox) ensureLaunched() error {
	s.mu.Lock()
	if s.launched {
		err := s.launchErr
		s.mu.Unlock()
		return err
	}
	s.launched = true
	s.mu.Unlock()

	err := s.launch()
	s.mu.Lock()
	s.launchErr = err
	s.mu.Unlock()
	return err
}

func (s *sandbox) launch() error {
	if s.extensionID == "" {
		return errors.New("spec must call expect_metadata(id=...) before " +
			"expectations or actions")
	}

	uri, err := workspaceapi.ParseURI("file://" + s.workspaceDir)
	if err != nil {
		return fmt.Errorf("parse workspace uri: %w", err)
	}
	scheme, err := workspace.NewFileScheme(s.ctx, config.MapConfig(map[string]any{}), uri)
	if err != nil {
		return fmt.Errorf("new file scheme: %w", err)
	}
	s.scheme = scheme

	stderrPath := filepath.Join(s.dataDir, "extension-stderr.log")
	s.stderrLog, err = os.OpenFile(stderrPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open extension stderr log: %w", err)
	}
	var stderr io.Writer = s.stderrLog
	if s.opts.Verbose {
		stderr = io.MultiWriter(s.stderrLog, s.opts.Log)
	}
	s.exec = newTeeExecutor(scheme, stderr)
	go debug.CapturePanicReport(func() {
		select {
		case <-s.exec.exitCh:
			// Fail blocked expectations fast unless the sandbox is the
			// one stopping the extension.
			s.mu.Lock()
			stopping := s.stopping
			s.mu.Unlock()
			if !stopping {
				s.cancelWait()
			}
		case <-s.ctx.Done():
		}
	})

	prompt := autoPromptOpener{}
	storage := storagestub.NewInMemoryService()
	tick := func(fn func()) bool { fn(); return true }
	trust := pkgtrust.NewStore(s.dataDir, nil)
	authorizer, err := ideauthorizer.NewAuthorizer(
		s.editor, prompt, storage, tick, nil, trust, ideauthorizer.Config{
			AutoAuthorizeExtensions: true,
			AutoAuthorizeCommands:   true,
		})
	if err != nil {
		return fmt.Errorf("new authorizer: %w", err)
	}

	cwd := workspace.NewSchemeWorkspace(uri, scheme, tick)
	res := s.browser.resources()
	res = extension.MergeResourceMap(res, s.editor.resources(s.browser))
	res = extension.MergeResourceMap(res,
		extension.WorkspaceResources(cwd, authorizer))
	s.extStorage = localstorage.New(s.ctx,
		filepath.Join(s.dataDir, "extensions"), docbson.Marshaler())
	res = extension.MergeResourceMap(res, extension.StorageResources(s.extStorage))
	res = extension.MergeResourceMap(res,
		extension.ConfigResources(config.MapConfig(s.cfg)))
	res = extension.MergeResourceMap(res, extension.SyntaxResources(stubParser{}))
	res = extension.MergeResourceMap(res, extension.SemanticResources(stubLSP{}))
	res = extension.MergeResourceMap(res, extension.DebugResources(stubDebugger{}))
	res = extension.MergeResourceMap(res, extension.LLMResources(stubLLM{}))

	runnerOpts := []extensionv2.Option{
		extensionv2.WithServerInterceptors(
			s.recorder.StreamInterceptor(), s.recorder.UnaryInterceptor()),
	}
	if s.opts.InsecureTransport {
		runnerOpts = append(runnerOpts, extensionv2.WithInsecureTransport())
	}
	if s.opts.InsecureAuth {
		runnerOpts = append(runnerOpts, extensionv2.WithInsecureAuth())
	}
	base, err := extensionv2.NewRunner(s.ctx, s.locker, s.dataDir, runnerOpts...)
	if err != nil {
		return fmt.Errorf("new extension runner: %w", err)
	}
	runner, err := base.WorkspaceExtensionsRunner(
		uri, res, authorizer, nopTrustVerifier{}, s.dataDir, s.dataDir, s.browser,
		scheme, s.exec, s.grantor, s.editor, prompt, storage, tick)
	if err != nil {
		return fmt.Errorf("new workspace extensions runner: %w", err)
	}
	s.runner = runner

	// The runner resolves relative paths against the workspace dir;
	// resolve the binary against the sandbox caller's cwd instead.
	argv := append([]string(nil), s.opts.ExtensionArgv...)
	if strings.Contains(argv[0], string(os.PathSeparator)) {
		abs, err := filepath.Abs(argv[0])
		if err != nil {
			return fmt.Errorf("resolve extension path %q: %w", argv[0], err)
		}
		argv[0] = abs
	}
	cmdAndArgs := strings.Join(argv, " ")
	start := time.Now()
	err = runner.Run(s.extensionID, cmdAndArgs, config.MapConfig(s.cfg))
	if err == nil {
		err = runner.WaitReady(s.ctx, s.extensionID)
	}
	s.mu.Lock()
	s.handshake = time.Since(start)
	s.mu.Unlock()
	if err != nil {
		if detail := s.grantor.failureDetail(); detail != "" {
			err = fmt.Errorf("%w: %s", err, detail)
		}
		return s.finishExpectation("handshake", start, err)
	}
	return s.finishExpectation("handshake", start, nil)
}

// shutdown terminates the extension (SIGTERM, grace period, SIGKILL)
// and tears down the host. It reports whether the extension had
// already exited on its own (a crash) and its exit status.
func (s *sandbox) shutdown() (crashed bool, exitDetail string) {
	s.mu.Lock()
	launched := s.launched && s.launchErr == nil
	s.stopping = true
	s.mu.Unlock()

	if s.exec != nil {
		if exitErr, exited := s.exec.exitStatus(); exited {
			crashed = true
			exitDetail = exitString(exitErr)
		} else if pid, ok := s.exec.processPid(); ok && launched {
			_ = s.scheme.Signal(pid, syscall.SIGTERM)
			if !s.exec.waitExit(s.opts.Grace) {
				fmt.Fprintf(s.opts.Log, "extension did not exit after SIGTERM; killing\n")
				_ = s.scheme.Signal(pid, syscall.SIGKILL)
				_ = s.exec.waitExit(s.opts.Grace)
			}
			if exitErr, exited := s.exec.exitStatus(); exited {
				exitDetail = exitString(exitErr)
			} else {
				exitDetail = "did not exit"
			}
		}
	}
	if s.runner != nil {
		_ = s.runner.Close()
	}
	if s.extStorage != nil {
		_ = s.extStorage.Close()
	}
	if s.scheme != nil {
		_ = s.scheme.Close()
	}
	if s.stderrLog != nil {
		_ = s.stderrLog.Close()
	}
	s.cancelWait()
	return crashed, exitDetail
}

func (s *sandbox) buildReport(pass bool, exitDetail string, start time.Time) *record.Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !pass {
		s.keepDirs = true
	}
	return &record.Report{
		Pass:           pass,
		ExtensionCmd:   strings.Join(s.opts.ExtensionArgv, " "),
		ExtensionExit:  exitDetail,
		Metadata:       s.grantor.observedMap(),
		HandshakeMS:    float64(s.handshake) / float64(time.Millisecond),
		WallTimeMS:     float64(time.Since(start)) / float64(time.Millisecond),
		Expectations:   s.expectations,
		UnexpectedRPCs: s.unexpected,
		Methods:        s.recorder.Stats(),
	}
}

// recordingGrantor validates the metadata the extension sends during
// the stdio handshake against the spec's expect_metadata declaration
// and keeps the observed metadata for the report.
type recordingGrantor struct {
	mu            sync.Mutex
	expectedID    string
	expectedPerms []string
	observed      *extensionapi.Metadata
	detail        string
}

var _ extension.Grantor = (*recordingGrantor)(nil)

func (g *recordingGrantor) expect(id string, perms []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expectedID = id
	g.expectedPerms = perms
}

// Grant satisfies extension.Grantor. The protocol layer already
// validates the extension id and required fields; Grant additionally
// enforces the exact permission set when the spec declares one.
func (g *recordingGrantor) Grant(meta extensionapi.Metadata, _ string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observed = &meta
	if g.expectedPerms == nil {
		return true, nil
	}
	want := make(map[string]bool, len(g.expectedPerms))
	for _, p := range g.expectedPerms {
		want[p] = true
	}
	var missing, extra []string
	for p := range want {
		if _, ok := meta.Permissions[extensionapi.Permission(p)]; !ok {
			missing = append(missing, p)
		}
	}
	for p := range meta.Permissions {
		if !want[string(p)] {
			extra = append(extra, string(p))
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return true, nil
	}
	sort.Strings(missing)
	sort.Strings(extra)
	g.detail = fmt.Sprintf(
		"metadata permissions mismatch: missing=%v extraneous=%v", missing, extra)
	// Deny with a nil error: the protocol reports "permission denied"
	// and the sandbox appends the recorded detail, avoiding the same
	// message twice in the handshake failure.
	return false, nil
}

func (g *recordingGrantor) failureDetail() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.detail
}

func (g *recordingGrantor) observedMap() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.observed == nil {
		return nil
	}
	perms := make([]string, 0, len(g.observed.Permissions))
	for p := range g.observed.Permissions {
		perms = append(perms, string(p))
	}
	sort.Strings(perms)
	return map[string]any{
		"developer_id":    g.observed.DeveloperID,
		"developer_email": g.observed.DeveloperEmail,
		"id":              g.observed.ExtensionID,
		"name":            g.observed.ExtensionName,
		"version":         g.observed.ExtensionVersion,
		"permissions":     perms,
	}
}

// autoPromptOpener answers permission prompts by selecting "yes" (or
// the first option) so the sandbox never blocks on interactive UI.
type autoPromptOpener struct{}

var _ ideauthorizer.PromptOpener = autoPromptOpener{}

func (autoPromptOpener) Prompt(
	_ string, options []string, _ []term.KeyComb,
	promptHandler sdkhandler.PromptHandler,
) browser.Window {
	for i, opt := range options {
		if opt == ideauthorizer.PromptOptionYes {
			promptHandler.OnSelect(i, opt)
			return browsertest.NopWindow()
		}
	}
	if len(options) > 0 {
		promptHandler.OnSelect(0, options[0])
	}
	return browsertest.NopWindow()
}

// nopProgress discards REPL progress updates.
type nopProgress struct{}

func (nopProgress) Progress(int64, int64, string) {}

// nopTrustVerifier trusts no extension entrypoint: the sandbox exercises
// unverified extensions, so verified-publisher shortcuts never apply.
type nopTrustVerifier struct{}

func (nopTrustVerifier) VerifyExtensionEntrypoint(string) (string, bool) { return "", false }

var eventTypes = map[string]textapi.EventType{
	"open":      textapi.EventTypeOpen,
	"close":     textapi.EventTypeClose,
	"flush":     textapi.EventTypeFlush,
	"create":    textapi.EventTypeCreate,
	"change":    textapi.EventTypeChange,
	"remove":    textapi.EventTypeRemove,
	"rename":    textapi.EventTypeRename,
	"edit":      textapi.EventTypeEdit,
	"scroll":    textapi.EventTypeScroll,
	"hidden":    textapi.EventTypeHidden,
	"visible":   textapi.EventTypeVisible,
	"focus":     textapi.EventTypeFocus,
	"unfocus":   textapi.EventTypeUnfocus,
	"cursor":    textapi.EventTypeCursor,
	"selection": textapi.EventTypeSelection,
}

func knownEventTypes() []string {
	names := make([]string, 0, len(eventTypes))
	for name := range eventTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var _ schemeapi.Executor = (*teeExecutor)(nil)
