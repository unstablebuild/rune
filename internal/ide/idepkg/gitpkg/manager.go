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

// Package gitpkg implements a release.Manager that installs extension
// packages straight from a git repository over HTTPS. Package IDs have
// the form <host>/<path> (for example github.com/owner/repo or
// gitlab.com/group/subgroup/repo) and double as the clone URL. Versions
// are the moving `latest` (default-branch HEAD), a published tag, or a
// commit hash. This manager handles only git IDs; routing between it and
// the official distribution lives in the multipkg package.
package gitpkg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	git "github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/storage/memory"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
)

// ErrNotSupported is returned for release.Manager operations that have
// no meaning for git packages (create, upload, remote delete).
var ErrNotSupported = errors.New("not supported for git packages")

// shortSHALen is the length of the abbreviated commit SHA used as the
// package version.
const shortSHALen = 12

// pkgIDSegment matches a single host or path segment. The character set
// is intentionally strict so a validated ID is safe by construction to
// nest in on-disk paths.
var pkgIDSegment = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// IsGitPkgID reports whether id is a git package ID of the form
// <host>/<path>, where host is a domain (contains a dot, e.g.
// github.com, gitlab.com, git.example.com) and path has at least two
// segments (owner/repo, or deeper for hosts with nested groups such as
// gitlab.com/group/subgroup/repo). The id doubles as the https clone
// URL, so every segment is validated to be safe to nest on disk.
func IsGitPkgID(id string) bool {
	parts := strings.Split(id, "/")
	if len(parts) < 3 {
		return false
	}
	host := parts[0]
	if !strings.Contains(host, ".") {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." || !pkgIDSegment.MatchString(part) {
			return false
		}
	}
	return true
}

// Option configures the git package manager.
type Option func(*manager)

// WithRemoteURL overrides how a git package ID is mapped to a git
// remote URL. Used by tests to point IDs at local fixture servers.
func WithRemoteURL(f func(pkgID string) string) Option {
	return func(m *manager) { m.remoteURL = f }
}

// manager implements release.Manager for git package IDs. It resolves
// versions and bundles straight from the repository; other package IDs
// are not its concern (see multipkg for routing).
type manager struct {
	remoteURL func(pkgID string) string
}

var _ release.Manager = (*manager)(nil)

