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

package browsertest

import "github.com/unstablebuild/rune-go-sdk/api/browserapi"

type nopNotifications struct{}

func (nopNotifications) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopNotifications) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopNotifications) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

// NopNotifications returns a browserapi.Notifications implementation
// whose methods are no-ops.
func NopNotifications() browserapi.Notifications {
	return nopNotifications{}
}
