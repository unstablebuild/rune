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
	"bytes"
	"math/rand"
	"os"
	"testing"

	"github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

const data = `{"address":"0x1400037cfc0","class":"firstmover.pubsub","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/ai/.dblock","msg":"client is waiting to receive a message for topic \"__pubsubinternal\"","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400189c258) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/ai974602340): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"class":"extutil.cmdSplitHandler","fields.level":"debug","level":"debug","msg":"extension being shutdown: extension manager is closing","thread":"fuzzy_line","time":"2025-08-06T18:43:43+02:00"}
{"fields.level":"trace","level":"trace","msg":"GranteeServer.Shutdown(reason:\"extension manager is closing\"): (, \u003cnil\u003e)","thread":"fuzzy_line","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"warning","msg":"stopping extension 'ai' due to extension manager is closing. Reload workspace to restart it.","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"warning","msg":"stopping extension 'rtc' due to extension manager is closing. Reload workspace to restart it.","time":"2025-08-06T18:43:43+02:00"}
{"id":"31546","level":"info","msg":"plugin process exited","plugin":"/Users/ernestrc/src/go-tui/bin/extension_fuzzy_line","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14001000450) Addr(): /Users/ernestrc/.sixdev/.extension/color_palette2972639848 ","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"debug","msg":"serve context is done: stopping mux server /Users/ernestrc/.sixdev/.extension/color_palette2972639848","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x14000b7ab40","class":"firstmover.Service","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_syntax/.dblock","msg":"Close called on peer...","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140006baf48) Addr(): /Users/ernestrc/.sixdev/.extension/lexer3343024571 ","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"debug","msg":"serve context is done: stopping mux server /Users/ernestrc/.sixdev/.extension/lexer3343024571","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x14000b7ab40","class":"firstmover.Service","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_syntax/.dblock","msg":"Monitoring for state changes. Current: SHUTDOWN","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140001b0000) Stop() ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140001b0000) Addr(): /Users/ernestrc/.sixdev/.extension/open_file_cursor1887316026 ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140001b0000) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/open_file_cursor1887316026): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"warning","msg":"stopping extension 'lexer' due to extension manager is closing. Reload workspace to restart it.","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x14000b7ab40","class":"firstmover.Service","level":"debug","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_syntax/.dblock","msg":"connection state is Shutdown","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x14000b7ab40","class":"firstmover.Service","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_syntax/.dblock","msg":"Service is closing or expected follow error (left 10 retries): err=grpc connection state = shutdown","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"warning","msg":"stopping extension 'fuzzy_syntax' due to extension manager is closing. Reload workspace to restart it.","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x140011d0500","class":"firstmover.Service","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_file/.dblock","msg":"Close called on peer...","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14001000468) Addr(): /Users/ernestrc/.sixdev/.extension/fuzzy_syntax1589413065 ","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"debug","msg":"serve context is done: stopping mux server /Users/ernestrc/.sixdev/.extension/fuzzy_syntax1589413065","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14001000450) Stop() ","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x140011d0500","class":"firstmover.Service","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_file/.dblock","msg":"Monitoring for state changes. Current: SHUTDOWN","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x140011d0500","class":"firstmover.Service","level":"debug","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_file/.dblock","msg":"connection state is Shutdown","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x140011d0500","class":"firstmover.Service","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_file/.dblock","msg":"Service is closing or expected follow error (left 10 retries): err=grpc connection state = shutdown","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x14000ee2000","class":"firstmover.pubsub","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_file/.dblock","msg":"client stream Recv returned: \"\u003cnil\u003e\", rpc error: code = Canceled desc = context canceled","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x14000ee2000","class":"firstmover.pubsub","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_file/.dblock","msg":"ignoring message \"\u003cnil\u003e\", err=rpc error: code = Canceled desc = context canceled: quit context is done","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"warning","msg":"stopping extension 'lsp' due to extension manager is closing. Reload workspace to restart it.","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"warning","msg":"stopping extension 'git' due to extension manager is closing. Reload workspace to restart it.","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14001000468) Stop() ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400120c660) Addr(): /Users/ernestrc/.sixdev/.extension/rtc3632046017 ","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"debug","msg":"serve context is done: stopping mux server /Users/ernestrc/.sixdev/.extension/rtc3632046017","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x1400037c3c0","class":"firstmover.pubsub","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_syntax/.dblock","msg":"client stream Recv returned: \"\u003cnil\u003e\", rpc error: code = Canceled desc = context canceled","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140006ba9c0) Addr(): /Users/ernestrc/.sixdev/.extension/fuzzy_file1730739684 ","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"debug","msg":"serve context is done: stopping mux server /Users/ernestrc/.sixdev/.extension/fuzzy_file1730739684","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14001000450) Addr(): /Users/ernestrc/.sixdev/.extension/color_palette2972639848 ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14001000450) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/color_palette2972639848): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140001b08a0) Addr(): /Users/ernestrc/.sixdev/.extension/file_bar753150270 ","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"debug","msg":"serve context is done: stopping mux server /Users/ernestrc/.sixdev/.extension/file_bar753150270","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400120c660) Stop() ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400120c660) Addr(): /Users/ernestrc/.sixdev/.extension/rtc3632046017 ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400120c660) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/rtc3632046017): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14001000348) Stop() ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140006baf48) Stop() ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140006ba9c0) Stop() ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400133e288) Addr(): /Users/ernestrc/.sixdev/.extension/git19169610 ","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"debug","msg":"serve context is done: stopping mux server /Users/ernestrc/.sixdev/.extension/git19169610","time":"2025-08-06T18:43:43+02:00"}
{"class":"textrpc.eventStreamClient","level":"trace","msg":"stop sending messages: context canceled","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"warning","msg":"stopping extension 'fuzzy_file' due to extension manager is closing. Reload workspace to restart it.","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"warning","msg":"stopping extension 'color_palette' due to extension manager is closing. Reload workspace to restart it.","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14000dc0168) Addr(): /Users/ernestrc/.sixdev/.extension/lsp242835981 ","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"debug","msg":"serve context is done: stopping mux server /Users/ernestrc/.sixdev/.extension/lsp242835981","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400133e288) Stop() ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400133e288) Addr(): /Users/ernestrc/.sixdev/.extension/git19169610 ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400133e288) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/git19169610): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"class":"textrpc.eventStreamClient","level":"trace","msg":"stop sending messages: context canceled","time":"2025-08-06T18:43:43+02:00"}
{"class":"textrpc.eventStreamClient","level":"trace","msg":"stop sending messages: context canceled","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"warning","msg":"stopping extension 'file_bar' due to extension manager is closing. Reload workspace to restart it.","time":"2025-08-06T18:43:43+02:00"}
{"class":"textrpc.eventStreamClient","level":"trace","msg":"stop sending messages: context canceled","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14000dc0168) Addr(): /Users/ernestrc/.sixdev/.extension/lsp242835981 ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14000dc0168) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/lsp242835981): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14000dc0168) Stop() ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140006baf48) Addr(): /Users/ernestrc/.sixdev/.extension/lexer3343024571 ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140006baf48) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/lexer3343024571): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140001b08a0) Stop() ","time":"2025-08-06T18:43:43+02:00"}
{"level":"debug","msg":"plugin exited","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14001000468) Addr(): /Users/ernestrc/.sixdev/.extension/fuzzy_syntax1589413065 ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14001000468) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/fuzzy_syntax1589413065): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x14001454280","class":"firstmover.Service","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_line/.dblock","msg":"Close called on peer...","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400000c600) Addr(): /Users/ernestrc/.sixdev/.extension/fuzzy_line3224280027 ","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"debug","msg":"serve context is done: stopping mux server /Users/ernestrc/.sixdev/.extension/fuzzy_line3224280027","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x1400144c0c0","class":"firstmover.pubsub","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_line/.dblock","msg":"client stream Recv returned: \"\u003cnil\u003e\", rpc error: code = Canceled desc = context canceled","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14001000348) Addr(): /Users/ernestrc/.sixdev/.extension/sed2345542988 ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x14001000348) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/sed2345542988): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140001b08a0) Addr(): /Users/ernestrc/.sixdev/.extension/file_bar753150270 ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140001b08a0) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/file_bar753150270): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140006ba9c0) Addr(): /Users/ernestrc/.sixdev/.extension/fuzzy_file1730739684 ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x140006ba9c0) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/fuzzy_file1730739684): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x14001454280","class":"firstmover.Service","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_line/.dblock","msg":"Monitoring for state changes. Current: SHUTDOWN","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x14001454280","class":"firstmover.Service","level":"debug","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_line/.dblock","msg":"connection state is Shutdown","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x14001454280","class":"firstmover.Service","level":"trace","lock":"/Users/ernestrc/.sixdev/.dbextension/fuzzy_line/.dblock","msg":"Service is closing or expected follow error (left 10 retries): err=grpc connection state = shutdown","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"class":"extension.Manager","level":"warning","msg":"stopping extension 'fuzzy_line' due to extension manager is closing. Reload workspace to restart it.","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400000c600) Stop() ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400000c600) Addr(): /Users/ernestrc/.sixdev/.extension/fuzzy_line3224280027 ","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"LoggingGRPCServer: (0x1400000c600) Serve(Result, addr=/Users/ernestrc/.sixdev/.extension/fuzzy_line3224280027): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"level":"trace","msg":"loggingBroker: Close(): \u003cnil\u003e","time":"2025-08-06T18:43:43+02:00"}
{"class":"browser.Component","level":"trace","msg":"new window: 1374392448064","time":"2025-08-06T18:43:43+02:00"}
{"class":"browser.Component","level":"trace","msg":"closing window: 1374392448064, cannot close last tiled window","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x1400040a000","class":"firstmover.Service","level":"trace","lock":"/Users/ernestrc/.sixdev/.dblock","msg":"Close called on peer...","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x140000e3980","class":"firstmover.pubsub","level":"trace","lock":"/Users/ernestrc/.sixdev/.dblock","msg":"server is broadcasting message \"BYE\" for topic \"__pubsubinternal\": subscribers: 0","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"address":"0x140000e3980","class":"firstmover.pubsub","level":"trace","lock":"/Users/ernestrc/.sixdev/.dblock","msg":"server is done broadcasting message \"BYE\" for topic \"__pubsubinternal\" to 0 subscribers","pid":"31527","time":"2025-08-06T18:43:43+02:00"}
{"URI":"file:///Users/ernestrc","class":"LoggingScheme","level":"trace","msg":"Close","scheme":"file","time":"2025-08-06T18:43:43+02:00"}
{"URI":"file:///Users/ernestrc","class":"LoggingScheme","level":"trace","msg":"Close: %!q(\u003cnil\u003e)","scheme":"file","time":"2025-08-06T18:43:43+02:00"}
{"URI":"file:///Users/ernestrc/src/go-tui","class":"LoggingScheme","level":"trace","msg":"Close","scheme":"file","time":"2025-08-06T18:43:43+02:00"}
{"URI":"file:///Users/ernestrc/src/go-tui","class":"LoggingScheme","level":"trace","msg":"Close: %!q(\u003cnil\u003e)","scheme":"file","time":"2025-08-06T18:43:43+02:00"}
{"class":"text.Component","level":"trace","msg":"unsubscribe sub=0x14001996090","time":"2025-08-06T18:43:43+02:00"}
{"class":"text.Publisher","level":"trace","msg":"unsubscribing subscriber sub=0x14001996090: unsubscribed called. subscribers=map[0:[0x14001a84900 0x14000a47800 0x14001483c80 0x14001a326f0 0x14001a32930 0x14001a32e40 0x14001d1ef60] 3:[0x14001a84900 0x14000a47800 0x14001483c80 0x14001a326f0 0x14001a32930 0x14001a32e40 0x14001d1ef60] 4:[0x14000a47800 0x14001483c80 0x14001d1ef60] 7:[0x14001a84900 0x14000a47800 0x14001483c80 0x14001a32e40] 8:[0x14001171830]]","time":"2025-08-06T18:43:43+02:00"}
{"class":"text.Server","level":"trace","msg":"unsubscribed subscriber: stream=0x14001c1b5e0","time":"2025-08-06T18:43:43+02:00"}
{"class":"text.Server","level":"trace","msg":"stream event completed: stream=0x14001c1b5e0","time":"2025-08-06T18:43:43+02:00"}
{"class":"text.Component","level":"trace","msg":"unsubscribe sub=0x14000a47800","time":"2025-08-06T18:43:43+02:00"}
{"class":"text.Publisher","level":"trace","msg":"unsubscribing subscriber sub=0x14000a47800: unsubscribed called. subscribers=map[0:[0x14001a84900 0x14001483c80 0x14001a326f0 0x14001a32930 0x14001a32e40 0x14001d1ef60] 3:[0x14001a84900 0x14001483c80 0x14001a326f0 0x14001a32930 0x14001a32e40 0x14001d1ef60] 4:[0x14001483c80 0x14001d1ef60] 7:[0x14001a84900 0x14001483c80 0x14001a32e40] 8:[0x14001171830]]","time":"2025-08-06T18:43:43+02:00"}
`

