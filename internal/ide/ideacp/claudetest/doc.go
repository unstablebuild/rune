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

// Package claudetest runs Claude Code through Rune's Agent Client Protocol
// stack end to end: an ideacp.Manager starts claude-agent-acp, configured
// as the claude-code package configures it, which runs the local claude
// CLI, and the acp command chats with its sessions in tabs that the tests
// drive and read like a user would.
//
// The tests carry the e2e build tag. They expect claude, logged in, and
// npx on the PATH, and skip without them.
package claudetest
