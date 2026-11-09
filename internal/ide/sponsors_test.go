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

package ide

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const defaultSponsorsURL = "https://github.com/sponsors/unstablebuild"

func sponsorsHeader(url string) string {
	return "# Sponsors\n\n" +
		"Writing code is being automated. Long-term vision and ownership of software systems " +
		"are not. When the stakes are high, you want an experienced engineer who can keep the " +
		"system coherent and take responsibility for it. That's who we build Rune for.\n\n" +
		"Rune is free and open source, built entirely through open-source contributions and " +
		"stewarded by Unstable Build, a small, self-funded company. Independent and " +
		"user-supported: your sponsorship keeps it that way. See the tiers section for more " +
		"details.\n\n" +
		"[Become a Sponsor](<" + url + ">)\n\n" +
		"---\n"
}

func tierURL(url, id string) string {
	return url + "/sponsorships?tier_id=" + id
}

func sponsorsTiers(url string) string {
	tier := func(name, id string) string {
		return "### [" + name + "](<" + tierURL(url, id) + ">)\n\n"
	}
	return "\n---\n\n## Tiers\n\n" +
		tier("$5 a month", "671932") +
		"- Your GitHub username is added to Rune's sponsors page\n" +
		"- 2 x small Rune sticker in the mail (50mm x 50mm or 2x2\").\n\n" +
		tier("$10 a month", "671952") +
		"- All benefits from previous tiers.\n" +
		"- 2 x medium Rune sticker in the mail (100mm x 100mm or 4x4\").\n\n" +
		tier("$50 a month", "671957") +
		"- All benefits from previous tiers.\n" +
		"- A Rune T-shirt, mailed to you.\n" +
		"- Your name or a small logo in Rune’s README.\n\n" +
		tier("$100 a month", "671963") +
		"- All benefits from previous tiers.\n" +
		"- Priority for the issues you report.\n" +
		"- Your extension or plugin gets distributed with Rune's package manager: " +
		"`pkg install <name>`.\n\n" +
		tier("$250 a month", "671981") +
		"- All benefits from previous tiers.\n" +
		"- A built-in Rune theme named after you.\n\n" +
		tier("$500 a month", "671967") +
		"- All benefits from previous tiers.\n" +
		"- A Medium size logo on Rune's sponsors page and in the README.\n" +
		"- A command named after you in Rune’s command prompt that displays markdown " +
		"content of your choice.\n\n" +
		tier("$1,000 a month", "671970") +
		"- All benefits from previous tiers.\n" +
		"- Our largest logo placement on Rune’s sponsors page and in the README\n" +
		"- A command named after you in Rune’s console, with custom behavior " +
		"(subject to approval).\n\n" +
		"[Become a Sponsor](<" + url + ">)\n\n" +
		"GitHub Sponsors handles billing, and you can change or cancel your sponsorship " +
		"at any time. Private sponsorships are never listed on this page.\n"
}

const slotLogo = "https://assets.rune.build/images/rune-logo-transparent-1024x1024.png"

// The attribute blocks sizing the pictures of each row.
const (
	diamondSize  = "{cols=36 rows=9}"
	platinumSize = "{cols=24 rows=6}"
	otherSize    = "{cols=18 rows=5}"
	backerSize   = "{rows=2}"
)

// picture is the image of src sized by size, captioned with name.
func picture(name, src, size string) string {
	return "![" + name + "](<" + src + "> \"" + name + "\")" + size
}

func linked(img, url string) string {
	return "[" + img + "](<" + url + ">)"
}

func openSlot(url, size string) string {
	return linked(picture("Your logo here", slotLogo, size), url) + "\n"
}

func diamondSlot(url string) string  { return openSlot(tierURL(url, "671970"), diamondSize) }
func platinumSlot(url string) string { return openSlot(tierURL(url, "671967"), platinumSize) }
func otherSlot(url string) string    { return openSlot(url, otherSize) }

// emptySponsorRows are the logo rows with only their open slot.
func emptySponsorRows(url string) string {
	return "\n### Diamond Sponsors\n\n" + diamondSlot(url) +
		"\n### Platinum Sponsors\n\n" + platinumSlot(url) +
		"\n### Other Sponsors\n\n" + otherSlot(url)
}

