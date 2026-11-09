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

package symboldb

import "unstable.build/rune/internal/ide/idelsp/symbolresolve"

const (
	// PartitionName is the IDE storage partition every workspace's
	// symbol database is nested under.
	PartitionName = "symboldb"

	filesPartition   = "files"
	symbolsPartition = "symbols"
	namesPartition   = "names"

	metaScanID = "scan"

	// schemaVersion identifies the layout of the persisted partitions.
	// A database written under a different version is fully re-indexed:
	// its derived tables (such as the names partition) may not exist,
	// and per-file records alone cannot rebuild them because unchanged
	// files are normally skipped.
	schemaVersion = 6
)

// Kinds of stored symbol locations. Lookups prefer refs, then defs,
// then method defs among the locations stored under the queried name;
// nested-module languages pre-materialize one name per dotted module
// suffix at extraction time, so a single exact Get answers every
// addressable form. The values are persisted, so the mapping from
// symbolresolve.SymbolKind is explicit rather than relying on that
// enum's ordering.
const (
	kindRef = iota
	kindDef
	kindMethodDef
)

// storedKind maps an extracted symbol kind to its persisted value.
func storedKind(k symbolresolve.SymbolKind) int {
	switch k {
	case symbolresolve.SymbolDef:
		return kindDef
	case symbolresolve.SymbolMethodDef:
		return kindMethodDef
	default:
		return kindRef
	}
}

// symbolLoc is one location contributed by a file to a symbol.
type symbolLoc struct {
	URI  string
	X    int
	Y    int
	Kind int
}

// symbolDoc aggregates every known location of one qualified symbol
// name. Stored in the symbols partition under the name itself.
//
// Version is a compare-and-swap counter: concurrent indexer workers
// merge their per-file contributions through Update calls
// preconditioned on it. Docs written before the counter existed carry
// no Version field; writers match that absence with a nil
// precondition, so a decoded Version of 0 always means "not yet
// stamped" — no writer ever stores 0.
//
// A doc whose Locs are empty is a tombstone: the symbol vanished from
// every file that contributed it. Deleting the doc instead would race
// a concurrent worker's location add, because Delete takes no
// preconditions. Readers treat an empty doc as a miss.
type symbolDoc struct {
	Name    string
	Locs    []symbolLoc
	Version int64
}

// nameDoc marks one symbol name as listed. Stored in the names
// partition under the name itself so ListReferencedSymbols can stream
// names without decoding the location-heavy symbol docs: the storage
// backend's List decodes every doc in a partition in full.
type nameDoc struct {
	Name string
}

// listed reports whether locs contain at least one reference,
// definition or method definition, the criterion for a name to appear
// in ListReferencedSymbols. Every stored kind is addressable, so all
// of them are offered as completion candidates.
func listed(locs []symbolLoc) bool {
	for _, l := range locs {
		if l.Kind == kindRef || l.Kind == kindDef || l.Kind == kindMethodDef {
			return true
		}
	}
	return false
}

// fileDoc records a file's contribution to the index: the timestamp
// and schema version it was indexed under, the symbol names it
// contributed (for invalidation) and its import alias→path map (for
// import dedup at query time). Stored in the files partition under the
// file URI. The per-record version lets an interrupted schema rebuild
// resume: records already rewritten by the current version are trusted
// on the next launch, records from older versions are not.
type fileDoc struct {
	URI     string
	Path    string
	ModTime int64
	Version int
	Grammar string
	Names   []string
	Imports map[string]string
}

// metaDoc marks whether a full workspace scan has ever completed and
// which schema version wrote the database.
type metaDoc struct {
	Complete bool
	Version  int
}
