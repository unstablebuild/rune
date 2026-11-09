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
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"
)

//go:embed sponsors.md.tmpl
var sponsorsTmplText string

var sponsorsTmpl = template.Must(template.New("sponsors").Funcs(template.FuncMap{
	"join": strings.Join,
}).Parse(sponsorsTmplText))

const (
	sponsorsWidth = 80
	// sponsorsFetchTimeout bounds how long :sponsors waits on the API
	// before showing the fallback, since nothing is on screen meanwhile.
	sponsorsFetchTimeout = 5 * time.Second
	// defaultSponsorURL is linked when the sponsor list, which carries
	// the authoritative link, is unavailable.
	defaultSponsorURL = "https://github.com/sponsors/unstablebuild"
	sponsorsMaxBytes  = 1 << 20
	// sponsorSlotLogo fills an open slot until a sponsor takes it.
	sponsorSlotLogo = "https://assets.rune.build/images/rune-logo-transparent-1024x1024.png"
	sponsorSlotText = "Your logo here"
	// githubAvatarHost serves GitHub profile photos at the size in pixels
	// their s parameter asks for, though not always.
	githubAvatarHost = "avatars.githubusercontent.com"
)

// sponsorsResponse mirrors the JSON served by the API's /api/sponsors.
type sponsorsResponse struct {
	SponsorURL   string        `json:"sponsor_url"`
	Tiers        []sponsorTier `json:"tiers"`
	PrivateCount int           `json:"private_count"`
}

type sponsorTier struct {
	ID       string    `json:"id"`
	Sponsors []sponsor `json:"sponsors"`
}

type sponsor struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	// AvatarURL is the sponsor's GitHub profile photo.
	AvatarURL string `json:"avatar_url"`
	// LogoURL is the sponsor's logo, or its profile photo when it has none.
	LogoURL string `json:"logo_url"`
}

// sponsorRows are the rows of the website's sponsors page, highest
// first, each listing its tiers' sponsors in the order the ids appear.
// Sponsors in any other tier are not shown.
var sponsorRows = []struct {
	label string
	tiers []string
	// openSlot rows are shown even when empty, ending in an invitation
	// to take the next spot.
	openSlot bool
	// githubTier is GitHub's numeric id for the one tier the row's open
	// slot leads to, matching the tier links on the page. Rows spanning
	// several tiers leave it 0 and link to the sponsor page instead.
	githubTier int
	// logoPx is the size in pixels the website shows the row's pictures
	// at, asked of GitHub's profile photos to keep downloads small.
	logoPx int
	// cols and rows bound the row's pictures in cells, whatever size
	// they are served at. A zero side follows the picture's aspect
	// ratio.
	cols, rows int
	// avatars rows show their sponsors' profile photos instead of their
	// logos.
	avatars bool
}{
	{
		label: "Diamond Sponsors", tiers: []string{"diamond"}, openSlot: true,
		githubTier: 671970, logoPx: 160, cols: 36, rows: 9,
	},
	{
		label: "Platinum Sponsors", tiers: []string{"platinum"}, openSlot: true,
		githubTier: 671967, logoPx: 112, cols: 24, rows: 6,
	},
	{
		label: "Other Sponsors", tiers: []string{"gold", "silver", "bronze"}, openSlot: true,
		logoPx: 64, cols: 18, rows: 5,
	},
	{label: "Backers", tiers: []string{"backer"}, logoPx: 48, rows: 2, avatars: true},
}

type sponsorsPage struct {
	SponsorURL   string
	Rows         []sponsorRow
	PrivateCount int
}

type sponsorRow struct {
	Label string
	// Paragraphs are the row's pictures, followed by its open slot if it
	// has one, and then the names of the sponsors whose picture cannot be
	// shown: among the pictures, a name would turn them all into text.
	Paragraphs []string
}

