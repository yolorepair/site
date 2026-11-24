package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestAddressPartsAreJoined(t *testing.T) {
	cases := map[string]struct {
		parts map[string]string
		want  string // "" means no return_address sent
	}{
		"all of it": {
			parts: map[string]string{
				"address_line1": " 123 Main St ", "address_line2": "Apt 4",
				"address_city": "West Sacramento", "address_state": "CA", "address_zip": "95691",
			},
			want: "123 Main St\nApt 4\nWest Sacramento, CA 95691",
		},
		"no second line": {
			parts: map[string]string{
				"address_line1": "123 Main St", "address_line2": "",
				"address_city": "Portland", "address_state": "OR", "address_zip": "97201",
			},
			want: "123 Main St\nPortland, OR 97201",
		},
		"no city": {
			parts: map[string]string{"address_line1": "PO Box 9", "address_state": "NV", "address_zip": "89501"},
			want:  "PO Box 9\nNV 89501",
		},
		"city only": {
			parts: map[string]string{"address_line1": "123 Main St", "address_city": "Davis"},
			want:  "123 Main St\nDavis",
		},
		"all blank, the drop-off case": {
			parts: map[string]string{
				"address_line1": "", "address_line2": " ", "address_city": "",
				"address_state": "", "address_zip": "",
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			form := goodForm()
			for k, v := range tc.parts {
				form.Set(k, v)
			}
			if loc := r.post(t, form); loc != thanksPath {
				t.Fatalf("redirected to %q, want %q", loc, thanksPath)
			}
			var sent map[string]string
			if err := json.Unmarshal(r.fake.seen[0].body, &sent); err != nil {
				t.Fatal(err)
			}
			for _, p := range addressParts {
				if _, has := sent[p.name]; has {
					t.Errorf("%s was forwarded; intake rejects unknown keys", p.name)
				}
			}
			got, has := sent["return_address"]
			if tc.want == "" {
				if has {
					t.Errorf("sent return_address %q, want none", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("return_address %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBadAddressPartsNeverReachIntake(t *testing.T) {
	edit := func(f func(url.Values)) url.Values {
		v := goodForm()
		v.Set("address_line1", "123 Main St")
		f(v)
		return v
	}
	cases := map[string]url.Values{
		"line too long":        edit(func(v url.Values) { v.Set("address_line1", strings.Repeat("a", 201)) }),
		"zip too long":         edit(func(v url.Values) { v.Set("address_zip", "95691-12345") }),
		"line break in a part": edit(func(v url.Values) { v.Set("address_line1", "123 Main St\r\nEvil: yes") }),
		"part sent twice":      edit(func(v url.Values) { v.Add("address_line1", "456 Oak Ave") }),
		"broken text":          edit(func(v url.Values) { v.Set("address_city", "\xff\xfe") }),
		"parts and the old box": edit(func(v url.Values) {
			v.Set("return_address", "somewhere else")
		}),
		"joined over 500": edit(func(v url.Values) {
			v.Set("address_line1", strings.Repeat("a", 200))
			v.Set("address_line2", strings.Repeat("b", 200))
			v.Set("address_city", strings.Repeat("c", 100))
		}),
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

// The old single box still works: a page cached before the split, or the
// end-to-end test's form.
func TestSingleBoxStillWorks(t *testing.T) {
	r := newRig(t)
	form := goodForm()
	form.Set("return_address", "123 Main St\r\nDavis, CA")
	if loc := r.post(t, form); loc != thanksPath {
		t.Fatalf("redirected to %q, want %q", loc, thanksPath)
	}
	var sent map[string]string
	if err := json.Unmarshal(r.fake.seen[0].body, &sent); err != nil {
		t.Fatal(err)
	}
	if got := sent["return_address"]; got != "123 Main St\nDavis, CA" {
		t.Errorf("return_address %q", got)
	}
}
