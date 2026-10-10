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

package pkgconsole

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ernestrc/go-multierror"
	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idepkg"
)

func (h *Handler) handleInstall(
	ctx context.Context, args []string, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	_, args = stripLangFlag(args)
	if len(args) == 0 {
		return nil, errors.New("package name is missing")
	}
	pkgID := args[0]
	version := release.Version(release.Latest)
	if len(args) >= 2 {
		version = release.Version(args[1])
	}
	if version == release.Latest {
		var err error
		version, err = h.getLatestVersion(ctx, pkgID)
		if err != nil {
			return nil, fmt.Errorf("install package: %w", err)
		}
	}
	if err := h.mgr.InstallPackageVersion(ctx, pkgID, version, pw); err != nil {
		return nil, err
	}
	return markdownOutput(fmt.Sprintf("Installed **%s@%s**", pkgID, version)), nil
}

func (h *Handler) handleRemove(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, errors.New("package name is missing")
	}
	pkgID := args[0]
	var version release.Version
	if len(args) >= 2 {
		version = release.Version(args[1])
	}
	if version == "" {
		err := h.mgr.DeletePackage(ctx, pkgID)
		if err != nil {
			if errors.Is(err, idepkg.ErrNotInstalled) {
				return nil, fmt.Errorf("package %s is not installed", pkgID)
			}
			return nil, err
		}
		return markdownOutput(fmt.Sprintf("Removed package **%s**", pkgID)), nil
	}
	err := h.mgr.DeletePackageVersion(ctx, pkgID, version, false)
	if err != nil {
		if errors.Is(err, idepkg.ErrNotInstalled) {
			return nil, fmt.Errorf("version %s of package %s is not installed", version, pkgID)
		}
		if errors.Is(err, idepkg.ErrVersionInUse) {
			return nil, fmt.Errorf("version %s of package %s is "+
				"currently in use, run 'pkg use' with some other version first before removing, "+
				"or pass no version argument to remove all package versions",
				version, pkgID)
		}
		return nil, err
	}
	return markdownOutput(
		fmt.Sprintf("Removed version **%s** of package **%s**", version, pkgID)), nil
}

func (h *Handler) handleUse(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) < 2 {
		return nil, errors.New("package name or version are missing")
	}
	pkgID := args[0]
	version := release.Version(args[1])
	if err := h.mgr.UsePackageVersion(ctx, pkgID, version); err != nil {
		if errors.Is(err, idepkg.ErrVersionInUse) {
			return nil, fmt.Errorf("version %s is already in use", version)
		}
		return nil, err
	}
	return markdownOutput(
		fmt.Sprintf("Version **%s** of package **%s** is now in use", version, pkgID)), nil
}

func (h *Handler) handleCurrent(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) < 1 {
		return nil, errors.New("package name is missing")
	}
	pkgID := args[0]
	version, ok, err := h.mgr.PackageVersionInUse(ctx, pkgID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf(
			"no version of package %s is currently in use", pkgID)
	}
	return markdownOutput(
		fmt.Sprintf("Version **%s** of package **%s** is in use", version, pkgID)), nil
}

func (h *Handler) handleDescribe(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, errors.New("package name is missing")
	}
	pkgID := args[0]
	pkg, err := h.mgr.DescribePackage(ctx, pkgID)
	if err != nil {
		return nil, err
	}
	version := release.Version(release.Latest)
	if len(args) >= 2 {
		version = release.Version(args[1])
	}
	if version == release.Latest {
		version, err = h.getLatestVersion(ctx, pkgID)
		if err != nil {
			return nil, err
		}
	}
	bundle, err := h.mgr.DescribeRelease(ctx, pkgID, string(version))
	if err != nil {
		return nil, err
	}
	return markdownOutput(describeMarkdown(pkg, version, bundle.Notes)), nil
}

func describeMarkdown(pkg release.Package, version release.Version, bundleNotes string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", pkg.Name)
	fmt.Fprintf(&b, "Latest version: **%s**\n\n", version)
	if pkg.Metadata[languageMetadataKey] == "true" {
		b.WriteString("_This is a language (runtime) package._\n\n")
	}
	b.WriteString("## Package notes\n\n")
	if pkg.Notes != "" {
		fmt.Fprintf(&b, "%s\n\n", pkg.Notes)
	} else {
		b.WriteString("_No package notes._\n\n")
	}
	fmt.Fprintf(&b, "## Release %s notes\n\n", version)
	if bundleNotes != "" {
		fmt.Fprintf(&b, "%s\n\n", bundleNotes)
	} else {
		b.WriteString("_No release notes._\n\n")
	}
	return b.String()
}

