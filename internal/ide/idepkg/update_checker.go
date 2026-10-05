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

package idepkg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/auth"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/text"
)

// Update represents an available package update.
type Update struct {
	Package string
	Current release.Version
	Latest  release.Version
}

// UpdateChecker detects available updates for installed packages
// and notifies/prompts the user.
type UpdateChecker struct {
	m      *Manager
	now    func() time.Time
	cancel context.CancelFunc
}

// NewUpdateChecker returns a new UpdateChecker.
func NewUpdateChecker(m *Manager) *UpdateChecker {
	return &UpdateChecker{m: m, now: time.Now}
}

// Close cancels any in-flight update check started by Start.
func (uc *UpdateChecker) Close() error {
	if uc.cancel != nil {
		uc.cancel()
	}
	return nil
}

// CheckForUpdates compares the in-use version of every package installed
// through pm against the registry and returns the available updates.
// Packages with no in-use version are skipped. It fails with
// auth.ErrNotAuthenticated when the registry requires a login.
func CheckForUpdates(ctx context.Context, pm PackageManager) ([]Update, error) {
	it, err := pm.ListInstalledPackages(ctx)
	if err != nil {
		return nil, fmt.Errorf("list installed packages: %w", err)
	}
	defer it.Close()

	seen := make(map[string]struct{})
	var updates []Update

	for {
		pkgID, ok := it.Next(ctx)
		if !ok {
			break
		}
		if pkgID == "" {
			continue
		}
		if _, dup := seen[pkgID]; dup {
			continue
		}
		seen[pkgID] = struct{}{}

		u, err := checkPackage(ctx, pm, pkgID)
		if err != nil {
			if errors.Is(err, auth.ErrNotAuthenticated) {
				// Skip the entire run silently; the user has not logged in.
				return nil, err
			}
			log.WithField(logging.KeyClass, "idepkg.CheckForUpdates").
				Warnf("update check for %s: %v", pkgID, err)
			continue
		}
		if u != nil {
			updates = append(updates, *u)
		}
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("installed packages iterator: %w", err)
	}

	return updates, nil
}

func checkPackage(ctx context.Context, pm PackageManager, pkgID string) (*Update, error) {
	current, ok, err := pm.PackageVersionInUse(ctx, pkgID)
	if err != nil {
		return nil, fmt.Errorf("version in use: %w", err)
	}
	if !ok {
		return nil, nil
	}

	pkg, err := pm.DescribePackage(ctx, pkgID)
	if err != nil {
		return nil, fmt.Errorf("describe package: %w", err)
	}
	if pkg.Latest == "" || release.Version(pkg.Latest) == current {
		return nil, nil
	}

	return &Update{
		Package: pkgID,
		Current: current,
		Latest:  release.Version(pkg.Latest),
	}, nil
}

// Start runs the update check flow in a background goroutine.
// Use Close to cancel the check if it's still running.
func (uc *UpdateChecker) Start(ctx context.Context) {
	ctx, uc.cancel = context.WithCancel(ctx)
	go debug.CapturePanicReport(func() {
		uc.run(ctx)
	})
}

const (
	updateCheckLastKey    = "update-check:last"
	updateAvailablePrefix = "update-available:"
	updateAvailableIndex  = "update-available:__index__"
	throttleDuration      = 24 * time.Hour
	promptAfterDuration   = 7 * 24 * time.Hour
)

type updateCheckValue struct {
	Timestamp time.Time
}

type updateAvailableValue struct {
	Package   string
	Current   release.Version
	Latest    release.Version
	FirstSeen time.Time
	Skipped   bool
}

type updateAvailableIndexValue struct {
	Packages []string
}

func (uc *UpdateChecker) run(ctx context.Context) {
	if uc.isThrottled(ctx) {
		return
	}

	updates, err := CheckForUpdates(ctx, uc.m)
	if err != nil {
		if errors.Is(err, auth.ErrNotAuthenticated) {
			// Update check is opportunistic; skip silently when not authenticated.
			return
		}
		uc.m.log(log.WarnLevel, "check for updates: %v", err)
		return
	}

	uc.persistThrottle(ctx)

	if len(updates) == 0 {
		uc.cleanupStaleRecords(ctx, nil)
		return
	}

	currentPkgIDs := make(map[string]struct{}, len(updates))
	for _, u := range updates {
		currentPkgIDs[u.Package] = struct{}{}
		uc.upsertUpdateRecord(ctx, u)
	}

	uc.cleanupStaleRecords(ctx, currentPkgIDs)

	summary := formatUpdateNotification(updates)
	uc.m.scheduleNextTick(func() {
		_, _ = uc.m.n.NotifyOnce(browserapi.LevelInfo, summary)
	})

	promptUpdates := uc.filterPromptUpdates(ctx, updates)
	if len(promptUpdates) > 0 {
		uc.showUpdatePrompt(ctx, promptUpdates)
	}
}

