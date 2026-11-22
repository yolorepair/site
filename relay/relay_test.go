package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

// rig is a relay wired to a fake intake, both on real sockets.
type rig struct {
	fake  *fakeIntake
	relay *httptest.Server
	logs  *bytes.Buffer
	http  *http.Client
}

func newRig(t *testing.T) *rig {
	t.Helper()
	c := loadContract(t)
	fake := newFakeIntake(c)
	intakeSrv := httptest.NewServer(fake)
	t.Cleanup(intakeSrv.Close)

	logs := &bytes.Buffer{}
	client := newIntakeClient(intakeSrv.URL, c.TestToken)
	client.backoff = time.Millisecond
	s := &server{intake: client, log: slog.New(slog.NewJSONHandler(logs, nil))}
	relaySrv := httptest.NewServer(s.routes())
	t.Cleanup(relaySrv.Close)

	return &rig{
		fake:  fake,
		relay: relaySrv,
		logs:  logs,
		// The redirect is the answer under test, so don't follow it.
		http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

// post sends a form to the relay and returns where it redirects to.
func (r *rig) post(t *testing.T, form url.Values) string {
	t.Helper()
	return r.postRaw(t, "application/x-www-form-urlencoded", form.Encode())
}

func (r *rig) postRaw(t *testing.T, contentType, body string) string {
	t.Helper()
	resp, err := r.http.Post(r.relay.URL+"/submit", contentType, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", got)
	}
	return resp.Header.Get("Location")
}

func goodForm() url.Values {
	return url.Values{
		"customer_name": {"Sam Example"},
		"email":         {"sam@example.com"},
		"device":        {"Game Boy Advance"},
		"symptoms":      {"No sound from the speaker."},
		"handoff":       {"drop_off"},
	}
}

func TestRulesMatchContract(t *testing.T) {
	c := loadContract(t)
	if maxJSONBytes != c.MaxBodyBytes {
		t.Errorf("maxJSONBytes is %d, contract says %d", maxJSONBytes, c.MaxBodyBytes)
	}
	if len(fields) != len(c.Fields) {
		t.Errorf("relay knows %d fields, contract lists %d", len(fields), len(c.Fields))
	}
	for _, f := range fields {
		spec, ok := c.Fields[f.name]
		if !ok {
			t.Errorf("%s is not in the contract", f.name)
			continue
		}
		if f.required != spec.Required || f.maxLen != spec.MaxLength ||
			strings.Join(f.values, ",") != strings.Join(spec.Values, ",") {
			t.Errorf("%s: relay has required=%v max=%d values=%v, contract has required=%v max=%d values=%v",
				f.name, f.required, f.maxLen, f.values, spec.Required, spec.MaxLength, spec.Values)
		}
	}
	got, _ := json.Marshal(oneOf)
	want, _ := json.Marshal(c.AtLeastOneOf)
	if string(got) != string(want) {
		t.Errorf("oneOf is %s, contract says %s", got, want)
	}
}

// Every example intake accepts, sent as a form, has to come out the other
// side as the same request.
func TestAcceptedExamplesGoThrough(t *testing.T) {
	c := loadContract(t)
	ran := 0
	for _, ex := range c.Examples {
		if ex.Response.Status != http.StatusAccepted {
			continue
		}
		ran++
		t.Run(ex.Name, func(t *testing.T) {
			var fieldsIn map[string]*string
			if err := json.Unmarshal(ex.Request.JSON, &fieldsIn); err != nil {
				t.Fatal(err)
			}
			form := url.Values{}
			want := map[string]string{}
			for name, v := range fieldsIn {
				if v == nil {
					continue // a form can't send null; absent is the same thing
				}
				form.Set(name, *v)
				if trimmed := strings.TrimSpace(*v); trimmed != "" {
					want[name] = trimmed
				}
			}

			r := newRig(t)
			if loc := r.post(t, form); loc != thanksPath {
				t.Fatalf("redirected to %q, want %q", loc, thanksPath)
			}
			if len(r.fake.stored) != 1 {
				t.Fatalf("intake stored %d requests, want 1", len(r.fake.stored))
			}
			if got := r.fake.stored[0]; !sameMap(got, want) {
				t.Errorf("intake stored %v, want %v", got, want)
			}
		})
	}
	if ran == 0 {
		t.Fatal("the contract has no accepted examples")
	}
}

func TestWhatIntakeIsSent(t *testing.T) {
	c := loadContract(t)
	r := newRig(t)
	form := goodForm()
	form.Set("symptoms", "  Dead.\r\nNo lights.  ")
	form.Set("phone", "   ")
	form.Set("website", "https://example.com") // not in the contract
	if loc := r.post(t, form); loc != thanksPath {
		t.Fatalf("redirected to %q, want %q", loc, thanksPath)
	}

	if len(r.fake.seen) != 1 {
		t.Fatalf("intake saw %d requests, want 1", len(r.fake.seen))
	}
	seen := r.fake.seen[0]
	if got := seen.header.Get("Authorization"); got != "Bearer "+c.TestToken {
		t.Errorf("Authorization %q", got)
	}
	if got := seen.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type %q", got)
	}
	if key := seen.header.Get("Idempotency-Key"); len(key) != 32 {
		t.Errorf("Idempotency-Key %q, want 32 hex characters", key)
	}
	var sent map[string]string
	if err := json.Unmarshal(seen.body, &sent); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"customer_name": "Sam Example",
		"email":         "sam@example.com",
		"device":        "Game Boy Advance",
		"symptoms":      "Dead.\nNo lights.",
		"handoff":       "drop_off",
	}
	if !sameMap(sent, want) {
		t.Errorf("sent %v, want %v", sent, want)
	}
}