// New returns a release.Manager that resolves git package IDs against
// their repositories.
func New(opts ...Option) release.Manager {
	m := &manager{
		remoteURL: func(pkgID string) string {
			return "https://" + pkgID
		},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// remoteRefs is the result of an ls-remote against a git package
// repository: the resolved default-branch HEAD commit and the tag names
// the repository publishes.
type remoteRefs struct {
	head plumbing.Hash
	tags []string
}

// resolveRefs lists the remote repository's refs without cloning, like
// git ls-remote, returning the resolved HEAD commit and the tag names.
func (m *manager) resolveRefs(ctx context.Context, pkgID string) (remoteRefs, error) {
	rem := git.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{
		Name: "origin",
		URLs: []string{m.remoteURL(pkgID)},
	})
	refs, err := rem.ListContext(ctx, &git.ListOptions{})
	if err != nil {
		return remoteRefs{}, fmt.Errorf("list refs of %s: %w", pkgID, err)
	}
	byName := make(map[plumbing.ReferenceName]*plumbing.Reference, len(refs))
	var tags []string
	for _, ref := range refs {
		byName[ref.Name()] = ref
		if ref.Name().IsTag() {
			tags = append(tags, ref.Name().Short())
		}
	}
	head, ok := byName[plumbing.HEAD]
	if !ok {
		return remoteRefs{}, fmt.Errorf("repository %s has no HEAD", pkgID)
	}
	if head.Type() == plumbing.SymbolicReference {
		target, ok := byName[head.Target()]
		if !ok {
			return remoteRefs{}, fmt.Errorf(
				"repository %s HEAD points at unknown ref %s", pkgID, head.Target())
		}
		head = target
	}
	return remoteRefs{head: head.Hash(), tags: tags}, nil
}

// resolveHead resolves just the default-branch HEAD commit.
func (m *manager) resolveHead(ctx context.Context, pkgID string) (plumbing.Hash, error) {
	refs, err := m.resolveRefs(ctx, pkgID)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return refs.head, nil
}

func shortSHA(h plumbing.Hash) release.Version {
	return release.Version(h.String()[:shortSHALen])
}

// GetPackage satisfies release.Manager. For git IDs the Latest
// version is the abbreviated SHA of the repository's HEAD commit.
func (m *manager) GetPackage(ctx context.Context, pkgID string) (release.Package, error) {
	head, err := m.resolveHead(ctx, pkgID)
	if err != nil {
		return release.Package{}, err
	}
	return release.Package{
		Name:   pkgID,
		Latest: shortSHA(head),
		Notes:  "git package " + pkgID,
	}, nil
}

// List satisfies release.Manager. A git package is installable at
// the moving `latest` version (the default-branch HEAD) plus each of
// the repository's tags, so users can pin to a tag or track HEAD.
func (m *manager) List(
	ctx context.Context, pkgID string, filters map[string]string,
) (iterator.Iterator[release.Bundle], error) {
	refs, err := m.resolveRefs(ctx, pkgID)
	if err != nil {
		return nil, err
	}
	bundles := make([]release.Bundle, 0, len(refs.tags)+1)
	bundles = append(bundles, release.Bundle{
		Package: pkgID,
		Version: release.Latest,
	})
	for _, tag := range refs.tags {
		bundles = append(bundles, release.Bundle{
			Package: pkgID,
			Version: release.Version(tag),
		})
	}
	return iterator.FromSlice(bundles), nil
}

// resolvedVersion describes how a requested version maps to a git
// checkout and to the version identity stored on disk.
type resolvedVersion struct {
	// stored is the version identity recorded for the install: the
	// short HEAD sha for `latest`, else the tag name or commit hash as
	// requested.
	stored release.Version
	// tag is the tag to clone, when the request named one.
	tag string
	// commitRev is the (possibly abbreviated) commit revision to check
	// out, when the request named one.
	commitRev string
	// head is set when the request resolves to the default-branch HEAD
	// (`latest` or empty).
	head bool
}

// resolveInstallVersion maps a requested version to a checkout target
// and the identity to store. `latest`/empty resolve to HEAD; a name
// matching a published tag resolves to that tag; a hex string of a
// commit-hash length resolves to that commit. Anything else is
// rejected before any clone happens.
func resolveInstallVersion(
	pkgID string, version release.Version, refs remoteRefs,
) (resolvedVersion, error) {
	if version == "" || version == release.Latest {
		return resolvedVersion{stored: shortSHA(refs.head), head: true}, nil
	}
	for _, tag := range refs.tags {
		if release.Version(tag) == version {
			return resolvedVersion{stored: version, tag: tag}, nil
		}
	}
	if isCommitHash(string(version)) {
		return resolvedVersion{
			stored:    version,
			commitRev: string(version),
		}, nil
	}
	return resolvedVersion{}, fmt.Errorf(
		"unknown version %q for %s: expected %q, a tag, or a commit hash",
		version, pkgID, release.Latest)
}

var commitHashPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

func isCommitHash(s string) bool {
	return commitHashPattern.MatchString(s)
}

// Get satisfies release.Manager. It resolves the requested version to a
// git checkout (`latest` → default-branch HEAD, a tag, or a commit
// hash), verifies the repository's package config, and streams a
// tar.gz of the worktree into the ProgressWriter. The discard writer
// (DescribeRelease) resolves the version identity without cloning.
func (m *manager) Get(
	ctx context.Context, pkgID string, version release.Version,
	pw release.ProgressWriter,
) (release.Bundle, error) {
	refs, err := m.resolveRefs(ctx, pkgID)
	if err != nil {
		return release.Bundle{}, err
	}
	rv, err := resolveInstallVersion(pkgID, version, refs)
	if err != nil {
		return release.Bundle{}, err
	}
	if discard, ok := pw.(release.IsDiscard); ok && discard.IsDiscard() {
		return release.Bundle{Package: pkgID, Version: rv.stored}, nil
	}

	dir, err := os.MkdirTemp("", "gitpkg-*")
	if err != nil {
		return release.Bundle{}, fmt.Errorf("create temp clone dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	repo, checkedOut, err := m.cloneVersion(ctx, dir, pkgID, rv)
	if err != nil {
		return release.Bundle{}, err
	}
	if err := verifyRepoConfig(dir); err != nil {
		return release.Bundle{}, fmt.Errorf("%s is not installable: %w", pkgID, err)
	}
	createdAt := commitTime(repo, checkedOut)
	if err := writeTarGz(dir, pw); err != nil {
		return release.Bundle{}, fmt.Errorf("bundle %s: %w", pkgID, err)
	}
	return release.Bundle{
		Package:   pkgID,
		Version:   rv.stored,
		CreatedAt: createdAt,
	}, nil
}

// cloneVersion clones pkgID into dir according to rv and returns the
// repository and the checked-out commit. `latest` and tags use a
// shallow single-ref clone; a specific commit needs the full history
// because the object may be unreachable from any tip of a shallow
// clone.
func (m *manager) cloneVersion(
	ctx context.Context, dir, pkgID string, rv resolvedVersion,
) (*git.Repository, plumbing.Hash, error) {
	switch {
	case rv.head:
		repo, err := git.PlainCloneContext(ctx, dir, &git.CloneOptions{
			URL:   m.remoteURL(pkgID),
			Depth: 1,
			Tags:  plumbing.NoTags,
		})
		if err != nil {
			return nil, plumbing.ZeroHash, fmt.Errorf("clone %s: %w", pkgID, err)
		}
		head, err := repo.Head()
		if err != nil {
			return nil, plumbing.ZeroHash, fmt.Errorf("resolve HEAD of %s: %w", pkgID, err)
		}
		return repo, head.Hash(), nil
	case rv.tag != "":
		repo, err := git.PlainCloneContext(ctx, dir, &git.CloneOptions{
			URL:           m.remoteURL(pkgID),
			ReferenceName: plumbing.NewTagReferenceName(rv.tag),
			SingleBranch:  true,
			Depth:         1,
			Tags:          plumbing.NoTags,
		})
		if err != nil {
			return nil, plumbing.ZeroHash, fmt.Errorf(
				"clone %s at tag %s: %w", pkgID, rv.tag, err)
		}
		head, err := repo.Head()
		if err != nil {
			return nil, plumbing.ZeroHash, fmt.Errorf("resolve HEAD of %s: %w", pkgID, err)
		}
		return repo, head.Hash(), nil
	default:
		repo, err := git.PlainCloneContext(ctx, dir, &git.CloneOptions{
			URL:        m.remoteURL(pkgID),
			NoCheckout: true,
		})
		if err != nil {
			return nil, plumbing.ZeroHash, fmt.Errorf("clone %s: %w", pkgID, err)
		}
		wt, err := repo.Worktree()
		if err != nil {
			return nil, plumbing.ZeroHash, fmt.Errorf("worktree of %s: %w", pkgID, err)
		}
		hash, err := repo.ResolveRevision(plumbing.Revision(rv.commitRev))
		if err != nil {
			return nil, plumbing.ZeroHash, fmt.Errorf(
				"resolve commit %s of %s: %w", rv.commitRev, pkgID, err)
		}
		if err := wt.Checkout(&git.CheckoutOptions{Hash: *hash}); err != nil {
			return nil, plumbing.ZeroHash, fmt.Errorf(
				"checkout commit %s of %s: %w", rv.commitRev, pkgID, err)
		}
		return repo, *hash, nil
	}
}

func commitTime(repo *git.Repository, h plumbing.Hash) time.Time {
	commit, err := repo.CommitObject(h)
	if err != nil {
		return time.Time{}
	}
	return commit.Committer.When
}

// ListPackages satisfies release.Manager. Git repositories cannot be
// enumerated, so no packages are listed.
func (m *manager) ListPackages(
	ctx context.Context, filters map[string]string,
) (iterator.Iterator[release.Package], error) {
	return iterator.FromSlice[release.Package](nil), nil
}

// Create satisfies release.Manager.
func (m *manager) Create(ctx context.Context, pkg release.Package) error {
	return fmt.Errorf("create package: %w", ErrNotSupported)
}

// UpdatePackageMetadata satisfies release.Manager.
func (m *manager) UpdatePackageMetadata(
	ctx context.Context, pkgID string, metadata map[string]string,
) error {
	return fmt.Errorf("update package metadata: %w", ErrNotSupported)
}

// DeletePackage satisfies release.Manager.
func (m *manager) DeletePackage(ctx context.Context, pkgID string) error {
	return fmt.Errorf("delete package: %w", ErrNotSupported)
}

// Upload satisfies release.Manager.
func (m *manager) Upload(
	ctx context.Context, bundle release.Bundle, pr release.ProgressReader,
) error {
	return fmt.Errorf("upload bundle: %w", ErrNotSupported)
}

// Delete satisfies release.Manager.
func (m *manager) Delete(
	ctx context.Context, pkgID string, version release.Version,
) error {
	return fmt.Errorf("delete bundle: %w", ErrNotSupported)
}