func (uc *UpdateChecker) isThrottled(ctx context.Context) bool {
	var val updateCheckValue
	err := uc.m.storage.Get(ctx, updateCheckLastKey, &val)
	if err != nil {
		return false
	}
	return uc.now().Sub(val.Timestamp) < throttleDuration
}

func (uc *UpdateChecker) persistThrottle(ctx context.Context) {
	val := updateCheckValue{Timestamp: uc.now()}
	if err := uc.m.storage.Set(ctx, updateCheckLastKey, val); err != nil {
		uc.m.log(log.WarnLevel, "persist throttle: %v", err)
	}
}

func (uc *UpdateChecker) upsertUpdateRecord(ctx context.Context, u Update) {
	key := updateAvailablePrefix + u.Package
	var existing updateAvailableValue
	err := uc.m.storage.Get(ctx, key, &existing)

	now := uc.now()
	if err != nil {
		// new record
		val := updateAvailableValue{
			Package:   u.Package,
			Current:   u.Current,
			Latest:    u.Latest,
			FirstSeen: now,
		}
		if err := uc.m.storage.Set(ctx, key, val); err != nil {
			uc.m.log(log.WarnLevel, "upsert update record %s: %v", u.Package, err)
		}
		uc.addToIndex(ctx, u.Package)
		return
	}

	if existing.Latest == u.Latest {
		// same version — update current only
		existing.Current = u.Current
		if err := uc.m.storage.Set(ctx, key, existing); err != nil {
			uc.m.log(log.WarnLevel, "upsert update record %s: %v", u.Package, err)
		}
		return
	}

	// newer version available — reset
	val := updateAvailableValue{
		Package:   u.Package,
		Current:   u.Current,
		Latest:    u.Latest,
		FirstSeen: now,
	}
	if err := uc.m.storage.Set(ctx, key, val); err != nil {
		uc.m.log(log.WarnLevel, "upsert update record %s: %v", u.Package, err)
	}
	uc.addToIndex(ctx, u.Package)
}

func (uc *UpdateChecker) addToIndex(ctx context.Context, pkgID string) {
	var idx updateAvailableIndexValue
	_ = uc.m.storage.Get(ctx, updateAvailableIndex, &idx)

	if slices.Contains(idx.Packages, pkgID) {
		return
	}
	idx.Packages = append(idx.Packages, pkgID)
	if err := uc.m.storage.Set(ctx, updateAvailableIndex, idx); err != nil {
		uc.m.log(log.WarnLevel, "update index: %v", err)
	}
}

func (uc *UpdateChecker) cleanupStaleRecords(ctx context.Context, currentPkgIDs map[string]struct{}) {
	var idx updateAvailableIndexValue
	if err := uc.m.storage.Get(ctx, updateAvailableIndex, &idx); err != nil {
		return
	}

	var remaining []string
	for _, pkgID := range idx.Packages {
		if currentPkgIDs != nil {
			if _, ok := currentPkgIDs[pkgID]; ok {
				remaining = append(remaining, pkgID)
				continue
			}
		}
		key := updateAvailablePrefix + pkgID
		_ = uc.m.storage.Delete(ctx, key)
	}

	if len(remaining) == 0 {
		_ = uc.m.storage.Delete(ctx, updateAvailableIndex)
		return
	}

	idx.Packages = remaining
	if err := uc.m.storage.Set(ctx, updateAvailableIndex, idx); err != nil {
		uc.m.log(log.WarnLevel, "cleanup index: %v", err)
	}
}

func (uc *UpdateChecker) filterPromptUpdates(ctx context.Context, updates []Update) []Update {
	var result []Update
	now := uc.now()
	for _, u := range updates {
		key := updateAvailablePrefix + u.Package
		var val updateAvailableValue
		if err := uc.m.storage.Get(ctx, key, &val); err != nil {
			continue
		}
		if val.Skipped {
			continue
		}
		if now.Sub(val.FirstSeen) >= promptAfterDuration {
			result = append(result, u)
		}
	}
	return result
}

func formatUpdateSummary(updates []Update) string {
	var b strings.Builder
	b.WriteString("## Updates available:")
	for _, u := range updates {
		fmt.Fprintf(&b, "\n- %s %s → %s", u.Package, u.Current, u.Latest)
	}
	return b.String()
}