func TestEachSubmissionGetsItsOwnKey(t *testing.T) {
	r := newRig(t)
	r.post(t, goodForm())
	r.post(t, goodForm())
	if len(r.fake.seen) != 2 {
		t.Fatalf("intake saw %d requests, want 2", len(r.fake.seen))
	}
	a := r.fake.seen[0].header.Get("Idempotency-Key")
	b := r.fake.seen[1].header.Get("Idempotency-Key")
	if a == b {
		t.Errorf("two submissions shared the key %q", a)
	}
}

func TestBadFormsNeverReachIntake(t *testing.T) {
	edit := func(f func(url.Values)) url.Values {
		form := goodForm()
		f(form)
		return form
	}
	cases := map[string]url.Values{
		"empty form":         {},
		"no name":            edit(func(v url.Values) { v.Del("customer_name") }),
		"blank name":         edit(func(v url.Values) { v.Set("customer_name", "  ") }),
		"no device":          edit(func(v url.Values) { v.Del("device") }),
		"no symptoms":        edit(func(v url.Values) { v.Del("symptoms") }),
		"no handoff":         edit(func(v url.Values) { v.Del("handoff") }),
		"unknown handoff":    edit(func(v url.Values) { v.Set("handoff", "courier") }),
		"handoff wrong case": edit(func(v url.Values) { v.Set("handoff", "Drop_Off") }),
		"no contact":         edit(func(v url.Values) { v.Del("email") }),
		"blank contact":      edit(func(v url.Values) { v.Set("email", " "); v.Set("phone", "") }),
		"name too long":      edit(func(v url.Values) { v.Set("customer_name", strings.Repeat("a", 121)) }),
		"email too long":     edit(func(v url.Values) { v.Set("email", strings.Repeat("a", 255)) }),
		"phone too long":     edit(func(v url.Values) { v.Set("phone", strings.Repeat("5", 41)) }),
		"device too long":    edit(func(v url.Values) { v.Set("device", strings.Repeat("a", 201)) }),
		"symptoms too long":  edit(func(v url.Values) { v.Set("symptoms", strings.Repeat("a", 4001)) }),
		"address too long":   edit(func(v url.Values) { v.Set("return_address", strings.Repeat("a", 501)) }),
		"field sent twice":   edit(func(v url.Values) { v.Add("email", "other@example.com") }),
		"not utf-8":          edit(func(v url.Values) { v.Set("device", "bad \xff byte") }),
	}
	for name, form := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			if loc := r.post(t, form); loc != problemPath {
				t.Errorf("redirected to %q, want %q", loc, problemPath)
			}
			if len(r.fake.seen) != 0 {
				t.Errorf("intake saw %d requests, want 0", len(r.fake.seen))
			}
		})
	}
}

