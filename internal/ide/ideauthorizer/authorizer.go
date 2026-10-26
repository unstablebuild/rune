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

// Package ideauthorizer provides the authorization logic for the IDE
// workspace extension host. An Authorizer satisfies both the workspace
// CommandAuthorizer interface (for per-command authorization of
// Executor.StartCommand payloads) and blueauth.Authorizer[Extension] (for
// gRPC middleware that authenticates and authorizes extension RPCs).
//
// The package lives under ide/ so that both ide and extension/extensionv2
// can depend on it without creating an import cycle.
package ideauthorizer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	blueauth "github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/blue/auth/grpcauth"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/text"
)

// Extension represents an authenticated extension which is
// associated with some access to some resources.
type Extension struct {
	extensionapi.Metadata
	Plugin            bool
	Path              string
	Args              []string
	VerifiedPublisher string
}

// PermissionRequest describes a single permission requested by an extension or
// ad-hoc plugin program.
type PermissionRequest struct {
	Path          string
	Args          []string
	LauncherPath  string
	LauncherArgs  []string
	ExtensionID   string
	ExtensionName string
	DeveloperID   string
	Permission    extensionapi.Permission
	Resource      string
	CommandPath   string
	CommandArgs   []string
	CommandDir    string
	// Programs are the programs the command runs, shells and wrappers
	// before the programs they run.
	Programs []ProgramSummary
	// CommandRemember is what deciding for good on the command does.
	CommandRemember CommandRemember
	// CommandOnceReason tells why no decision for good can cover the
	// command when CommandRemember is RememberNothing, or is "" when
	// Programs tell already.
	CommandOnceReason string
}

// CommandRemember is what deciding for good on a command does, which
// decides the options of its prompt.
type CommandRemember int

const (
	// RememberScopes lets the user pick the scopes Always and Never
	// cover.
	RememberScopes CommandRemember = iota
	// RememberPrograms is for commands whose decisions are all made but
	// that run programs the user never approved, which Always approves.
	// There is nothing for Never to cover.
	RememberPrograms
	// RememberNothing is for commands no decision for good can cover,
	// which the user is asked about every time with Yes and No.
	RememberNothing
)

// ProgramSummary describes a program a command runs.
type ProgramSummary struct {
	Name string
	// ResolvedPath is the file that runs, or "" when there is none: the
	// program was not found, is a shell builtin or is Opaque.
	ResolvedPath string
	Builtin      bool
	// Opaque reports that the program is not known until the command
	// runs.
	Opaque bool
	// Changed reports that the file changed since the user approved a
	// command that runs it.
	Changed bool
	// RunsCode reports that the program runs arbitrary code its
	// arguments carry, such as the script of sed or the command of
	// docker run.
	RunsCode bool
	// DirUnknown tells why the directory the program runs in is not
	// known, which leaves ResolvedPath empty and has the user asked about
	// the command every time, or is "".
	DirUnknown string
	// Absent reports that no file runs for the program, which approving
	// the command approves for as long as there is none.
	Absent bool
}

// ExecutableChange is an executable whose contents changed since the
// user approved commands that run it.
type ExecutableChange struct {
	Path string
	// Scopes are the approved commands that run the executable.
	Scopes   []string
	Old, New ExecutableFingerprint
}

// RenewalRequest asks the user whether approvals of commands still hold
// after executables they run changed.
type RenewalRequest struct {
	// Requester names who runs the command, such as "extension Agent".
	Requester string
	Changes   []ExecutableChange
}

// RenewalDecision is the user's answer to a RenewalRequest.
type RenewalDecision string

const (
	// RenewalRenew approves the changed executables for good.
	RenewalRenew RenewalDecision = "renew"
	// RenewalOnce allows the command this time only.
	RenewalOnce RenewalDecision = "once"
	// RenewalDeny denies the command this time.
	RenewalDeny RenewalDecision = "deny"
)

// PermissionDecision is the user's decision for an extension permission request.
type PermissionDecision string

const (
	// PermissionAllowOnce allows the requested permission temporarily.
	PermissionAllowOnce PermissionDecision = "allow-once"
	// PermissionAllowAlways persists an allow decision for the extension identity
	// and requested permission.
	PermissionAllowAlways PermissionDecision = "allow-always"
	// PermissionDenyOnce denies the requested permission temporarily.
	PermissionDenyOnce PermissionDecision = "deny-once"
	// PermissionDenyAlways persists a deny decision for the extension identity and
	// requested permission.
	PermissionDenyAlways PermissionDecision = "deny-always"
)

