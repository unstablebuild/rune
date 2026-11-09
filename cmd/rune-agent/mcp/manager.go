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

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/internal/debug"
)

// Overridden in tests.
var (
	// connectTimeout bounds the MCP handshake only. It must not cover
	// process startup, which can block on a user authorization prompt.
	connectTimeout = 30 * time.Second
	// connectRetryDelays are the pauses before each retry of a remote
	// server's first connect, after a failure that may pass.
	connectRetryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	// reconnectAttempts and reconnectDelay shape the reconnects after a
	// remote server's session ends. The delay doubles each attempt.
	reconnectAttempts = 5
	reconnectDelay    = time.Second
	// headersHelperTimeout bounds a headersHelper run.
	headersHelperTimeout = 10 * time.Second
	// loginTimeout bounds a browser sign-in.
	loginTimeout = 5 * time.Minute
)

// ServerStatus represents the current connection state of an MCP server.
type ServerStatus string

const (
	// StatusConnected indicates that the MCP server is connected.
	StatusConnected ServerStatus = "connected"
	// StatusConnecting indicates that the MCP server is starting up and
	// may be waiting on command authorization.
	StatusConnecting ServerStatus = "connecting"
	// StatusError indicates that the MCP server encountered an error.
	StatusError ServerStatus = "error"
	// StatusDisconnected indicates that the MCP server is disconnected.
	StatusDisconnected ServerStatus = "disconnected"
	// StatusNeedsAuth indicates that the server needs the user to sign
	// in with `agent mcp login`.
	StatusNeedsAuth ServerStatus = "needs-auth"
)

// ErrNeedsAuth matches the error of a server that needs the user to
// sign in.
var ErrNeedsAuth = errors.New("needs sign-in")

type needsAuthError struct {
	name  string
	scope string
}

func (e *needsAuthError) Error() string {
	msg := fmt.Sprintf("needs sign-in: run `agent mcp login %s`", e.name)
	if e.scope != "" {
		msg += fmt.Sprintf(" (the server requires scope %q)", e.scope)
	}
	return msg
}

func (e *needsAuthError) Is(target error) bool { return target == ErrNeedsAuth }

// ServerInfo holds a snapshot of a connected MCP server's state.
type ServerInfo struct {
	Name string
	// Transport is one of TransportStdio, TransportHTTP or TransportSSE.
	Transport string
	// URL is set for remote servers, Command for stdio servers.
	URL             string
	Command         string
	ProtocolVersion string
	ServerName      string
	ServerVersion   string
	Status          ServerStatus
	Error           string
	// Blocked reports that the server is kept from connecting until it
	// is registered again, so reconnecting it cannot help. Error says
	// why.
	Blocked     bool
	Warnings    []string
	ConnectedAt time.Time
	ToolCount   int
	// ToolNames are the tools' names on the server, sorted.
	ToolNames []string
	toolStats map[string]*ToolStats
}

// ToolStats returns the execution stats for the named tool, or nil.
func (si *ServerInfo) ToolStats(name string) *ToolStats {
	if si.toolStats == nil {
		return nil
	}
	return si.toolStats[name]
}

// serverEntry tracks a single MCP server connection. Its fields are
// guarded by the manager's lock unless noted.
type serverEntry struct {
	m       *Manager
	name    string
	config  ServerConfig
	kind    string
	auth    *authState
	session *gomcp.ClientSession
	info    *ServerInfo
	// tools are the adapters handed out, by the tool's name on the
	// server.
	tools map[string]*toolAdapter
	// warnings are the config's warnings, shown with the ones a
	// connection adds.
	warnings []string
	// blocked keeps the server from connecting until the user acts.
	blocked error
	// epoch changes whenever the user reconnects, signs out or closes
	// the server, which supersedes any automatic reconnect under way.
	epoch uint64
	// prompters are those of the tool calls in flight, latest last.
	prompters []agent.Prompter
	// connectMu serializes connects. It is taken before the manager's
	// lock.
	connectMu sync.Mutex
}