// formatUpdateNotification is formatUpdateSummary for notifications,
// which render plain text rather than markdown.
func formatUpdateNotification(updates []Update) string {
	var b strings.Builder
	b.WriteString("Updates available:")
	for _, u := range updates {
		fmt.Fprintf(&b, "\n- %s %s → %s", u.Package, u.Current, u.Latest)
	}
	b.WriteString("\n\nRun 'pkg update-all' in the console to update.")
	return b.String()
}

func markdownOrFallback(
	parser syntaxapi.Parser, scheduleNextTick func(func()) bool,
) func(string) component.Floating {
	return func(str string) component.Floating {
		mcfg := markdown.DefaultConfig()
		mcfg.HeaderPrefix = false
		mcfg.ScheduleNextTick = scheduleNextTick
		mcfg.Parser = parser
		mkd, err := markdown.NewWithConfig(str, mcfg)
		if err == nil {
			return component.NewSpan(mkd, component.SpanConfig{
				PadHorizontal:    4,
				PadVertical:      2,
				ContentAlignment: component.AlignmentCentered,
			})
		}
		cfg := component.StringResponsiveConfig{
			NoSplitWords: true,
			StringConfig: component.StringConfig{
				PaddingVertical:   4,
				PaddingHorizontal: 4,
				Alignment:         component.AlignmentCentered,
			},
		}
		messageResponsive := component.NewResponsiveString(str, cfg)

		return component.NewAspectRatioFloatingResponsive(
			messageResponsive, component.DefaultAspectRatio)
	}
}

func (uc *UpdateChecker) showUpdatePrompt(ctx context.Context, updates []Update) {
	message := formatUpdateSummary(updates) + "\n\nInstall updates?"

	prompt := handler.NewPrompt(handler.PromptConfig{
		HighlightAttr: term.Attributes{
			Attrs: term.AttrBold,
			Bg:    term.ColorRed,
		},
		OptionAttr: term.Attributes{
			Attrs: term.AttrBold,
			Bg:    term.ColorGray,
		},
		OptionBindings: []term.KeyComb{{Ch: 'u'}, {Ch: 'r'}, {Ch: 's'}},
		PromptConfig: component.PromptConfig{
			Message:    message,
			Options:    []string{"   Upgrade All   ", "   Remind Later", "   Skip   "},
			NewMessage: markdownOrFallback(uc.m.parser, uc.m.scheduleNextTick),
		},
		PromptHandler: handler.FuncPromptHandler(func(idx int, _ string) {
			switch idx {
			case 0: // Update All
				go debug.CapturePanicReport(func() {
					for _, u := range updates {
						pw := text.NewNotifyProgressWriter(uc.m.n, uc.m.interrupter,
							fmt.Sprintf("install %s@%s", u.Package, u.Latest),
							uc.m.scheduleNextTick)
						if err := uc.m.InstallPackageVersion(ctx, u.Package, u.Latest, pw); err != nil {
							uc.m.log(log.WarnLevel, "install update %s %s: %v", u.Package, u.Latest, err)
						}
					}
				})
			case 2: // Skip These Versions
				for _, u := range updates {
					if err := uc.SkipVersion(ctx, u.Package, u.Latest); err != nil {
						uc.m.log(log.WarnLevel, "skip version %s %s: %v", u.Package, u.Latest, err)
					}
				}
			}
			// case 1 (Remind Later): no-op
		}, func() error {
			return nil
		}),
	})

	uc.m.scheduleNextTick(func() {
		_, err := uc.m.wm.Floating(prompt, browserapi.FloatingConfig{
			Alignment: component.AlignmentCentered,
		})
		if err != nil {
			uc.m.log(log.WarnLevel, "show update prompt: %v", err)
		}
	})
}

// SkipVersion marks a specific version of a package as skipped,
// so the user won't be prompted to install it.
func (uc *UpdateChecker) SkipVersion(ctx context.Context, pkgID string, version release.Version) error {
	key := updateAvailablePrefix + pkgID
	var val updateAvailableValue
	err := uc.m.storage.Get(ctx, key, &val)
	if err != nil {
		if errors.Is(err, storageapi.ErrNotFound) {
			val = updateAvailableValue{
				Package:   pkgID,
				Latest:    version,
				FirstSeen: uc.now(),
				Skipped:   true,
			}
			return uc.m.storage.Set(ctx, key, val)
		}
		return err
	}

	if val.Latest != version {
		return nil
	}

	val.Skipped = true
	return uc.m.storage.Set(ctx, key, val)
}
