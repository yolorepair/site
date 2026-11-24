// The repair-request form's address help. Two things, both optional:
//
// 1. Suggestions on the Street box. As someone types, it asks the site's own
//    /request/address (nginx proxies that to a Photon geocoder in the
//    cluster) and offers up to five matches. Picking one fills the street,
//    city, state and ZIP boxes.
// 2. A note on drop-off vs mail-in from the ZIP. Pre-selects one if nothing
//    is picked yet; never changes a choice someone made.
//
// With this script off, blocked or failing, the boxes are plain text fields
// and the form works the same. Nothing here leaves the site's own origin.

const MIN_CHARS = 3;
const DEBOUNCE_MS = 250;

// "The 916": the Sacramento area, by the first three digits of the ZIP. Close
// enough for a hint; the shop decides drop-offs when it replies.
const LOCAL_ZIP_PREFIXES = ['956', '957', '958'];

const STATES = {
  Alabama: 'AL', Alaska: 'AK', Arizona: 'AZ', Arkansas: 'AR', California: 'CA',
  Colorado: 'CO', Connecticut: 'CT', Delaware: 'DE', 'District of Columbia': 'DC',
  Florida: 'FL', Georgia: 'GA', Hawaii: 'HI', Idaho: 'ID', Illinois: 'IL',
  Indiana: 'IN', Iowa: 'IA', Kansas: 'KS', Kentucky: 'KY', Louisiana: 'LA',
  Maine: 'ME', Maryland: 'MD', Massachusetts: 'MA', Michigan: 'MI',
  Minnesota: 'MN', Mississippi: 'MS', Missouri: 'MO', Montana: 'MT',
  Nebraska: 'NE', Nevada: 'NV', 'New Hampshire': 'NH', 'New Jersey': 'NJ',
  'New Mexico': 'NM', 'New York': 'NY', 'North Carolina': 'NC',
  'North Dakota': 'ND', Ohio: 'OH', Oklahoma: 'OK', Oregon: 'OR',
  Pennsylvania: 'PA', 'Rhode Island': 'RI', 'South Carolina': 'SC',
  'South Dakota': 'SD', Tennessee: 'TN', Texas: 'TX', Utah: 'UT', Vermont: 'VT',
  Virginia: 'VA', Washington: 'WA', 'West Virginia': 'WV', Wisconsin: 'WI',
  Wyoming: 'WY', 'Puerto Rico': 'PR',
};

const $ = (id) => document.getElementById(id);

function setUp() {
  const street = $('address_line1');
  const city = $('address_city');
  const state = $('address_state');
  const zip = $('address_zip');
  if (!street || !city || !state || !zip) return;

  setUpSuggestions(street, city, state, zip);
  setUpHandoffNote(zip);
}

// --- Suggestions -----------------------------------------------------------