// currentSession returns the live session, or why there is none.
func (e *serverEntry) currentSession() (*gomcp.ClientSession, error) {
	e.m.mu.Lock()
	defer e.m.mu.Unlock()
	if e.session != nil {
		return e.session, nil
	}
	switch e.info.Status {
	case StatusNeedsAuth:
		return nil, fmt.Errorf("MCP server %q needs sign-in: run `agent mcp login %s`", e.name, e.name)
	case StatusConnecting:
		return nil, fmt.Errorf("MCP server %q is reconnecting", e.name)
	default:
		return nil, fmt.Errorf("MCP server %q is unavailable: %s", e.name, e.info.Error)
	}
}

func (e *serverEntry) pushPrompter(p agent.Prompter) {
	e.m.mu.Lock()
	defer e.m.mu.Unlock()
	e.prompters = append(e.prompters, p)
}

func (e *serverEntry) popPrompter(p agent.Prompter) {
	e.m.mu.Lock()
	defer e.m.mu.Unlock()
	if i := slices.Index(e.prompters, p); i >= 0 {
		e.prompters = slices.Delete(e.prompters, i, i+1)
	}
}

func (e *serverEntry) activePrompter() agent.Prompter {
	e.m.mu.Lock()
	defer e.m.mu.Unlock()
	if len(e.prompters) == 0 {
		return nil
	}
	return e.prompters[len(e.prompters)-1]
}

// TransportFactory creates a transport for a given server configuration.
type TransportFactory func(name string, cfg ServerConfig) (gomcp.Transport, error)

// Manager manages the lifecycle of MCP server connections.
//
// Servers are connected in the background while the agent is already
// running, so every access to the server list is guarded.
type Manager struct {
	client           *gomcp.Client
	transportFactory TransportFactory
	exec             workspaceapi.Executor
	dir              string
	creds            *CredentialStore
	ctx              context.Context
	cancel           context.CancelFunc

	mu      sync.Mutex
	servers []*serverEntry
	// issued maps every tool name handed out to its server.
	issued map[string]string
	logins map[string]*callbackServer
	closed bool

	// OnServerExit, if set before connecting, observes servers that
	// stop unexpectedly and do not come back. Called outside the
	// manager lock.
	OnServerExit ExitFunc
	// OnToolsChanged, if set before connecting, receives every change
	// to a server's tools after ConnectServer returned its first set:
	// sign-in, reconnects and the server's own list changes. added are
	// new tools, removed the names of tools that are gone.
	OnToolsChanged func(server string, added []agent.Tool, removed []string)
	// OnNeedsAuth, if set before connecting, observes a connected
	// server that starts asking for sign-in.
	OnNeedsAuth func(name string)
	// Open shows a URL to the user, for servers that ask the user to
	// visit one.
	Open func(url string) error
}

// NewManager creates a Manager that starts stdio MCP servers through
// exec, rooted at dir, and connects remote servers with the sign-ins in
// creds. Routing through the workspace executor keeps every launch and
// every headersHelper subject to the IDE's StartCommand authorization.
// creds may be nil, in which case sign-ins last for the process only.
func NewManager(exec workspaceapi.Executor, dir string, creds *CredentialStore) *Manager {
	m := NewManagerWithTransport(nil)
	m.exec = exec
	m.dir = dir
	m.creds = creds
	stdio := ExecutorTransport(exec, dir, m.serverExited)
	m.transportFactory = func(name string, cfg ServerConfig) (gomcp.Transport, error) {
		kind, err := cfg.Transport()
		if err != nil {
			return nil, err
		}
		if kind == TransportStdio {
			return stdio(name, cfg)
		}
		return httpTransport(cfg, m.authFor(name))
	}
	return m
}