// PromptOpener opens the prompts and floating windows that ask the user
// for extension host decisions: Prompt asks for the decision, Floating
// shows the picker of what a decision to always allow or never allow a
// command covers.
type PromptOpener interface {
	Prompt(
		message string, options []string,
		bindings []term.KeyComb,
		promptHandler handler.PromptHandler,
	) browser.Window
	Floating(h browser.Floating, cfg browserapi.FloatingConfig) (browser.Window, error)
}

// Config controls which permission requests are granted without
// prompting, and how the user is told about those that are not.
type Config struct {
	AutoAuthorizeExtensions        bool
	AutoAuthorizeCommands          bool
	AutoAuthorizeVerifiedPublisher bool
	// OnPromptWindow, when set, is told on the host event loop of the
	// window showing each permission prompt and of the uri its blocked
	// status is reported under, so the status can be traced back to it.
	OnPromptWindow func(uri workspaceapi.URI, win browser.Window)
	// WorkspaceConfigs, when set, makes the authorizer command list and
	// revoke the user's workspace config trust decisions too.
	WorkspaceConfigs WorkspaceConfigTrust
	// Notifications, when set, tells the user of the command approvals
	// saved by older versions of Rune that were forgotten.
	Notifications browserapi.Notifications
}

// NewAuthorizer returns an Authorizer that satisfies both the workspace
// CommandAuthorizer interface and the blue auth authorizer used by the
// extension runner for gRPC middleware. resolver finds the executables
// commands run on the workspace host, which approvals are bound to.
//
// Command approvals saved by older versions of Rune, which covered every
// program with the same name, are deleted.
func NewAuthorizer(
	editor text.Editor,
	promptOpener PromptOpener, storage storageapi.Service,
	scheduleNextTick func(func()) bool,
	status browser.TabManager,
	trust *pkgtrust.Store, resolver ExecutableResolver, config Config,
) (*Authorizer, error) {
	if editor == nil {
		return nil, errors.New("editor is required")
	}
	if status == nil {
		return nil, errors.New("status reporter is required")
	}
	if trust == nil {
		return nil, errors.New("trust store is required")
	}
	if resolver == nil {
		return nil, errors.New("executable resolver is required")
	}
	a := &Authorizer{
		prompter: newPermissionPrompter(promptOpener, scheduleNextTick, status),
		storage:  storage,
		config:   config,
		trust:    trust,
		resolver: resolver,
		once:     make(map[string]pluginPermissionOnceDecision),
		pending:  make(map[string]*pendingPrompt),
	}
	if a.prompter != nil {
		a.prompter.onWindow = config.OnPromptWindow
	}
	if err := a.forgetLegacyCommandDecisions(context.Background()); err != nil {
		return nil, err
	}
	if err := registerAuthorizerREPLCommand(editor, a); err != nil {
		return nil, fmt.Errorf("register authorizer repl command: %w", err)
	}
	return a, nil
}

// forgetLegacyCommandDecisions deletes the command decisions saved before
// they were bound to executables, and tells the user.
func (a *Authorizer) forgetLegacyCommandDecisions(ctx context.Context) error {
	if a.storage == nil {
		return nil
	}
	decisions, err := a.listPermissionDecisions(ctx)
	if err != nil {
		return err
	}
	var legacy []string
	for _, stored := range decisions {
		if stored.legacyCommand() {
			legacy = append(legacy, stored.Key)
		}
	}
	for _, key := range legacy {
		if err := a.storage.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete legacy command decision: %w", err)
		}
	}
	if len(legacy) > 0 && a.config.Notifications != nil {
		_, _ = a.config.Notifications.Notify(browserapi.LevelWarn,
			"Rune forgot %d command approvals saved by an older version: they "+
				"covered more programs than shown. Rune asks again the next time "+
				"an extension runs one of those commands.", len(legacy))
	}
	return nil
}

// Authorizer implements both workspacerpc.CommandAuthorizer and
// blueauth.Authorizer[Extension]. It is used by the extension runner to
// authorize gRPC requests and by the workspace server to authorize
// individual StartCommand payloads.
type Authorizer struct {
	prompter *permissionPrompter
	storage  storageapi.Service
	config   Config
	trust    *pkgtrust.Store
	resolver ExecutableResolver

	onceMu sync.Mutex
	once   map[string]pluginPermissionOnceDecision

	// pendingMu guards pending. It serializes access to the in-flight
	// prompt map so concurrent Authorize calls for the same onceKey share
	// a single prompt instead of each opening their own (which the
	// browser would dedup by message, silently dropping all but the first
	// caller's PromptHandler and leaving the rest stuck on their result
	// channel). See RUNE-97.
	pendingMu         sync.Mutex
	pending           map[string]*pendingPrompt
	decisionObservers []func()
}

