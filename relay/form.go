package main

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// The form's fields, as intake's contract defines them. The contract is
// pinned in testdata/request-contract.json, and TestRulesMatchContract fails
// if these drift from it.
type field struct {
	name     string
	required bool
	maxLen   int      // characters, counted after trimming
	values   []string // allowed values; nil means any text
}

var fields = []field{
	{name: "customer_name", required: true, maxLen: 120},
	{name: "email", maxLen: 254},
	{name: "phone", maxLen: 40},
	{name: "device", required: true, maxLen: 200},
	{name: "symptoms", required: true, maxLen: 4000},
	{name: "handoff", required: true, values: []string{"drop_off", "mail_in"}},
	{name: "return_address", maxLen: 500},
}

// At least one field of each group has to be filled in.
var oneOf = [][]string{{"email", "phone"}}

// maxJSONBytes is intake's cap on the body the relay sends it.
const maxJSONBytes = 16384

// parseForm turns a decoded form into the request intake expects: known
// fields only, trimmed, blanks left out. ok is false when the form breaks the
// contract. It never says which field or why, because nothing downstream
// shows that to anyone and the values must not reach a log.
//
// Form fields the contract doesn't list are ignored rather than forwarded:
// intake rejects unknown keys.
func parseForm(form url.Values) (req map[string]string, ok bool) {
	req = make(map[string]string, len(fields))
	for _, f := range fields {
		raw := form[f.name]
		if len(raw) > 1 {
			return nil, false // a browser sends each of these once
		}
		if len(raw) == 0 {
			if f.required {
				return nil, false
			}
			continue
		}
		v := raw[0]
		if !utf8.ValidString(v) {
			return nil, false
		}
		// Browsers send textarea line breaks as CRLF. Count and store them
		// as one character, the way the form's maxlength counted them.
		v = strings.TrimSpace(strings.ReplaceAll(v, "\r\n", "\n"))
		if v == "" {
			if f.required {
				return nil, false
			}
			continue
		}
		if f.maxLen > 0 && utf8.RuneCountInString(v) > f.maxLen {
			return nil, false
		}
		if f.values != nil && !contains(f.values, v) {
			return nil, false
		}
		req[f.name] = v
	}
	for _, group := range oneOf {
		found := false
		for _, name := range group {
			if _, has := req[name]; has {
				found = true
			}
		}
		if !found {
			return nil, false
		}
	}
	return req, true
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