func (h *Handler) handleUpdateAll(
	ctx context.Context, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	it, err := h.mgr.ListInstalledPackages(ctx)
	if err != nil {
		return nil, err
	}
	packages, err := blueiterator.ToSlice(ctx, it)
	if err != nil {
		return nil, err
	}
	if len(packages) == 0 {
		return nil, errors.New("no packages are installed")
	}

	type result struct {
		pkg     string
		latest  release.Version
		updated bool
		err     error
	}
	results := make([]result, len(packages))
	var wg sync.WaitGroup
	wg.Add(len(packages))
	for i := range packages {
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			pkgID := packages[i]
			results[i].pkg = pkgID
			inUse, ok, err := h.mgr.PackageVersionInUse(ctx, pkgID)
			if err != nil {
				results[i].err = err
				return
			}
			if !ok {
				results[i].err = fmt.Errorf(
					"no version of package %s is currently in use", pkgID)
				return
			}
			latest, err := h.getLatestVersion(ctx, pkgID)
			if err != nil {
				results[i].err = fmt.Errorf("cannot update package %s: %w", pkgID, err)
				return
			}
			results[i].latest = latest
			if latest == inUse {
				return
			}
			if err := h.mgr.InstallPackageVersion(ctx, pkgID, latest, pw); err != nil {
				results[i].err = fmt.Errorf("update package version: %w", err)
				return
			}
			results[i].updated = true
		})
	}
	wg.Wait()

	var b strings.Builder
	b.WriteString("## Package updates\n\n")
	var ret error
	for _, r := range results {
		switch {
		case r.err != nil:
			fmt.Fprintf(&b, "- **%s**: error: %v\n", r.pkg, r.err)
			ret = multierror.Append(ret, r.err)
		case r.updated:
			fmt.Fprintf(&b, "- **%s**: updated to %s\n", r.pkg, r.latest)
		default:
			fmt.Fprintf(&b, "- **%s**: already at latest version (%s)\n", r.pkg, r.latest)
		}
	}
	return markdownOutput(b.String()), ret
}

func (h *Handler) handleUpdateCheck(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	updates, err := idepkg.CheckForUpdates(ctx, h.mgr)
	if err != nil {
		return nil, err
	}
	if len(updates) == 0 {
		return markdownOutput("All packages are up to date."), nil
	}
	var b strings.Builder
	b.WriteString("## Available updates\n\n")
	for _, u := range updates {
		fmt.Fprintf(&b, "- **%s**: %s → %s\n", u.Package, u.Current, u.Latest)
	}
	return markdownOutput(b.String()), nil
}

// languageMetadataKey marks a package as a language/runtime package when its
// value is "true". The `--lang` flag filters `pkg install` completions to these
// packages.
const languageMetadataKey = "language"

// langFlag is the autocomplete-only flag that scopes `pkg install` to language
// packages.
const langFlag = "--lang"

// stripLangFlag reports whether args contains the --lang flag and returns args
// with that flag removed so positional indices stay correct.
func stripLangFlag(args []string) (bool, []string) {
	lang := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == langFlag {
			lang = true
			continue
		}
		rest = append(rest, a)
	}
	return lang, rest
}

func (h *Handler) completePkgInstall(
	ctx context.Context, args []string,
) (iterator.Iterator[string], error) {
	if len(args) == 1 && strings.HasPrefix(args[0], "-") {
		return iterator.FromSlice(filterNames([]string{langFlag}, args[0])), nil
	}
	lang, args := stripLangFlag(args)
	if len(args) <= 1 {
		it, err := h.mgr.ListPackages(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("list packages: %w", err)
		}
		it = blueiterator.Filter(it, func(in release.Package) bool {
			return (in.Metadata[languageMetadataKey] == "true") == lang
		})
		return blueiterator.Map(it,
			func(in release.Package) string { return in.Name }), nil
	}
	if len(args) == 2 {
		it, err := h.mgr.ListPackageVersions(ctx, args[0], nil)
		if err != nil {
			return nil, fmt.Errorf("list packages: %w", err)
		}
		return blueiterator.Map(it,
			func(in release.Bundle) string { return string(in.Version) }), nil
	}
	return iterator.FromSlice[string](nil), nil
}

func (h *Handler) completePkgInstalled(
	ctx context.Context, args []string, showVersions bool,
) (iterator.Iterator[string], error) {
	if len(args) <= 1 {
		it, err := h.mgr.ListInstalledPackages(ctx)
		if err != nil {
			return nil, fmt.Errorf("list packages: %w", err)
		}
		return it, nil
	}
	if len(args) == 2 && showVersions {
		it, err := h.mgr.ListInstalledPackageVersions(ctx, args[0])
		if err != nil {
			return nil, fmt.Errorf("list packages: %w", err)
		}
		return blueiterator.Map(it,
			func(in release.Version) string { return string(in) }), nil
	}
	return iterator.FromSlice[string](nil), nil
}

func (h *Handler) getLatestVersion(
	ctx context.Context, pack string,
) (release.Version, error) {
	version, err := h.mgr.LatestVersion(ctx, pack)
	if err != nil {
		if errors.Is(err, idepkg.ErrPackageNotFound) {
			return "", storageapi.ErrNotFound
		}
		return "", err
	}
	return version, nil
}