func (a *Authorizer) onDecisionChange(fn func()) {
	a.decisionObservers = append(a.decisionObservers, fn)
}

// notifyDecisionChanged invokes every registered observer.
func (a *Authorizer) notifyDecisionChanged() {
	for _, fn := range a.decisionObservers {
		fn()
	}
}

// GRPCAuthServerOptions returns the gRPC server options that install
// this authorizer as middleware for extension authentication/authorization.
// When creds is nil, insecure OAuth2 is used (transport secured externally).
func (a *Authorizer) GRPCAuthServerOptions(
	keys blueauth.Keys, creds credentials.TransportCredentials,
) []grpc.ServerOption {
	cache := newAuthCache(
		grpcauth.Oauth2UnaryInterceptor(keys, a),
		grpcauth.Oauth2StreamInterceptor(keys, a),
	)
	a.onDecisionChange(cache.evict)
	if creds == nil {
		return grpcauth.GRPCServerWithInsecureOauth2Interceptors(cache.Unary, cache.Stream)
	}
	return grpcauth.GRPCServerWithOauth2Interceptors(cache.Unary, cache.Stream, creds)
}

// Authorize satisfies blueauth.Authorizer[Extension].
func (a *Authorizer) Authorize(
	ctx context.Context, claims blueauth.UserClaims[Extension], resource string,
) (err error) {
	perm, ok := extensionapi.PermissionForResource(resource)
	if !ok {
		err = fmt.Errorf("extraneous rpc resource %s: %w", resource, blueauth.ErrForbidden)
		return
	}
	// StartCommand is a client-streaming RPC, so the generic gRPC auth layer
	// only knows the method name here. Do not prompt/cache a blanket
	// PermissionExecute decision before seeing the command payload. Let the
	// stream open; workspacerpc.Server.StartCommand reads the first payload,
	// builds the workspaceapi.Cmd, and then calls AuthorizeCommand with
	// path/args/dir before any process is started.
	if resource == workspacerpc.Executor_StartCommand_FullMethodName {
		return nil
	}
	if claims.Extra.Plugin {
		return a.authorizePlugin(ctx, claims.Extra, perm, resource)
	}
	if _, ok := claims.Extra.Permissions[perm]; !ok {
		err = blueauth.ErrForbidden
		return
	}
	return a.authorizeExtension(ctx, claims.Extra, perm, resource)
}

func (a *Authorizer) authorizePlugin(
	ctx context.Context, ext Extension, perm extensionapi.Permission, resource string,
) error {
	identity := pluginPermissionIdentityFromContext(ctx, ext)
	key := pluginPermissionProgramStorageKey(identity.Path, perm)
	onceKey := pluginPermissionOnceKey(identity, perm, nil)
	return a.authorizePermission(ctx, ext, identity, key, onceKey, perm, resource,
		a.config.AutoAuthorizeExtensions)
}

