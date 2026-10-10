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

// Package pkgtrust verifies provenance supplied with official package bundles.
package pkgtrust

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	log "github.com/sirupsen/logrus"
	bluecrypto "github.com/unstablebuild/blue/crypto"
	"github.com/unstablebuild/blue/release"
	"unstable.build/rune/internal/debug"
)

const (
	// MetadataSigningKeyID is the bundle metadata key carrying the PGP key
	// ID that signed an official release (see blue's signedrelease).
	MetadataSigningKeyID = "pgp-signing-key-id"
	// MetadataSignature is the bundle metadata key carrying the armored
	// detached PGP signature over the raw release tarball.
	MetadataSignature = "pgp-signature"
)

//go:embed trusted_keys.asc
var embeddedKeyring []byte

// fetchWaitTimeout bounds how long the first VerifyBundle blocks for the
// background keyring fetch to finish before falling back to the cached or
// embedded ring. A stale in-memory ring is acceptable — restarting Rune
// re-fetches — so verification never waits indefinitely.
const fetchWaitTimeout = 5 * time.Second

// KeyringFetcher retrieves the armored PGP keyring the API advertises. It is
// injected so pkgtrust stays decoupled from the oauth2 config transport.
type KeyringFetcher func() (armored []byte, err error)

// Store holds the trust ring used to verify official package bundles.
// A single Store is created at process startup and distributed to every
// component that verifies provenance. When constructed with a
// KeyringFetcher it fetches the served keyring once, in the background, and
// the first VerifyBundle blocks briefly for that fetch to land; the fetched
// ring is never mutated afterwards, so a stale in-memory ring is tolerated
// until the next restart.
type Store struct {
	mu   sync.RWMutex
	ring openpgp.EntityList
	// cachePath persists the last fetched keyring; empty for test stores.
	cachePath string
	// fetchDone is closed once the background fetch completes (success or
	// failure). Nil when no fetcher was supplied, in which case
	// VerifyBundle never waits.
	fetchDone chan struct{}
}