// Lengths are counted in characters, not bytes, and at the cap is fine.
func TestLengthsAtTheCap(t *testing.T) {
	r := newRig(t)
	form := goodForm()
	form.Set("customer_name", strings.Repeat("é", 120))
	form.Set("device", strings.Repeat("ü", 200))
	form.Set("return_address", strings.Repeat("a\r\n", 250))
	if loc := r.post(t, form); loc != thanksPath {
		t.Fatalf("redirected to %q, want %q", loc, thanksPath)
	}
}

func TestBodiesThatAreNotAForm(t *testing.T) {
	r := newRig(t)
	if loc := r.postRaw(t, "application/json", `{"customer_name":"Sam Example"}`); loc != problemPath {
		t.Errorf("JSON body redirected to %q", loc)
	}
	if loc := r.postRaw(t, "application/x-www-form-urlencoded", "customer_name=%zz"); loc != problemPath {
		t.Errorf("broken encoding redirected to %q", loc)
	}
	big := goodForm()
	big.Set("symptoms", strings.Repeat("a", maxFormBytes))
	if loc := r.post(t, big); loc != problemPath {
		t.Errorf("oversized form redirected to %q", loc)
	}
	if len(r.fake.seen) != 0 {
		t.Errorf("intake saw %d requests, want 0", len(r.fake.seen))
	}
}

// A form inside its field caps can still be over intake's byte cap.
func TestFormOverIntakesByteCap(t *testing.T) {
	r := newRig(t)
	form := goodForm()
	form.Set("symptoms", strings.Repeat("\U0001F525", 4000)) // 4 bytes each
	form.Set("return_address", strings.Repeat("\U0001F525", 500))
	if loc := r.post(t, form); loc != problemPath {
		t.Errorf("redirected to %q, want %q", loc, problemPath)
	}
	if len(r.fake.seen) != 0 {
		t.Errorf("intake saw %d requests, want 0", len(r.fake.seen))
	}
}

func TestRetryReusesTheKey(t *testing.T) {
	r := newRig(t)
	r.fake.fail = []int{http.StatusServiceUnavailable}
	if loc := r.post(t, goodForm()); loc != thanksPath {
		t.Fatalf("redirected to %q, want %q", loc, thanksPath)
	}
	if len(r.fake.seen) != 2 {
		t.Fatalf("intake saw %d requests, want 2", len(r.fake.seen))
	}
	a := r.fake.seen[0].header.Get("Idempotency-Key")
	b := r.fake.seen[1].header.Get("Idempotency-Key")
	if a == "" || a != b {
		t.Errorf("keys %q and %q: the retry has to reuse the first", a, b)
	}
	if len(r.fake.stored) != 1 {
		t.Errorf("intake stored %d requests, want 1", len(r.fake.stored))
	}
}

func TestIntakeSaysNo(t *testing.T) {
	cases := map[string]struct {
		setup   func(*rig)
		outcome string
		tries   int
	}{
		"queue full":     {func(r *rig) { r.fake.maxPending = 0 }, "intake_full", 1},
		"wrong token":    {func(r *rig) { r.fake.token = "another-token" }, "intake_unauthorized", 1},
		"no token set":   {func(r *rig) { r.fake.token = "" }, "intake_unauthorized", 1},
		"still failing":  {func(r *rig) { r.fake.fail = []int{503, 503} }, "intake_error", 2},
		"server error":   {func(r *rig) { r.fake.fail = []int{500} }, "intake_error", 1},
		"contract drift": {func(r *rig) { r.fake.fail = []int{400} }, "intake_rejected", 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			tc.setup(r)
			if loc := r.post(t, goodForm()); loc != problemPath {
				t.Errorf("redirected to %q, want %q", loc, problemPath)
			}
			if len(r.fake.seen) != tc.tries {
				t.Errorf("intake saw %d requests, want %d", len(r.fake.seen), tc.tries)
			}
			if len(r.fake.stored) != 0 {
				t.Errorf("intake stored %d requests, want 0", len(r.fake.stored))
			}
			if !strings.Contains(r.logs.String(), `"outcome":"`+tc.outcome+`"`) {
				t.Errorf("log doesn't record %s: %s", tc.outcome, r.logs)
			}
		})
	}
}