function setUpSuggestions(street, city, state, zip) {
  const list = document.createElement('ul');
  list.id = 'address-suggestions';
  list.className = 'suggestions';
  list.setAttribute('role', 'listbox');
  list.setAttribute('aria-label', 'Address suggestions');
  list.hidden = true;
  street.parentElement.appendChild(list);

  const status = document.createElement('p');
  status.className = 'visually-hidden';
  status.setAttribute('role', 'status');
  street.parentElement.appendChild(status);

  street.setAttribute('role', 'combobox');
  street.setAttribute('aria-autocomplete', 'list');
  street.setAttribute('aria-controls', list.id);
  street.setAttribute('aria-expanded', 'false');

  let matches = [];
  let active = -1;
  let timer = 0;
  let inflight = null;
  let lastQuery = '';

  const close = () => {
    list.hidden = true;
    list.replaceChildren();
    matches = [];
    active = -1;
    street.setAttribute('aria-expanded', 'false');
    street.removeAttribute('aria-activedescendant');
  };

  const highlight = (i) => {
    active = i;
    [...list.children].forEach((li, n) => li.setAttribute('aria-selected', String(n === i)));
    if (i >= 0) {
      street.setAttribute('aria-activedescendant', list.children[i].id);
      list.children[i].scrollIntoView({ block: 'nearest' });
    } else {
      street.removeAttribute('aria-activedescendant');
    }
  };

  const pick = (i) => {
    const m = matches[i];
    if (!m) return;
    street.value = m.line1;
    if (m.city) city.value = m.city;
    if (m.state) state.value = m.state;
    if (m.zip) {
      zip.value = m.zip;
      zip.dispatchEvent(new Event('input', { bubbles: true }));
    }
    lastQuery = street.value.trim();
    close();
    status.textContent = 'Address filled in.';
  };

  const show = (found) => {
    matches = found;
    list.replaceChildren();
    if (found.length === 0) {
      close();
      return;
    }
    found.forEach((m, i) => {
      const li = document.createElement('li');
      li.id = `address-suggestion-${i}`;
      li.setAttribute('role', 'option');
      li.setAttribute('aria-selected', 'false');
      li.textContent = m.label;
      // mousedown, not click: click comes after the input's blur, which
      // would close the list first.
      li.addEventListener('mousedown', (e) => {
        e.preventDefault();
        pick(i);
      });
      list.appendChild(li);
    });
    active = -1;
    list.hidden = false;
    street.setAttribute('aria-expanded', 'true');
    status.textContent = `${found.length} address ${found.length === 1 ? 'suggestion' : 'suggestions'}. Use the arrow keys to choose.`;
  };

  const lookUp = async (q) => {
    if (inflight) inflight.abort();
    inflight = new AbortController();
    try {
      const res = await fetch(`/request/address?q=${encodeURIComponent(q)}`, {
        signal: inflight.signal,
        headers: { Accept: 'application/json' },
      });
      if (!res.ok) return close();
      const data = await res.json();
      // Stale answer for something no longer in the box.
      if (street.value.trim() !== q) return;
      show(toMatches(data));
    } catch {
      // Aborted, offline, or Photon down: no suggestions, nothing else changes.
      close();
    }
  };

  street.addEventListener('input', () => {
    clearTimeout(timer);
    const q = street.value.trim();
    if (q.length < MIN_CHARS || q === lastQuery) {
      if (q.length < MIN_CHARS) close();
      return;
    }
    timer = setTimeout(() => {
      lastQuery = q;
      lookUp(q);
    }, DEBOUNCE_MS);
  });

  street.addEventListener('keydown', (e) => {
    if (list.hidden) return;
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        highlight((active + 1) % matches.length);
        break;
      case 'ArrowUp':
        e.preventDefault();
        highlight(active <= 0 ? matches.length - 1 : active - 1);
        break;
      case 'Enter':
        // Only take over Enter when an option is highlighted; otherwise it
        // submits the form as usual.
        if (active >= 0) {
          e.preventDefault();
          pick(active);
        }
        break;
      case 'Escape':
        e.preventDefault();
        close();
        break;
    }
  });

  street.addEventListener('blur', close);
}

// Photon answers in GeoJSON. Keep the ones that make a usable street line.
function toMatches(data) {
  const seen = new Set();
  const out = [];
  for (const f of (data && data.features) || []) {
    const p = f.properties || {};
    if (p.countrycode && p.countrycode !== 'US') continue;
    const streetName = p.street || (p.type === 'street' ? p.name : '');
    if (!streetName) continue;
    const line1 = p.housenumber ? `${p.housenumber} ${streetName}` : streetName;
    const city = p.city || p.town || p.village || p.locality || p.district || '';
    const state = STATES[p.state] || p.state || '';
    const zip = (p.postcode || '').split(';')[0].trim();
    const label = [line1, city, [state, zip].filter(Boolean).join(' ')].filter(Boolean).join(', ');
    if (seen.has(label)) continue;
    seen.add(label);
    out.push({ line1, city, state, zip, label });
    if (out.length === 5) break;
  }
  return out;
}

// --- Drop-off or mail-in ---------------------------------------------------

function setUpHandoffNote(zip) {
  const note = $('handoff-note');
  const dropOff = document.querySelector('input[name="handoff"][value="drop_off"]');
  const mailIn = document.querySelector('input[name="handoff"][value="mail_in"]');
  if (!note || !dropOff || !mailIn) return;

  // Did a person pick one? Changes made from here don't count.
  let chosenByPerson = dropOff.checked || mailIn.checked;
  for (const radio of [dropOff, mailIn]) {
    radio.addEventListener('change', (e) => {
      if (e.isTrusted) chosenByPerson = true;
      update();
    });
  }

  function update() {
    const digits = zip.value.replace(/\D/g, '');
    if (digits.length < 5) {
      note.textContent = '';
      return;
    }
    const local = LOCAL_ZIP_PREFIXES.includes(digits.slice(0, 3));
    if (!chosenByPerson && !dropOff.checked && !mailIn.checked) {
      (local ? dropOff : mailIn).checked = true;
    }
    if (local && mailIn.checked) {
      note.textContent = "That's a Sacramento-area ZIP. Drop-off works too, by appointment.";
    } else if (!local && dropOff.checked) {
      note.textContent = "That ZIP's outside the 916. Mail-in is probably easier.";
    } else {
      note.textContent = '';
    }
  }

  zip.addEventListener('input', update);
  zip.addEventListener('change', update);
}

setUp();
