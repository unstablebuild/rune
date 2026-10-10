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
	"strings"
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
	Path               string
	Args               []string
	LauncherPath       string
	LauncherArgs       []string
	ExtensionID        string
	ExtensionName      string
	DeveloperID        string
	Permission         extensionapi.Permission
	Resource           string
	CommandPath        string
	CommandArgs        []string
	CommandDir         string
	CommandScopeLabel  string
	CommandScopeLabels []string
	// CommandScopeExact reports that approving Always covers only this exact
	// command path, args and dir, because the programs it runs couldn't be
	// determined. The scope labels are empty when it is set.
	CommandScopeExact bool
}

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

// PromptOpener opens a browser prompt for extension host decisions.
type PromptOpener interface {
	Prompt(
		message string, options []string,
		bindings []term.KeyComb,
		promptHandler handler.PromptHandler,
	) browser.Window
}

// Config controls which permission requests are granted without prompting.
type Config struct {
	AutoAuthorizeExtensions        bool
	AutoAuthorizeCommands          bool
	AutoAuthorizeVerifiedPublisher bool
	OnboardingActive               func() bool
}

// NewAuthorizer returns an Authorizer that satisfies both the workspace
// CommandAuthorizer interface and the blue auth authorizer used by the
// extension runner for gRPC middleware.
func NewAuthorizer(
	editor text.Editor,
	promptOpener PromptOpener, storage storageapi.Service,
	scheduleNextTick func(func()) bool,
	notifications browserapi.Notifications,
	trust *pkgtrust.Store, config Config,
) (*Authorizer, error) {
	if editor == nil {
		return nil, errors.New("editor is required")
	}
	if trust == nil {
		return nil, errors.New("trust store is required")
	}
	a := &Authorizer{
		prompter: newPermissionPrompter(promptOpener, scheduleNextTick, notifications),
		storage:  storage,
		config:   config,
		trust:    trust,
		once:     make(map[string]pluginPermissionOnceDecision),
		pending:  make(map[string]*pendingPrompt),
	}
	if err := registerAuthorizerREPLCommand(editor, a); err != nil {
		return nil, fmt.Errorf("register authorizer repl command: %w", err)
	}
	return a, nil
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
	return a.authorizePermission(ctx, ext, identity, []string{key}, onceKey, perm, resource,
		nil, a.config.AutoAuthorizeExtensions)
}

// AuthorizeCommand satisfies workspacerpc.CommandAuthorizer.
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
	}
	perm := extensionapi.PermissionExecute
	resource := workspacerpc.Executor_StartCommand_FullMethodName
	var identity pluginPermissionIdentity
	var keys []string
	var onceKey string
	if claims.Extra.Plugin {
		identity = pluginPermissionIdentityFromContext(ctx, claims.Extra)
		keys = pluginPermissionCommandStorageKeys(identity.Path, identity.Args, perm, command)
		onceKey = pluginPermissionOnceKey(identity, perm, &command)
	} else {
		if _, ok := claims.Extra.Permissions[perm]; !ok {
			return blueauth.ErrForbidden
		}
		identity = extensionPermissionIdentity(claims.Extra)
		keys = extensionPermissionCommandStorageKeys(claims.Extra, perm, command)
		onceKey = stablePermissionOnceKey(identity, perm, &command)
	}
	// Commands run silently only when the operator opted into
	// auto-authorizing commands AND the extension is from a verified
	// publisher; either alone still prompts. First-run onboarding stands
	// in for the operator opt-in while it lasts (see Config.OnboardingActive).
	autoAuthorize := (a.config.AutoAuthorizeCommands || a.onboardingActive()) &&
		!claims.Extra.Plugin && a.verified(claims.Extra)
	return a.authorizePermission(ctx, claims.Extra, identity, keys, onceKey, perm, resource,
		&command, autoAuthorize)
}

func (a *Authorizer) authorizeExtension(
	ctx context.Context, ext Extension, perm extensionapi.Permission, resource string,
) error {
	identity := extensionPermissionIdentity(ext)
	key := extensionPermissionStorageKey(ext, perm)
	onceKey := stablePermissionOnceKey(identity, perm, nil)
	return a.authorizePermission(ctx, ext, identity, []string{key}, onceKey, perm, resource,
		nil, a.config.AutoAuthorizeExtensions || a.verified(ext))
}

func (a *Authorizer) verified(ext Extension) bool {
	return a.config.AutoAuthorizeVerifiedPublisher && ext.VerifiedPublisher != "" &&
		a.trust.IsTrustedFingerprint(ext.VerifiedPublisher)
}

func (a *Authorizer) onboardingActive() bool {
	return a.config.OnboardingActive != nil && a.config.OnboardingActive()
}

