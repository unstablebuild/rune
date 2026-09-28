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

package debug

import (
	"fmt"
	"os"

	log "github.com/sirupsen/logrus"
	sdkdebug "github.com/unstablebuild/rune-go-sdk/debug"
	"gopkg.in/yaml.v3"
)

// CapturePanicReportWith captures a panic in fn and writes to disk
// a yaml report. The boolean value returned indicates if fn run with no panics.
// If the value is false, the returned string indicates the fs location of the report.
// An error is returned if there was a panic but the report couldn't be stored.
func CapturePanicReportWith(dir, pkg, version string, run func()) (
	panicValue any, err error, ok bool,
) {
	defer func() {
		panicValue = recover()
		if panicValue == nil {
			return
		}
		report := sdkdebug.BuildCrashReport(pkg, version, panicValue)

		var data []byte
		data, err = yaml.Marshal(report)
		if err != nil {
			log.Errorf("yaml %v: %v", report, err)
			return
		}
		var f *os.File
		f, err = os.CreateTemp(dir, fmt.Sprintf("%s_crash_report_", pkg))
		if err != nil {
			log.Errorf("temp file: %v", err)
			return
		}
		defer f.Close()
		if _, err = f.Write(data); err != nil {
			log.Errorf("write to report %q: %v", f.Name(), err)
			return
		}
		log.Warnf("saved crash report file://%v", f.Name())
	}()

	run()
	ok = true
	return
}

// CapturePanicReport captures a panic with CapturePanicReportWith,
// and exits or simply returns if there was no panic in fn. It uses
// the compile-time variables Tag, Package and ReportsDir, so make
// sure they're injected at compile-time when using this helper.
func CapturePanicReport(fn func()) {
	defer func() {
		panicValue := recover()
		if panicValue == nil {
			return
		}
		defer panic(panicValue)
		report := sdkdebug.BuildCrashReport(Package, Tag, panicValue)

		var data []byte
		data, err := yaml.Marshal(report)
		if err != nil {
			log.Errorf("yaml %v: %v", report, err)
			return
		}
		var f *os.File
		f, err = os.CreateTemp(ReportsDir, fmt.Sprintf("%s_crash_report_", Package))
		if err != nil {
			log.Errorf("temp file: %v", err)
			return
		}
		defer f.Close()
		if _, err = f.Write(data); err != nil {
			log.Errorf("write to report %q: %v", f.Name(), err)
			return
		}
		log.Warnf("saved crash report file://%v", f.Name())
		fmt.Fprintf(os.Stderr, "saved crash report file://%v", f.Name())
	}()

	fn()
}
