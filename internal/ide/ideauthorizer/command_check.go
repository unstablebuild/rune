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
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"mvdan.cc/sh/v3/syntax"
)

// stdPATH is the PATH that command -p looks programs up in.
var stdPATH = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

const commandPreviewLength = 60

// commandScope is a set of commands one decision can cover.
type commandScope struct {
	Key         string
	Label       string
	Exact       bool
	Recommended bool
	// Executables index the executables the commands of the scope run.
	Executables []int
}

type checkedExecutable struct {
	Name    string
	Path    string
	Current ExecutableFingerprint
	// Err tells why the executable could not be fingerprinted.
	Err    error
	Stored *storedExecutable
}

func (e checkedExecutable) known() bool {
	return e.Err == nil && e.Stored != nil && e.Stored.SHA256 == e.Current.SHA256
}

func (e checkedExecutable) changed() bool {
	return e.Err == nil && e.Stored != nil && e.Stored.SHA256 != e.Current.SHA256
}

type checkedProgram struct {
	inv programInvocation
	// path is the file the program runs, the shell's for a builtin, or ""
	// when it was not found.
	path string
	// exe indexes the executable of the program, or is -1.
	exe        int
	prefixKeys []string
	exactKey   string
	allowed    bool
	// allowedScopes are the scopes of the allow decisions that cover the
	// program.
	allowedScopes []string
}

// commandCheck is what the authorizer knows of a command, and of the
// decisions and executables that cover it.
type commandCheck struct {
	host        string
	command     pluginPermissionCommandDetail
	plan        commandPlan
	programs    []checkedProgram
	executables []checkedExecutable
	scopes      []commandScope
	exactKey    string
	denied      bool
	covered     bool
	// unbound reports that a program the command runs was not found, so
	// no file can be bound to an approval and only the user can allow
	// the command, each time.
	unbound bool
	// allowedKeys are the keys of the scopes the user already allowed.
	allowedKeys map[string]bool
}

// envOverrides returns the variables of env, as "NAME=VALUE" sorted by
// name, whose values differ from the IDE's own.
func envOverrides(env []string) []string {
	last := map[string]string{}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		last[name] = value
	}
	var ret []string
	for name, value := range last {
		if current, ok := os.LookupEnv(name); !ok || current != value {
			ret = append(ret, name+"="+value)
		}
	}
	sort.Strings(ret)
	return ret
}

// envPATH returns the PATH env sets, if it sets one.
func envPATH(env []string) ([]string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], "PATH="); ok {
			return strings.Split(v, ":"), true
		}
	}
	return nil, false
}

