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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"golang.org/x/oauth2"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idelsp/languages"
)

const telemetryPath = "/telemetry"

// flushTimeout bounds the final usage post performed on Close so that
// shutting down Rune while offline is not delayed by a hanging request.
const flushTimeout = 300 * time.Millisecond

// unknownLanguage buckets files whose language cannot be derived from the
// extension, or whose derived id is not a plausible language name, so that no
// path-derived data ends up in the payload.
const unknownLanguage = "other"

// maxLanguageLen and maxLanguages bound what a single session can report: long
// or numerous ids come from generated or scratch files rather than from
// languages a user needs support for. Opens past maxLanguages are dropped
// rather than folded into unknownLanguage, which would misreport a real
// language as one Rune could not identify.
const (
	maxLanguageLen = 24
	maxLanguages   = 64
)

// TelemetryEvents returns the events that telemetry's text.EventHandler needs
// to collects user statistics.
func TelemetryEvents() []textapi.EventType {
	return []textapi.EventType{
		textapi.EventTypeOpen,
		textapi.EventTypeClose,
		textapi.EventTypeFlush,
		textapi.EventTypeEdit,
	}
}

type telemetry struct {
	auth      oauth2.TokenSource
	url       string
	period    time.Duration
	quitCtx   context.Context
	cancelCtx func()

	installID  string
	tampered   bool
	installErr string
	sessionID  string
	sysinfo    sysinfo
	version    string
	editorMode string

	opened         atomic.Int32
	closed         atomic.Int32
	flushed        atomic.Int32
	edited         atomic.Int32
	watchedChanges atomic.Int32
	commands       atomic.Int32

	// languages maps a language id to its *atomic.Int32 open count, bounded
	// in cardinality by numLanguages.
	languages    sync.Map
	numLanguages atomic.Int32

	// usageMu serializes usage posts: usageLanguages is refilled in place by
	// every post, so a post must complete before the next one overwrites it.
	usageMu        sync.Mutex
	usageLanguages map[string]int
}

func newTelemetry(
	auth oauth2.TokenSource,
	url *url.URL,
	period time.Duration,
	version string,
	editorMode string,
	store storageapi.Service,
	installBackupDir string,
) *telemetry {
	if period <= 0 {
		panic(fmt.Sprintf("apiclient: TelemetryPeriod must be positive, got %v", period))
	}
	if installBackupDir == "" {
		panic("apiclient: InstallBackupDir is required")
	}
	ret := new(telemetry)
	ret.auth = auth
	ret.usageLanguages = make(map[string]int, maxLanguages)

	ret.url = url.JoinPath(telemetryPath).String()
	ret.sessionID = uuid.New().String()
	ret.sysinfo, _ = uname()
	ret.version = version
	ret.editorMode = editorMode
	ret.period = period

	ret.quitCtx, ret.cancelCtx = context.WithCancel(context.Background())

	ret.installID, ret.tampered, ret.installErr = getInstallID(ret.quitCtx, store, installBackupDir)

	return ret
}

func (t *telemetry) start() {
	go debug.CapturePanicReport(func() {
		t.periodicPostData(t.period)
	})
}

func (t *telemetry) periodicPostData(period time.Duration) {
	log.Tracef("Starting period posting of telemetry data every %v", period)

	timer := time.NewTimer(period)
	defer timer.Stop()

	buf := new(bytes.Buffer)

	// start with a declaration of the client's system event
	data := t.getSystemData()
	err := t.postData(buf, period, data)
	if err != nil {
		log.Tracef("Could not post telemetry system data: %v", err)
	}

	for {
		select {
		case <-timer.C:
			timer.Reset(period)
		case <-t.quitCtx.Done():
			return
		}

		if err := t.postUsage(buf, period); err != nil {
			log.Tracef("Could not post telemetry usage data: %v", err)
		}
	}
}

func (t *telemetry) postUsage(buf *bytes.Buffer, period time.Duration) error {
	t.usageMu.Lock()
	defer t.usageMu.Unlock()

	data := t.getUsage()
	if err := t.postData(buf, period, data); err != nil {
		return err
	}
	t.resetUsage(data)
	return nil
}

func (t *telemetry) getUsage() telemetryUsagePayload {
	var data telemetryUsagePayload
	data.Opened = int(t.opened.Load())
	data.Closed = int(t.closed.Load())
	data.Flushed = int(t.flushed.Load())
	data.Edited = int(t.edited.Load())
	data.WatchedChanges = int(t.watchedChanges.Load())
	data.Commands = int(t.commands.Load())
	data.Languages = t.snapshotLanguages()
	data.SID = t.sessionID
	data.Type = "ClientUsage"
	data.EditorMode = t.editorMode
	return data
}

func (t *telemetry) snapshotLanguages() map[string]int {
	clear(t.usageLanguages)
	t.languages.Range(func(lang, count any) bool {
		if n := int(count.(*atomic.Int32).Load()); n > 0 {
			t.usageLanguages[lang.(string)] = n
		}
		return true
	})
	if len(t.usageLanguages) == 0 {
		return nil
	}
	return t.usageLanguages
}

