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

package ideauthorizer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/text"
)

const (
	authorizerREPLCommand            = "authorizer"
	authorizerREPLCommandList        = "list"
	authorizerREPLCommandRevoke      = "revoke"
	authorizerREPLCommandExecutables = "executables"
)

// WorkspaceConfigDecision is the user's answer to whether a workspace's
// config may apply.
type WorkspaceConfigDecision struct {
	Workspace workspaceapi.URI
	// Fingerprint identifies the exact contents of the workspace config
	// the decision covers.
	Fingerprint string
	// Trusted reports whether those contents apply.
	Trusted bool
	// Persisted reports whether the decision outlives the Rune session.
	Persisted bool
}

// WorkspaceConfigTrust holds the user's workspace config trust decisions.
type WorkspaceConfigTrust interface {
	WorkspaceConfigDecisions(ctx context.Context) ([]WorkspaceConfigDecision, error)
	// RevokeWorkspaceConfigDecision forgets d, so the workspace asks again
	// the next time it opens. A workspace that is already open keeps the
	// config it opened with. Revoking a decision that no longer holds,
	// such as trust the user has since replaced, changes nothing.
	RevokeWorkspaceConfigDecision(ctx context.Context, d WorkspaceConfigDecision) error
}

func registerAuthorizerREPLCommand(
	editor text.Editor, authorizer *Authorizer,
) error {
	return editor.RegisterREPLCommand(textapi.CommandManual{
		Name:     authorizerREPLCommand,
		Summary:  "Manage extension permission and workspace config trust decisions.",
		Synopsis: "(list|revoke|executables) [<decision>]",
		Commands: []textapi.CommandManual{
			{
				Name:    authorizerREPLCommandList,
				Summary: "List extension permission and workspace config trust decisions.",
			},
			{
				Name:     authorizerREPLCommandRevoke,
				Summary:  "Revoke a decision, by ID or command scope, so Rune asks again.",
				Synopsis: "<decision>",
			},
			{
				Name:    authorizerREPLCommandExecutables,
				Summary: "List the executables approved commands run, with their fingerprints.",
			},
		},
	}, authorizerREPLHandler{authorizer: authorizer})
}

type authorizerREPLHandler struct {
	authorizer *Authorizer
}

func (h authorizerREPLHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return authorizerREPLHelp(), nil
	}
	switch cmd.Args[0] {
	case authorizerREPLCommandList:
		return h.handleList(ctx)
	case authorizerREPLCommandRevoke:
		return h.handleRevoke(ctx, cmd.Args[1:])
	case authorizerREPLCommandExecutables:
		return h.handleExecutables(ctx)
	default:
		return nil, fmt.Errorf("unknown authorizer subcommand %q", cmd.Args[0])
	}
}

func (h authorizerREPLHandler) Complete(
	ctx context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	subcommands := []string{
		authorizerREPLCommandList,
		authorizerREPLCommandRevoke,
		authorizerREPLCommandExecutables,
	}
	if len(args) == 0 {
		return iterator.FromSlice(subcommands), nil
	}
	if len(args) == 1 {
		var ret []string
		for _, subcommand := range subcommands {
			if strings.HasPrefix(subcommand, args[0]) {
				ret = append(ret, subcommand)
			}
		}
		return iterator.FromSlice(ret), nil
	}
	if args[0] != authorizerREPLCommandRevoke {
		return iterator.Empty[string](), nil
	}
	entries, err := h.authorizer.storedDecisions(ctx)
	if err != nil {
		return nil, err
	}
	decisions, err := h.workspaceConfigDecisions(ctx)
	if err != nil {
		return nil, err
	}
	prefix := args[len(args)-1]
	matches := func(id string) bool {
		return strings.HasPrefix(id, prefix) || strings.Contains(id, ":"+prefix)
	}
	var ret []string
	for _, entry := range entries {
		id := entry.ID()
		if matches(id) || strings.HasPrefix(entry.Key, prefix) {
			ret = append(ret, id)
		}
	}
	for _, d := range decisions {
		if id := workspaceConfigDecisionID(d); matches(id) {
			ret = append(ret, id)
		}
	}
	return iterator.FromSlice(ret), nil
}

