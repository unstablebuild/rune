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

package extensionv2

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

var _ io.Writer = (*logCollector)(nil)

// logCollector parses an extension's stderr stream into structured
// log records. Lines that parse as JSON are forwarded to logrus and
// end up homogenized in $RUNE_DATADIR/debug.log; the same record is also
// written to a per-extension log file in human-readable text form so
// that file remains the full record of the extension's stderr.
// Lines that do not parse as JSON are written verbatim to the same
// per-extension file. Either way, `extensions logs <id>` shows
// exactly what the extension said.
//
// It is written to from a single goroutine — os/exec's internal
// stderr-copy goroutine — so it is not safe for concurrent Write
// calls. Close must only be called after that goroutine has finished
// (i.e. after exec.Cmd.Wait returns), which the workspaceRunner
// guarantees by invoking Close from setExtensionExit.
type logCollector struct {
	buffer        bytes.Buffer
	scanner       *bufio.Reader
	logger        *log.Logger
	extensionID   string
	workspace     workspaceapi.URI
	logFile       *os.File
	fileFormatter log.Formatter
	// fileEntry and fileBuf are reused across Write calls so that
	// rendering each parsed record to the per-extension log file
	// does not allocate a fresh logrus.Entry plus bytes.Buffer per
	// line. Write is single-writer (see type doc), so reusing them
	// is safe.
	fileEntry *log.Entry
	fileBuf   bytes.Buffer
	// fields is the scratch map filled by json.Unmarshal on each
	// line. WithFields and TextFormatter.Format both consume it
	// without retaining a reference, so we can reuse it across
	// Write iterations to avoid one map allocation per line.
	fields      log.Fields
	writeErrLog bool
}

// newCollector creates a logCollector that forwards JSON log records
// to logrus and writes any non-JSON lines into logFile. The collector
// takes ownership of logFile and closes it when Close is called.
// logFile must not be nil — every extension is allocated a
// per-extension log file at start time and the rest of the runtime
// (including `extensions logs`) relies on that invariant.
func newCollector(
	extensionID string, workspace workspaceapi.URI, logFile *os.File,
) *logCollector {
	if logFile == nil {
		panic("extensionv2: newCollector requires a non-nil logFile")
	}
	ret := new(logCollector)
	ret.logger = log.StandardLogger()
	ret.scanner = bufio.NewReader(&ret.buffer)
	ret.extensionID = extensionID
	ret.workspace = workspace
	ret.logFile = logFile
	// Text format chosen so the per-extension file reads like a
	// regular log tail; timestamps are already carried as a field on
	// the parsed JSON record, so we disable the formatter's own
	// timestamp to avoid printing it twice.
	ret.fileFormatter = &log.TextFormatter{
		DisableColors:    true,
		DisableTimestamp: true,
	}
	ret.fileEntry = log.NewEntry(ret.logger)
	ret.fields = make(log.Fields)
	return ret
}

func (c *logCollector) Write(data []byte) (int, error) {
	n, _ := c.buffer.Write(data)
	for {
		line, err := c.scanner.ReadBytes('\n')
		if err == io.EOF {
			_, _ = c.buffer.Write(line)
			return n, nil
		}
		if err != nil {
			return n, err
		}
		clear(c.fields)
		if err := json.Unmarshal(line, &c.fields); err != nil {
			// Not JSON: write verbatim to the per-extension log file
			// so the homogenized debug log stays clean.
			c.writeToFile(line)
			continue
		}
		c.fields[logging.KeyThread] = c.extensionID
		levelIfc, okFound := c.fields[logging.KeyLevel]
		levelStr, okStr := levelIfc.(string)
		level, err := log.ParseLevel(levelStr)
		if !okFound || !okStr || err != nil {
			level = log.WarnLevel
		}
		c.log(level, c.fields, "")
		c.writeFormattedToFile(level, c.fields)
	}
}

func (c *logCollector) log(level log.Level, fields log.Fields, msg string, args ...any) {
	if !c.logger.IsLevelEnabled(level) {
		return
	}
	fields["workspace"] = c.workspace.String()
	c.logger.WithFields(fields).Logf(level, msg, args...)
}

// writeFormattedToFile renders the parsed structured fields as a
// human-readable text line and writes it to the per-extension log
// file. It reuses a single Entry and bytes.Buffer across calls so
// the steady-state per-line cost stays a single Format pass with no
// extra allocations.
func (c *logCollector) writeFormattedToFile(level log.Level, fields log.Fields) {
	c.fileBuf.Reset()
	c.fileEntry.Buffer = &c.fileBuf
	c.fileEntry.Level = level
	c.fileEntry.Time = time.Now()
	c.fileEntry.Data = fields
	b, err := c.fileFormatter.Format(c.fileEntry)
	// Drop references so we do not pin the caller-owned fields map
	// or the buffer between Write calls; both are reattached on the
	// next invocation.
	c.fileEntry.Buffer = nil
	c.fileEntry.Data = nil
	if err != nil {
		return
	}
	c.writeToFile(b)
}

// writeToFile writes raw bytes to the per-extension log file owned
// by this collector. Write errors are logged once and then ignored
// so a broken disk does not stop the collector.
func (c *logCollector) writeToFile(b []byte) {
	if _, err := c.logFile.Write(b); err != nil {
		if !c.writeErrLog {
			c.writeErrLog = true
			log.WithFields(log.Fields{
				logging.KeyThread: c.extensionID,
				"workspace":       c.workspace.String(),
			}).Warnf("extension log file write failed: %v", err)
		}
	}
}

// Close releases the per-extension log file. It must only be called
// after the writer goroutine (os/exec's stderr copier) has exited,
// which the workspaceRunner ensures by invoking Close from
// setExtensionExit. Calling Close more than once is safe; subsequent
// calls are no-ops.
func (c *logCollector) Close() error {
	if c.logFile == nil {
		return nil
	}
	err := c.logFile.Close()
	c.logFile = nil
	return err
}