func TestIntakeDown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close() // a port nothing is listening on

	logs := &bytes.Buffer{}
	client := newIntakeClient(dead.URL, "token")
	client.backoff = time.Millisecond
	s := &server{intake: client, log: slog.New(slog.NewJSONHandler(logs, nil))}

	req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(goodForm().Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != problemPath {
		t.Errorf("got %d to %q, want 303 to %q", rec.Code, rec.Header().Get("Location"), problemPath)
	}
	if !strings.Contains(logs.String(), `"outcome":"intake_unreachable"`) {
		t.Errorf("log doesn't record intake_unreachable: %s", logs)
	}
}

// Nothing a person typed may reach the log, whatever happens to the request.
func TestLogsCarryNothingFromTheForm(t *testing.T) {
	secrets := []string{"Zebediah", "zeb@example.com", "555-0199", "Technics-1210", "hums-loudly", "42 Nowhere Lane"}
	form := url.Values{
		"customer_name":  {secrets[0]},
		"email":          {secrets[1]},
		"phone":          {secrets[2]},
		"device":         {secrets[3]},
		"symptoms":       {secrets[4]},
		"handoff":        {"mail_in"},
		"return_address": {secrets[5]},
	}
	c := loadContract(t)

	r := newRig(t)
	r.post(t, form) // accepted
	r.fake.fail = []int{400}
	r.post(t, form) // turned down by intake
	r.fake.token = "another-token"
	r.post(t, form) // unauthorized
	bad := url.Values{}
	for k, v := range form {
		bad[k] = v
	}
	bad.Del("device")
	r.post(t, bad) // invalid

	out := r.logs.String()
	if strings.Count(out, "\n") != 4 {
		t.Fatalf("want one log line per request, got:\n%s", out)
	}
	for _, s := range append(secrets, c.TestToken) {
		if strings.Contains(out, s) {
			t.Errorf("log contains %q:\n%s", s, out)
		}
	}
}

func TestOnlyPostIsAllowed(t *testing.T) {
	r := newRig(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(method, r.relay.URL+"/submit", nil)
		resp, err := r.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodPost {
			t.Errorf("%s: %d, Allow %q", method, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
	if len(r.fake.seen) != 0 {
		t.Errorf("intake saw %d requests, want 0", len(r.fake.seen))
	}
}

func TestHealthzAndNothingElse(t *testing.T) {
	r := newRig(t)
	resp, err := r.http.Get(r.relay.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Errorf("healthz: %d %q", resp.StatusCode, body)
	}
	for _, path := range []string{"/", "/submit/", "/api/requests", "/metrics"} {
		resp, err := r.http.Get(r.relay.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cfg, err := loadConfig(env(map[string]string{
		"INTAKE_URL":          " http://benchhud-intake:8000/ ",
		"INTAKE_SUBMIT_TOKEN": " tok \n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.intakeURL != "http://benchhud-intake:8000" || cfg.token != "tok" || cfg.listenAddr != ":8080" {
		t.Errorf("got %+v", cfg)
	}

	bad := []map[string]string{
		{},
		{"INTAKE_URL": "http://benchhud-intake:8000"},
		{"INTAKE_SUBMIT_TOKEN": "tok"},
		{"INTAKE_URL": "benchhud-intake:8000", "INTAKE_SUBMIT_TOKEN": "tok"},
		{"INTAKE_URL": "ftp://benchhud-intake", "INTAKE_SUBMIT_TOKEN": "tok"},
		{"INTAKE_URL": "http://benchhud-intake:8000", "INTAKE_SUBMIT_TOKEN": "  "},
	}
	for _, m := range bad {
		if _, err := loadConfig(env(m)); err == nil {
			t.Errorf("%v: want an error", m)
		} else if strings.Contains(err.Error(), "tok") {
			t.Errorf("the error names the token: %v", err)
		}
	}
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if a[k] != b[k] {
			return false
		}
	}
	return true
}
