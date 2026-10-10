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

package hostenv

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrependPATH(t *testing.T) {
	const (
		unixDir = "/rune/bin"
		winDir  = `C:\rune\bin`
	)
	for _, tc := range []struct {
		goos, name, base, want string
	}{
		{"linux", "empty base adds no empty entry", "", "/rune/bin"},
		{"linux", "single entry", "/usr/bin", "/rune/bin:/usr/bin"},
		{"linux", "several entries keep order", "/usr/local/bin:/usr/bin:/bin", "/rune/bin:/usr/local/bin:/usr/bin:/bin"},
		{"linux", "base is only dir", "/rune/bin", "/rune/bin"},
		{"linux", "dir already first", "/rune/bin:/usr/bin", "/rune/bin:/usr/bin"},
		{"linux", "dir in the middle moves to front", "/usr/bin:/rune/bin:/bin", "/rune/bin:/usr/bin:/bin"},
		{"linux", "dir last moves to front", "/usr/bin:/bin:/rune/bin", "/rune/bin:/usr/bin:/bin"},
		{"linux", "dir twice at front", "/rune/bin:/rune/bin:/usr/bin", "/rune/bin:/usr/bin"},
		{"linux", "dir three times scattered", "/rune/bin:/usr/bin:/rune/bin:/bin:/rune/bin", "/rune/bin:/usr/bin:/bin"},
		{"linux", "base is only copies of dir", "/rune/bin:/rune/bin:/rune/bin", "/rune/bin"},
		{"linux", "trailing slash is the same dir", "/usr/bin:/rune/bin/", "/rune/bin:/usr/bin"},
		{"linux", "repeated slashes are the same dir", "/usr/bin://rune///bin//", "/rune/bin:/usr/bin"},
		{"linux", "dot segment is the same dir", "/usr/bin:/rune/./bin", "/rune/bin:/usr/bin"},
		{"linux", "dot-dot segment is the same dir", "/usr/bin:/rune/x/../bin", "/rune/bin:/usr/bin"},
		{"linux", "sibling with dir as prefix is kept", "/rune/bin2:/rune/binx", "/rune/bin:/rune/bin2:/rune/binx"},
		{"linux", "subdir and parent are kept", "/rune/bin/sub:/rune", "/rune/bin:/rune/bin/sub:/rune"},
		{"linux", "case differs is another dir", "/Rune/Bin:/RUNE/BIN", "/rune/bin:/Rune/Bin:/RUNE/BIN"},
		{"linux", "relative entries are kept", ".:rune/bin:bin", "/rune/bin:.:rune/bin:bin"},
		{"linux", "surrounding whitespace is another dir", " /rune/bin:/rune/bin ", "/rune/bin: /rune/bin:/rune/bin "},
		{"linux", "quotes are literal", `"/rune/bin":/usr/bin`, `/rune/bin:"/rune/bin":/usr/bin`},
		{"linux", "semicolon is part of an entry", "/a;/rune/bin:/usr/bin", "/rune/bin:/a;/rune/bin:/usr/bin"},
		{"linux", "non-ASCII entries are kept", "/opt/ünïcödé/bin:/usr/bin", "/rune/bin:/opt/ünïcödé/bin:/usr/bin"},
		// An empty POSIX entry means the current directory: keep every one.
		{"linux", "empty entry in the middle", "/usr/bin::/bin", "/rune/bin:/usr/bin::/bin"},
		{"linux", "leading empty entry", ":/usr/bin", "/rune/bin::/usr/bin"},
		{"linux", "trailing empty entry", "/usr/bin:", "/rune/bin:/usr/bin:"},
		{"linux", "base is a lone separator", ":", "/rune/bin::"},
		{"linux", "base is only separators", ":::", "/rune/bin::::"},
		{"linux", "empty entries around copies of dir", "::/rune/bin::/rune/bin", "/rune/bin:::"},
		{"darwin", "darwin uses POSIX syntax", "/usr/bin:/rune/bin:/rune/bin/", "/rune/bin:/usr/bin"},

		{"windows", "empty base adds no empty entry", "", winDir},
		{"windows", "several entries keep order", `C:\Windows;C:\Tools`, `C:\rune\bin;C:\Windows;C:\Tools`},
		{"windows", "base is only dir", winDir, winDir},
		{"windows", "dir in the middle moves to front", `C:\Windows;C:\rune\bin;C:\Tools`, `C:\rune\bin;C:\Windows;C:\Tools`},
		{"windows", "dir three times scattered", `C:\rune\bin;C:\Windows;C:\rune\bin;C:\rune\bin`, `C:\rune\bin;C:\Windows`},
		{"windows", "case differs is the same dir", `C:\Windows;c:\RUNE\Bin`, `C:\rune\bin;C:\Windows`},
		{"windows", "forward slashes are the same dir", `C:\Windows;C:/rune/bin`, `C:\rune\bin;C:\Windows`},
		{"windows", "trailing separator is the same dir", `C:\Windows;C:\rune\bin\;C:\rune\bin/`, `C:\rune\bin;C:\Windows`},
		{"windows", "dot segments are the same dir", `C:\Windows;C:\rune\.\bin;C:\rune\x\..\bin`, `C:\rune\bin;C:\Windows`},
		{"windows", "quoted dir is the same dir", `"C:\rune\bin";C:\Windows`, `C:\rune\bin;C:\Windows`},
		{"windows", "partly quoted dir is the same dir", `C:\"rune"\bin;C:\Windows`, `C:\rune\bin;C:\Windows`},
		{"windows", "quoted entry with a separator is kept verbatim", `"C:\a;b";C:\Tools`, `C:\rune\bin;"C:\a;b";C:\Tools`},
		{"windows", "unbalanced quote runs to the end", `C:\Tools;"C:\a;C:\rune\bin`, `C:\rune\bin;C:\Tools;"C:\a;C:\rune\bin`},
		{"windows", "colon is not a separator", `C:\a:C:\rune\bin`, `C:\rune\bin;C:\a:C:\rune\bin`},
		{"windows", "sibling with dir as prefix is kept", `C:\rune\bin2;C:\rune\bin\sub`, `C:\rune\bin;C:\rune\bin2;C:\rune\bin\sub`},
		{"windows", "other drive is another dir", `D:\rune\bin`, `C:\rune\bin;D:\rune\bin`},
		{"windows", "UNC path is another dir", `\\server\rune\bin`, `C:\rune\bin;\\server\rune\bin`},
		{"windows", "empty entries are kept", `C:\Windows;;C:\Tools;`, `C:\rune\bin;C:\Windows;;C:\Tools;`},
		{"windows", "base is only separators", ";;", `C:\rune\bin;;;`},
	} {
		t.Run(tc.goos+"/"+tc.name, func(t *testing.T) {
			dir := unixDir
			if tc.goos == "windows" {
				dir = winDir
			}
			got := prependPATH(tc.goos, dir, tc.base)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, got, prependPATH(tc.goos, dir, got), "prepending again must be a no-op")
		})
	}
}

