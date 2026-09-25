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

//revive:disable:exported
package vtescanner

import (
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
)

type loggingDriverer struct {
	driver Driver
}

func WithLoggingDriver(p Driver) Driver {
	return loggingDriverer{driver: p}
}

func (p loggingDriverer) Print(r rune) {
	p.log(log.TraceLevel, "print %c", r)
	p.driver.Print(r)
}
func (p loggingDriverer) Execute(ch byte) {
	p.log(log.TraceLevel, "execute %c", ch)
	p.driver.Execute(ch)
}
func (p loggingDriverer) Hook(
	params [][]uint16, intermediates []byte,
	ignore bool, action rune,
) {
	p.log(log.TraceLevel, "hook params=%+v, len(intermediates)=%d, "+
		"ignore=%t, action=%c",
		params, len(intermediates), ignore, action)
	p.driver.Hook(params, intermediates, ignore, action)
}

func (p loggingDriverer) Put(ch byte) {
	p.log(log.TraceLevel, "put %c", ch)
	p.driver.Put(ch)
}
func (p loggingDriverer) Unhook() {
	p.log(log.TraceLevel, "unhook")
	p.driver.Unhook()
}
func (p loggingDriverer) OSCDispatch(params [][]byte, bellTerminated bool) {
	p.log(log.TraceLevel, "OSCDispatch params=%v, bell=%t", params, bellTerminated)
	p.driver.OSCDispatch(params, bellTerminated)
}
func (p loggingDriverer) CSIDispatch(
	params [][]uint16, intermediates []byte,
	ignore bool, action rune,
) {
	p.log(log.TraceLevel, "CSIDispatch params=%+v, len(intermediates)=%d, "+
		"ignore=%t, action=%c",
		params, len(intermediates), ignore, action)
	p.driver.CSIDispatch(params, intermediates, ignore, action)
}
func (p loggingDriverer) ESCDispatch(
	intermediates []byte, ignore bool, ch byte,
) {
	p.log(log.TraceLevel, "ESCDispatch len(intermediates)=%d, "+
		"ignore=%t, ch=%c",
		len(intermediates), ignore, ch)
	p.driver.ESCDispatch(intermediates, ignore, ch)
}

func (p loggingDriverer) APCDispatch(data []byte) {
	p.log(log.TraceLevel, "APCDispatch len(data)=%d", len(data))
	p.driver.APCDispatch(data)
}

func (p loggingDriverer) log(level log.Level, msg string, args ...any) {
	log.WithField(logging.KeyClass, "scanner.Driver").
		Logf(level, msg, args...)
}
