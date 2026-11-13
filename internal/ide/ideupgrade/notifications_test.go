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

package ideupgrade

import (
	"strconv"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
)

// recordingNotifications is a browserapi.Notifications that counts the
// notifications posted.
type recordingNotifications struct {
	mu      sync.Mutex
	notifyN int
}

func (n *recordingNotifications) Notify(_ browserapi.NotificationLevel, _ string, _ ...any) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notifyN++
	return "noti-" + strconv.Itoa(n.notifyN), nil
}

func (n *recordingNotifications) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *recordingNotifications) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

func (n *recordingNotifications) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.notifyN
}
