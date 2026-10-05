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
	"fmt"

	multierr "github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc"
)

var _ SchemeManager = (*Manager)(nil)

// Manager manages resources for a collection of workspaces.
// It allows clients to register new Schemes and add new workspaces.
type Manager struct {
	cfg              config.Config
	workspaceFunc    func(workspaceapi.URI, schemeapi.Scheme, func(func()) bool) Workspace
	scheduleNextTick func(func()) bool

	schemes    map[string]schemeapi.SchemeFunc
	workspaces map[string]managerWorkspace
	refs       map[string]int
}

// NewManager allocates storage for a new Manager and initializes it
// with the default workspace constructor (NewSchemeWorkspace).
// See Manager.Init for more details.
func NewManager(
	cfg config.Config, scheduleNextTick func(func()) bool,
) *Manager {
	return NewManagerWithWorkspaceFunc(cfg, scheduleNextTick, NewSchemeWorkspace)
}

// NewManagerWithWorkspaceFunc allocates storage for a new Manager and initializes it
// with the given workspace constructor. See Manager.Init for more details.
func NewManagerWithWorkspaceFunc(
	cfg config.Config,
	scheduleNextTick func(func()) bool,
	workspaceFunc func(workspaceapi.URI, schemeapi.Scheme, func(func()) bool) Workspace,
) *Manager {
	ret := new(Manager)
	ret.Init(cfg, scheduleNextTick, workspaceFunc)
	return ret
}

// Init initializes m and register a default implementation for local file management
// under the file:// scheme.
func (m *Manager) Init(
	cfg config.Config,
	scheduleNextTick func(func()) bool,
	workspaceFunc func(workspaceapi.URI, schemeapi.Scheme, func(func()) bool) Workspace,
) {
	m.cfg = cfg
	m.schemes = make(map[string]schemeapi.SchemeFunc)
	m.workspaces = make(map[string]managerWorkspace)
	m.refs = make(map[string]int)
	m.workspaceFunc = workspaceFunc
	m.scheduleNextTick = scheduleNextTick
}

// RegisterScheme registers a new scheme for the given scheme and uses fn
// to allocate it for new workspaces. It returns ErrSchemeAlreadyRegistered if there's
// already a scheme registered for the given scheme.
func (m *Manager) RegisterScheme(scheme string, fn schemeapi.SchemeFunc) error {
	_, ok := m.schemes[scheme]
	if ok {
		return schemeapi.ErrSchemeAlreadyRegistered
	}
	m.log(log.DebugLevel, "RegisterScheme %q", scheme)
	if log.IsLevelEnabled(log.TraceLevel) {
		fn = LoggingScheme(scheme, fn)
	}
	m.schemes[scheme] = fn
	return nil
}

// UnregisterScheme unregisters the given scheme.
func (m *Manager) UnregisterScheme(scheme string) error {
	_, ok := m.schemes[scheme]
	if !ok {
		return fmt.Errorf("scheme %q not registered", scheme)
	}
	m.log(log.DebugLevel, "UnregisterScheme %q", scheme)
	delete(m.schemes, scheme)
	return nil
}

func (m *Manager) removeWorkspace(uri workspaceapi.URI) {
	delete(m.workspaces, uri.String())
	delete(m.refs, uri.String())
}

// Scheme returns a SchemeFunc for the given URI.
func (m *Manager) Scheme(uri workspaceapi.URI) (schemeapi.SchemeFunc, error) {
	schemeFn, ok := m.schemes[uri.Scheme()]
	if !ok {
		return nil, fmt.Errorf("scheme not registered %q", uri.Scheme())
	}
	return schemeFn, nil
}

// AddWorkspace returns a Workspace capable of managing resources on
// the given URI. It returns an error if no Scheme has been registered
// (previously via RegisterScheme) for the given workspace's scheme. Note that
// the given URI should not be a file URI.
//
// If a workspace has already been added for the given URI, then this
// method returns it. This method does not follow the same semantics as
// Workspace as the latter uses IsWorkspaceURI semantics and this
// will create a new workspace if the uri strings are different.
func (m *Manager) AddWorkspace(
	ctx context.Context, uri workspaceapi.URI,
) (Workspace, error) {
	if w, ok := m.workspaces[uri.String()]; ok {
		return w, nil
	}

	schemeFn, ok := m.schemes[uri.Scheme()]
	if !ok {
		return nil, fmt.Errorf("scheme not registered %q", uri.Scheme())
	}
	cfg, err := m.cfg.GetConfig(uri.Scheme())
	if err != nil && err != config.ErrNotFound {
		return nil, fmt.Errorf("unable to load scheme config: %s", err)
	}
	if cfg == nil {
		cfg = config.NopConfig()
	}
	scheme, err := schemeFn(ctx, cfg, uri)
	if err != nil {
		return nil, fmt.Errorf("new workspace %q: %w", uri, err)
	}

	workspace := m.workspaceFunc(uri, scheme, m.scheduleNextTick)
	managerWorkspace := managerWorkspace{uri: uri, m: m, Workspace: workspace}
	m.workspaces[uri.String()] = managerWorkspace

	m.log(log.DebugLevel, "AddWorkspace(%q)", uri.String())

	return managerWorkspace, nil
}