// NewManagerWithTransport creates a new Manager using the given transport factory
// instead of the default one. This is useful for testing with in-memory transports.
func NewManagerWithTransport(factory TransportFactory) *Manager {
	m := &Manager{
		transportFactory: factory,
		issued:           make(map[string]string),
		logins:           make(map[string]*callbackServer),
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.client = gomcp.NewClient(
		&gomcp.Implementation{Name: "rune-agent", Version: "v1.0.0"},
		&gomcp.ClientOptions{
			ToolListChangedHandler: m.toolListChanged,
			ElicitationHandler:     m.elicit,
			// Advertising capabilities explicitly also stops the SDK
			// from advertising roots, which Rune does not serve.
			Capabilities: &gomcp.ClientCapabilities{
				Elicitation: &gomcp.ElicitationCapabilities{
					Form: &gomcp.FormElicitationCapabilities{},
					URL:  &gomcp.URLElicitationCapabilities{},
				},
			},
		},
	)
	return m
}

func (m *Manager) authFor(name string) *authState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.lookup(name); e != nil {
		return e.auth
	}
	return nil
}

func (m *Manager) lookup(name string) *serverEntry {
	for _, s := range m.servers {
		if s.name == name {
			return s
		}
	}
	return nil
}

func (m *Manager) entry(name string) (*serverEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.lookup(name); e != nil {
		return e, nil
	}
	return nil, fmt.Errorf("no MCP server named %q", name)
}

