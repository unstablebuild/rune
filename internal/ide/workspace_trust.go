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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/ideauthorizer"
)

const workspaceTrustPartition = "workspace-trust"

// The answers to the workspace trust prompt, as indices of
// workspaceTrustOptions.
const (
	workspaceTrustPersist = iota
	workspaceTrustOnce
	workspaceTrustIgnore
)

var workspaceTrustOptions = []string{"   trust    ", " trust once ", "   ignore   "}

// workspaceTrustPrompt shows message with options and blocks until the
// user picks one, returning its index. It returns context.Canceled when the
// user dismisses the prompt. Any other error, or ctx ending, means the user
// made no decision.
type workspaceTrustPrompt func(ctx context.Context, message string, options []string) (int, error)

type workspaceTrustRecord struct {
	Workspace   string
	Fingerprint string
}

// workspaceTrust decides whether a workspace's config may apply. The user
// trusts the exact contents of the file in one workspace, so any change to
// it, or the same file in another workspace, asks again.
type workspaceTrust struct {
	storage storageapi.Service
	builtin map[string]struct{}
	prompt  workspaceTrustPrompt

	mu sync.Mutex
	// session holds the answers that last until Rune exits, keyed by
	// workspaceTrustSessionKey.
	session map[string]ideauthorizer.WorkspaceConfigDecision
}

var _ ideauthorizer.WorkspaceConfigTrust = (*workspaceTrust)(nil)

// newWorkspaceTrust returns a workspaceTrust that persists trust in
// storage and trusts without asking any contents whose
// workspaceConfigFingerprint is in builtin.
func newWorkspaceTrust(
	storage storageapi.Service, builtin map[string]struct{}, prompt workspaceTrustPrompt,
) *workspaceTrust {
	return &workspaceTrust{
		storage: storage,
		builtin: builtin,
		prompt:  prompt,
		session: make(map[string]ideauthorizer.WorkspaceConfigDecision),
	}
}

