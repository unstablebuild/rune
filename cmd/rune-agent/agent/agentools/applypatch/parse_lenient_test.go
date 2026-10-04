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

package applypatch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLenient(t *testing.T) {
	join := strings.Join

	tests := []struct {
		name    string
		input   string
		want    Patch
		wantErr string
	}{
		{
			name: "missing begin marker tolerated",
			input: join([]string{
				"*** Add File: foo.txt",
				"+content",
				"*** End Patch",
			}, "\n"),
			want: Patch{Ops: []FileOp{
				{
					Type:  OpAdd,
					Path:  "foo.txt",
					Lines: []Line{{Kind: LineAdd, Content: "content"}},
				},
			}},
		},
		{
			name: "missing end marker tolerated",
			input: join([]string{
				"*** Begin Patch",
				"*** Add File: foo.txt",
				"+content",
			}, "\n"),
			want: Patch{Ops: []FileOp{
				{
					Type:  OpAdd,
					Path:  "foo.txt",
					Lines: []Line{{Kind: LineAdd, Content: "content"}},
				},
			}},
		},
		{
			name: "both delimiters missing tolerated",
			input: join([]string{
				"*** Add File: new.txt",
				"+hello world",
			}, "\n"),
			want: Patch{Ops: []FileOp{
				{
					Type:  OpAdd,
					Path:  "new.txt",
					Lines: []Line{{Kind: LineAdd, Content: "hello world"}},
				},
			}},
		},
		{
			name: "preamble text before begin marker skipped",
			input: join([]string{
				"Here is the patch:",
				"",
				"*** Begin Patch",
				"*** Delete File: old.txt",
				"*** End Patch",
			}, "\n"),
			want: Patch{Ops: []FileOp{
				{Type: OpDelete, Path: "old.txt"},
			}},
		},
		{
			name: "preamble text before file directive skipped",
			input: join([]string{
				"I'll update the file now:",
				"*** Update File: main.go",
				"@@",
				" package main",
				"-// old",
				"+// new",
				"*** End Patch",
			}, "\n"),
			want: Patch{Ops: []FileOp{
				{
					Type: OpUpdate,
					Path: "main.go",
					Hunks: []Hunk{
						{
							Lines: []Line{
								{Kind: LineContext, Content: "package main"},
								{Kind: LineRemove, Content: "// old"},
								{Kind: LineAdd, Content: "// new"},
							},
						},
					},
				},
			}},
		},
		{
			name: "trailing text after end patch ignored",
			input: join([]string{
				"*** Begin Patch",
				"*** Delete File: old.txt",
				"*** End Patch",
				"",
				"That should fix the issue.",
			}, "\n"),
			want: Patch{Ops: []FileOp{
				{Type: OpDelete, Path: "old.txt"},
			}},
		},
		{
			name:    "empty input errors",
			input:   "",
			wantErr: "empty patch",
		},
		{
			name:    "only preamble text errors",
			input:   "just some commentary\nnothing useful here",
			wantErr: "empty patch",
		},
		{
			// Reproduces a real failure from the "lucky-goshawk"
			// conversation: the model emitted the terminator as a
			// "+"-prefixed content line, which used to be written into
			// the file verbatim instead of ending the add block.
			name: "add block with plus-prefixed end marker errors",
			input: join([]string{
				"*** Begin Patch",
				"*** Add File: setup-gcs-audit.sh",
				"+#!/usr/bin/env bash",
				"+echo done",
				"+SQL",
				"+*** End Patch",
			}, "\n"),
			wantErr: prefixEnd,
		},
		{
			name: "update hunk with minus-prefixed end marker errors",
			input: join([]string{
				"*** Begin Patch",
				"*** Update File: setup-downloads-rollup.sh",
				"@@",
				"   ORDER BY total DESC",
				" SQL",
				"-*** End Patch",
				"*** End Patch",
			}, "\n"),
			wantErr: prefixEnd,
		},
		{
			name: "update hunk with plus-prefixed end marker errors",
			input: join([]string{
				"*** Begin Patch",
				"*** Update File: foo.txt",
				"@@",
				" keep",
				"+*** End Patch",
				"*** End Patch",
			}, "\n"),
			wantErr: prefixEnd,
		},
		{
			name: "add block with plus-prefixed begin marker errors",
			input: join([]string{
				"*** Begin Patch",
				"*** Add File: foo.txt",
				"+real content",
				"+*** Begin Patch",
			}, "\n"),
			wantErr: prefixBegin,
		},
		{
			name: "add block with plus-prefixed file directive errors",
			input: join([]string{
				"*** Begin Patch",
				"*** Add File: foo.txt",
				"+real content",
				"+*** Add File: bar.txt",
			}, "\n"),
			wantErr: "*** Add File:",
		},
		{
			name: "marker with surrounding whitespace as content errors",
			input: join([]string{
				"*** Begin Patch",
				"*** Add File: foo.txt",
				"+content",
				"+   *** End Patch   ",
			}, "\n"),
			wantErr: prefixEnd,
		},
		{
			name: "marker as substring is content",
			input: join([]string{
				"*** Begin Patch",
				"*** Add File: doc.md",
				"+The *** End Patch *** marker terminates a patch.",
				"*** End Patch",
			}, "\n"),
			want: Patch{Ops: []FileOp{
				{
					Type: OpAdd,
					Path: "doc.md",
					Lines: []Line{
						{Kind: LineAdd, Content: "The *** End Patch *** marker terminates a patch."},
					},
				},
			}},
		},
		{
			name: "blank lines inside add block preserved",
			input: join([]string{
				"*** Begin Patch",
				"*** Add File: spaced.txt",
				"+first",
				"",
				"+third",
				"*** End Patch",
			}, "\n"),
			want: Patch{Ops: []FileOp{
				{
					Type: OpAdd,
					Path: "spaced.txt",
					Lines: []Line{
						{Kind: LineAdd, Content: "first"},
						{Kind: LineAdd, Content: ""},
						{Kind: LineAdd, Content: "third"},
					},
				},
			}},
		},
		{
			name: "implicit end with single add line tolerated",
			input: join([]string{
				"*** Add File: note.txt",
				"+just one line",
			}, "\n"),
			want: Patch{Ops: []FileOp{
				{
					Type:  OpAdd,
					Path:  "note.txt",
					Lines: []Line{{Kind: LineAdd, Content: "just one line"}},
				},
			}},
		},
		{
			name: "update missing hunk header errors",
			input: join([]string{
				"*** Begin Patch",
				"*** Update File: main.go",
				" package main",
				"-old",
				"+new",
				"*** End Patch",
			}, "\n"),
			wantErr: "@@",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err, "input:\n%s", tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}