// upsert records the server's config, creating its entry on first use.
func (m *Manager) upsert(name string, cfg ServerConfig) *serverEntry {
	kind, _ := cfg.Transport()
	var auth *authState
	if kind == TransportHTTP || kind == TransportSSE {
		m.mu.Lock()
		e := m.lookup(name)
		if e != nil && e.auth != nil && e.auth.serverURL == cfg.URL {
			auth = e.auth
		}
		m.mu.Unlock()
		if auth == nil {
			// Loading the stored sign-in may wait on the keychain, so
			// it happens outside the lock.
			auth = newAuthState(m.ctx, cfg.URL, cfg.OAuth, m.creds)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.lookup(name)
	if e == nil {
		e = &serverEntry{m: m, name: name, tools: make(map[string]*toolAdapter)}
		e.info = &ServerInfo{Name: name, Status: StatusConnecting, toolStats: make(map[string]*ToolStats)}
		m.servers = append(m.servers, e)
	}
	e.config = cfg
	e.kind = kind
	e.auth = auth
	e.info.Transport = kind
	e.info.URL, e.info.Command = "", ""
	if kind == TransportStdio {
		e.info.Command = cfg.Command
	} else {
		e.info.URL = cfg.URL
	}
	return e
}

// Register records a configured server without connecting it, so its
// state can be inspected. warnings are shown with the server. A non-nil
// blocked keeps the server from connecting through Reconnect, and is
// shown as its error, until the server is registered again without it.
func (m *Manager) Register(name string, cfg ServerConfig, warnings []string, blocked error) {
	e := m.upsert(name, cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	e.warnings = slices.Clone(warnings)
	e.info.Warnings = slices.Clone(warnings)
	e.blocked = blocked
	e.info.Blocked = blocked != nil
	if blocked != nil {
		e.info.Status = StatusError
		e.info.Error = blocked.Error()
	} else if e.session == nil {
		e.info.Status = StatusConnecting
		e.info.Error = ""
	}
}

// Connect connects to all configured MCP servers and returns their tools.
// Failed servers are logged and skipped.
func (m *Manager) Connect(ctx context.Context, cfg Config) []agent.Tool {
	var tools []agent.Tool
	for name, serverCfg := range cfg.MCPServers {
		serverTools, _ := m.ConnectServer(ctx, name, serverCfg)
		tools = append(tools, serverTools...)
	}
	return tools
}

// ConnectServer connects to a single configured MCP server and returns
// its tools. The error reports why the server is unavailable; the
// failure is also recorded in the server's status. Later changes to
// the server's tools reach OnToolsChanged.
func (m *Manager) ConnectServer(
	ctx context.Context, name string, cfg ServerConfig,
) ([]agent.Tool, error) {
	e := m.upsert(name, cfg)
	m.mu.Lock()
	e.blocked = nil
	e.info.Blocked = false
	m.mu.Unlock()
	added, removed, err := m.connect(ctx, e, connectFirst, 0)
	if len(removed) > 0 {
		m.toolsChanged(name, nil, removed)
	}
	return added, err
}

// Reconnect drops the server's connection and connects it again. The
// name "all" reconnects every server that failed or needs sign-in.
func (m *Manager) Reconnect(ctx context.Context, name string) error {
	if name != "all" {
		e, err := m.entry(name)
		if err != nil {
			return err
		}
		return m.reconnect(ctx, e)
	}
	m.mu.Lock()
	var targets []*serverEntry
	for _, e := range m.servers {
		if e.blocked == nil && (e.info.Status == StatusError || e.info.Status == StatusNeedsAuth) {
			targets = append(targets, e)
		}
	}
	m.mu.Unlock()
	var errs []error
	for _, e := range targets {
		if err := m.reconnect(ctx, e); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.name, err))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) reconnect(ctx context.Context, e *serverEntry) error {
	m.mu.Lock()
	blocked := e.blocked
	m.mu.Unlock()
	if blocked != nil {
		return blocked
	}
	added, removed, err := m.connect(ctx, e, connectUser, 0)
	m.toolsChanged(e.name, added, removed)
	return err
}

type connectMode uint8

const (
	// connectFirst is the server's first connect, which retries
	// failures that may pass.
	connectFirst connectMode = iota
	// connectUser is a connect the user asked for.
	connectUser
	// connectAuto is a reconnect after the session ended, which gives
	// way to anything the user does meanwhile.
	connectAuto
)

var errSuperseded = errors.New("superseded")

// connect (re)establishes the server's session and reconciles its
// tools with the server's list. For connectAuto it gives up with
// errSuperseded if the epoch moved past epoch.
func (m *Manager) connect(
	ctx context.Context, e *serverEntry, mode connectMode, epoch uint64,
) (added []agent.Tool, removed []string, err error) {
	e.connectMu.Lock()
	defer e.connectMu.Unlock()
	log := slog.With("server", e.name)

	m.mu.Lock()
	if mode == connectAuto && (e.epoch != epoch || m.closed) {
		m.mu.Unlock()
		return nil, nil, errSuperseded
	}
	if mode != connectAuto {
		e.epoch++
	}
	old := e.session
	e.session = nil
	e.info.Status = StatusConnecting
	e.info.Error = ""
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	if _, err := e.config.Transport(); err != nil {
		log.Warn("mcp: unsupported server config", "error", err)
		m.fail(e, err)
		return nil, nil, err
	}

	session, err := m.dial(ctx, e)
	for i := 0; err != nil && mode == connectFirst && i < len(connectRetryDelays) &&
		m.retryable(ctx, e, err); i++ {
		log.Info("mcp: connect failed, retrying", "error", err, "delay", connectRetryDelays[i])
		if !sleepCtx(ctx, connectRetryDelays[i]) {
			break
		}
		session, err = m.dial(ctx, e)
	}
	if err != nil {
		log.Warn("mcp: failed to connect", "error", err)
		m.fail(e, err)
		return nil, nil, err
	}

	var list []*gomcp.Tool
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			log.Warn("mcp: failed to list tools", "error", err)
			_ = session.Close()
			m.fail(e, err)
			return nil, nil, err
		}
		list = append(list, t)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = session.Close()
		return nil, nil, errors.New("MCP manager is closed")
	}
	e.session = session
	e.info.Status = StatusConnected
	e.info.ConnectedAt = time.Now()
	if res := session.InitializeResult(); res != nil {
		e.info.ProtocolVersion = res.ProtocolVersion
		if res.ServerInfo != nil {
			e.info.ServerName = res.ServerInfo.Name
			e.info.ServerVersion = res.ServerInfo.Version
		}
	}
	added, removed = m.applyToolsLocked(e, list)
	m.mu.Unlock()

	if e.auth != nil {
		e.auth.connected()
		go debug.CapturePanicReport(func() { m.watch(e, session) })
	}
	log.Info("mcp: connected", "tools", len(list))
	return added, removed, nil
}