// AuthorizeCommand satisfies workspacerpc.CommandAuthorizer.
//
// A command runs without prompting when decisions the user saved cover
// every program it runs, and every executable it runs is the file the
// user approved. When an executable changed, the user is asked to renew
// the approvals.
func (a *Authorizer) AuthorizeCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) error {
	claims, ok := blueauth.ClaimsFromContext[Extension](ctx)
	if !ok {
		return nil
	}
	command := pluginPermissionCommandDetail{
		Path: cmd.Path,
		Args: append([]string(nil), cmd.Args...),
		Dir:  cmd.Dir,
		Env:  envOverrides(cmd.Env),
	}
	perm := extensionapi.PermissionExecute
	resource := workspacerpc.Executor_StartCommand_FullMethodName
	var identity pluginPermissionIdentity
	var prefix, onceKey string
	if claims.Extra.Plugin {
		identity = pluginPermissionIdentityFromContext(ctx, claims.Extra)
		prefix = pluginPermissionStorageKey(identity.Path, identity.Args, perm)
		onceKey = pluginPermissionOnceKey(identity, perm, &command)
	} else {
		if _, ok := claims.Extra.Permissions[perm]; !ok {
			return blueauth.ErrForbidden
		}
		identity = extensionPermissionIdentity(claims.Extra)
		prefix = extensionPermissionStorageKey(claims.Extra, perm)
		onceKey = stablePermissionOnceKey(identity, perm, &command)
	}
	c, err := a.checkCommand(ctx, prefix, command, cmd.Env)
	if err != nil {
		return err
	}
	if c.denied {
		return blueauth.ErrForbidden
	}
	unknown := c.unknownExecutables()
	changed := c.changedExecutables()
	if c.covered && len(unknown) == 0 && len(changed) == 0 {
		return nil
	}
	// Commands run silently only when the operator opted into
	// auto-authorizing commands AND the extension is from a verified
	// publisher; either alone still prompts.
	if a.config.AutoAuthorizeCommands && !claims.Extra.Plugin && a.verified(claims.Extra) {
		return nil
	}
	if done, err := a.onceDecided(onceKey); done {
		return err
	}
	if a.prompter == nil {
		return blueauth.ErrForbidden
	}
	req := permissionRequest(claims.Extra, identity, perm, resource)
	req.CommandPath = command.Path
	req.CommandArgs = append([]string(nil), command.Args...)
	req.CommandDir = command.Dir
	req.Programs = c.programSummaries()
	req.CommandRemember = c.remember()
	req.CommandOnceReason = c.plan.Reason
	run := func(ctx context.Context) (PermissionDecision, error) {
		return a.approveCommand(ctx, c, req, identity, perm)
	}
	if c.covered && len(unknown) == 0 {
		renewal := RenewalRequest{Requester: requesterName(req), Changes: c.executableChanges(changed)}
		run = func(ctx context.Context) (PermissionDecision, error) {
			return a.renewExecutables(ctx, c, changed, renewal)
		}
	}
	decision, err := a.coalescedPrompt(ctx, onceKey, run)
	if err != nil {
		return err
	}
	return a.applyDecision(onceKey, identity, perm, &command, decision)
}

// approveCommand asks the user whether the command of c may run and, when
// the user decides for good, which scopes the decision covers.
func (a *Authorizer) approveCommand(
	ctx context.Context, c *commandCheck, req PermissionRequest,
	identity pluginPermissionIdentity, perm extensionapi.Permission,
) (PermissionDecision, error) {
	decision, err := a.prompter.PromptPermission(ctx, req)
	if err != nil {
		return decision, err
	}
	if decision == PermissionAllowAlways && req.CommandRemember == RememberPrograms {
		err := a.recordExecutables(ctx, c, append(c.unknownExecutables(), c.changedExecutables()...))
		if err != nil {
			return PermissionDenyOnce, err
		}
		return decision, nil
	}
	var stored string
	var once PermissionDecision
	switch decision {
	case PermissionAllowAlways:
		stored, once = pluginPermissionDecisionAllow, PermissionAllowOnce
	case PermissionDenyAlways:
		stored, once = pluginPermissionDecisionDeny, PermissionDenyOnce
	default:
		return decision, nil
	}
	selected, ok, err := a.prompter.PickScopes(ctx, scopePickerRequest{
		Allow:     stored == pluginPermissionDecisionAllow,
		Requester: requesterName(req),
		Scopes:    c.scopes,
		Selected:  c.preselectedScopes(),
	})
	if err != nil {
		return PermissionDenyOnce, err
	}
	if !ok {
		return PermissionDenyOnce, nil
	}
	if len(selected) == 0 {
		return once, nil
	}
	if err := a.persistScopes(ctx, c, selected, identity, perm, stored); err != nil {
		return PermissionDenyOnce, err
	}
	return decision, nil
}

// renewExecutables asks the user whether the approvals of the command of
// c still hold after the executables changed changed.
func (a *Authorizer) renewExecutables(
	ctx context.Context, c *commandCheck, changed []int, req RenewalRequest,
) (PermissionDecision, error) {
	decision, err := a.prompter.PromptRenewal(ctx, req)
	if err != nil {
		return PermissionDenyOnce, err
	}
	switch decision {
	case RenewalRenew:
		if err := a.recordExecutables(ctx, c, changed); err != nil {
			return PermissionDenyOnce, err
		}
		return PermissionAllowAlways, nil
	case RenewalOnce:
		return PermissionAllowOnce, nil
	default:
		return PermissionDenyOnce, nil
	}
}

