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

package ociregistry_test

import (
	"strings"
	"testing"

	"unstable.build/rune/internal/llm/llamacpp/ociregistry"
)

func TestParseReference(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  ociregistry.Reference
	}{
		{
			name:  "hf with user-repo and tag",
			input: "hf.co/bartowski/Llama-3.2-1B-Instruct-GGUF:Q4_K_M",
			want: ociregistry.Reference{
				Host:       "hf.co",
				Repository: "bartowski/Llama-3.2-1B-Instruct-GGUF",
				Tag:        "Q4_K_M",
			},
		},
		{
			name:  "hf with huggingface.co",
			input: "huggingface.co/bartowski/repo",
			want: ociregistry.Reference{
				Host:       "huggingface.co",
				Repository: "bartowski/repo",
			},
		},
		{
			name:  "scheme stripped",
			input: "https://hf.co/bartowski/repo:q4",
			want: ociregistry.Reference{
				Host:       "hf.co",
				Repository: "bartowski/repo",
				Tag:        "q4",
			},
		},
		{
			name:  "digest-only reference",
			input: "registry.ollama.ai/library/llama3@sha256:" + strings.Repeat("a", 64),
			want: ociregistry.Reference{
				Host:       "registry.ollama.ai",
				Repository: "library/llama3",
				Digest:     "sha256:" + strings.Repeat("a", 64),
			},
		},
		{
			name:  "both tag and digest",
			input: "hf.co/u/r:v@sha256:" + strings.Repeat("b", 64),
			want: ociregistry.Reference{
				Host:       "hf.co",
				Repository: "u/r",
				Tag:        "v",
				Digest:     "sha256:" + strings.Repeat("b", 64),
			},
		},
		{
			name:  "localhost with port is a host",
			input: "localhost:5000/foo/bar:v1",
			want: ociregistry.Reference{
				Host:       "localhost:5000",
				Repository: "foo/bar",
				Tag:        "v1",
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := ociregistry.ParseReference(tc.input)
			if err != nil {
				t.Fatalf("ParseReference(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("ParseReference(%q):\n got  %+v\n want %+v", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseReference_DefaultHost(t *testing.T) {
	ref, err := ociregistry.ParseReferenceWithDefault("library/llama3:8b", "registry.ollama.ai")
	if err != nil {
		t.Fatalf("ParseReferenceWithDefault: %v", err)
	}
	if ref.Host != "registry.ollama.ai" || ref.Repository != "library/llama3" || ref.Tag != "8b" {
		t.Fatalf("unexpected: %+v", ref)
	}
}

func TestParseReference_HFDefault(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  ociregistry.Reference
	}{
		{
			name:  "bare owner/repo",
			input: "unsloth/gemma-3n-E2B-it-GGUF",
			want: ociregistry.Reference{
				Host:       "huggingface.co",
				Repository: "unsloth/gemma-3n-E2B-it-GGUF",
			},
		},
		{
			name:  "bare with tag",
			input: "unsloth/gemma-3n-E2B-it-GGUF:Q4_K_M",
			want: ociregistry.Reference{
				Host:       "huggingface.co",
				Repository: "unsloth/gemma-3n-E2B-it-GGUF",
				Tag:        "Q4_K_M",
			},
		},
		{
			name:  "explicit host wins over default",
			input: "docker.io/library/alpine:3",
			want: ociregistry.Reference{
				Host:       "docker.io",
				Repository: "library/alpine",
				Tag:        "3",
			},
		},
		{
			name:  "scheme + hostless (not a real form but should not crash)",
			input: "https://foo/bar",
			want: ociregistry.Reference{
				// `foo` is the first segment and looks like a host only
				// if it contains `.` or `:`; here it does not, so the
				// default host applies.
				Host:       "huggingface.co",
				Repository: "foo/bar",
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := ociregistry.ParseReferenceWithDefault(tc.input, "huggingface.co")
			if err != nil {
				t.Fatalf("ParseReferenceWithDefault(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("\n got  %+v\n want %+v", got, tc.want)
			}
		})
	}
}

func TestParseReference_Invalid(t *testing.T) {
	cases := []string{
		"",
		"bareword",
		"hf.co/:tag",
		"hf.co/repo:",
		"hf.co/repo@sha256:tooshort",
		"hf.co/repo@unknown:abc",
	}
	for _, c := range cases {
		c := c
		t.Run(c, func(t *testing.T) {
			if _, err := ociregistry.ParseReference(c); err == nil {
				t.Fatalf("expected error parsing %q", c)
			}
		})
	}
}

func TestReferenceString_RoundTrip(t *testing.T) {
	in := "hf.co/bartowski/Llama-3.2-1B-Instruct-GGUF:Q4_K_M"
	ref, err := ociregistry.ParseReference(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := ref.String(); got != in {
		t.Fatalf("round trip:\n got  %q\n want %q", got, in)
	}
}

func TestReferenceTarget(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	r, _ := ociregistry.ParseReference("hf.co/u/r@" + digest)
	if r.Target() != digest {
		t.Fatalf("digest target: got %q want %q", r.Target(), digest)
	}
	r, _ = ociregistry.ParseReference("hf.co/u/r:v")
	if r.Target() != "v" {
		t.Fatalf("tag target: got %q want %q", r.Target(), "v")
	}
	r, _ = ociregistry.ParseReference("hf.co/u/r")
	if r.Target() != ociregistry.DefaultTag {
		t.Fatalf("default target: got %q want %q", r.Target(), ociregistry.DefaultTag)
	}
}