// dial connects one session. A rejected token is refreshed once, and a
// rejected headersHelper run once more, before the failure counts.
func (m *Manager) dial(ctx context.Context, e *serverEntry) (*gomcp.ClientSession, error) {
	refreshed, reranHelper := false, false
	for {
		if e.auth != nil {
			e.auth.beginConnect()
			if e.config.HeadersHelper != "" {
				headers, err := m.runHeadersHelper(ctx, e.config.HeadersHelper)
				if err != nil {
					return nil, fmt.Errorf("headersHelper failed: %w", err)
				}
				e.auth.setHelperHeaders(headers)
			}
		}

		transport, err := m.transportFactory(e.name, e.config)
		if err != nil {
			return nil, err
		}
		connCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		session, err := m.client.Connect(connCtx, transport, nil)
		cancel()
		if err == nil {
			return session, nil
		}
		if detail := transportExitDetail(transport); detail != "" {
			err = fmt.Errorf("server %s", detail)
		}
		closeTransport(transport)
		if e.auth == nil {
			return nil, err
		}

		_, ch, needsAuth := e.auth.status()
		if ch == nil && !needsAuth {
			return nil, err
		}
		status := http.StatusUnauthorized
		if ch != nil {
			status = ch.status
		}
		switch {
		case hasHeader(e.config.Headers, "Authorization"):
			return nil, fmt.Errorf("server rejected the configured Authorization header (HTTP %d)", status)
		case e.config.HeadersHelper != "":
			if !reranHelper && status == http.StatusUnauthorized {
				reranHelper = true
				continue
			}
			return nil, fmt.Errorf("server rejected the headers from headersHelper (HTTP %d)", status)
		case status == http.StatusForbidden && ch.bearerParam("error") != "insufficient_scope":
			return nil, fmt.Errorf("server refused access (HTTP %d)", status)
		case !refreshed && e.auth.forceRefresh():
			refreshed = true
			continue
		}
		var scope string
		if status == http.StatusForbidden {
			scope = ch.bearerParam("scope")
		}
		return nil, &needsAuthError{name: e.name, scope: scope}
	}
}