func TestLoggingCollector(t *testing.T) {
	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(log.TraceLevel)

	var buf bytes.Buffer
	left, _ := buf.Write([]byte(data))

	logFile, err := os.CreateTemp(t.TempDir(), "logging-test-*.log")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(logFile.Name()) })
	collector := newCollector("abc", workspaceapi.URI{}, logFile)
	collector.logger = logger
	for left > 0 {
		n := rand.Intn(left)
		if left < 50 {
			n = 50
		}
		slice := make([]byte, n)
		m, err := buf.Read(slice)
		slice = slice[:m]
		require.NoError(t, err)
		_, err = collector.Write([]byte(string(slice)))
		require.NoError(t, err)
		left -= n
	}

	assert.Equal(t, 103, len(hook.AllEntries()))
	entry := hook.LastEntry()
	assert.Equal(t, log.TraceLevel, entry.Level)
	assert.Equal(t, "text.Publisher", entry.Data["class"])
	assert.Equal(t, "unsubscribing subscriber sub=0x14000a47800: unsubscribed called. subscribers=map[0:[0x14001a84900 0x14001483c80 0x14001a326f0 0x14001a32930 0x14001a32e40 0x14001d1ef60] 3:[0x14001a84900 0x14001483c80 0x14001a326f0 0x14001a32930 0x14001a32e40 0x14001d1ef60] 4:[0x14001483c80 0x14001d1ef60] 7:[0x14001a84900 0x14001483c80 0x14001a32e40] 8:[0x14001171830]]",
		entry.Data["msg"])
	assert.Equal(t, "2025-08-06T18:43:43+02:00", entry.Data["time"])
}

