package main

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// The form asks for the return address in parts, so a browser or password
// manager can fill it in and the type-ahead can drop a pick into the right
// boxes. Intake's contract has one return_address string, so the relay joins
// the parts into it before anything else looks at the form:
//
//	123 Main St
//	Apt 4
//	West Sacramento, CA 95691
//
// The joined text then goes through return_address's own rules (trimmed,
// 500 characters at most). The per-part caps are the relay's, not intake's,
// and match the form's maxlength attributes.
type addressPart struct {
	name   string
	maxLen int
}

var addressParts = []addressPart{
	{name: "address_line1", maxLen: 200},
	{name: "address_line2", maxLen: 200},
	{name: "address_city", maxLen: 100},
	{name: "address_state", maxLen: 50},
	{name: "address_zip", maxLen: 10},
}

// joinAddress returns the form with return_address built from the address
// parts, and the parts themselves removed. A form with no parts comes back
// as it was, so the old single-box form still works. ok is false when a part
// breaks its rules, or when a form sends both the parts and a return_address
// (the page never does both).
func joinAddress(form url.Values) (url.Values, bool) {
	parts := make(map[string]string, len(addressParts))
	sent := false
	for _, p := range addressParts {
		raw, has := form[p.name]
		if !has {
			continue
		}
		sent = true
		if len(raw) > 1 || !utf8.ValidString(raw[0]) {
			return nil, false
		}
		// One line each: a line break inside a part would let it pass as
		// several lines of the joined address.
		v := strings.TrimSpace(raw[0])
		if strings.ContainsAny(v, "\r\n") || utf8.RuneCountInString(v) > p.maxLen {
			return nil, false
		}
		parts[p.name] = v
	}
	if !sent {
		return form, true
	}
	if _, has := form["return_address"]; has {
		return nil, false
	}

	var lines []string
	for _, name := range []string{"address_line1", "address_line2"} {
		if parts[name] != "" {
			lines = append(lines, parts[name])
		}
	}
	// "City, ST 95691", leaving out whatever is blank.
	last := parts["address_city"]
	region := strings.TrimSpace(parts["address_state"] + " " + parts["address_zip"])
	switch {
	case last != "" && region != "":
		last += ", " + region
	case last == "":
		last = region
	}
	if last != "" {
		lines = append(lines, last)
	}

	out := make(url.Values, len(form))
	for k, v := range form {
		out[k] = v
	}
	for _, p := range addressParts {
		delete(out, p.name)
	}
	if len(lines) > 0 {
		out.Set("return_address", strings.Join(lines, "\n"))
	}
	return out, true
}
