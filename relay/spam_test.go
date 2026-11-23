package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// postFrom sends a form with a client address, the way nginx passes it on.
func (r *rig) postFrom(t *testing.T, ip string, form url.Values) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.relay.URL+"/submit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if ip != "" {
		req.Header.Set("CF-Connecting-IP", ip)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

func (r *rig) logged(outcome string) bool {
	return strings.Contains(r.logs.String(), `"outcome":"`+outcome+`"`)
}

func TestHoneypot(t *testing.T) {
	t.Run("filled: thanks page, nothing sent", func(t *testing.T) {
		r := newRig(t)
		form := goodForm()
		form.Set("website", "https://example.com")
		if loc := r.post(t, form); loc != thanksPath {
			t.Errorf("redirected to %q, want %q so the bot isn't told to retry", loc, thanksPath)
		}
		if len(r.fake.seen) != 0 {
			t.Errorf("intake saw %d requests, want 0", len(r.fake.seen))
		}
		if !r.logged("honeypot") {
			t.Errorf("log doesn't record the honeypot: %s", r.logs)
		}
	})
	t.Run("filled on a form that is otherwise invalid: still thanks", func(t *testing.T) {
		r := newRig(t)
		if loc := r.post(t, url.Values{"website": {"x"}}); loc != thanksPath {
			t.Errorf("redirected to %q, want %q", loc, thanksPath)
		}
	})
	t.Run("present but empty, as a browser sends it: goes through", func(t *testing.T) {
		r := newRig(t)
		form := goodForm()
		form.Set("website", "  ")
		if loc := r.post(t, form); loc != thanksPath {
			t.Fatalf("redirected to %q, want %q", loc, thanksPath)
		}
		if len(r.fake.stored) != 1 {
			t.Fatalf("intake stored %d requests, want 1", len(r.fake.stored))
		}
		if _, sent := r.fake.stored[0]["website"]; sent {
			t.Error("the honeypot field was forwarded to intake")
		}
	})
}

func TestLinkCheck(t *testing.T) {
	withSymptoms := func(s string) url.Values {
		form := goodForm()
		form.Set("symptoms", s)
		return form
	}
	spam := map[string]url.Values{
		"only a link":       withSymptoms("https://example.com/buy-now"),
		"link and a word":   withSymptoms("Great deals www.example.com"),
		"three links":       withSymptoms("The mixer hums and I have tried everything. See http://a.example/1 and http://b.example/2 and https://c.example/3 for what it does."),
		"upper-case scheme": withSymptoms("HTTPS://EXAMPLE.COM/x"),
		"link in the name":  func() url.Values { f := goodForm(); f.Set("customer_name", "Visit https://example.com"); return f }(),
	}
	for name, form := range spam {
		t.Run("turned away: "+name, func(t *testing.T) {
			r := newRig(t)
			if loc := r.post(t, form); loc != problemPath {
				t.Errorf("redirected to %q, want %q", loc, problemPath)
			}
			if len(r.fake.seen) != 0 {
				t.Errorf("intake saw %d requests, want 0", len(r.fake.seen))
			}
			if !r.logged("spam") {
				t.Errorf("log doesn't record spam: %s", r.logs)
			}
		})
	}

	fine := map[string]url.Values{
		"no links":                        withSymptoms("No sound from the speaker."),
		"one link with a description":     withSymptoms("Channel 2 cuts out when the fader moves. It's this model: https://example.com/djm-900"),
		"two links with a description":    withSymptoms("Fans spin, no picture. Card: https://example.com/card Video of it: https://example.com/video"),
		"mentions the web without a link": withSymptoms("It can't load http pages or anything on the www since the update."),
	}
	for name, form := range fine {
		t.Run("goes through: "+name, func(t *testing.T) {
			r := newRig(t)
			if loc := r.post(t, form); loc != thanksPath {
				t.Errorf("redirected to %q, want %q", loc, thanksPath)
			}
			if len(r.fake.stored) != 1 {
				t.Errorf("intake stored %d requests, want 1", len(r.fake.stored))
			}
		})
	}
}