// NewStore returns the trust store rooted at dataDir. A keyring previously
// fetched from the API — cached under dataDir — fully replaces the embedded
// ring so served updates can both rotate and revoke trust anchors; the
// embedded ring backs verification until a fetch succeeds. When fetch is
// non-nil, the store fetches the served keyring once in the background and
// the first VerifyBundle waits up to fetchWaitTimeout for it. Panics if the
// embedded keyring does not parse, which is a build defect.
func NewStore(dataDir string, fetch KeyringFetcher) *Store {
	ring, err := parseArmoredKeyring(embeddedKeyring)
	if err != nil {
		panic(fmt.Sprintf("pkgtrust: parse embedded keyring: %v", err))
	}
	s := &Store{ring: ring, cachePath: filepath.Join(dataDir, "trusted_keys.asc")}
	if data, err := os.ReadFile(s.cachePath); err == nil {
		if cached, perr := parseArmoredKeyring(data); perr == nil {
			s.ring = cached
		} else {
			log.Warnf("parse package trust keyring cache %s: %v", s.cachePath, perr)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.Warnf("read package trust keyring cache: %v", err)
	}
	if fetch != nil {
		s.fetchDone = make(chan struct{})
		go debug.CapturePanicReport(func() { s.runFetch(fetch) })
	}
	return s
}

// NewStoreWithKeyring returns a store over a fixed ring with no cache
// persistence and no background fetch. Tests use it to inject their own
// trust anchors.
func NewStoreWithKeyring(ring openpgp.EntityList) *Store {
	if len(ring) == 0 {
		panic("pkgtrust: empty keyring")
	}
	return &Store{ring: ring}
}

// runFetch performs the one-shot background keyring fetch and closes
// fetchDone so the first VerifyBundle can proceed.
func (s *Store) runFetch(fetch KeyringFetcher) {
	defer close(s.fetchDone)
	armored, err := fetch()
	if err != nil {
		log.Debugf("fetch package trust keyring: %v", err)
		return
	}
	ring, err := parseArmoredKeyring(armored)
	if err != nil {
		log.Warnf("parse fetched package trust keyring: %v", err)
		return
	}
	s.mu.Lock()
	s.ring = ring
	s.mu.Unlock()
	if err := s.persist(armored); err != nil {
		log.Warnf("persist package trust keyring cache: %v", err)
	}
}

// persist atomically writes the fetched armored keyring to the on-disk cache
// so a later offline startup reuses it.
func (s *Store) persist(armored []byte) error {
	cachePath := s.cachePath
	if cachePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return fmt.Errorf("create keyring cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(cachePath), ".trusted_keys-*")
	if err != nil {
		return fmt.Errorf("create keyring cache: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(armored); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write keyring cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close keyring cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), cachePath); err != nil {
		return fmt.Errorf("persist keyring cache: %w", err)
	}
	return nil
}

// awaitFetch blocks until the background fetch finishes or fetchWaitTimeout
// elapses. It is a no-op for stores without a fetcher.
func (s *Store) awaitFetch() {
	if s.fetchDone == nil {
		return
	}
	select {
	case <-s.fetchDone:
	case <-time.After(fetchWaitTimeout):
	}
}

func (s *Store) keyring() openpgp.EntityList {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ring
}

func parseArmoredKeyring(armored []byte) (openpgp.EntityList, error) {
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return nil, fmt.Errorf("read armored keyring: %w", err)
	}
	if len(ring) == 0 {
		return nil, errors.New("armored keyring contains no keys")
	}
	return ring, nil
}

// Provenance identifies the trusted signer of an official package bundle.
type Provenance struct {
	KeyID           string
	Fingerprint     string
	PrimaryIdentity string
	Signature       string
}

// Entry describes one extracted package filesystem entry.
type Entry struct {
	Path   string
	SHA256 string
	Mode   fs.FileMode
	Link   string
}

// Manifest describes the files extracted from a package tarball.
type Manifest []Entry

// SHA256 returns the stable digest of the serialized manifest.
func (m Manifest) SHA256() (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// VerifyEntries checks that every manifest entry exists and remains unchanged.
func VerifyEntries(dir string, entries []Entry) error {
	for _, entry := range entries {
		path := filepath.Join(dir, filepath.FromSlash(entry.Path))
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("stat %s: %w", entry.Path, err)
		}
		if info.Mode() != entry.Mode {
			return fmt.Errorf("mode mismatch for %s", entry.Path)
		}
		if entry.Link != "" {
			link, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read link %s: %w", entry.Path, err)
			}
			if link != entry.Link {
				return fmt.Errorf("link mismatch for %s", entry.Path)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Path, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != entry.SHA256 {
			return fmt.Errorf("sha256 mismatch for %s", entry.Path)
		}
	}
	return nil
}

// VerifyBundle validates an official bundle's detached signature over its raw
// tarball bytes and returns the signing provenance.
func (s *Store) VerifyBundle(bundle release.Bundle, tar io.ReadSeeker) (Provenance, error) {
	s.awaitFetch()
	ring := s.keyring()
	keyID := bundle.Metadata[MetadataSigningKeyID]
	if keyID == "" {
		return Provenance{}, errors.New("bundle lacks pgp-signing-key-id metadata")
	}
	parsedID, err := strconv.ParseUint(strings.TrimPrefix(keyID, "0x"), 16, 64)
	if err != nil {
		return Provenance{}, fmt.Errorf("parse signing key ID %q: %w", keyID, err)
	}
	keys := ring.KeysById(parsedID)
	if len(keys) == 0 {
		return Provenance{}, fmt.Errorf("signing key %q is not trusted", keyID)
	}
	signature := bundle.Metadata[MetadataSignature]
	if signature == "" {
		return Provenance{}, errors.New("bundle lacks PGP signature metadata")
	}
	if _, err := tar.Seek(0, io.SeekStart); err != nil {
		return Provenance{}, fmt.Errorf("seek tarball: %w", err)
	}
	var verified openpgp.Key
	var verifyErr error
	for _, key := range keys {
		if _, err := tar.Seek(0, io.SeekStart); err != nil {
			return Provenance{}, fmt.Errorf("seek tarball: %w", err)
		}
		verifyErr = bluecrypto.Verify(tar, strings.NewReader(signature), bluecrypto.Key(key))
		if verifyErr == nil {
			verified = key
			break
		}
	}
	if verifyErr != nil {
		return Provenance{}, fmt.Errorf("verify bundle signature: %w", verifyErr)
	}
	identity := ""
	if primary := bluecrypto.Key(verified).PrimaryIdentity(); primary != nil {
		identity = primary.Name
	}
	return Provenance{
		KeyID:           fmt.Sprintf("%016X", verified.PublicKey.KeyId),
		Fingerprint:     strings.ToUpper(hex.EncodeToString(verified.Entity.PrimaryKey.Fingerprint)),
		PrimaryIdentity: identity,
		Signature:       signature,
	}, nil
}

// IsTrustedFingerprint reports whether fp is a primary-key fingerprint in the
// trust ring.
func (s *Store) IsTrustedFingerprint(fp string) bool {
	for _, entity := range s.keyring() {
		fingerprint := strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint))
		if strings.EqualFold(fp, fingerprint) {
			return true
		}
	}
	return false
}