func TestLoggingCollectorRoutesToFile(t *testing.T) {
	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(log.TraceLevel)

	logFile, err := os.CreateTemp(t.TempDir(), "logging-route-test-*.log")
	require.NoError(t, err)

	collector := newCollector("ext-id", workspaceapi.URI{}, logFile)
	collector.logger = logger

	_, err = collector.Write([]byte(`{"level":"info","msg":"hello"}` + "\n"))
	require.NoError(t, err)
	_, err = collector.Write([]byte("panic: not json\n"))
	require.NoError(t, err)

	// JSON record: logged structurally to logrus...
	require.Len(t, hook.AllEntries(), 1)
	assert.Equal(t, log.InfoLevel, hook.LastEntry().Level)
	assert.Equal(t, "hello", hook.LastEntry().Data["msg"])
	assert.Equal(t, "ext-id", hook.LastEntry().Data[logging.KeyThread])

	// ...and also rendered to the per-extension log file in text
	// form, alongside the raw non-JSON line written verbatim.
	require.NoError(t, logFile.Sync())
	contents, err := os.ReadFile(logFile.Name())
	require.NoError(t, err)
	got := string(contents)
	assert.Contains(t, got, "hello",
		"structured records should be teed to the log file in text form")
	assert.Contains(t, got, "level=info")
	assert.Contains(t, got, "panic: not json",
		"non-JSON lines should be written verbatim to the log file")
}