func (h authorizerREPLHandler) Help(
	context.Context, []string,
) (iterator.Iterator[component.Responsive], error) {
	return authorizerREPLHelp(), nil
}

func (h authorizerREPLHandler) handleList(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	entries, err := h.authorizer.permissionDecisions(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	var lines []string
	if len(entries) == 0 {
		lines = append(lines, "No extension permission decisions.")
	} else {
		lines = append(lines, "Extension permission decisions:")
		for _, entry := range entries {
			lines = append(lines, fmt.Sprintf("- %s — %s", entry.ID(), entry.Display()))
		}
	}
	if h.authorizer.config.WorkspaceConfigs == nil {
		return authorizerREPLLines(lines...), nil
	}
	decisions, err := h.workspaceConfigDecisions(ctx)
	if err != nil {
		return nil, err
	}
	if len(decisions) == 0 {
		lines = append(lines, "No workspace config decisions.")
	} else {
		lines = append(lines, "Workspace config decisions:")
		for _, d := range decisions {
			lines = append(lines, fmt.Sprintf("- %s — %s",
				workspaceConfigDecisionID(d), workspaceConfigDecisionDisplay(d)))
		}
	}
	return authorizerREPLLines(lines...), nil
}

func (h authorizerREPLHandler) handleRevoke(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("expected permission to revoke")
	}
	selected := strings.Join(args, " ")
	entry, ok, err := h.authorizer.findStoredDecision(ctx, selected)
	if err != nil {
		return nil, err
	}
	if !ok {
		if d, ok, err := h.findWorkspaceConfigDecision(ctx, selected); err != nil {
			return nil, err
		} else if ok {
			if err := h.authorizer.config.WorkspaceConfigs.RevokeWorkspaceConfigDecision(
				ctx, d,
			); err != nil {
				return nil, fmt.Errorf("revoke workspace config decision: %w", err)
			}
			return authorizerREPLLines(fmt.Sprintf(
				"Revoked %s. Rune asks again the next time the workspace opens.",
				workspaceConfigDecisionDisplay(d))), nil
		}
		if isPermissionStorageKey(selected) {
			if err := h.authorizer.deleteStoredDecision(ctx, selected); err != nil {
				return nil, err
			}
			return authorizerREPLLines(fmt.Sprintf("Revoked %s", selected)), nil
		}
		return nil, fmt.Errorf("decision not found: %s", selected)
	}
	if err := h.authorizer.deleteStoredDecision(ctx, entry.Key); err != nil {
		return nil, err
	}
	return authorizerREPLLines(fmt.Sprintf("Revoked %s", entry.Display())), nil
}

func (h authorizerREPLHandler) handleExecutables(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	records, err := h.authorizer.storedExecutables(ctx)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return authorizerREPLLines("No approved executables."), nil
	}
	lines := []string{"Approved executables:"}
	for _, r := range records {
		name := r.Path
		if r.Host != "" {
			name = r.Host + ":" + r.Path
		}
		lines = append(lines, fmt.Sprintf("- %s sha256 %s size %d modified %s, approved %s",
			name, fingerprintPreview(r.SHA256), r.Size,
			r.ModTime.Format(time.RFC3339), r.Updated.Format(time.RFC3339)))
	}
	return authorizerREPLLines(lines...), nil
}

// workspaceConfigDecisions returns the workspace config decisions sorted
// for display, or none when the authorizer has no WorkspaceConfigs.
func (h authorizerREPLHandler) workspaceConfigDecisions(
	ctx context.Context,
) ([]WorkspaceConfigDecision, error) {
	trust := h.authorizer.config.WorkspaceConfigs
	if trust == nil {
		return nil, nil
	}
	decisions, err := trust.WorkspaceConfigDecisions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspace config decisions: %w", err)
	}
	sort.Slice(decisions, func(i, j int) bool {
		return workspaceConfigDecisionDisplay(decisions[i]) <
			workspaceConfigDecisionDisplay(decisions[j])
	})
	return decisions, nil
}