func TestRateLimitPerClient(t *testing.T) {
	r := newRig(t)
	r.srv.limiter = newRateLimiter(2, 100, time.Hour)

	for i := 1; i <= 2; i++ {
		if loc := r.postFrom(t, "203.0.113.7", goodForm()); loc != thanksPath {
			t.Fatalf("request %d redirected to %q, want %q", i, loc, thanksPath)
		}
	}
	if loc := r.postFrom(t, "203.0.113.7", goodForm()); loc != problemPath {
		t.Errorf("the third from one address redirected to %q, want %q", loc, problemPath)
	}
	if !r.logged("rate_limited") {
		t.Errorf("log doesn't record rate_limited: %s", r.logs)
	}
	// Someone else is not held up by it.
	if loc := r.postFrom(t, "203.0.113.8", goodForm()); loc != thanksPath {
		t.Errorf("another address redirected to %q, want %q", loc, thanksPath)
	}
	if len(r.fake.stored) != 3 {
		t.Errorf("intake stored %d requests, want 3", len(r.fake.stored))
	}
	if strings.Contains(r.logs.String(), "203.0.113") {
		t.Errorf("the log holds a client address: %s", r.logs)
	}
}

func TestRateLimitOverall(t *testing.T) {
	r := newRig(t)
	r.srv.limiter = newRateLimiter(100, 3, time.Hour)

	for _, ip := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"} {
		if loc := r.postFrom(t, ip, goodForm()); loc != thanksPath {
			t.Fatalf("%s redirected to %q, want %q", ip, loc, thanksPath)
		}
	}
	if loc := r.postFrom(t, "203.0.113.4", goodForm()); loc != problemPath {
		t.Errorf("the fourth overall redirected to %q, want %q", loc, problemPath)
	}
	if len(r.fake.stored) != 3 {
		t.Errorf("intake stored %d requests, want 3", len(r.fake.stored))
	}
}

// Junk that is turned away for another reason doesn't use up the allowance.
func TestOnlyForwardedRequestsCount(t *testing.T) {
	r := newRig(t)
	r.srv.limiter = newRateLimiter(1, 100, time.Hour)

	bot := goodForm()
	bot.Set("website", "x")
	bad := goodForm()
	bad.Del("device")
	for i := 0; i < 5; i++ {
		r.postFrom(t, "203.0.113.7", bot)
		r.postFrom(t, "203.0.113.7", bad)
	}
	if loc := r.postFrom(t, "203.0.113.7", goodForm()); loc != thanksPath {
		t.Errorf("redirected to %q, want %q", loc, thanksPath)
	}
}

func TestRateLimitWindowSlides(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := newRateLimiter(2, 100, time.Hour)
	l.now = func() time.Time { return now }

	if !l.allow("a") || !l.allow("a") {
		t.Fatal("the first two should pass")
	}
	if l.allow("a") {
		t.Fatal("the third inside the window should not")
	}
	now = now.Add(59 * time.Minute)
	if l.allow("a") {
		t.Error("still inside the window")
	}
	now = now.Add(2 * time.Minute)
	if !l.allow("a") {
		t.Error("the window has passed: this should pass")
	}
	// Clients with nothing left in the window are forgotten.
	now = now.Add(2 * time.Hour)
	l.allow("b")
	if _, kept := l.clients["a"]; kept || len(l.clients) != 1 {
		t.Errorf("remembered clients: %v, want only b", l.clients)
	}
}

func TestClientKey(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":           "203.0.113.7",
		" 203.0.113.7 ":         "203.0.113.7",
		"::ffff:203.0.113.7":    "203.0.113.7",
		"2001:db8:1:2:aaaa::1":  "2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb::2":  "2001:db8:1:2::/64",
		"2001:db8:1:3::1":       "2001:db8:1:3::/64",
		"":                      "unknown",
		"not an address":        "unknown",
		"203.0.113.7, 10.0.0.1": "unknown",
	}
	for header, want := range cases {
		req, _ := http.NewRequest(http.MethodPost, "/submit", nil)
		if header != "" {
			req.Header.Set("CF-Connecting-IP", header)
		}
		if got := clientKey(req); got != want {
			t.Errorf("%q: key %q, want %q", header, got, want)
		}
	}
}