func (a *Authorizer) authorizePermission(
	ctx context.Context, ext Extension, identity pluginPermissionIdentity,
	keys []string, onceKey string, perm extensionapi.Permission, resource string,
	command *pluginPermissionCommandDetail, autoAuthorize bool,
) error {
	missingKeys := keys
	if a.storage != nil && len(keys) > 0 {
		var err error
		missingKeys, err = a.filterMissingStoredDecisions(ctx, keys)
		if err != nil {
			return err
		}
		if len(missingKeys) == 0 {
			return nil
		}
	}

	if autoAuthorize {
		return nil
	}

	if decision, ok := a.getOnceDecision(onceKey, time.Now()); ok {
		switch decision {
		case pluginPermissionDecisionAllow:
			return nil
		case pluginPermissionDecisionDeny:
			return blueauth.ErrForbidden
		default:
			return fmt.Errorf("unknown cached plugin permission decision %q", decision)
		}
	}

	if a.prompter == nil {
		return blueauth.ErrForbidden
	}
	req := PermissionRequest{
		Path:         identity.Path,
		Args:         append([]string(nil), identity.Args...),
		LauncherPath: identity.LauncherPath,
		LauncherArgs: append([]string(nil), identity.LauncherArgs...),
		Permission:   perm,
		Resource:     resource,
	}
	if command != nil {
		req.CommandPath = command.Path
		req.CommandArgs = append([]string(nil), command.Args...)
		req.CommandDir = command.Dir
		labels := pluginPermissionApprovalScopeLabels(*command)
		req.CommandScopeLabels = labels
		req.CommandScopeLabel = strings.Join(labels, ", ")
		req.CommandScopeExact = len(labels) == 0
	}
	if !ext.Plugin {
		req.ExtensionID = ext.ExtensionID
		req.ExtensionName = ext.ExtensionName
		req.DeveloperID = ext.DeveloperID
	}
	decision, err := a.coalescedPrompt(ctx, onceKey, req)
	if err != nil {
		return err
	}
	a.notifyDecisionChanged()

	switch decision {
	case PermissionAllowOnce:
		a.setOnceDecision(onceKey, identity, perm, command,
			pluginPermissionDecisionAllow, time.Now())
		return nil
	case PermissionAllowAlways:
		for _, k := range missingKeys {
			if err := a.setStoredDecision(ctx, k, identity, perm, command,
				pluginPermissionDecisionAllow); err != nil {
				return err
			}
		}
		return nil
	case PermissionDenyOnce:
		a.setOnceDecision(onceKey, identity, perm, command,
			pluginPermissionDecisionDeny, time.Now())
		return blueauth.ErrForbidden
	case PermissionDenyAlways:
		for _, k := range missingKeys {
			if err := a.setStoredDecision(ctx, k, identity, perm, command,
				pluginPermissionDecisionDeny); err != nil {
				return err
			}
		}
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
// share a single prompt. The first caller opens the prompt; subsequent
// callers wait on the same pendingPrompt and observe the same decision.
// When ctx is canceled, the caller returns early without affecting other
// waiters or the in-flight prompt.
func (a *Authorizer) coalescedPrompt(
	ctx context.Context, onceKey string, req PermissionRequest,
) (PermissionDecision, error) {
	// onceKey is derived from identity + permission (+ command for
	// Execute); callers with the same onceKey would otherwise open
	// identical prompts that the browser dedup by message.
	if onceKey == "" {
		return a.prompter.PromptPermission(ctx, req)
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
	go func() {
		decision, err := a.prompter.PromptPermission(context.Background(), req)
		a.pendingMu.Lock()
		pending.decision = decision
		pending.err = err
		delete(a.pending, onceKey)
		a.pendingMu.Unlock()
		close(pending.done)
	}()

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

// filterMissingStoredDecisions returns the subset of keys that have no
// persisted allow/deny decision. If any key has a stored deny, it returns
// blueauth.ErrForbidden immediately. Returns nil when all keys are stored
// as allow (meaning no prompt is required).
func (a *Authorizer) filterMissingStoredDecisions(
	ctx context.Context, keys []string,
) ([]string, error) {
	missing := make([]string, 0, len(keys))
	for _, k := range keys {
		var stored storedPermissionDecision
		err := a.storage.Get(ctx, k, &stored)
		if err == nil {
			switch stored.Decision {
			case pluginPermissionDecisionAllow:
				continue
			case pluginPermissionDecisionDeny:
				return nil, blueauth.ErrForbidden
			default:
				return nil, fmt.Errorf(
					"unknown stored plugin permission decision %q", stored.Decision)
			}
		}
		if !errors.Is(err, storageapi.ErrNotFound) {
			return nil, fmt.Errorf("get plugin permission decision: %w", err)
		}
		missing = append(missing, k)
	}
	return missing, nil
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
	perm extensionapi.Permission, command *pluginPermissionCommandDetail, decision string,
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
	if command != nil {
		stored.Command = copyPluginPermissionCommandDetail(*command)
	}
	err := a.storage.Set(ctx, key, stored)
	if err != nil {
		return fmt.Errorf("set plugin permission decision: %w", err)
	}
	return nil
}