func (h authorizerREPLHandler) findWorkspaceConfigDecision(
	ctx context.Context, selected string,
) (WorkspaceConfigDecision, bool, error) {
	decisions, err := h.workspaceConfigDecisions(ctx)
	if err != nil {
		return WorkspaceConfigDecision{}, false, err
	}
	for _, d := range decisions {
		if selected == workspaceConfigDecisionID(d) ||
			selected == workspaceConfigDecisionDisplay(d) {
			return d, true, nil
		}
	}
	return WorkspaceConfigDecision{}, false, nil
}

func workspaceConfigDecisionScope(d WorkspaceConfigDecision) string {
	if d.Persisted {
		return "persisted"
	}
	return "transient"
}

func workspaceConfigDecisionVerb(d WorkspaceConfigDecision) string {
	if d.Trusted {
		return "trust"
	}
	return "ignore"
}

// workspaceConfigDecisionID is unique per workspace and config contents:
// workspaces that share a directory name, or one workspace's config
// before and after a change, get different IDs.
func workspaceConfigDecisionID(d WorkspaceConfigDecision) string {
	sum := sha256.Sum256([]byte(d.Workspace.String() + "\x00" + d.Fingerprint))
	return strings.Join([]string{
		workspaceConfigDecisionScope(d),
		workspaceConfigDecisionVerb(d),
		"workspace-config",
		sanitizePluginPermissionIDPart(programBaseName(d.Workspace.Path())),
		hex.EncodeToString(sum[:4]),
	}, ":")
}

func workspaceConfigDecisionDisplay(d WorkspaceConfigDecision) string {
	return fmt.Sprintf("%s %s %s sha256:%s",
		workspaceConfigDecisionScope(d), workspaceConfigDecisionVerb(d),
		d.Workspace.String(), d.Fingerprint[:min(8, len(d.Fingerprint))])
}

func authorizerREPLHelp() iterator.Iterator[component.Responsive] {
	return authorizerREPLLines(
		"Usage: authorizer <list|revoke|executables>",
		"Lists or revokes extension permission and workspace config trust decisions, "+
			"or lists the executables approved commands run.",
	)
}

func authorizerREPLLines(lines ...string) iterator.Iterator[component.Responsive] {
	out := make([]component.Responsive, len(lines))
	for i, line := range lines {
		out[i] = component.NewResponsiveString(line, component.StringResponsiveConfig{})
	}
	return iterator.FromSlice(out)
}

type pluginPermissionStoredDecisionEntry struct {
	Key        string
	Decision   string
	Path       string
	Args       []string
	Permission string
	Scope      string
	Expires    time.Time
	Command    *pluginPermissionCommandDetail
	// CommandScope is the commands a command decision covers, such as
	// "git commit *".
	CommandScope string
	Executables  []executableEntry
}

// executableEntry is an executable a command decision is bound to, with
// the fingerprint it was approved at, if any.
type executableEntry struct {
	executableRef
	SHA256 string
}

func (e pluginPermissionStoredDecisionEntry) Display() string {
	if e.Path == "" || e.Permission == "" {
		return e.Key
	}
	scope := e.Scope
	if scope == "" {
		scope = "persisted"
	}
	ret := fmt.Sprintf("%s %s %s %s %v", scope, e.Decision, e.Permission, e.Path, e.Args)
	switch {
	case e.CommandScope != "":
		ret += " cmd " + e.CommandScope
		exes := make([]string, len(e.Executables))
		for i, exe := range e.Executables {
			exes[i] = exe.Path
			if exe.SHA256 != "" {
				exes[i] += " sha256 " + fingerprintPreview(exe.SHA256)
			}
		}
		if len(exes) > 0 {
			ret += " ← " + strings.Join(exes, ", ")
		}
	case e.Command != nil:
		ret += " cmd " + pluginPermissionExactCommandLabel(*e.Command)
	}
	if !e.Expires.IsZero() {
		ret += fmt.Sprintf(" expires %s", e.Expires.Format(time.RFC3339))
	}
	return ret
}