// renderSponsors lays out the sponsors page as the website does. The
// zero response renders every row with only its open slot, which is
// what is shown when the sponsor list cannot be fetched.
func renderSponsors(resp sponsorsResponse) (string, error) {
	byTier := make(map[string][]sponsor, len(resp.Tiers))
	for _, t := range resp.Tiers {
		byTier[t.ID] = append(byTier[t.ID], t.Sponsors...)
	}
	page := sponsorsPage{
		SponsorURL:   cmp.Or(resp.SponsorURL, defaultSponsorURL),
		PrivateCount: resp.PrivateCount,
	}
	for _, r := range sponsorRows {
		row := sponsorRow{Label: r.label}
		size := sponsorPictureSize(r.cols, r.rows)
		var pictures, names []string
		for _, id := range r.tiers {
			for _, s := range byTier[id] {
				pic := s.LogoURL
				if r.avatars {
					pic = s.AvatarURL
				}
				if img, ok := sponsorPicture(s, pic, r.logoPx, size); ok {
					pictures = append(pictures, img)
				} else {
					names = append(names, sponsorLink(s))
				}
			}
		}
		var slotURL string
		switch {
		case r.openSlot && r.githubTier != 0:
			slotURL = fmt.Sprintf("%s/sponsorships?tier_id=%d", page.SponsorURL, r.githubTier)
		case r.openSlot:
			slotURL = page.SponsorURL
		}
		if slotURL != "" {
			slot := "![" + sponsorSlotText + "](<" + sponsorSlotLogo + "> \"" +
				sponsorSlotText + "\")" + size
			pictures = append(pictures, "["+slot+"](<"+slotURL+">)")
		}
		if len(pictures) > 0 {
			row.Paragraphs = append(row.Paragraphs, strings.Join(pictures, " "))
		}
		if len(names) > 0 {
			row.Paragraphs = append(row.Paragraphs, strings.Join(names, ", "))
		}
		if len(row.Paragraphs) > 0 {
			page.Rows = append(page.Rows, row)
		}
	}
	var b strings.Builder
	if err := sponsorsTmpl.Execute(&b, page); err != nil {
		return "", fmt.Errorf("render sponsors: %w", err)
	}
	return b.String(), nil
}

// sponsorPictureSize is the attribute block that bounds a picture to cols
// by rows cells, leaving a zero side out.
func sponsorPictureSize(cols, rows int) string {
	var attrs []string
	if cols > 0 {
		attrs = append(attrs, "cols="+strconv.Itoa(cols))
	}
	if rows > 0 {
		attrs = append(attrs, "rows="+strconv.Itoa(rows))
	}
	return "{" + strings.Join(attrs, " ") + "}"
}

// sponsorPicture shows a sponsor as the picture at pic, sized by the
// attribute block size and captioned with its name, which is also the
// alt text shown until it loads. It is linked as sponsorLink links. A
// GitHub profile photo is requested at px pixels. It reports false for a
// picture that is not an https URL fitting in a <...> destination.
func sponsorPicture(s sponsor, pic string, px int, size string) (string, bool) {
	u, ok := markdownURL(pic, "https")
	if !ok {
		return "", false
	}
	if u.Host == githubAvatarHost {
		q := u.Query()
		q.Set("s", strconv.Itoa(px))
		u.RawQuery = q.Encode()
	}
	name := cmp.Or(s.Name, s.Login)
	img := "![" + escapeMarkdown(name) + "](<" + u.String() + "> \"" +
		titleEscaper.Replace(name) + "\")" + size
	if _, ok := markdownURL(s.URL, "http", "https"); !ok {
		return img, true
	}
	return "[" + img + "](<" + s.URL + ">)", true
}

// sponsorLink links a sponsor's name only to an http(s) URL that
// markdownURL accepts.
func sponsorLink(s sponsor) string {
	name := sponsorName(s)
	if _, ok := markdownURL(s.URL, "http", "https"); !ok {
		return name
	}
	return "[" + name + "](<" + s.URL + ">)"
}

func sponsorName(s sponsor) string {
	return escapeMarkdown(cmp.Or(s.Name, s.Login))
}

// markdownURL parses raw if it is an absolute URL with one of schemes
// that fits in a <...> destination: anything else would break out of the
// markup, or be opened as a workspace link when clicked.
func markdownURL(raw string, schemes ...string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || !slices.Contains(schemes, u.Scheme) || u.Host == "" ||
		strings.ContainsAny(raw, "<> \t\r\n") {
		return nil, false
	}
	return u, true
}

var markdownEscaper = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", `*`, `\*`, `_`, `\_`, `[`, `\[`, `]`, `\]`,
	`<`, `\<`, `>`, `\>`, `#`, `\#`, `|`, `\|`,
)

// escapeMarkdown keeps names and logins chosen on GitHub from being
// read as markup.
func escapeMarkdown(s string) string {
	return markdownEscaper.Replace(s)
}

// titleEscaper keeps a name in a "..." link title from ending it or
// being read as a character reference.
var titleEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `&`, `\&`)

func fetchSponsors(ctx context.Context, client *http.Client, endpoint string) (sponsorsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, sponsorsFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return sponsorsResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return sponsorsResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return sponsorsResponse{}, fmt.Errorf("GET %s: %s", endpoint, resp.Status)
	}
	var ret sponsorsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, sponsorsMaxBytes)).Decode(&ret); err != nil {
		return sponsorsResponse{}, fmt.Errorf("decode sponsors: %w", err)
	}
	return ret, nil
}