func TestRenderSponsors(t *testing.T) {
	d := defaultSponsorsURL
	for _, tc := range []struct {
		name string
		resp sponsorsResponse
		want string
	}{
		{
			name: "no sponsors",
			resp: sponsorsResponse{SponsorURL: d},
			want: sponsorsHeader(d) + emptySponsorRows(d) + sponsorsTiers(d),
		},
		{
			name: "missing sponsor url falls back",
			resp: sponsorsResponse{},
			want: sponsorsHeader(d) + emptySponsorRows(d) + sponsorsTiers(d),
		},
		{
			name: "sponsor url is used for every link",
			resp: sponsorsResponse{SponsorURL: "https://example.com/sponsor"},
			want: sponsorsHeader("https://example.com/sponsor") +
				emptySponsorRows("https://example.com/sponsor") +
				sponsorsTiers("https://example.com/sponsor"),
		},
		{
			name: "private sponsors after the rows",
			resp: sponsorsResponse{
				SponsorURL:   d,
				Tiers:        []sponsorTier{{ID: "backer", Sponsors: []sponsor{{Login: "alice"}}}},
				PrivateCount: 3,
			},
			want: sponsorsHeader(d) + emptySponsorRows(d) +
				"\n### Backers\n\n" +
				"alice\n" +
				"\n3 sponsors chose to keep their sponsorship private.\n" +
				sponsorsTiers(d),
		},
		{
			name: "one private sponsor",
			resp: sponsorsResponse{SponsorURL: d, PrivateCount: 1},
			want: sponsorsHeader(d) + emptySponsorRows(d) +
				"\n1 sponsor chose to keep their sponsorship private.\n" +
				sponsorsTiers(d),
		},
		{
			name: "all tiers",
			resp: sponsorsResponse{
				SponsorURL: d,
				Tiers: []sponsorTier{
					{ID: "diamond", Sponsors: []sponsor{
						{
							Login: "acme", Name: "Acme Corp", URL: "https://acme.dev",
							AvatarURL: "https://avatars.githubusercontent.com/u/1?v=4",
							LogoURL:   "https://acme.dev/logo.png",
						},
						{Login: "plain", LogoURL: "https://avatars.githubusercontent.com/u/2?v=4"},
					}},
					{ID: "platinum", Sponsors: []sponsor{{
						Login: "beta", Name: "Beta", URL: "https://beta.dev",
						LogoURL: "https://avatars.githubusercontent.com/u/3?v=4",
					}}},
					{ID: "gold", Sponsors: []sponsor{{
						Login: "g", Name: "Gold Co", URL: "https://g.dev",
						LogoURL: "https://avatars.githubusercontent.com/u/4?v=4",
					}}},
					{ID: "silver", Sponsors: []sponsor{{
						Login: "s", Name: "Silver Co", LogoURL: "https://s.dev/s.webp",
					}}},
					{ID: "bronze", Sponsors: []sponsor{{
						Login: "b", Name: "B", URL: "https://b.dev",
						LogoURL: "https://avatars.githubusercontent.com/u/5?s=460&v=4",
					}}},
					{ID: "backer", Sponsors: []sponsor{
						{
							Login: "alice", URL: "https://github.com/alice",
							AvatarURL: "https://avatars.githubusercontent.com/u/6?u=abc&v=4",
							LogoURL:   "https://alice.dev/logo.png",
						},
						{
							Login: "bob", Name: "Bob",
							AvatarURL: "https://avatars.githubusercontent.com/u/7?v=4",
						},
					}},
				},
			},
			want: sponsorsHeader(d) +
				"\n### Diamond Sponsors\n\n" +
				linked(picture("Acme Corp", "https://acme.dev/logo.png", diamondSize),
					"https://acme.dev") + " " +
				picture("plain", "https://avatars.githubusercontent.com/u/2?s=160&v=4",
					diamondSize) + " " +
				diamondSlot(d) +
				"\n### Platinum Sponsors\n\n" +
				linked(picture("Beta", "https://avatars.githubusercontent.com/u/3?s=112&v=4",
					platinumSize), "https://beta.dev") + " " +
				platinumSlot(d) +
				"\n### Other Sponsors\n\n" +
				linked(picture("Gold Co", "https://avatars.githubusercontent.com/u/4?s=64&v=4",
					otherSize), "https://g.dev") + " " +
				picture("Silver Co", "https://s.dev/s.webp", otherSize) + " " +
				linked(picture("B", "https://avatars.githubusercontent.com/u/5?s=64&v=4",
					otherSize), "https://b.dev") + " " +
				otherSlot(d) +
				"\n### Backers\n\n" +
				linked(picture("alice", "https://avatars.githubusercontent.com/u/6?s=48&u=abc&v=4",
					backerSize), "https://github.com/alice") + " " +
				picture("Bob", "https://avatars.githubusercontent.com/u/7?s=48&v=4", backerSize) +
				"\n" +
				sponsorsTiers(d),
		},
		{
			name: "other sponsors follow tier order, not response order",
			resp: sponsorsResponse{
				SponsorURL: d,
				Tiers: []sponsorTier{
					{ID: "bronze", Sponsors: []sponsor{{Login: "b", Name: "B", LogoURL: "https://b.dev/b.png"}}},
					{ID: "gold", Sponsors: []sponsor{{Login: "g", Name: "G", LogoURL: "https://g.dev/g.png"}}},
				},
			},
			want: sponsorsHeader(d) +
				"\n### Diamond Sponsors\n\n" + diamondSlot(d) +
				"\n### Platinum Sponsors\n\n" + platinumSlot(d) +
				"\n### Other Sponsors\n\n" +
				picture("G", "https://g.dev/g.png", otherSize) + " " +
				picture("B", "https://b.dev/b.png", otherSize) + " " +
				otherSlot(d) +
				sponsorsTiers(d),
		},
		{
			name: "unknown tiers are not shown",
			resp: sponsorsResponse{
				SponsorURL: d,
				Tiers:      []sponsorTier{{ID: "titanium", Sponsors: []sponsor{{Login: "t", Name: "T"}}}},
			},
			want: sponsorsHeader(d) + emptySponsorRows(d) + sponsorsTiers(d),
		},
		{
			name: "names from github are escaped",
			resp: sponsorsResponse{
				SponsorURL: d,
				Tiers: []sponsorTier{
					{ID: "bronze", Sponsors: []sponsor{
						{Login: "x", Name: "*[evil](x)*", LogoURL: "https://x.dev/x.png"},
						{Login: "q", Name: `A "B" &amp; C\`, LogoURL: "https://q.dev/q.png"},
					}},
					{ID: "backer", Sponsors: []sponsor{{Login: "under_score"}}},
				},
			},
			want: sponsorsHeader(d) +
				"\n### Diamond Sponsors\n\n" + diamondSlot(d) +
				"\n### Platinum Sponsors\n\n" + platinumSlot(d) +
				"\n### Other Sponsors\n\n" +
				`![\*\[evil\](x)\*](<https://x.dev/x.png> "*[evil](x)*")` + otherSize + " " +
				`![A "B" &amp; C\\](<https://q.dev/q.png> "A \"B\" \&amp; C\\")` + otherSize + " " +
				otherSlot(d) +
				"\n### Backers\n\n" +
				`under\_score` + "\n" +
				sponsorsTiers(d),
		},
		{
			name: "only http urls are linked",
			resp: sponsorsResponse{
				SponsorURL: d,
				Tiers: []sponsorTier{
					{ID: "backer", Sponsors: []sponsor{
						{Login: "a", Name: "A", URL: "file:///etc/passwd", AvatarURL: "https://a.dev/a.png"},
						{Login: "b", Name: "B", URL: "https://b.dev/>[x](y)", AvatarURL: "https://b.dev/b.png"},
						{Login: "c", Name: "C", URL: "relative/path", AvatarURL: "https://c.dev/c.png"},
						{Login: "d", Name: "D", URL: "http://d.dev/ok", AvatarURL: "https://d.dev/d.png"},
					}},
				},
			},
			want: sponsorsHeader(d) + emptySponsorRows(d) +
				"\n### Backers\n\n" +
				picture("A", "https://a.dev/a.png", backerSize) + " " +
				picture("B", "https://b.dev/b.png", backerSize) + " " +
				picture("C", "https://c.dev/c.png", backerSize) + " " +
				linked(picture("D", "https://d.dev/d.png", backerSize), "http://d.dev/ok") + "\n" +
				sponsorsTiers(d),
		},
		{
			name: "sponsors whose pictures cannot be shown are named",
			resp: sponsorsResponse{
				SponsorURL: d,
				Tiers: []sponsorTier{
					{ID: "gold", Sponsors: []sponsor{
						{Login: "a", Name: "A", URL: "https://a.dev", LogoURL: "http://a.dev/a.png"},
						{Login: "b", Name: "B", LogoURL: "file:///b.png"},
						{Login: "c", Name: "C", LogoURL: "https://c.dev/>[x](y)"},
						{Login: "d", Name: "D", LogoURL: "logo.png"},
						{Login: "e", Name: "E"},
					}},
					{ID: "backer", Sponsors: []sponsor{
						// Backers show profile photos, never logos.
						{Login: "f", Name: "F", LogoURL: "https://f.dev/f.png"},
					}},
				},
			},
			want: sponsorsHeader(d) +
				"\n### Diamond Sponsors\n\n" + diamondSlot(d) +
				"\n### Platinum Sponsors\n\n" + platinumSlot(d) +
				"\n### Other Sponsors\n\n" +
				otherSlot(d) +
				"\n[A](<https://a.dev>), B, C, D, E\n" +
				"\n### Backers\n\n" +
				"F\n" +
				sponsorsTiers(d),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderSponsors(tc.resp)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFetchSponsors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration
		want    sponsorsResponse
		wantErr string
	}{
		{
			name: "success",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"sponsor_url":"https://s","updated_at":"2026-10-22T00:00:00Z",
					"tiers":[{"id":"gold","name":"Gold","monthly_usd":250,
					"sponsors":[{"login":"g","name":"G","url":"https://g","theme":"aurum",
					"avatar_url":"https://a/g","logo_url":"https://g/logo.png"}]}],
					"private_count":4}`))
			},
			want: sponsorsResponse{SponsorURL: "https://s", Tiers: []sponsorTier{
				{ID: "gold", Sponsors: []sponsor{{
					Login: "g", Name: "G", URL: "https://g",
					AvatarURL: "https://a/g", LogoURL: "https://g/logo.png",
				}}},
			}, PrivateCount: 4},
		},
		{
			name: "unavailable",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "sponsors unavailable", http.StatusServiceUnavailable)
			},
			wantErr: "503",
		},
		{
			name: "malformed body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{`))
			},
			wantErr: "decode",
		},
		{
			name: "timeout",
			handler: func(_ http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			},
			timeout: 50 * time.Millisecond,
			wantErr: "deadline exceeded",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			got, err := fetchSponsors(ctx, srv.Client(), srv.URL)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
