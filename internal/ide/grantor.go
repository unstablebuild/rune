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
	"net/url"
	"sort"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
)

var _ extension.Grantor = (*permissionGrantor)(nil)

const (
	permissionDecisionAllow = "allow"
)

const (
	permissionPromptAllowOnce   = "   Once   "
	permissionPromptAllowAlways = "   Always   "
	permissionPromptDenyOnce    = "   No   "
)

type permissionDecision struct {
	Decision string
}

// permissionGrantor prompts once at extension startup for extensions that are
// not attested by a signed package from a trusted publisher. An "Always"
// decision is persisted so the prompt does not reappear across sessions.
type permissionGrantor struct {
	promptOpener     ideauthorizer.PromptOpener
	storage          storageapi.Service
	scheduleNextTick func(func()) bool
	trust            *pkgtrust.Store
}

func newExtensionPromptGrantor(
	promptOpener ideauthorizer.PromptOpener,
	storage storageapi.Service,
	scheduleNextTick func(func()) bool,
	trust *pkgtrust.Store,
) extension.Grantor {
	if promptOpener == nil || storage == nil || scheduleNextTick == nil || trust == nil {
		panic("newExtensionPromptGrantor: nil dependency")
	}
	return &permissionGrantor{
		promptOpener:     promptOpener,
		storage:          storage,
		scheduleNextTick: scheduleNextTick,
		trust:            trust,
	}
}

func (g *permissionGrantor) Grant(
	meta extensionapi.Metadata, verifiedPublisher string,
) (bool, error) {
	if verifiedPublisher != "" && g.trust.IsTrustedFingerprint(verifiedPublisher) {
		return true, nil
	}
	ctx := context.Background()
	key := permissionStorageKey(meta)
	var stored permissionDecision
	err := g.storage.Get(ctx, key, &stored)
	if err == nil {
		switch stored.Decision {
		case permissionDecisionAllow:
			return true, nil
		default:
			return false, fmt.Errorf("unknown stored extension "+
				"permission decision %q", stored.Decision)
		}
	}
	if !errors.Is(err, storageapi.ErrNotFound) {
		return false, fmt.Errorf("get extension permission decision: %w", err)
	}

	decision := g.prompt(meta)
	switch decision {
	case permissionPromptAllowAlways:
		err := g.storage.Set(ctx, key, permissionDecision{Decision: permissionDecisionAllow})
		if err != nil {
			return false, fmt.Errorf("set extension permission decision: %w", err)
		}
		return true, nil
	case permissionPromptAllowOnce:
		return true, nil
	case permissionPromptDenyOnce:
		return false, nil
	default:
		return false, nil
	}
}

func (g *permissionGrantor) prompt(meta extensionapi.Metadata) string {
	result := make(chan string, 1)
	var b strings.Builder
	fmt.Fprintf(&b, "Allow extension **%s** (%s) by **%s** to run?",
		meta.ExtensionName, meta.ExtensionVersion, meta.DeveloperID)
	if len(meta.Permissions) > 0 {
		b.WriteString("\n\nIt requests permission to:\n")
		perms := make([]string, 0, len(meta.Permissions))
		for perm := range meta.Permissions {
			perms = append(perms, ideauthorizer.PermissionActionText(perm))
		}
		sort.Strings(perms)
		for _, action := range perms {
			b.WriteString("\n- " + action)
		}
	}
	message := b.String()
	options := []string{
		permissionPromptAllowOnce,
		permissionPromptAllowAlways,
		permissionPromptDenyOnce,
	}
	bindings := []term.KeyComb{{Ch: 'o'}, {Ch: 'a'}, {Ch: 'n'}}
	scheduled := g.scheduleNextTick(func() {
		g.promptOpener.Prompt(message, options, bindings, handler.FuncPromptHandler(
			func(i int, opt string) {
				select {
				case result <- opt:
				default:
				}
			},
			func() error {
				select {
				case result <- permissionPromptDenyOnce:
				default:
				}
				return nil
			}))
	})
	if !scheduled {
		return permissionPromptDenyOnce
	}
	return <-result
}

func permissionStorageKey(meta extensionapi.Metadata) string {
	parts := []string{
		"extension-permission",
		meta.DeveloperID,
		meta.DeveloperKey,
		meta.ExtensionID,
		meta.ExtensionName,
	}
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