func (e pluginPermissionStoredDecisionEntry) ID() string {
	if e.Key == "" {
		return ""
	}
	scope := e.Scope
	if scope == "" {
		scope = "persisted"
	}
	parts := []string{
		sanitizePluginPermissionIDPart(scope),
		sanitizePluginPermissionIDPart(e.Decision),
		sanitizePluginPermissionIDPart(e.Permission),
		sanitizePluginPermissionIDPart(programBaseName(e.Path)),
	}
	switch {
	case len(e.Executables) > 0:
		parts = append(parts,
			sanitizePluginPermissionIDPart(programBaseName(e.Executables[0].Path)))
	case e.Command != nil:
		parts = append(parts,
			sanitizePluginPermissionIDPart(programBaseName(e.Command.Path)))
	}
	parts = append(parts, shortPluginPermissionKey(e.Key))
	return strings.Join(parts, ":")
}

func sanitizePluginPermissionIDPart(part string) string {
	part = strings.ToLower(part)
	var b strings.Builder
	for _, r := range part {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	ret := strings.Trim(b.String(), "-")
	if ret == "" {
		return "unknown"
	}
	return ret
}

func programBaseName(path string) string {
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "unknown"
	}
	idx := strings.LastIndex(path, "/")
	if idx == -1 {
		return path
	}
	return path[idx+1:]
}

func shortPluginPermissionKey(key string) string {
	// Use the final ":"-delimited segment so that command-scoped keys —
	// which append a per-command identity hash after the program hash and
	// permission — disambiguate. Fall back to the program-hash segment
	// (index 2) for permission-only keys with no trailing segment.
	parts := strings.Split(key, ":")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "" {
			continue
		}
		// Skip the well-known prefix segments so we don't return e.g.
		// "plugin-permissions" for a malformed key.
		if i <= 1 {
			break
		}
		seg := parts[i]
		if len(seg) > 8 {
			seg = seg[:8]
		}
		return sanitizePluginPermissionIDPart(seg)
	}
	if len(key) <= 8 {
		return sanitizePluginPermissionIDPart(key)
	}
	return sanitizePluginPermissionIDPart(key[:8])
}

func (a *Authorizer) permissionDecisions(
	ctx context.Context, now time.Time,
) ([]pluginPermissionStoredDecisionEntry, error) {
	entries, err := a.storedDecisions(ctx)
	if err != nil {
		return nil, err
	}
	entries = append(entries, a.transientDecisions(now)...)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Display() < entries[j].Display()
	})
	return entries, nil
}

func (a *Authorizer) storedDecisions(
	ctx context.Context,
) ([]pluginPermissionStoredDecisionEntry, error) {
	if a == nil || a.storage == nil {
		return nil, nil
	}
	decisions, err := a.listPermissionDecisions(ctx)
	if err != nil {
		return nil, err
	}
	var ret []pluginPermissionStoredDecisionEntry
	for _, stored := range decisions {
		entry := pluginPermissionStoredDecisionEntry{
			Key:          stored.Key,
			Decision:     stored.Decision,
			Path:         stored.Path,
			Args:         append([]string(nil), stored.Args...),
			Permission:   string(stored.Permission),
			Scope:        "persisted",
			CommandScope: stored.Scope,
		}
		if stored.Command.Path != "" {
			cmd := copyPluginPermissionCommandDetail(stored.Command)
			entry.Command = &cmd
		}
		for _, ref := range stored.Executables {
			exe := executableEntry{executableRef: ref}
			var record storedExecutable
			err := a.storage.Get(ctx, executableStorageKey(ref.Host, ref.Path), &record)
			switch {
			case err == nil:
				exe.SHA256 = record.SHA256
			case !errors.Is(err, storageapi.ErrNotFound):
				return nil, fmt.Errorf("get executable fingerprint: %w", err)
			}
			entry.Executables = append(entry.Executables, exe)
		}
		ret = append(ret, entry)
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Display() < ret[j].Display()
	})
	return ret, nil
}