// requesterName names who a permission is requested for in prompts.
func requesterName(req PermissionRequest) string {
	if req.ExtensionName != "" {
		return "extension " + req.ExtensionName
	}
	return "program " + req.Path
}

func (a *Authorizer) authorizeExtension(
	ctx context.Context, ext Extension, perm extensionapi.Permission, resource string,
) error {
	identity := extensionPermissionIdentity(ext)
	key := extensionPermissionStorageKey(ext, perm)
	onceKey := stablePermissionOnceKey(identity, perm, nil)
	return a.authorizePermission(ctx, ext, identity, key, onceKey, perm, resource,
		a.config.AutoAuthorizeExtensions || a.verified(ext))
}

func (a *Authorizer) verified(ext Extension) bool {
	return a.config.AutoAuthorizeVerifiedPublisher && ext.VerifiedPublisher != "" &&
		a.trust.IsTrustedFingerprint(ext.VerifiedPublisher)
}

func (a *Authorizer) authorizePermission(
	ctx context.Context, ext Extension, identity pluginPermissionIdentity,
	key string, onceKey string, perm extensionapi.Permission, resource string,
	autoAuthorize bool,
) error {
	stored, ok, err := a.storedDecision(ctx, key)
	if err != nil {
		return err
	}
	if ok {
		if stored.Decision == pluginPermissionDecisionDeny {
			return blueauth.ErrForbidden
		}
		return nil
	}

	if autoAuthorize {
		return nil
	}

	if done, err := a.onceDecided(onceKey); done {
		return err
	}

	if a.prompter == nil {
		return blueauth.ErrForbidden
	}
	req := permissionRequest(ext, identity, perm, resource)
	decision, err := a.coalescedPrompt(ctx, onceKey,
		func(ctx context.Context) (PermissionDecision, error) {
			return a.prompter.PromptPermission(ctx, req)
		})
	if err != nil {
		return err
	}
	switch decision {
	case PermissionAllowAlways:
		err = a.setStoredDecision(ctx, key, identity, perm, pluginPermissionDecisionAllow)
	case PermissionDenyAlways:
		err = a.setStoredDecision(ctx, key, identity, perm, pluginPermissionDecisionDeny)
	}
	if err != nil {
		return err
	}
	return a.applyDecision(onceKey, identity, perm, nil, decision)
}

// onceDecided reports whether the user decided onceKey a moment ago, and
// the error that decision calls for.
func (a *Authorizer) onceDecided(onceKey string) (bool, error) {
	decision, ok := a.getOnceDecision(onceKey, time.Now())
	if !ok {
		return false, nil
	}
	switch decision {
	case pluginPermissionDecisionAllow:
		return true, nil
	case pluginPermissionDecisionDeny:
		return true, blueauth.ErrForbidden
	default:
		return true, fmt.Errorf("unknown cached plugin permission decision %q", decision)
	}
}

func permissionRequest(
	ext Extension, identity pluginPermissionIdentity, perm extensionapi.Permission,
	resource string,
) PermissionRequest {
	req := PermissionRequest{
		Path:         identity.Path,
		Args:         append([]string(nil), identity.Args...),
		LauncherPath: identity.LauncherPath,
		LauncherArgs: append([]string(nil), identity.LauncherArgs...),
		Permission:   perm,
		Resource:     resource,
	}
	if !ext.Plugin {
		req.ExtensionID = ext.ExtensionID
		req.ExtensionName = ext.ExtensionName
		req.DeveloperID = ext.DeveloperID
	}
	return req
}

// applyDecision remembers the user's decision for onceKey when it holds
// only for a moment, and returns the error the decision calls for.
func (a *Authorizer) applyDecision(
	onceKey string, identity pluginPermissionIdentity, perm extensionapi.Permission,
	command *pluginPermissionCommandDetail, decision PermissionDecision,
) error {
	a.notifyDecisionChanged()
	switch decision {
	case PermissionAllowOnce:
		a.setOnceDecision(onceKey, identity, perm, command,
			pluginPermissionDecisionAllow, time.Now())
		return nil
	case PermissionAllowAlways:
		return nil
	case PermissionDenyOnce:
		a.setOnceDecision(onceKey, identity, perm, command,
			pluginPermissionDecisionDeny, time.Now())
		return blueauth.ErrForbidden
	default:
		return blueauth.ErrForbidden
	}
}