// checkCommand plans command, resolves and fingerprints the executables
// it runs, and looks up the decisions under prefix that cover it. env is
// the environment the command is started with.
func (a *Authorizer) checkCommand(
	ctx context.Context, prefix string, command pluginPermissionCommandDetail, env []string,
) (*commandCheck, error) {
	c := &commandCheck{
		host:    a.resolver.HostID(),
		command: command,
		plan:    planCommand(command.Path, command.Args, command.Env),
	}
	invocations := c.plan.Programs
	if c.plan.Opaque {
		invocations = []programInvocation{{
			Name: command.Path, Role: roleBound, Code: -1, Shell: -1, TopLevel: true,
		}}
	}
	var innerPATH []string
	innerPATHRead := false
	exes := map[string]int{}
	for _, inv := range invocations {
		p := checkedProgram{inv: inv, exe: -1}
		switch {
		case inv.Opaque:
		case inv.Shell >= 0:
			p.path = c.programs[inv.Shell].path
		default:
			var pathList []string
			switch {
			case inv.TopLevel:
			case inv.StdPATH:
				pathList = stdPATH
			default:
				if !innerPATHRead {
					innerPATH = a.innerPATH(ctx, env)
					innerPATHRead = true
				}
				pathList = innerPATH
			}
			if !inv.TopLevel && c.plan.DirChanged && dirDependent(inv.Name, pathList) {
				break
			}
			resolved, err := a.resolver.Resolve(ctx, inv.Name, command.Dir, pathList)
			if err != nil {
				break
			}
			p.path = resolved
			idx, ok := exes[resolved]
			if !ok {
				idx = len(c.executables)
				exes[resolved] = idx
				e := checkedExecutable{Name: inv.Name, Path: resolved}
				e.Current, e.Err = a.resolver.Fingerprint(ctx, resolved)
				c.executables = append(c.executables, e)
			}
			p.exe = idx
		}
		c.unbound = c.unbound || (p.path == "" && !inv.Opaque)
		c.programs = append(c.programs, p)
	}
	c.keys(prefix)
	c.buildScopes()
	if err := a.lookupCommandDecisions(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// innerPATH returns the PATH programs a command starts are looked up in.
func (a *Authorizer) innerPATH(ctx context.Context, env []string) []string {
	if p, ok := envPATH(env); ok {
		return p
	}
	p, err := a.resolver.HostPATH(ctx)
	if err != nil || p == nil {
		return []string{}
	}
	return p
}

// dirDependent reports whether what name runs depends on the directory
// it is looked up from.
func dirDependent(name string, pathList []string) bool {
	if strings.Contains(name, "/") {
		return !path.IsAbs(name) && !strings.HasPrefix(name, "~") && !strings.HasPrefix(name, "$")
	}
	for _, d := range pathList {
		if !path.IsAbs(d) {
			return true
		}
	}
	return false
}

func (c *commandCheck) keys(prefix string) {
	top := "?" + c.command.Path
	for _, p := range c.programs {
		if p.inv.TopLevel && p.path != "" {
			top = p.path
			break
		}
	}
	parts := []string{c.host, "command", top, c.command.Path, c.command.Dir,
		strconv.Itoa(len(c.command.Args))}
	parts = append(parts, c.command.Args...)
	parts = append(parts, c.command.Env...)
	c.exactKey = commandScopeKey(prefix, parts...)
	if c.plan.Opaque {
		return
	}
	for i := range c.programs {
		p := &c.programs[i]
		if p.inv.Role != roleScoped || p.inv.Opaque {
			continue
		}
		if p.path == "" {
			continue
		}
		id := p.path
		if p.inv.Shell >= 0 {
			id += "#" + p.inv.Name
		}
		if p.inv.Complete {
			parts := append([]string{c.host, id, "exact", strconv.Itoa(len(p.inv.Args))},
				p.inv.Args...)
			p.exactKey = commandScopeKey(prefix, parts...)
		}
		for k := 0; k <= len(p.inv.Scope); k++ {
			parts := append([]string{c.host, id, "prefix", strconv.Itoa(k)}, p.inv.Scope[:k]...)
			p.prefixKeys = append(p.prefixKeys, commandScopeKey(prefix, parts...))
		}
	}
}

// recommendation returns the scope recommended for p: the prefix of k
// arguments, or the exact invocation when exact. ok is false when no
// scope narrower than the whole command is safe to recommend.
func recommendation(p checkedProgram) (k int, exact, ok bool) {
	inv := p.inv
	switch {
	case inv.Code >= 0:
		return 0, true, inv.Complete
	case len(inv.Scope) > 0 && !strings.HasPrefix(inv.Scope[0], "-"):
		return 1, false, true
	case len(inv.Scope) > 0 || inv.Complete:
		return 0, false, true
	default:
		return 0, false, false
	}
}

func (c *commandCheck) addScope(s commandScope) {
	for i := range c.scopes {
		if c.scopes[i].Key == s.Key {
			c.scopes[i].Recommended = c.scopes[i].Recommended || s.Recommended
			return
		}
	}
	c.scopes = append(c.scopes, s)
}

func (c *commandCheck) buildScopes() {
	whole := c.plan.Opaque || len(c.programs) > 1 || !c.programs[0].inv.Complete
	wholeRecommended := c.plan.Opaque
	for _, p := range c.programs {
		if p.inv.Role != roleScoped {
			continue
		}
		if p.inv.Opaque {
			wholeRecommended = true
			continue
		}
		if p.path == "" {
			continue
		}
		k, exact, ok := recommendation(p)
		wholeRecommended = wholeRecommended || !ok
		var exes []int
		if p.exe >= 0 {
			exes = []int{p.exe}
		}
		suffix := ""
		if p.inv.Shell >= 0 {
			suffix = "  (shell builtin)"
		}
		n := len(p.inv.Scope)
		for i, key := range p.prefixKeys {
			if i == n && p.inv.Complete && n >= 2 {
				break
			}
			c.addScope(commandScope{
				Key:         key,
				Label:       shellJoin(p.inv.Name, p.inv.Scope[:i]) + " *" + suffix,
				Recommended: ok && !exact && k == i,
				Executables: exes,
			})
		}
		if p.exactKey != "" {
			c.addScope(commandScope{
				Key:         p.exactKey,
				Label:       shellJoin(p.inv.Name, p.inv.Args) + suffix,
				Exact:       true,
				Recommended: ok && exact,
				Executables: exes,
			})
		}
	}
	if c.unbound || (!whole && !wholeRecommended) {
		return
	}
	all := make([]int, len(c.executables))
	for i := range all {
		all[i] = i
	}
	c.addScope(commandScope{
		Key:         c.exactKey,
		Label:       "this exact command (" + commandPreview(c.command) + ")",
		Exact:       true,
		Recommended: wholeRecommended,
		Executables: all,
	})
}

// shellJoin renders a command the way it would be typed in a shell.
func shellJoin(name string, args []string) string {
	words := make([]string, 0, len(args)+1)
	for _, w := range append([]string{name}, args...) {
		q, err := syntax.Quote(w, syntax.LangBash)
		if err != nil {
			q = strconv.Quote(w)
		}
		words = append(words, q)
	}
	return strings.Join(words, " ")
}

func commandPreview(command pluginPermissionCommandDetail) string {
	preview := strings.Join(strings.Fields(shellJoin(command.Path, command.Args)), " ")
	if r := []rune(preview); len(r) > commandPreviewLength {
		preview = string(r[:commandPreviewLength-1]) + "…"
	}
	if command.Dir != "" {
		preview += " in " + command.Dir
	}
	return preview
}

func (a *Authorizer) storedDecision(
	ctx context.Context, key string,
) (storedPermissionDecision, bool, error) {
	var stored storedPermissionDecision
	if a.storage == nil || key == "" {
		return stored, false, nil
	}
	err := a.storage.Get(ctx, key, &stored)
	if errors.Is(err, storageapi.ErrNotFound) {
		return stored, false, nil
	}
	if err != nil {
		return stored, false, fmt.Errorf("get plugin permission decision: %w", err)
	}
	switch stored.Decision {
	case pluginPermissionDecisionAllow, pluginPermissionDecisionDeny:
		return stored, true, nil
	default:
		return stored, false, fmt.Errorf(
			"unknown stored plugin permission decision %q", stored.Decision)
	}
}

func (a *Authorizer) lookupCommandDecisions(ctx context.Context, c *commandCheck) error {
	c.allowedKeys = map[string]bool{}
	check := func(key string) (storedPermissionDecision, bool, error) {
		stored, ok, err := a.storedDecision(ctx, key)
		if err != nil || !ok {
			return stored, false, err
		}
		if stored.Decision == pluginPermissionDecisionDeny {
			c.denied = true
			return stored, false, nil
		}
		c.allowedKeys[key] = true
		return stored, true, nil
	}
	_, wholeAllowed, err := check(c.exactKey)
	if err != nil {
		return err
	}
	everyProgram := !c.plan.Opaque
	for i := range c.programs {
		p := &c.programs[i]
		if p.inv.Role != roleScoped {
			continue
		}
		for _, key := range append(append([]string(nil), p.prefixKeys...), p.exactKey) {
			stored, allowed, err := check(key)
			if err != nil {
				return err
			}
			if allowed {
				p.allowed = true
				p.allowedScopes = append(p.allowedScopes, stored.Scope)
			}
		}
		everyProgram = everyProgram && p.allowed
	}
	c.covered = !c.denied && !c.unbound && (wholeAllowed || everyProgram)
	if a.storage == nil {
		return nil
	}
	for i := range c.executables {
		e := &c.executables[i]
		var stored storedExecutable
		err := a.storage.Get(ctx, executableStorageKey(c.host, e.Path), &stored)
		switch {
		case err == nil:
			e.Stored = &stored
		case !errors.Is(err, storageapi.ErrNotFound):
			return fmt.Errorf("get executable fingerprint: %w", err)
		}
	}
	return nil
}

// unknownExecutables index the executables the user never approved a
// command of, or that could not be fingerprinted.
func (c *commandCheck) unknownExecutables() []int {
	var ret []int
	for i, e := range c.executables {
		if !e.known() && !e.changed() {
			ret = append(ret, i)
		}
	}
	return ret
}

// changedExecutables index the executables whose contents changed since
// the user approved a command of them.
func (c *commandCheck) changedExecutables() []int {
	var ret []int
	for i, e := range c.executables {
		if e.changed() {
			ret = append(ret, i)
		}
	}
	return ret
}

// alwaysScopes index the scopes a decision to always allow, or never
// allow, the command saves: each program it runs, with any arguments, as
// long as the program runs the same file, and the exact command when part
// of it is known only when it runs. Never leaves the programs the user
// allowed already allowed.
func (c *commandCheck) alwaysScopes(allow bool) []int {
	programs := map[string]bool{}
	for _, p := range c.programs {
		if len(p.prefixKeys) > 0 {
			programs[p.prefixKeys[0]] = true
		}
	}
	var ret []int
	for i, s := range c.scopes {
		if !allow && c.allowedKeys[s.Key] {
			continue
		}
		if programs[s.Key] || (s.Key == c.exactKey && s.Recommended) {
			ret = append(ret, i)
		}
	}
	return ret
}

// executableChanges describes the executables idx for a renewal prompt,
// with the approved scopes that run them.
func (c *commandCheck) executableChanges(idx []int) []ExecutableChange {
	var all []string
	for _, p := range c.programs {
		all = appendUnique(all, p.allowedScopes...)
	}
	ret := make([]ExecutableChange, 0, len(idx))
	for _, i := range idx {
		e := c.executables[i]
		var scopes []string
		for _, p := range c.programs {
			if p.exe == i {
				scopes = appendUnique(scopes, p.allowedScopes...)
			}
		}
		if len(scopes) == 0 {
			scopes = all
		}
		ret = append(ret, ExecutableChange{
			Path:   e.Path,
			Scopes: scopes,
			Old: ExecutableFingerprint{
				SHA256: e.Stored.SHA256, Size: e.Stored.Size, ModTime: e.Stored.ModTime,
			},
			New: e.Current,
		})
	}
	return ret
}

func appendUnique(list []string, items ...string) []string {
	for _, item := range items {
		if item != "" && !slices.Contains(list, item) {
			list = append(list, item)
		}
	}
	return list
}

// programSummaries describes the programs of the command for prompts,
// once each.
func (c *commandCheck) programSummaries() []ProgramSummary {
	var ret []ProgramSummary
	seen := map[string]bool{}
	for _, p := range c.programs {
		s := ProgramSummary{Name: p.inv.Name, Opaque: p.inv.Opaque, Builtin: p.inv.Shell >= 0}
		if !s.Builtin {
			s.ResolvedPath = p.path
		}
		if p.exe >= 0 {
			s.Changed = c.executables[p.exe].changed()
		}
		id := s.Name
		if s.ResolvedPath != "" {
			id = s.ResolvedPath
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ret = append(ret, s)
	}
	return ret
}

func (a *Authorizer) persistScopes(
	ctx context.Context, c *commandCheck, selected []int,
	identity pluginPermissionIdentity, perm extensionapi.Permission, decision string,
) error {
	if a.storage == nil {
		return nil
	}
	for _, i := range selected {
		s := c.scopes[i]
		refs := make([]executableRef, len(s.Executables))
		for j, e := range s.Executables {
			refs[j] = executableRef{
				Name: c.executables[e].Name, Host: c.host, Path: c.executables[e].Path,
			}
		}
		stored := storedPermissionDecision{
			Key:         s.Key,
			Decision:    decision,
			Path:        identity.Path,
			Args:        append([]string(nil), identity.Args...),
			Permission:  perm,
			Command:     copyPluginPermissionCommandDetail(c.command),
			Scope:       s.Label,
			Executables: refs,
		}
		if err := a.storage.Set(ctx, s.Key, stored); err != nil {
			return fmt.Errorf("set plugin permission decision: %w", err)
		}
	}
	if decision != pluginPermissionDecisionAllow || len(selected) == 0 {
		return nil
	}
	all := make([]int, len(c.executables))
	for i := range all {
		all[i] = i
	}
	return a.recordExecutables(ctx, c, all)
}

// recordExecutables saves the current fingerprints of the executables
// idx, which the user approved commands of.
func (a *Authorizer) recordExecutables(ctx context.Context, c *commandCheck, idx []int) error {
	if a.storage == nil {
		return nil
	}
	now := time.Now()
	for _, i := range idx {
		e := c.executables[i]
		if e.Err != nil {
			continue
		}
		key := executableStorageKey(c.host, e.Path)
		if err := a.storage.Set(ctx, key, storedExecutable{
			Key:     key,
			Host:    c.host,
			Path:    e.Path,
			SHA256:  e.Current.SHA256,
			Size:    e.Current.Size,
			ModTime: e.Current.ModTime,
			Updated: now,
		}); err != nil {
			return fmt.Errorf("set executable fingerprint: %w", err)
		}
	}
	return nil
}