// listPermissionDecisions returns the permission decisions in storage.
func (a *Authorizer) listPermissionDecisions(
	ctx context.Context,
) ([]storedPermissionDecision, error) {
	it, err := a.storage.List(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list plugin permission decisions: %w", err)
	}
	defer it.Close()
	var ret []storedPermissionDecision
	for it.HasNext() {
		var stored storedPermissionDecision
		if err := it.NextTo(&stored); err != nil {
			return nil, fmt.Errorf("read plugin permission decision: %w", err)
		}
		if stored.Key != "" && isPermissionStorageKey(stored.Key) {
			ret = append(ret, stored)
		}
	}
	return ret, nil
}

// storedExecutables returns the executables approved commands run, sorted
// by host and path.
func (a *Authorizer) storedExecutables(ctx context.Context) ([]storedExecutable, error) {
	if a == nil || a.storage == nil {
		return nil, nil
	}
	it, err := a.storage.List(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list executable fingerprints: %w", err)
	}
	defer it.Close()
	var ret []storedExecutable
	for it.HasNext() {
		var stored storedExecutable
		if err := it.NextTo(&stored); err != nil {
			return nil, fmt.Errorf("read executable fingerprint: %w", err)
		}
		if strings.HasPrefix(stored.Key, executableStoragePrefix) {
			ret = append(ret, stored)
		}
	}
	sort.Slice(ret, func(i, j int) bool {
		if ret[i].Host != ret[j].Host {
			return ret[i].Host < ret[j].Host
		}
		return ret[i].Path < ret[j].Path
	})
	return ret, nil
}

func (a *Authorizer) transientDecisions(
	now time.Time,
) []pluginPermissionStoredDecisionEntry {
	if a == nil {
		return nil
	}
	a.onceMu.Lock()
	defer a.onceMu.Unlock()
	a.purgeExpiredOnceDecisionsLocked(now)
	ret := make([]pluginPermissionStoredDecisionEntry, 0, len(a.once))
	for key, decision := range a.once {
		entry := pluginPermissionStoredDecisionEntry{
			Key:        key,
			Decision:   decision.Decision,
			Path:       decision.Path,
			Args:       append([]string(nil), decision.Args...),
			Permission: string(decision.Permission),
			Scope:      "transient",
			Expires:    decision.Expires,
		}
		if decision.Command.Path != "" {
			cmd := copyPluginPermissionCommandDetail(decision.Command)
			entry.Command = &cmd
		}
		ret = append(ret, entry)
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Display() < ret[j].Display()
	})
	return ret
}

func (a *Authorizer) findStoredDecision(
	ctx context.Context, selected string,
) (pluginPermissionStoredDecisionEntry, bool, error) {
	entries, err := a.storedDecisions(ctx)
	if err != nil {
		return pluginPermissionStoredDecisionEntry{}, false, err
	}
	for _, entry := range entries {
		if selected == entry.Key || selected == entry.ID() || selected == entry.Display() {
			return entry, true, nil
		}
	}
	var byScope []pluginPermissionStoredDecisionEntry
	for _, entry := range entries {
		if entry.CommandScope != "" && selected == entry.CommandScope {
			byScope = append(byScope, entry)
		}
	}
	switch len(byScope) {
	case 0:
		return pluginPermissionStoredDecisionEntry{}, false, nil
	case 1:
		return byScope[0], true, nil
	default:
		ids := make([]string, len(byScope))
		for i, entry := range byScope {
			ids[i] = entry.ID()
		}
		return pluginPermissionStoredDecisionEntry{}, false, fmt.Errorf(
			"%d decisions cover %s; revoke one by ID: %s",
			len(byScope), selected, strings.Join(ids, ", "))
	}
}

func isPermissionStorageKey(key string) bool {
	return strings.HasPrefix(key, pluginPermissionStoragePrefix) ||
		strings.HasPrefix(key, extensionPermissionStoragePrefix)
}

func (a *Authorizer) deleteStoredDecision(
	ctx context.Context, key string,
) error {
	if a == nil || a.storage == nil {
		return nil
	}
	if err := a.storage.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete plugin permission decision: %w", err)
	}
	return nil
}