func workspaceConfigFingerprint(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func workspaceTrustSessionKey(uri workspaceapi.URI, fingerprint string) string {
	return uri.String() + "\x00" + fingerprint
}

// trusted reports whether contents, read from filename in the workspace
// uri, may apply, asking the user when no earlier answer covers them. An
// error means the user made no decision, which is not remembered.
func (t *workspaceTrust) trusted(
	ctx context.Context, uri workspaceapi.URI, filename string, contents []byte,
) (bool, error) {
	fingerprint := workspaceConfigFingerprint(contents)
	if _, ok := t.builtin[fingerprint]; ok {
		return true, nil
	}
	sessionKey := workspaceTrustSessionKey(uri, fingerprint)
	t.mu.Lock()
	decision, ok := t.session[sessionKey]
	t.mu.Unlock()
	if ok {
		return decision.Trusted, nil
	}

	id := uri.String()
	var record workspaceTrustRecord
	switch err := t.storage.Get(ctx, id, &record); {
	case err == nil && record.Fingerprint == fingerprint:
		return true, nil
	case err != nil && !errors.Is(err, storageapi.ErrNotFound):
		return false, fmt.Errorf("read workspace trust: %w", err)
	}

	location := workspaceapi.Join(uri, filename).String()
	answer, err := t.prompt(ctx, workspaceTrustMessage(location, contents), workspaceTrustOptions)
	switch {
	case ctx.Err() != nil:
		return false, ctx.Err()
	case errors.Is(err, context.Canceled):
		answer = workspaceTrustIgnore
	case err != nil:
		return false, err
	}

	decision = ideauthorizer.WorkspaceConfigDecision{
		Workspace:   uri,
		Fingerprint: fingerprint,
		Trusted:     answer == workspaceTrustPersist || answer == workspaceTrustOnce,
	}
	if answer == workspaceTrustPersist {
		err := t.storage.Set(ctx, id, workspaceTrustRecord{Workspace: id, Fingerprint: fingerprint})
		if err == nil {
			return true, nil
		}
		// The user still trusted it, so it holds as if trusted once.
		log.Warnf("remember trust of %s: %v", location, err)
	}
	t.mu.Lock()
	t.session[sessionKey] = decision
	t.mu.Unlock()
	return decision.Trusted, nil
}

// WorkspaceConfigDecisions returns the persisted trust and every answer
// of this session.
func (t *workspaceTrust) WorkspaceConfigDecisions(
	ctx context.Context,
) ([]ideauthorizer.WorkspaceConfigDecision, error) {
	it, err := t.storage.List(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list workspace trust: %w", err)
	}
	defer it.Close()
	var ret []ideauthorizer.WorkspaceConfigDecision
	for it.HasNext() {
		var record workspaceTrustRecord
		if err := it.NextTo(&record); err != nil {
			return nil, fmt.Errorf("read workspace trust: %w", err)
		}
		uri, err := workspaceapi.ParseURI(record.Workspace)
		if err != nil {
			log.Warnf("skip workspace trust of %q: %v", record.Workspace, err)
			continue
		}
		ret = append(ret, ideauthorizer.WorkspaceConfigDecision{
			Workspace:   uri,
			Fingerprint: record.Fingerprint,
			Trusted:     true,
			Persisted:   true,
		})
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, d := range t.session {
		ret = append(ret, d)
	}
	return ret, nil
}

// RevokeWorkspaceConfigDecision implements
// ideauthorizer.WorkspaceConfigTrust.
func (t *workspaceTrust) RevokeWorkspaceConfigDecision(
	ctx context.Context, d ideauthorizer.WorkspaceConfigDecision,
) error {
	if !d.Persisted {
		t.mu.Lock()
		delete(t.session, workspaceTrustSessionKey(d.Workspace, d.Fingerprint))
		t.mu.Unlock()
		return nil
	}
	id := d.Workspace.String()
	var record workspaceTrustRecord
	switch err := t.storage.Get(ctx, id, &record); {
	case errors.Is(err, storageapi.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("read workspace trust: %w", err)
	case record.Fingerprint != d.Fingerprint:
		return nil
	}
	if err := t.storage.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete workspace trust: %w", err)
	}
	return nil
}

func workspaceTrustMessage(location string, contents []byte) string {
	display := workspaceConfigDisplay(contents)
	lines := strings.Count(display, "\n")
	if display != "" && !strings.HasSuffix(display, "\n") {
		lines++
		display += "\n"
	}
	lineCount := fmt.Sprintf("%d lines", lines)
	if lines == 1 {
		lineCount = "1 line"
	}
	fence := strings.Repeat("`", max(3, longestBacktickRun(display)+1))
	return fmt.Sprintf(`## 🚨 Trust this workspace's config?

%s (%s) changes your Rune settings for this workspace. A workspace config can
run programs on your machine (extensions, language servers, aliases, terminal
shell, …), so only trust it if you trust whoever can change this repository.

%syaml
%s%s

**ignore** opens the workspace with only your own config. Rune asks again
whenever this file changes.`,
		markdownCodeSpan(workspaceConfigDisplay([]byte(location))), lineCount,
		fence, display, fence)
}

// workspaceConfigDisplay renders contents so that what the user reads is
// what YAML parses: characters that are invisible, or that reorder or
// break lines on screen, are spelled out as escapes.
func workspaceConfigDisplay(contents []byte) string {
	s := strings.ReplaceAll(string(contents), "\r\n", "\n")
	var b strings.Builder
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x{%02X}`, s[0])
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r), unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp):
			fmt.Fprintf(&b, `\u{%04X}`, r)
		default:
			b.WriteString(s[:size])
		}
		s = s[size:]
	}
	return b.String()
}

func longestBacktickRun(s string) int {
	longest, run := 0, 0
	for _, r := range s {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	return longest
}

// markdownCodeSpan renders s as inline code that no backtick in s can end
// early.
func markdownCodeSpan(s string) string {
	fence := strings.Repeat("`", longestBacktickRun(s)+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}
