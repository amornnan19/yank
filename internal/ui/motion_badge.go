package ui

import (
	"net/url"
	"strings"
	"time"
)

// siteBadge names the site under the box once what is in it parses as a URL:
// a glyph and the host, fading in from faint. What does not parse turns the
// box's border yellow instead. An empty box is neither.
//
// The host is text the user typed or pasted. It is only ever drawn through
// fit, which sanitises and cuts it before it is styled.
type siteBadge struct {
	host    string
	invalid bool
	fade    cue
	last    time.Time
}

// badgeFade is how long a new badge stays faint.
const badgeFade = 150 * time.Millisecond

// siteGlyph is the glyph and ANSI colour for a host.
type siteGlyph struct {
	glyph  string
	colour string
}

// badgeSites are the hosts with a glyph of their own. Every other host gets
// badgeDefault.
var badgeSites = map[string]siteGlyph{
	"youtube.com":    {"▶", ansiRed},
	"youtu.be":       {"▶", ansiRed},
	"soundcloud.com": {"♪", ansiYellow},
}

var badgeDefault = siteGlyph{"↗", ansiBlue}

func newSiteBadge() effect { return siteBadge{} }

func (e siteBadge) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown, evEdit:
		host := badgeHost(ev.value)
		e.invalid = host == "" && strings.TrimSpace(ev.value) != ""
		if host != e.host {
			e.host = host
			e.fade.stop()
			if host != "" {
				e.fade.arm()
			}
		}
	case evTick:
		e.fade.settle(ev.now)
		e.last = ev.now
		if e.fade.running && e.fade.elapsed(ev.now) >= badgeFade {
			e.fade.stop()
		}
	}
	return e
}

func (e siteBadge) busy() bool { return e.fade.live() }

func (e siteBadge) wake() time.Time { return time.Time{} }

func (e siteBadge) paint(f *inputFrame) {
	if e.invalid {
		f.border = ansiYellow
	}
	if e.host == "" {
		return
	}
	site, ok := badgeSites[badgeSite(e.host)]
	if !ok {
		site = badgeDefault
	}
	f.badge = site.glyph + " " + e.host
	f.badgeColour = site.colour
	f.badgeFaint = e.fade.waiting || (e.fade.running && e.fade.elapsed(f.now) < badgeFade)
}

// badgeHost is the host to name for what is in the box, or "" when it does not
// parse as an http(s) URL with a host — by the same test enter applies. A
// leading "www." is dropped: it names nothing the user needs to read.
func badgeHost(value string) string {
	target, ok := normaliseURL(value)
	if !ok {
		return ""
	}
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	return strings.TrimPrefix(host, "www.")
}

// badgeSite is the registrable part of host that badgeSites is keyed on, so
// music.youtube.com and m.soundcloud.com get their site's glyph.
func badgeSite(host string) string {
	for site := range badgeSites {
		if host == site || strings.HasSuffix(host, "."+site) {
			return site
		}
	}
	return host
}
