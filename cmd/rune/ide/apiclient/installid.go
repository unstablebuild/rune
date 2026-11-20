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

package apiclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
)

const (
	// installIDDocID is the document ID under the "telemetry" storage
	// partition that holds the authoritative install identifier.
	installIDDocID = "install"

	// installIDTempSalt seeds installIDTempFileName. Only the salt lives in the
	// binary; the backup filename is derived at runtime so a `strings` scan of
	// the executable never reveals the name to look for in TempDir. It is not a
	// secret, just one extra step of indirection.
	installIDTempSalt = "lsd-cache-v1"
)

// installIDDoc is the stored shape of the authoritative install identifier.
type installIDDoc struct {
	ID string
}

// installIDTempFileName derives the obscure, dotfile-style backup filename from
// installIDTempSalt. It is deterministic (same name on every machine) so the
// two locations stay in sync across runs.
func installIDTempFileName() string {
	sum := sha256.Sum256([]byte(installIDTempSalt))
	return "." + hex.EncodeToString(sum[:8])
}

// getInstallID resolves a stable, self-generated install identifier from two
// independent locations: the authoritative storage partition and an obscure
// backup file in backupDir.
//
// It returns the identifier, a tampered flag, and a diagnostic error string.
// tampered is true only when the authoritative store was missing while the
// backup survived, which is the signal of a naive attempt to reset the
// identifier by wiping the data directory. A missing backup is never treated
// as tampering because OS temp reaping is routine.
//
// Resolution never blocks telemetry: on failure it degrades to whatever
// identifier it could recover (possibly empty). The returned string is empty
// on success and otherwise carries the persistence/self-heal failures so they
// are reported through telemetry instead of being silently dropped.
func getInstallID(
	ctx context.Context, store storageapi.Service, backupDir string,
) (id string, tampered bool, errStr string) {
	var errs []error
	backupPath := filepath.Join(backupDir, installIDTempFileName())

	stored, hasStored, readErr := readStoredInstallID(ctx, store)
	if readErr != nil {
		errs = append(errs, fmt.Errorf("read store: %w", readErr))
	}
	backup, hasBackup := readTempInstallID(backupPath)

	switch {
	case hasStored && hasBackup:
		// Both present. Trust the authoritative store; realign the backup if
		// they diverged (e.g. a stale copy from a previous install).
		if stored != backup {
			if err := writeTempInstallID(backupPath, stored); err != nil {
				errs = append(errs, fmt.Errorf("write temp: %w", err))
			}
		}
		id = stored

	case !hasStored && hasBackup:
		// Authoritative store vanished but the obscure backup survived: the
		// tampered signal. Recover the identifier and rewrite the store.
		if err := writeStoredInstallID(ctx, store, backup); err != nil {
			errs = append(errs, fmt.Errorf("write store: %w", err))
		}
		id, tampered = backup, true

	case hasStored && !hasBackup:
		// Backup vanished (routine temp reaping). Recover it silently.
		if err := writeTempInstallID(backupPath, stored); err != nil {
			errs = append(errs, fmt.Errorf("write temp: %w", err))
		}
		id = stored

	default:
		// Fresh install: generate and persist to both locations.
		newID := uuid.New().String()
		if err := writeStoredInstallID(ctx, store, newID); err != nil {
			errs = append(errs, fmt.Errorf("write store: %w", err))
		}
		if err := writeTempInstallID(backupPath, newID); err != nil {
			errs = append(errs, fmt.Errorf("write temp: %w", err))
		}
		id = newID
	}

	return id, tampered, joinInstallIDErrs(errs)
}

func joinInstallIDErrs(errs []error) string {
	if len(errs) == 0 {
		return ""
	}
	return errors.Join(errs...).Error()
}

// readStoredInstallID reports the stored identifier and whether it is present.
// A missing document is an expected absence, not an error, so it yields
// ("", false, nil); only unexpected failures return a non-nil error.
func readStoredInstallID(ctx context.Context, store storageapi.Service) (string, bool, error) {
	var doc installIDDoc
	err := store.Get(ctx, installIDDocID, &doc)
	switch {
	case errors.Is(err, storageapi.ErrNotFound):
		return "", false, nil
	case err != nil:
		return "", false, err
	case doc.ID == "":
		return "", false, nil
	default:
		return doc.ID, true, nil
	}
}

func writeStoredInstallID(ctx context.Context, store storageapi.Service, id string) error {
	return store.Set(ctx, installIDDocID, installIDDoc{ID: id})
}

func readTempInstallID(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", false
	}
	return id, true
}

func writeTempInstallID(path, id string) error {
	return os.WriteFile(path, []byte(id), 0o600)
}
