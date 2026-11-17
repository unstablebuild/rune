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

package vterpc

import (
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/term/vte"
)

// FromModel fills x with info.
func (x *Info) FromModel(info vte.Info) {
	x.Id = string(info.ID)
	x.Uri = info.URI.String()
	x.Title = info.Title
	x.CommandAndArgs = info.CommandAndArgs
	x.Width = int32(info.Width)
	x.Height = int32(info.Height)
	x.Attachments = int32(info.Attachments)
	x.Complete = info.Complete
	x.CreatedAtUnixNano = info.CreatedAt.UnixNano()
}

// ToModel converts x to its model representation. A URI that does not
// parse is left empty rather than failing the listing it came in.
func (x *Info) ToModel() vte.Info {
	uri, _ := workspaceapi.ParseURI(x.GetUri())
	return vte.Info{
		ID:             vte.ID(x.GetId()),
		URI:            uri,
		Title:          x.GetTitle(),
		CommandAndArgs: x.GetCommandAndArgs(),
		Width:          int(x.GetWidth()),
		Height:         int(x.GetHeight()),
		Attachments:    int(x.GetAttachments()),
		Complete:       x.GetComplete(),
		CreatedAt:      time.Unix(0, x.GetCreatedAtUnixNano()),
	}
}

// FromModel fills x with change.
func (x *Change) FromModel(change vte.Change) {
	x.Id = string(change.ID)
	x.Uri = change.URI.String()
	x.Title = change.Title
	x.Attachments = int32(change.Attachments)
	x.Liveness = LivenessFromModel(change.Liveness)
	x.Complete = change.Complete
	x.Deleted = change.Deleted
}

// ToModel converts x to its model representation.
func (x *Change) ToModel() vte.Change {
	uri, _ := workspaceapi.ParseURI(x.GetUri())
	return vte.Change{
		ID:          vte.ID(x.GetId()),
		URI:         uri,
		Title:       x.GetTitle(),
		Attachments: int(x.GetAttachments()),
		Liveness:    x.GetLiveness().ToModel(),
		Complete:    x.GetComplete(),
		Deleted:     x.GetDeleted(),
	}
}

// LivenessFromModel converts l to its wire representation.
func LivenessFromModel(l vte.Liveness) Liveness {
	switch l {
	case vte.LivenessIdle:
		return Liveness_IDLE
	case vte.LivenessRunning:
		return Liveness_RUNNING
	default:
		return Liveness_UNKNOWN
	}
}

// ToModel converts x to its model representation.
func (x Liveness) ToModel() vte.Liveness {
	switch x {
	case Liveness_IDLE:
		return vte.LivenessIdle
	case Liveness_RUNNING:
		return vte.LivenessRunning
	default:
		return vte.LivenessUnknown
	}
}
