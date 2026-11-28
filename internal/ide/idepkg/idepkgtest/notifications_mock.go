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

package idepkgtest

import (
	"errors"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
)

var _ browserapi.Notifications = (*Notifications)(nil)

// Notifications is a version of browserapi.Notifications for testing.
type Notifications struct {
	ExpectErrorNotification bool

	t  testing.TB
	mu sync.Mutex
	wg *sync.WaitGroup

	i      int
	active map[string]Noti
	err    error
}

// SetWg creates a new sync.WaitGroup with the given count and sets it
// as the active wait group. Notify will call Done on it for every
// LevelError or LevelSuccess notification. Use Wait to block until
// the expected notifications have fired.
func (n *Notifications) SetWg(count int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	wg := new(sync.WaitGroup)
	wg.Add(count)
	n.wg = wg
}

// ClearWg removes the active wait group so that subsequent
// notifications do not call Done.
func (n *Notifications) ClearWg() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.wg = nil
}

// Wait blocks until the active wait group counter reaches zero.
func (n *Notifications) Wait() {
	n.mu.Lock()
	wg := n.wg
	n.mu.Unlock()
	if wg != nil {
		wg.Wait()
	}
}

// NewNotifications returns an instance of Notifications.
func NewNotifications(t testing.TB) *Notifications {
	return &Notifications{
		t:      t,
		active: make(map[string]Noti),
	}
}

// ExpectReturnErr configures Notifications to return an error
// on the next call to Notify or NotifyOnce.
func (n *Notifications) ExpectReturnErr(err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.err = err
}

// Notify satisfies browserapi.Notifications.
func (n *Notifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.ExpectErrorNotification && level == browserapi.LevelError {
		n.t.Logf("no error notification was expected: %s", fmt.Sprintf(msg, args...))
		if n.wg != nil {
			n.wg.Done()
		}
		n.t.FailNow()
	}
	n.i++
	id := strconv.Itoa(n.i)
	n.active[id] = Noti{Level: level, Msg: fmt.Sprintf(msg, args...)}
	switch level {
	case browserapi.LevelError, browserapi.LevelSuccess:
		if n.wg != nil {
			n.wg.Done()
		}
	}
	return id, n.err
}

// NotifyOnce satisfies browserapi.Notifications.
func (n *Notifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

// UpdateNotificationProgress satisfies browserapi.Notifications.
func (n *Notifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.active[id]
	if !ok {
		return errors.New("unknown notification")
	}
	if progress == total {
		delete(n.active, id)
	}
	return n.err
}

// Reset resets the active notifications and error expectations.
func (n *Notifications) Reset() {
	n.mu.Lock()
	defer n.mu.Unlock()

	clear(n.active)
	n.ExpectErrorNotification = false
}

// Active returns the currently active notifications.
func (n *Notifications) Active() (ret map[string]Noti) {
	n.mu.Lock()
	defer n.mu.Unlock()

	ret = make(map[string]Noti)
	maps.Copy(ret, n.active)
	return
}

// RequireNoErrorNotification asserts that there are no error notifications active.
func (n *Notifications) RequireNoErrorNotification() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, noti := range n.active {
		require.NotEqual(n.t, browserapi.LevelError, noti.Level, noti.Msg)
		require.NotEqual(n.t, browserapi.LevelWarn, noti.Level, noti.Msg)
	}
}

// RequireErrorNotification asserts that there are indeed at least
// one error notifications active.
func (n *Notifications) RequireErrorNotification() {
	n.mu.Lock()
	defer n.mu.Unlock()
	var found bool
	for _, noti := range n.active {
		if noti.Level == browserapi.LevelError {
			found = true
		}
	}
	require.True(n.t, found, n.active)
}

// Noti is an active notification.
type Noti struct {
	Level browserapi.NotificationLevel
	Msg   string
}