func FuzzPrependPATH(f *testing.F) {
	for _, seed := range []string{
		"", ":", ";", `"`, "/usr/bin:/rune/bin:/rune/bin/", `C:\Windows;"C:\a;b";c:\RUNE\BIN\`,
		`"C:\a;C:\rune\bin`, `C:\rune\bin";"C:\x`, "::/rune/bin::", ";;C:/rune/bin;;",
	} {
		f.Add(seed, false)
		f.Add(seed, true)
	}
	f.Fuzz(func(t *testing.T, base string, windows bool) {
		goos, dir := "linux", "/rune/bin"
		if windows {
			goos, dir = "windows", `C:\rune\bin`
		}
		got := prependPATH(goos, dir, base)
		require.Equal(t, got, prependPATH(goos, dir, got), "prepending again must be a no-op")

		entries := splitPATH(got, windows)
		require.Equal(t, dir, entries[0])
		kept := []string{}
		for _, e := range splitPATH(base, windows) {
			if !samePATHDir(e, dir, windows) {
				kept = append(kept, e)
			}
		}
		require.Equal(t, kept, entries[1:], "other entries must survive the join unchanged and in order")

		if windows == (runtime.GOOS == "windows") {
			want := filepath.SplitList(base)
			split := splitPATH(base, windows)
			for i := range split {
				if windows {
					split[i] = strings.ReplaceAll(split[i], `"`, "")
				}
			}
			if len(want) == 0 {
				want = nil
			}
			require.Equal(t, want, split, "splitPATH must agree with filepath.SplitList")
		}
	})
}