func (t *telemetry) getSystemData() telemetrySystemPayload {
	var data telemetrySystemPayload
	data.InstallID = t.installID
	data.Tampered = t.tampered
	data.InstallIDErr = t.installErr
	data.SID = t.sessionID
	data.Type = "ClientSystem"
	data.SystemArquitecture = t.sysinfo.Machine
	data.SystemOS = t.sysinfo.OS
	data.SystemName = t.sysinfo.Node
	data.SystemRelease = t.sysinfo.Release
	data.SystemVersion = t.sysinfo.Version
	data.Version = t.version
	data.EditorMode = t.editorMode
	return data
}

func (t *telemetry) resetUsage(data telemetryUsagePayload) {
	// reset exactly the counts that were posted
	t.opened.Add(-int32(data.Opened))
	t.closed.Add(-int32(data.Closed))
	t.flushed.Add(-int32(data.Flushed))
	t.edited.Add(-int32(data.Edited))
	t.watchedChanges.Add(-int32(data.WatchedChanges))
	t.commands.Add(-int32(data.Commands))

	for lang, n := range data.Languages {
		if count, ok := t.languages.Load(lang); ok {
			count.(*atomic.Int32).Add(-int32(n))
		}
	}
}

func (t *telemetry) postData(buf *bytes.Buffer, period time.Duration, data any) error {
	ctx, cancel := context.WithTimeout(t.quitCtx, period)
	defer cancel()
	return t.postDataCtx(ctx, buf, data)
}

func (t *telemetry) postDataCtx(ctx context.Context, buf *bytes.Buffer, data any) error {
	buf.Reset()
	err := json.NewEncoder(buf).Encode(data)
	if err != nil {
		return fmt.Errorf("json encode: %v", err)
	}

	r, err := http.NewRequestWithContext(ctx, "POST", t.url, buf)
	if err != nil {
		return fmt.Errorf("new request: %v", err)
	}

	token, err := t.auth.Token()
	if err == nil && token.Valid() {
		r.Header["Authorization"] = []string{fmt.Sprintf("%s %s", token.TokenType, token.AccessToken)}
	}

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return fmt.Errorf("post request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("response status code non-200")
	}
	return nil
}

func (t *telemetry) Handle(ctx context.Context, ev textapi.Event) bool {
	switch ev.Type {
	case textapi.EventTypeOpen:
		t.opened.Add(1)
		t.recordLanguage(ev.URI)
	case textapi.EventTypeClose:
		t.closed.Add(1)
	case textapi.EventTypeFlush:
		t.flushed.Add(1)
	case textapi.EventTypeEdit:
		t.edited.Add(1)
	}
	return false
}

func (t *telemetry) recordWatchedFilesChange(n int) {
	t.watchedChanges.Add(int32(n))
}

func (t *telemetry) recordCommand() {
	t.commands.Add(1)
}

// recordLanguage counts the language of an opened file so we can tell which
// languages users need support for. Only the language id is reported, never
// the file name or path.
func (t *telemetry) recordLanguage(uri workspaceapi.URI) {
	lang := languageID(filepath.Base(uri.Path()))
	count, seen := t.languages.Load(lang)
	if !seen {
		if t.numLanguages.Load() >= maxLanguages {
			return
		}
		var loaded bool
		count, loaded = t.languages.LoadOrStore(lang, new(atomic.Int32))
		if !loaded {
			t.numLanguages.Add(1)
		}
	}
	count.(*atomic.Int32).Add(1)
}

func languageID(filename string) string {
	lang, err := languages.LanguageForFile(filename)
	if err != nil {
		return unknownLanguage
	}
	lang = strings.ToLower(lang)
	if len(lang) > maxLanguageLen || strings.IndexFunc(lang, isNotLanguageRune) >= 0 {
		return unknownLanguage
	}
	return lang
}

func isNotLanguageRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return false
	case r == '_', r == '-', r == '+', r == '.':
		return false
	}
	return true
}

func (t *telemetry) Close() error {
	t.flushFinalUsage()
	t.cancelCtx()
	return nil
}

func (t *telemetry) flushFinalUsage() {
	// a periodic post already in flight carries the same counters, and waiting
	// for it would delay shutdown by as long as its request takes.
	if !t.usageMu.TryLock() {
		return
	}
	defer t.usageMu.Unlock()

	data := t.getUsage()
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	_ = t.postDataCtx(ctx, new(bytes.Buffer), data)
}

type telemetryUsagePayload struct {
	Type           string
	SID            string
	EditorMode     string
	Opened         int
	Closed         int
	Edited         int
	Flushed        int
	WatchedChanges int
	Commands       int
	Languages      map[string]int
}

type telemetrySystemPayload struct {
	Type               string
	InstallID          string
	Tampered           bool
	InstallIDErr       string
	SID                string
	Version            string
	EditorMode         string
	SystemArquitecture string
	SystemOS           string
	SystemName         string
	SystemRelease      string
	SystemVersion      string
}

// ExampleUsagePayloadJSON returns a formatted JSON representation of a
// representative usage telemetry payload for display in onboarding and documentation.
func ExampleUsagePayloadJSON() string {
	payload := telemetryUsagePayload{
		Type:           "ClientUsage",
		SID:            "s_abc123",
		EditorMode:     "modal",
		Opened:         12,
		Closed:         8,
		Edited:         45,
		Flushed:        10,
		WatchedChanges: 3,
		Commands:       14,
		Languages: map[string]int{
			"go":   8,
			"rust": 4,
		},
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(data)
}