// Leaving the header off is not a way around the limit.
func TestNoAddressSharesOneBucket(t *testing.T) {
	r := newRig(t)
	r.srv.limiter = newRateLimiter(1, 100, time.Hour)
	if loc := r.postFrom(t, "", goodForm()); loc != thanksPath {
		t.Fatalf("redirected to %q, want %q", loc, thanksPath)
	}
	if loc := r.postFrom(t, "junk", goodForm()); loc != problemPath {
		t.Errorf("redirected to %q, want %q", loc, problemPath)
	}
}

type failingChallenge struct{}

func (failingChallenge) passes(*http.Request, url.Values) bool { return false }

func TestChallengeSeam(t *testing.T) {
	t.Run("none passes everything", func(t *testing.T) {
		for _, name := range []string{"", "none"} {
			c, err := newChallenge(name)
			if err != nil {
				t.Fatalf("%q: %v", name, err)
			}
			if !c.passes(nil, nil) {
				t.Errorf("%q turned a request away", name)
			}
		}
	})
	t.Run("anything else refuses to start", func(t *testing.T) {
		for _, name := range []string{"turnstile", "None", "off"} {
			if _, err := newChallenge(name); err == nil {
				t.Errorf("%q: want an error", name)
			}
		}
	})
	t.Run("a failed challenge sends nothing and uses no allowance", func(t *testing.T) {
		r := newRig(t)
		r.srv.limiter = newRateLimiter(1, 100, time.Hour)
		r.srv.challenge = failingChallenge{}
		if loc := r.post(t, goodForm()); loc != problemPath {
			t.Errorf("redirected to %q, want %q", loc, problemPath)
		}
		if len(r.fake.seen) != 0 {
			t.Errorf("intake saw %d requests, want 0", len(r.fake.seen))
		}
		r.srv.challenge = noChallenge{}
		if loc := r.post(t, goodForm()); loc != thanksPath {
			t.Errorf("after the challenge passes: redirected to %q, want %q", loc, thanksPath)
		}
	})
}

func TestSpamSettings(t *testing.T) {
	base := map[string]string{"INTAKE_URL": "http://benchhud-intake:8000", "INTAKE_SUBMIT_TOKEN": "tok"}
	with := func(extra map[string]string) func(string) string {
		return func(k string) string {
			if v, ok := extra[k]; ok {
				return v
			}
			return base[k]
		}
	}

	cfg, err := loadConfig(with(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.perIP != 5 || cfg.overall != 30 {
		t.Errorf("defaults are %d per address and %d overall, want 5 and 30", cfg.perIP, cfg.overall)
	}
	if _, ok := cfg.challenge.(noChallenge); !ok {
		t.Errorf("default challenge is %T, want noChallenge", cfg.challenge)
	}

	cfg, err = loadConfig(with(map[string]string{"RATE_LIMIT_PER_IP": " 3 ", "RATE_LIMIT_TOTAL": "50", "CHALLENGE": "none"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.perIP != 3 || cfg.overall != 50 {
		t.Errorf("got %d and %d, want 3 and 50", cfg.perIP, cfg.overall)
	}

	for _, bad := range []map[string]string{
		{"RATE_LIMIT_PER_IP": "0"},
		{"RATE_LIMIT_PER_IP": "-1"},
		{"RATE_LIMIT_TOTAL": "lots"},
		{"RATE_LIMIT_TOTAL": "5 per hour"},
		{"CHALLENGE": "turnstile"},
	} {
		if _, err := loadConfig(with(bad)); err == nil {
			t.Errorf("%v: want an error", bad)
		}
	}
}