// pendingPrompt holds the state of a single in-flight permission prompt
// so that concurrent Authorize calls for the same onceKey share its
// outcome instead of each opening their own prompt. The browser prompt
// component deduplicates by message, so without coalescing at this
// layer only the first caller would unblock and the rest would stay
// stuck on their private result channel until ctx.Done() fires (or
// forever, for long-lived RPCs). See RUNE-97.
type pendingPrompt struct {
	done     chan struct{}
	decision PermissionDecision
	err      error
}

// coalescedPrompt ensures that concurrent callers with the same onceKey
// share a single prompt. The first caller runs prompt, which asks the user
// and saves what they decide for good; subsequent callers wait on the
// same pendingPrompt and observe the same decision. When ctx is canceled,
// the caller returns early without affecting other waiters or the
// in-flight prompt.
func (a *Authorizer) coalescedPrompt(
	ctx context.Context, onceKey string,
	prompt func(context.Context) (PermissionDecision, error),
) (PermissionDecision, error) {
	// onceKey is derived from identity + permission (+ command for
	// Execute); callers with the same onceKey would otherwise open
	// identical prompts that the browser dedup by message.
	if onceKey == "" {
		return prompt(ctx)
	}
	a.pendingMu.Lock()
	if existing, ok := a.pending[onceKey]; ok {
		a.pendingMu.Unlock()
		return a.awaitPending(ctx, existing)
	}
	pending := &pendingPrompt{done: make(chan struct{})}
	a.pending[onceKey] = pending
	a.pendingMu.Unlock()

	// Issue the prompt on behalf of all concurrent waiters on a detached
	// goroutine with a background context, so no single caller's
	// ctx.Done() can tear down the prompt for the others. Waiters each
	// honor their own ctx in awaitPending below.
	go debug.CapturePanicReport(func() {
		decision, err := prompt(context.Background())
		a.pendingMu.Lock()
		pending.decision = decision
		pending.err = err
		delete(a.pending, onceKey)
		a.pendingMu.Unlock()
		close(pending.done)
	})

	return a.awaitPending(ctx, pending)
}

// awaitPending blocks until the given pendingPrompt resolves or ctx is
// canceled. If ctx is canceled first, the caller returns without
// cancelling the underlying prompt (other waiters and the prompt owner
// continue unaffected).
func (a *Authorizer) awaitPending(
	ctx context.Context, p *pendingPrompt,
) (PermissionDecision, error) {
	select {
	case <-p.done:
		if p.err != nil {
			return PermissionDenyOnce, p.err
		}
		return p.decision, nil
	case <-ctx.Done():
		return PermissionDenyOnce, ctx.Err()
	}
}

func (a *Authorizer) getOnceDecision(
	key string, now time.Time,
) (string, bool) {
	if key == "" {
		return "", false
	}
	a.onceMu.Lock()
	defer a.onceMu.Unlock()
	decision, ok := a.once[key]
	if !ok {
		return "", false
	}
	if !now.Before(decision.Expires) {
		delete(a.once, key)
		return "", false
	}
	return decision.Decision, true
}

func (a *Authorizer) setOnceDecision(
	key string, identity pluginPermissionIdentity,
	perm extensionapi.Permission, command *pluginPermissionCommandDetail,
	decision string, now time.Time,
) {
	if key == "" {
		return
	}
	a.onceMu.Lock()
	defer a.onceMu.Unlock()
	a.purgeExpiredOnceDecisionsLocked(now)
	onceDecision := pluginPermissionOnceDecision{
		Decision:   decision,
		Path:       identity.Path,
		Args:       append([]string(nil), identity.Args...),
		Permission: perm,
		Expires:    now.Add(pluginPermissionOnceTTL),
	}
	if command != nil {
		onceDecision.Command = copyPluginPermissionCommandDetail(*command)
	}
	a.once[key] = onceDecision
}

func (a *Authorizer) purgeExpiredOnceDecisionsLocked(now time.Time) {
	for key, decision := range a.once {
		if !now.Before(decision.Expires) {
			delete(a.once, key)
		}
	}
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (a *Authorizer) setStoredDecision(
	ctx context.Context, key string, identity pluginPermissionIdentity,
	perm extensionapi.Permission, decision string,
) error {
	if a.storage == nil {
		return nil
	}
	stored := storedPermissionDecision{
		Key:        key,
		Decision:   decision,
		Path:       identity.Path,
		Args:       append([]string(nil), identity.Args...),
		Permission: perm,
	}
	err := a.storage.Set(ctx, key, stored)
	if err != nil {
		return fmt.Errorf("set plugin permission decision: %w", err)
	}
	return nil
}
