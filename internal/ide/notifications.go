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
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"strconv"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/notifications"
)

type notisManager struct {
	storage storageapi.Service
	cfg     notifications.Config
	parent  workspaceManagerIfc
}

func newWorkspaceNotifications(
	storage storageapi.Service,
	cfg notifications.Config,
	parent workspaceManagerIfc,
) *notisManager {
	return &notisManager{
		storage: storage,
		cfg:     cfg,
		parent:  parent,
	}
}

func (w *notisManager) new(
	uri workspaceapi.URI, c *notifications.Container,
) browserapi.Notifications {
	return &notis{
		cfg:     w.cfg,
		parent:  w.parent,
		storage: w.storage,
		root:    c,
		uri:     uri,
	}
}

func (w *notisManager) current() browserapi.Notifications {
	return &notisRouter{parent: w.parent}
}

type notisRouter struct {
	parent workspaceManagerIfc
}

// uriHashSep separates the origin workspace's URI hash from the
// underlying notification id. The underlying ids are decimal digit
// strings, so this byte never appears inside them.
const uriHashSep = ':'

// withURIHash prefixes id with a hash of the origin workspace URI so a
// later UpdateNotificationProgress can be routed back to the container
// that created the notification, regardless of current focus.
func withURIHash(uri workspaceapi.URI, id string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(uri.String()))
	return strconv.FormatUint(h.Sum64(), 16) + string(uriHashSep) + id
}

// uriHash returns the hash withURIHash embeds for uri.
func uriHash(uri workspaceapi.URI) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(uri.String()))
	return strconv.FormatUint(h.Sum64(), 16)
}

// splitURIHash reverses withURIHash. ok is false when id carries no
// hash prefix (e.g. it originated outside the router).
func splitURIHash(id string) (hash, realID string, ok bool) {
	i := strings.IndexByte(id, uriHashSep)
	if i < 0 {
		return "", "", false
	}
	return id[:i], id[i+1:], true
}

type workspaceManagerIfc interface {
	focusHandler() tui.Handler
	focusURI() workspaceapi.URI
	// addWorkspaceNotification counts a notification the workspace at
	// the uri received while out of focus, so its bar tab can show it
	// with the attribute of the highest level pending.
	addWorkspaceNotification(workspaceapi.URI, browserapi.NotificationLevel, term.Attributes)
	// notificationsForURIHash returns the pinned notifications of the
	// installed workspace whose URI hashes to h, or nil when no such
	// workspace is installed. It lets the router deliver an id-addressed
	// update to the workspace that originally created the notification.
	notificationsForURIHash(h string) browserapi.Notifications
}

func (r *notisRouter) focusNotifications() browserapi.Notifications {
	focus := r.parent.focusHandler()
	if focus == nil {
		return nopNotifications{}
	}
	if h, ok := focus.(*workspaceHandler); ok {
		if h == nil || h.ex == nil || h.ex.notifications == nil {
			return nopNotifications{}
		}
		return h.ex.notifications
	}
	// empty workspace
	ex, ok := focus.(*ex)
	if !ok || ex == nil || ex.notifications == nil {
		return nopNotifications{}
	}
	return ex.notifications
}

func (r *notisRouter) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	id, err := r.focusNotifications().Notify(level, msg, args...)
	if err != nil || id == "" {
		return id, err
	}
	return withURIHash(r.parent.focusURI(), id), nil
}

func (r *notisRouter) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	id, err := r.focusNotifications().NotifyOnce(level, msg, args...)
	if err != nil || id == "" {
		return id, err
	}
	return withURIHash(r.parent.focusURI(), id), nil
}

func (r *notisRouter) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	// Notify/NotifyOnce prepend the origin workspace's URI hash to the
	// id, so a background progress update reaches the container that
	// created the notification even after focus moves elsewhere. Without
	// the prefix (e.g. an id from another source) fall back to focus.
	hash, realID, ok := splitURIHash(id)
	if !ok {
		return r.focusNotifications().
			UpdateNotificationProgress(id, message, progress, total)
	}
	target := r.parent.notificationsForURIHash(hash)
	if target == nil {
		return r.focusNotifications().
			UpdateNotificationProgress(realID, message, progress, total)
	}
	return target.UpdateNotificationProgress(realID, message, progress, total)
}

type nopNotifications struct{}

func (nopNotifications) Notify(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	return "", nil
}

func (nopNotifications) NotifyOnce(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	return "", nil
}

func (nopNotifications) UpdateNotificationProgress(
	string, string, int64, int64,
) error {
	return nil
}

type notifier interface {
	Notify(level notifications.Level, msg string) string
	ID(level notifications.Level, msg string) string
	UpdateProgress(id, message string, progress, total int64) bool
	PauseAll()
}

type notis struct {
	parent  workspaceManagerIfc
	root    notifier
	storage storageapi.Service
	uri     workspaceapi.URI
	cfg     notifications.Config
}

// stand-in type for NotifyOnce
type storedNotification struct {
	Kind string
	ID   string
}

const (
	notificationDocumentKind   = "notification"
	notificationDocumentPrefix = "notifications:"
)

func notificationDocumentID(id string) string {
	return notificationDocumentPrefix + url.QueryEscape(id)
}

// Notify formats the given msg and args and displays it on next Draw.
func (c *notis) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	id := c.root.Notify(notifications.Level(level), fmt.Sprintf(msg, args...))
	if !c.inFocus() {
		c.root.PauseAll()
		c.setTabAttr(level)
	}
	return id, nil
}

// NotifyOnce behaves like Notify, but only sends this notification once.
func (c *notis) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	ctx := context.Background()
	id := c.root.ID(notifications.Level(level), fmt.Sprintf(msg, args...))
	var value storedNotification
	value.Kind = notificationDocumentKind
	value.ID = id
	err := c.storage.Create(ctx, notificationDocumentID(id), value)
	if err == storageapi.ErrAlreadyExists {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("storage create: %v", err)
	}
	id = c.root.Notify(notifications.Level(level), fmt.Sprintf(msg, args...))
	if !c.inFocus() {
		c.root.PauseAll()
		c.setTabAttr(level)
	}
	return id, nil
}

// UpdateNotificationProgress satisfies browser.Notifications.
func (c *notis) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	ok := c.root.UpdateProgress(id, message, progress, total)
	if !ok {
		return errors.New("could not find notification with the " +
			"given id, or it already expired")
	}
	if !c.inFocus() {
		c.root.PauseAll()
	}
	return nil
}

func (c *notis) inFocus() bool {
	uri := c.parent.focusURI()
	return uri.Equal(c.uri)
}

func (c *notis) setTabAttr(level browserapi.NotificationLevel) {
	attr := term.Attributes{Fg: term.ColorWhite}
	switch level {
	case browserapi.LevelError:
		attr.Fg = c.cfg.ColorError.Fg
	case browserapi.LevelWarn:
		attr.Fg = c.cfg.ColorWarning.Fg
	case browserapi.LevelInfo:
		attr.Fg = c.cfg.ColorInfo.Fg
	case browserapi.LevelSuccess:
		attr.Fg = c.cfg.ColorSuccess.Fg
	}
	c.parent.addWorkspaceNotification(c.uri, level, attr)
}
