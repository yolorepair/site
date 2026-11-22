package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// contract is intake's pinned fixture, copied from
// benchhud-intake/tests/fixtures/request-contract.json. Copy the file again
// when intake changes it; don't edit it here.
type contract struct {
	Version  int `json:"version"`
	Endpoint struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	} `json:"endpoint"`
	Headers struct {
		IdempotencyKey struct {
			MaxLength int `json:"max_length"`
		} `json:"Idempotency-Key"`
	} `json:"headers"`
	MaxBodyBytes int `json:"max_body_bytes"`
	Fields       map[string]struct {
		Required  bool     `json:"required"`
		MaxLength int      `json:"max_length"`
		Values    []string `json:"values"`
	} `json:"fields"`
	AtLeastOneOf [][]string `json:"at_least_one_of"`
	Responses    map[string]struct {
		IDPattern string `json:"id_pattern"`
	} `json:"responses"`
	ErrorFields []string  `json:"error_fields"`
	TestToken   string    `json:"test_token"`
	Examples    []example `json:"examples"`
}

type example struct {
	Name  string `json:"name"`
	Given struct {
		SendTwice             bool  `json:"send_twice"`
		SubmitTokenConfigured *bool `json:"submit_token_configured"`
		MaxPending            *int  `json:"max_pending"`
	} `json:"given"`
	Request struct {
		Headers map[string]string `json:"headers"`
		JSON    json.RawMessage   `json:"json"`
		Raw     *string           `json:"raw"`
	} `json:"request"`
	Response struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"response"`
}

func loadContract(t *testing.T) *contract {
	t.Helper()
	raw, err := os.ReadFile("testdata/request-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var c contract
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 {
		t.Fatalf("contract version %d: the fake intake and the relay were written for version 1", c.Version)
	}
	return &c
}

// fakeIntake answers POST /api/requests the way the contract says intake
// does. TestFakeIntakeMatchesContract replays every example in the fixture
// against it, so a relay test that passes here is a test against the contract.
type fakeIntake struct {
	c          *contract
	token      string // "" means no submit token is configured
	maxPending int

	mu     sync.Mutex
	stored []map[string]string
	keys   map[string]string // Idempotency-Key -> id
	seen   []seenRequest     // every request, accepted or not
	fail   []int             // statuses to answer with first, one per request
}

type seenRequest struct {
	header http.Header
	body   []byte
}

func newFakeIntake(c *contract) *fakeIntake {
	return &fakeIntake{c: c, token: c.TestToken, maxPending: 200, keys: map[string]string{}}
}

type fieldError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

func (f *fakeIntake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, seenRequest{header: r.Header.Clone(), body: body})

	if len(f.fail) > 0 {
		status := f.fail[0]
		f.fail = f.fail[1:]
		w.WriteHeader(status)
		return
	}
	if r.Method != f.c.Endpoint.Method || r.URL.Path != f.c.Endpoint.Path {
		http.NotFound(w, r)
		return
	}
	if f.token == "" || r.Header.Get("Authorization") != "Bearer "+f.token {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "unauthorized"})
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"detail": "unsupported media type"})
		return
	}
	if len(body) > f.c.MaxBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "payload too large"})
		return
	}

	record, errs := f.validate(body)
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if utf8.RuneCountInString(key) > f.c.Headers.IdempotencyKey.MaxLength {
		errs = append(errs, fieldError{"Idempotency-Key", "too_long"})
	}
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid request", "errors": errs})
		return
	}
	if id, ok := f.keys[key]; ok && key != "" {
		writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
		return
	}
	if len(f.stored) >= f.maxPending {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"detail": "too many pending requests"})
		return
	}
	id := "fakeintakeid" + strings.Repeat("0", 8) + string(rune('a'+len(f.stored)%26))
	f.stored = append(f.stored, record)
	if key != "" {
		f.keys[key] = id
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

// validate applies the contract's field rules: field errors in the
// contract's field order, then any "body" error.
func (f *fakeIntake) validate(body []byte) (map[string]string, []fieldError) {
	var obj map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, []fieldError{{"body", "invalid_json"}}
	}

	record := map[string]string{}
	codes := map[string]string{}
	for name, spec := range f.c.Fields {
		raw, present := obj[name]
		value, isString := "", false
		if present && string(raw) != "null" {
			if err := json.Unmarshal(raw, &value); err != nil {
				codes[name] = "invalid"
				continue
			}
			isString = true
		}
		value = strings.TrimSpace(value)
		switch {
		case !isString || value == "":
			if spec.Required {
				codes[name] = "required"
			}
		case spec.MaxLength > 0 && utf8.RuneCountInString(value) > spec.MaxLength:
			codes[name] = "too_long"
		case spec.Values != nil && !contains(spec.Values, value):
			codes[name] = "invalid"
		default:
			record[name] = value
		}
	}
	for _, group := range f.c.AtLeastOneOf {
		missing := true
		for _, name := range group {
			if _, ok := record[name]; ok || codes[name] != "" {
				missing = false
			}
		}
		if missing {
			for _, name := range group {
				codes[name] = "one_required"
			}
		}
	}

	var errs []fieldError
	for _, name := range f.c.ErrorFields {
		if code, ok := codes[name]; ok {
			errs = append(errs, fieldError{name, code})
		}
	}
	for name := range obj {
		if _, known := f.c.Fields[name]; !known {
			errs = append(errs, fieldError{"body", "unknown_field"})
			break
		}
	}
	return record, errs
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// TestFakeIntakeMatchesContract replays every example in the fixture against
// the fake, the way intake's own test replays them against the real endpoint.
func TestFakeIntakeMatchesContract(t *testing.T) {
	c := loadContract(t)
	if len(c.Examples) == 0 {
		t.Fatal("the contract has no examples")
	}
	idPattern := regexp.MustCompile(c.Responses["202"].IDPattern)

	for _, ex := range c.Examples {
		t.Run(ex.Name, func(t *testing.T) {
			fake := newFakeIntake(c)
			if ex.Given.SubmitTokenConfigured != nil && !*ex.Given.SubmitTokenConfigured {
				fake.token = ""
			}
			if ex.Given.MaxPending != nil {
				fake.maxPending = *ex.Given.MaxPending
			}

			send := func() (int, map[string]any) {
				body := []byte(ex.Request.JSON)
				if ex.Request.Raw != nil {
					body = []byte(*ex.Request.Raw)
				}
				req, _ := http.NewRequest(c.Endpoint.Method, c.Endpoint.Path, bytes.NewReader(body))
				for k, v := range ex.Request.Headers {
					req.Header.Set(k, v)
				}
				rec := newRecorder()
				fake.ServeHTTP(rec, req)
				var got map[string]any
				_ = json.Unmarshal(rec.body.Bytes(), &got)
				return rec.status, got
			}

			status, got := send()
			if status != ex.Response.Status {
				t.Fatalf("status %d, contract says %d (body %v)", status, ex.Response.Status, got)
			}
			if status == http.StatusAccepted {
				id, _ := got["id"].(string)
				if !idPattern.MatchString(id) {
					t.Errorf("id %q doesn't match %s", id, idPattern)
				}
				if ex.Given.SendTwice {
					status2, got2 := send()
					if status2 != http.StatusAccepted || got2["id"] != id {
						t.Errorf("repeat with the same key: %d %v, want 202 with id %q", status2, got2, id)
					}
				}
				if len(fake.stored) != 1 {
					t.Errorf("stored %d requests, want 1", len(fake.stored))
				}
				return
			}
			var want map[string]any
			if err := json.Unmarshal(ex.Response.Body, &want); err != nil {
				t.Fatal(err)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("body %s, contract says %s", gotJSON, wantJSON)
			}
			if len(fake.stored) != 0 {
				t.Errorf("stored %d requests on a %d", len(fake.stored), status)
			}
		})
	}
}

// recorder is a minimal http.ResponseWriter for driving a handler directly.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newRecorder() *recorder { return &recorder{header: http.Header{}, status: http.StatusOK} }

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(status int)      { r.status = status }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
