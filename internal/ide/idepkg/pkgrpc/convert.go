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

package pkgrpc

import (
	"time"
	"unicode/utf8"

	"github.com/unstablebuild/blue/release"
	"google.golang.org/protobuf/types/known/timestamppb"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc/pkgrpcpb"
)

func packageToProto(p release.Package) *pkgrpcpb.Package {
	return &pkgrpcpb.Package{
		Name:      p.Name,
		Latest:    string(p.Latest),
		Notes:     p.Notes,
		Metadata:  p.Metadata,
		CreatedAt: timeToProto(p.CreatedAt),
	}
}

func packageFromProto(p *pkgrpcpb.Package) release.Package {
	return release.Package{
		Name:      p.GetName(),
		Latest:    release.Version(p.GetLatest()),
		Notes:     p.GetNotes(),
		Metadata:  p.GetMetadata(),
		CreatedAt: timeFromProto(p.GetCreatedAt()),
	}
}

func bundleToProto(b release.Bundle) *pkgrpcpb.Bundle {
	return &pkgrpcpb.Bundle{
		Package:   b.Package,
		Version:   string(b.Version),
		Notes:     b.Notes,
		Metadata:  b.Metadata,
		CreatedAt: timeToProto(b.CreatedAt),
	}
}

func bundleFromProto(b *pkgrpcpb.Bundle) release.Bundle {
	return release.Bundle{
		Package:   b.GetPackage(),
		Version:   release.Version(b.GetVersion()),
		Notes:     b.GetNotes(),
		Metadata:  b.GetMetadata(),
		CreatedAt: timeFromProto(b.GetCreatedAt()),
	}
}

func promptToProto(id uint64, p idepkg.ConfigPrompt) *pkgrpcpb.Prompt {
	options := make([]*pkgrpcpb.PromptOption, len(p.Options))
	for i, opt := range p.Options {
		options[i] = &pkgrpcpb.PromptOption{Label: opt.Label}
		if opt.Key != 0 {
			options[i].Key = string(opt.Key)
		}
	}
	return &pkgrpcpb.Prompt{
		Id:       id,
		Message:  p.Message,
		Options:  options,
		MaxWidth: uint32(max(p.MaxWidth, 0)),
	}
}

func promptFromProto(p *pkgrpcpb.Prompt) idepkg.ConfigPrompt {
	options := make([]idepkg.PromptOption, len(p.GetOptions()))
	for i, opt := range p.GetOptions() {
		options[i] = idepkg.PromptOption{Label: opt.GetLabel()}
		if key, _ := utf8.DecodeRuneInString(opt.GetKey()); key != utf8.RuneError {
			options[i].Key = key
		}
	}
	return idepkg.ConfigPrompt{
		Message:  p.GetMessage(),
		Options:  options,
		MaxWidth: int(p.GetMaxWidth()),
	}
}

// timeToProto keeps a zero time zero across the wire.
func timeToProto(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func timeFromProto(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime()
}
