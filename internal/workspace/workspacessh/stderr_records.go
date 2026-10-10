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

package workspacessh

import (
	"encoding/json"
	"io"
)

// Sentinels tagging the JSON control records the remote `rune -x` server
// writes to stderr, so the local side never mistakes unrelated JSON, or
// a plain line, for one of them.
const (
	// readySentinel is written right before the server starts serving
	// on stdout.
	readySentinel = "ready"
	// warningSentinel carries a warning for the local user.
	warningSentinel = "warning"
)

// stderrRecord is a control record serialized as one JSON object per
// line.
type stderrRecord struct {
	Rune    string `json:"rune"`
	Message string `json:"msg,omitempty"`
}

func encodeStderrRecord(r stderrRecord) (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

func parseStderrRecord(line []byte) (stderrRecord, bool) {
	var r stderrRecord
	if err := json.Unmarshal(line, &r); err != nil {
		return stderrRecord{}, false
	}
	return r, r.Rune != ""
}

func encodeServerReady() (string, error) {
	return encodeStderrRecord(stderrRecord{Rune: readySentinel})
}

// WriteWarning shows message to the local user of the `rune -x` server
// whose stderr is w. The `rune -x` server has no UI of its own.
func WriteWarning(w io.Writer, message string) error {
	line, err := encodeStderrRecord(stderrRecord{
		Rune: warningSentinel, Message: message,
	})
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, line)
	return err
}

// ParseWarningLine returns the message of a line written by
// WriteWarning. ok is false for any other line.
func ParseWarningLine(line []byte) (message string, ok bool) {
	r, ok := parseStderrRecord(line)
	if !ok || r.Rune != warningSentinel {
		return "", false
	}
	return r.Message, true
}

func parseServerReadyLine(line []byte) bool {
	r, ok := parseStderrRecord(line)
	return ok && r.Rune == readySentinel
}
