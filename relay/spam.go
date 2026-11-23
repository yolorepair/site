package main

import (
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The spam checks. Nothing here calls anyone else's service: a hidden field,
// a look at the text, and two counters. intake's cap on pending requests is
// the backstop behind all of them.

// honeypotField is on the form but hidden from people. A browser sends it
// empty; a bot that fills in every field doesn't.
const honeypotField = "website"

func honeypotFilled(form url.Values) bool {
	for _, v := range form[honeypotField] {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

var linkPattern = regexp.MustCompile(`(?i)(https?://|www\.)\S+`)

const (
	maxLinks        = 2  // a product page and a video is a repair; five links is an ad
	minTextWithLink = 20 // characters of description besides the links
)

// looksLikeSpam is the cheap content check: a description that is mostly
// links, or a name with a link in it, is not a repair. One or two links next
// to a real description are fine.
func looksLikeSpam(req map[string]string) bool {
	if linkPattern.MatchString(req["customer_name"]) {
		return true
	}
	symptoms := req["symptoms"]
	links := linkPattern.FindAllStringIndex(symptoms, -1)
	if len(links) == 0 {
		return false
	}
	if len(links) > maxLinks {
		return true
	}
	rest := 0
	for _, r := range linkPattern.ReplaceAllString(symptoms, "") {
		if !unicode.IsSpace(r) {
			rest++
		}
	}
	return rest < minTextWithLink
}

// challenge is the seam for a third-party check. Today there is one, which
// passes everything. Turnstile goes here: it would read its token from the
// form and verify it with Cloudflare.
type challenge interface {
	passes(r *http.Request, form url.Values) bool
}

type noChallenge struct{}

func (noChallenge) passes(*http.Request, url.Values) bool { return true }

func newChallenge(name string) (challenge, error) {
	switch name {
	case "", "none":
		return noChallenge{}, nil
	default:
		return nil, fmt.Errorf("CHALLENGE %q isn't supported: the only value today is none", name)
	}
}

// rateLimiter counts forwarded requests over a sliding window: so many per
// client, so many overall. It lives in memory, so it resets on a restart and
// isn't shared between replicas. The relay runs as one.
//
// Only requests that passed every other check are counted, so the number of
// clients remembered at once is never more than the overall limit.
type rateLimiter struct {
	perClient int
	overall   int
	window    time.Duration
	now       func() time.Time

	mu      sync.Mutex
	clients map[string][]time.Time
	all     []time.Time
}

func newRateLimiter(perClient, overall int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		perClient: perClient,
		overall:   overall,
		window:    window,
		now:       time.Now,
		clients:   map[string][]time.Time{},
	}
}

// allow reports whether this client may send one more, and counts it if so.
func (l *rateLimiter) allow(client string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)
	l.all = since(l.all, cutoff)
	for key, times := range l.clients {
		if kept := since(times, cutoff); len(kept) == 0 {
			delete(l.clients, key)
		} else {
			l.clients[key] = kept
		}
	}

	if len(l.clients[client]) >= l.perClient || len(l.all) >= l.overall {
		return false
	}
	l.clients[client] = append(l.clients[client], now)
	l.all = append(l.all, now)
	return true
}

// since drops the times at or before cutoff. times is in order.
func since(times []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	return times[i:]
}

// clientKey names the sender for rate limiting, from the header Cloudflare
// sets. That header is only worth believing because nothing but the site's
// nginx, behind the tunnel, can reach the relay.
//
// An IPv6 sender is counted by its /64, since one customer line owns the
// whole of it. Requests with no usable address share one bucket, so leaving
// the header off is not a way around the limit. The key is held in memory
// for the window and never logged.
func clientKey(r *http.Request) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP")))
	if err != nil {
		return "unknown"
	}
	addr = addr.Unmap()
	if addr.Is6() {
		prefix, _ := addr.Prefix(64)
		return prefix.String()
	}
	return addr.String()
}