// Workspace returns a Workspace suitable for the given file
// or false if there's currently no Workspace initialized.
func (m *Manager) Workspace(file workspaceapi.URI) (Workspace, bool, error) {
	for _, workspace := range m.workspaces {
		is, err := CanWorkspaceURI(workspace, file)
		if err != nil {
			return nil, false, fmt.Errorf("CanWorkspaceURI: %s", err)
		}
		if is {
			return workspace, true, nil
		}
	}
	return nil, false, nil
}

// HasWorkspace reports whether this Manager currently tracks a workspace
// registered under the given URI. This is an exact key lookup, unlike
// Workspace which matches any workspace whose scheme/host/port/user are
// compatible with the given URI.
func (m *Manager) HasWorkspace(uri workspaceapi.URI) bool {
	_, ok := m.workspaces[uri.String()]
	return ok
}

// IncrementReference bumps the refcount for the workspace
// registered under uri. See [WorkspaceManager.IncrementReference].
// Calling IncrementReference for a uri that was never added via
// AddWorkspace is a no-op so a stray increment cannot resurrect a
// torn-down entry.
func (m *Manager) IncrementReference(uri workspaceapi.URI) {
	if _, ok := m.workspaces[uri.String()]; !ok {
		return
	}
	m.refs[uri.String()]++
}

// DecrementReference decrements the refcount for the workspace at
// uri and closes it once the count reaches zero. See
// [WorkspaceManager.DecrementReference].
func (m *Manager) DecrementReference(uri workspaceapi.URI) error {
	key := uri.String()
	count, referenced := m.refs[key]
	if !referenced {
		return nil
	}
	count--
	if count > 0 {
		m.refs[key] = count
		return nil
	}
	w, ok := m.workspaces[key]
	if !ok {
		delete(m.refs, key)
		return nil
	}
	return w.Close()
}

// RemoveWorkspace unregisters uri and returns the underlying
// Workspace, unwrapped so closing it cannot touch the manager's maps.
// Callers are responsible for calling Close on the returned
// workspace. Stale wrapped handles from before the remove stay
// harmless: their Close only evicts the entry they correspond to.
func (m *Manager) RemoveWorkspace(uri workspaceapi.URI) (Workspace, bool) {
	w, ok := m.workspaces[uri.String()]
	if !ok {
		return nil, false
	}
	m.removeWorkspace(uri)
	m.log(log.DebugLevel, "RemoveWorkspace(%q)", uri.String())
	return w.Workspace, true
}

// Close closes all resources associated with this Manager.
func (m *Manager) Close() error {
	var ret error
	for _, workspace := range m.workspaces {
		if err := workspace.Close(); err != nil {
			ret = multierr.Append(ret, err)
		}
	}
	return ret
}

func (m *Manager) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "workspace.Manager").
		Logf(level, msg, args...)
}

// adds remove on Close
type managerWorkspace struct {
	m   *Manager
	uri workspaceapi.URI
	Workspace
}

func (w managerWorkspace) Close() error {
	ret := w.Workspace.Close()
	// A stale handle can outlive RemoveWorkspace and a re-add of the
	// same uri: only evict the entry this handle corresponds to, so a
	// late Close cannot remove a successor instance.
	cur, ok := w.m.workspaces[w.uri.String()]
	if ok && cur.Workspace == w.Workspace {
		w.m.removeWorkspace(w.uri)
	}
	return ret
}

// OnDisconnect forwards to the wrapped workspace's RemoteScheme
// when present. The embedded Workspace interface field does not
// promote OnDisconnect (RemoteScheme is an optional interface);
// without this explicit forwarder, callers that type-assert the
// value returned by Manager.AddWorkspace against RemoteScheme
// would silently miss SSH transport drops and leave dead VTEs in
// the reservoir after reconnect.
func (w managerWorkspace) OnDisconnect() <-chan struct{} {
	if rs, ok := w.Workspace.(RemoteScheme); ok {
		return rs.OnDisconnect()
	}
	return nil
}

// WaitConnected forwards [RemoteScheme.WaitConnected] when the
// wrapped workspace is remote. Local workspaces are always
// "connected".
func (w managerWorkspace) WaitConnected(ctx context.Context) error {
	if rs, ok := w.Workspace.(RemoteScheme); ok {
		return rs.WaitConnected(ctx)
	}
	return nil
}

// HostConn forwards [PackageHost.HostConn] when the wrapped workspace
// supports it.
func (w managerWorkspace) HostConn() (grpc.ClientConnInterface, bool) {
	return hostConn(w.Workspace)
}