func TestLoggingCollectorLifecycle(t *testing.T) {
	logger, _ := logtest.NewNullLogger()
	logFile, err := os.CreateTemp(t.TempDir(), "logging-lifecycle-test-*.log")
	require.NoError(t, err)
	collector := newCollector("ext-id", workspaceapi.URI{}, logFile)
	collector.logger = logger

	stderr := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(stderr)
		_, _ = collector.Write([]byte("from the extension\n"))
	})
	collector.lifecycle(log.InfoLevel, "started with pid %d", 7)
	<-stderr
	require.NoError(t, collector.Close())

	contents, err := os.ReadFile(logFile.Name())
	require.NoError(t, err)
	got := string(contents)
	assert.Contains(t, got, "from the extension\n")
	assert.Regexp(t, `(?m)^time="[^"]+" level=info msg="started with pid 7" source=rune$`, got)
}

func BenchmarkLoggingCollectorWrite(b *testing.B) {
	logger, _ := logtest.NewNullLogger()
	logger.SetLevel(log.InfoLevel)

	logFile, err := os.CreateTemp(b.TempDir(), "logging-bench-*.log")
	require.NoError(b, err)
	c := newCollector("ext-id", workspaceapi.URI{}, logFile)
	c.logger = logger

	line := []byte(`{"level":"info","msg":"hello","class":"benchmark"}` + "\n")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Write(line); err != nil {
			b.Fatal(err)
		}
	}
}