// retryable reports whether a remote server's failed first connect may
// succeed if tried again: the server was not up yet, timed out, or was
// overloaded.
func (m *Manager) retryable(ctx context.Context, e *serverEntry, err error) bool {
	if e.auth == nil || ctx.Err() != nil || errors.Is(err, ErrNeedsAuth) {
		return false
	}
	status, ch, _ := e.auth.status()
	if ch != nil {
		return false
	}
	if status >= 500 || status == http.StatusTooManyRequests {
		return true
	}
	if status >= 400 {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// watch follows a remote session until it ends, then brings the server
// back unless the session was closed on purpose.
func (m *Manager) watch(e *serverEntry, session *gomcp.ClientSession) {
	waitErr := session.Wait()
	// Closing the ended session also ends its subscription stream.
	_ = session.Close()
	m.mu.Lock()
	if e.session != session || m.closed {
		m.mu.Unlock()
		return
	}
	e.session = nil
	e.info.Status = StatusConnecting
	epoch := e.epoch
	m.mu.Unlock()
	slog.Info("mcp: session ended", "server", e.name, "error", waitErr)

	_, ch, needsAuth := e.auth.status()
	stepUp := ch != nil && ch.status == http.StatusForbidden &&
		ch.bearerParam("error") == "insufficient_scope"
	if needsAuth || stepUp || (ch != nil && ch.status == http.StatusUnauthorized) {
		if !stepUp && e.auth.forceRefresh() {
			added, removed, err := m.connect(m.ctx, e, connectAuto, epoch)
			if err == nil {
				m.toolsChanged(e.name, added, removed)
				return
			}
			if !errors.Is(err, ErrNeedsAuth) {
				return
			}
		} else {
			var scope string
			if stepUp {
				scope = ch.bearerParam("scope")
			}
			m.fail(e, &needsAuthError{name: e.name, scope: scope})
		}
		if m.OnNeedsAuth != nil {
			m.OnNeedsAuth(e.name)
		}
		return
	}

	lastErr := waitErr
	for i := range reconnectAttempts {
		if !sleepCtx(m.ctx, reconnectDelay<<i) {
			return
		}
		added, removed, err := m.connect(m.ctx, e, connectAuto, epoch)
		switch {
		case err == nil:
			m.toolsChanged(e.name, added, removed)
			return
		case errors.Is(err, errSuperseded):
			return
		case errors.Is(err, ErrNeedsAuth):
			if m.OnNeedsAuth != nil {
				m.OnNeedsAuth(e.name)
			}
			return
		}
		lastErr = err
	}
	detail := fmt.Sprintf("disconnected after %d reconnect attempts: %v", reconnectAttempts, lastErr)
	m.mu.Lock()
	if e.epoch != epoch || m.closed {
		m.mu.Unlock()
		return
	}
	e.info.Status = StatusError
	e.info.Error = detail
	onExit := m.OnServerExit
	m.mu.Unlock()
	slog.Warn("mcp: server lost", "server", e.name, "detail", detail)
	if onExit != nil {
		onExit(e.name, detail)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// applyToolsLocked reconciles the server's adapters with list. A tool
// whose definition changed is replaced, and a tool whose name another
// server already holds is skipped with a warning.
func (m *Manager) applyToolsLocked(
	e *serverEntry, list []*gomcp.Tool,
) (added []agent.Tool, removed []string) {
	next := make(map[string]*gomcp.Tool, len(list))
	for _, t := range list {
		next[t.Name] = t
	}
	for _, raw := range slices.Sorted(maps.Keys(e.tools)) {
		ad := e.tools[raw]
		if t, ok := next[raw]; ok && sameTool(ad.mcpTool, t) {
			continue
		}
		removed = append(removed, ad.qualified)
		delete(m.issued, ad.qualified)
		delete(e.tools, raw)
	}

	var collisions []string
	for _, raw := range slices.Sorted(maps.Keys(next)) {
		if _, kept := e.tools[raw]; kept {
			continue
		}
		qualified := qualifiedToolName(e.name, raw)
		if owner, taken := m.issued[qualified]; taken {
			msg := fmt.Sprintf("tool %q skipped: its name %s is taken by server %q", raw, qualified, owner)
			if owner == e.name {
				msg = fmt.Sprintf("tool %q skipped: its name %s is taken by another of its tools", raw, qualified)
			}
			slog.Warn("mcp: tool name collision", "server", e.name, "tool", raw, "name", qualified, "owner", owner)
			collisions = append(collisions, msg)
			continue
		}
		m.issued[qualified] = e.name
		stats := e.info.toolStats[raw]
		if stats == nil {
			stats = &ToolStats{}
		}
		ad := &toolAdapter{
			entry:     e,
			toolName:  raw,
			qualified: qualified,
			mcpTool:   next[raw],
			stats:     stats,
		}
		e.tools[raw] = ad
		added = append(added, ad)
		slog.Debug("mcp: registered tool", "server", e.name, "tool", qualified)
	}

	stats := make(map[string]*ToolStats, len(e.tools))
	for raw, ad := range e.tools {
		stats[raw] = ad.stats
	}
	e.info.toolStats = stats
	e.info.ToolNames = slices.Sorted(maps.Keys(e.tools))
	e.info.ToolCount = len(e.tools)
	e.info.Warnings = append(slices.Clone(e.warnings), collisions...)
	return added, removed
}

func sameTool(a, b *gomcp.Tool) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

func (m *Manager) toolsChanged(name string, added []agent.Tool, removed []string) {
	if len(added) == 0 && len(removed) == 0 {
		return
	}
	if m.OnToolsChanged != nil {
		m.OnToolsChanged(name, added, removed)
	}
}

// toolListChanged re-lists the tools of the server that announced a
// change. The list is fetched off the session's read loop, which has to
// stay free to deliver the response.
func (m *Manager) toolListChanged(_ context.Context, req *gomcp.ToolListChangedRequest) {
	e := m.entryForSession(req.Session)
	if e == nil {
		return
	}
	session := req.Session
	go debug.CapturePanicReport(func() {
		ctx, cancel := context.WithTimeout(m.ctx, connectTimeout)
		defer cancel()
		var list []*gomcp.Tool
		for t, err := range session.Tools(ctx, nil) {
			if err != nil {
				slog.Warn("mcp: re-list tools", "server", e.name, "error", err)
				return
			}
			list = append(list, t)
		}
		m.mu.Lock()
		if e.session != session {
			m.mu.Unlock()
			return
		}
		added, removed := m.applyToolsLocked(e, list)
		m.mu.Unlock()
		m.toolsChanged(e.name, added, removed)
	})
}

func (m *Manager) entryForSession(s *gomcp.ClientSession) *serverEntry {
	if s == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.servers {
		if e.session == s {
			return e
		}
	}
	return nil
}

// runHeadersHelper runs the server's headersHelper through the
// workspace executor and parses the headers it prints.
func (m *Manager) runHeadersHelper(ctx context.Context, helper string) (map[string]string, error) {
	if m.exec == nil {
		return nil, errors.New("no workspace executor to run it")
	}
	ctx, cancel := context.WithTimeout(ctx, headersHelperTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	watcher := &processWatcher{ch: make(chan error, 1)}
	env := make([]string, 0, len(credentialEnvKeys))
	for _, k := range credentialEnvKeys {
		env = append(env, k+"=")
	}
	if _, err := m.exec.Start(ctx, workspaceapi.Cmd{
		Path:        "sh",
		Args:        []string{"-c", helper},
		Dir:         m.dir,
		Env:         env,
		Stdout:      &stdout,
		Stderr:      &stderr,
		Watcher:     watcher,
		Description: "MCP headersHelper",
	}); err != nil {
		return nil, err
	}
	// The executor kills the process when ctx ends; the watcher fires
	// once its output has been copied.
	procErr := <-watcher.WatchProcess()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("timed out after %s", headersHelperTimeout)
	}
	if procErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", procErr, msg)
		}
		return nil, procErr
	}
	var headers map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &headers); err != nil {
		return nil, fmt.Errorf("output is not a JSON object of strings: %w", err)
	}
	return headers, nil
}

// Login signs in to a remote server through the browser. open shows
// the authorization URL, and out receives it first so the user can
// visit it by hand. A server that connects without sign-in returns nil
// without calling out. The sign-in may take up to five minutes, and
// ends early when ctx does.
func (m *Manager) Login(
	ctx context.Context, name string, open func(string) error, out func(string),
) error {
	e, err := m.remoteEntry(name)
	if err != nil {
		return err
	}
	if hasHeader(e.config.Headers, "Authorization") || e.config.HeadersHelper != "" {
		return fmt.Errorf("MCP server %q authenticates with its configured headers; sign-in does not apply", name)
	}
	if _, ch, _ := e.auth.status(); ch == nil {
		err := m.reconnect(ctx, e)
		if err == nil {
			return nil
		}
		if _, ch, _ := e.auth.status(); ch == nil {
			return err
		}
	}

	var cb *callbackServer
	if e.config.OAuth != nil && e.config.OAuth.CallbackPort != 0 {
		cb, err = startCallbackServer(e.config.OAuth.CallbackPort)
	} else if port := e.auth.previousRedirectPort(); port != 0 {
		if cb, err = startCallbackServer(port); err != nil {
			cb, err = startCallbackServer(0)
		}
	} else {
		cb, err = startCallbackServer(0)
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	if prev := m.logins[name]; prev != nil {
		_ = prev.Close()
	}
	m.logins[name] = cb
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.logins[name] == cb {
			delete(m.logins, name)
		}
		m.mu.Unlock()
		_ = cb.Close()
	}()

	loginCtx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()
	err = e.auth.Login(loginCtx, cb, func(u string) error {
		out(u)
		return open(u)
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return fmt.Errorf("sign-in timed out after %s", loginTimeout)
		}
		return err
	}
	return m.reconnect(ctx, e)
}

// CompleteLogin finishes the sign-in under way for the server with the
// redirect URL the user copied from the browser.
func (m *Manager) CompleteLogin(name, redirectURL string) error {
	m.mu.Lock()
	cb := m.logins[name]
	m.mu.Unlock()
	if cb == nil {
		return fmt.Errorf("no sign-in to MCP server %q is under way", name)
	}
	return cb.Deliver(redirectURL)
}

// Logout forgets the server's sign-in, revoking its tokens where the
// authorization server supports it, and disconnects the server.
func (m *Manager) Logout(ctx context.Context, name string) error {
	e, err := m.remoteEntry(name)
	if err != nil {
		return err
	}
	e.connectMu.Lock()
	defer e.connectMu.Unlock()
	err = e.auth.Logout(ctx)
	m.mu.Lock()
	e.epoch++
	session := e.session
	e.session = nil
	e.info.Status = StatusNeedsAuth
	e.info.Error = (&needsAuthError{name: name}).Error()
	m.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
	return err
}

func (m *Manager) remoteEntry(name string) (*serverEntry, error) {
	e, err := m.entry(name)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.auth == nil {
		return nil, fmt.Errorf("MCP server %q is not a remote server", name)
	}
	if e.blocked != nil {
		return nil, e.blocked
	}
	return e, nil
}

// serverExited transitions a connected server to the error state when
// its process dies without being asked to, and forwards the event.
func (m *Manager) serverExited(name, detail string) {
	m.mu.Lock()
	var unexpected bool
	for _, s := range m.servers {
		if s.name != name || s.info.Status != StatusConnected {
			continue
		}
		s.session = nil
		s.info.Status = StatusError
		s.info.Error = detail
		unexpected = true
	}
	onExit := m.OnServerExit
	m.mu.Unlock()

	if unexpected {
		slog.Warn("mcp: server exited", "server", name, "detail", detail)
		if onExit != nil {
			onExit(name, detail)
		}
	}
}

// fail records why a server is unavailable.
func (m *Manager) fail(e *serverEntry, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.info.Status = StatusError
	if errors.Is(err, ErrNeedsAuth) {
		e.info.Status = StatusNeedsAuth
	}
	e.info.Error = err.Error()
}

// closeTransport tears down a transport that owns an already-running
// server process but never reached a usable session.
func closeTransport(t gomcp.Transport) {
	if c, ok := t.(io.Closer); ok {
		_ = c.Close()
	}
}

// transportExitDetail asks a transport why its server process died.
// Empty when the transport does not track the process or it is alive.
func transportExitDetail(t gomcp.Transport) string {
	if d, ok := t.(interface{ ExitDetail() string }); ok {
		return d.ExitDetail()
	}
	return ""
}

// Servers returns a snapshot of all known servers, sorted by name. The
// returned values are copies: connection state may still be changing on
// the goroutine that is bringing the servers up.
func (m *Manager) Servers() []*ServerInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	infos := make([]*ServerInfo, len(m.servers))
	for i, s := range m.servers {
		snapshot := *s.info
		snapshot.Warnings = slices.Clone(s.info.Warnings)
		snapshot.ToolNames = slices.Clone(s.info.ToolNames)
		infos[i] = &snapshot
	}
	slices.SortFunc(infos, func(a, b *ServerInfo) int {
		return strings.Compare(a.Name, b.Name)
	})
	return infos
}

// Close closes all active sessions. Server entries are retained so that
// status remains inspectable after shutdown.
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	var sessions []*gomcp.ClientSession
	for _, s := range m.servers {
		s.epoch++
		s.info.Status = StatusDisconnected
		if s.session != nil {
			sessions = append(sessions, s.session)
			s.session = nil
		}
	}
	logins := slices.Collect(maps.Values(m.logins))
	clear(m.logins)
	m.mu.Unlock()

	for _, cb := range logins {
		_ = cb.Close()
	}
	var firstErr error
	for _, s := range sessions {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
