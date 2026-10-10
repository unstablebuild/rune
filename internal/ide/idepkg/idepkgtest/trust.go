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

package idepkgtest

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/ProtonMail/go-crypto/openpgp"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
)

// testEntity is the lazily generated PGP entity that signs every tarball the
// mock ReleaseManager returns. Generating it once keeps signing cheap across a
// package's tests while still exercising the real verification path.
var (
	testEntityOnce sync.Once
	testEntity     *openpgp.Entity
)

func sharedTestEntity() *openpgp.Entity {
	testEntityOnce.Do(func() {
		entity, err := openpgp.NewEntity("Rune Test Publisher", "", "test@rune.build", nil)
		if err != nil {
			panic(fmt.Sprintf("idepkgtest: generate test entity: %v", err))
		}
		testEntity = entity
	})
	return testEntity
}

// TrustStore returns a pkgtrust.Store that trusts the entity the mock
// ReleaseManager signs bundles with, so idepkg.Manager tests exercise
// mandatory verification without a production backdoor.
func TrustStore() *pkgtrust.Store {
	return pkgtrust.NewStoreWithKeyring(openpgp.EntityList{sharedTestEntity()})
}

// signTestPayload signs payload with the shared test entity and returns the
// signing key ID and armored detached signature for bundle metadata.
func signTestPayload(payload []byte) (keyID, signature string, err error) {
	entity := sharedTestEntity()
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, entity, bytes.NewReader(payload), nil); err != nil {
		return "", "", fmt.Errorf("sign test payload: %w", err)
	}
	return fmt.Sprintf("%016X", entity.PrimaryKey.KeyId), sig.String(), nil
}

func cloneMetadata(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}
