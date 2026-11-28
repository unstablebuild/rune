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

import (
	"encoding/binary"
	"errors"

	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
)

const (
	// PartitionName is the IDE storage partition every workspace's
	// symbol database is nested under.
	PartitionName = "symboldb"

	filesPartition   = "files"
	symbolsPartition = "symbols"
	namesPartition   = "names"

	metaScanID = "scan"

	// schemaVersion identifies the layout and indexing rules of the
	// persisted partitions. A database written under a different
	// version is fully re-indexed: its derived tables (such as the
	// names partition) may not exist, and per-file records alone cannot
	// rebuild them because unchanged files are normally skipped.
	schemaVersion = 8
)

// skippedParse is the reason a file failed to parse has no definitions.
const skippedParse = "parse failed"

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
// contributed (for invalidation), its import alias→path map (for
// import dedup at query time) and its definitions. Stored in the files
// partition under the file URI. The per-record version lets an
// interrupted schema rebuild resume: records already rewritten by the
// current version are trusted on the next launch, records from older
// versions are not.
type fileDoc struct {
	URI string
	// Path is workspace-relative.
	Path    string
	ModTime int64
	Version int
	// Lang is the language the file was parsed as.
	Lang string
	// Grammar is the fingerprint of the grammar it was parsed with.
	Grammar string
	Names   []string
	Imports map[string]string
	// Skipped is why the file has no definitions, if it has none for a
	// reason.
	Skipped string
	// Defs packs the file's definitions (see encodeDefs): a structured
	// document per definition would be several times larger.
	Defs []byte
}

// metaDoc marks whether a full workspace scan has ever completed and
// which schema version wrote the database.
type metaDoc struct {
	Complete bool
	Version  int
}

// record is one definition of a file. Line and Col are from 1.
type record struct {
	Kind      Kind
	Line, Col int
	Name      string
}

// encodeDefs packs recs: a uvarint count, then for each record its kind
// byte, uvarint line and column, and uvarint-prefixed name.
func encodeDefs(recs []record) []byte {
	if len(recs) == 0 {
		return nil
	}
	n := binary.MaxVarintLen64
	for _, r := range recs {
		n += 1 + 3*binary.MaxVarintLen64 + len(r.Name)
	}
	b := make([]byte, 0, n)
	b = binary.AppendUvarint(b, uint64(len(recs)))
	for _, r := range recs {
		b = append(b, byte(r.Kind))
		b = binary.AppendUvarint(b, uint64(r.Line))
		b = binary.AppendUvarint(b, uint64(r.Col))
		b = binary.AppendUvarint(b, uint64(len(r.Name)))
		b = append(b, r.Name...)
	}
	return b
}

var errCorrupt = errors.New("symboldb: corrupt definitions")

// defsCount returns how many records b packs.
func defsCount(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n, _ := binary.Uvarint(b)
	return int(n)
}

// decodeDefs calls fn with every record b packs, until fn returns false.
func decodeDefs(b []byte, fn func(record) bool) error {
	if len(b) == 0 {
		return nil
	}
	count, k := binary.Uvarint(b)
	if k <= 0 {
		return errCorrupt
	}
	b = b[k:]
	for range count {
		if len(b) == 0 {
			return errCorrupt
		}
		r := record{Kind: Kind(b[0])}
		b = b[1:]
		var vals [3]uint64
		for i := range vals {
			v, k := binary.Uvarint(b)
			if k <= 0 {
				return errCorrupt
			}
			vals[i], b = v, b[k:]
		}
		if uint64(len(b)) < vals[2] {
			return errCorrupt
		}
		r.Line, r.Col, r.Name = int(vals[0]), int(vals[1]), string(b[:vals[2]])
		b = b[vals[2]:]
		if !fn(r) {
			return nil
		}
	}
	return nil
}
