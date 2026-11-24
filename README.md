# yolorepair.com

The site for **YOLO Electronics Repair** — board-level repair in West Sacramento. Laptops, GPUs, DJ gear, power amps, retro hardware.

It's a static site. [Astro](https://astro.build) builds plain HTML and CSS, nginx serves it, and the whole thing ships as one container image. No JavaScript reaches the browser, and the page loads nothing from anyone else's server — fonts included.

## Run it

You need Node 22.23.4 or newer.

```bash
npm ci
npm run dev
```

That serves the site at `http://localhost:4321` and reloads as you edit.

## Check it

The same checks CI runs. Build first — the validators read `dist/`.

```bash
npm run build
npm run check
```

`check` validates the HTML and follows every internal link, including font URLs in the CSS. Links to other sites are skipped on purpose: someone else's outage shouldn't fail a build.

## Run the real thing

`npm run dev` is Astro's dev server. What runs in production is nginx in a container, with its own headers and cache rules. To see that:

```bash
docker build -t yolorepair-site:test .
test/smoke.sh
```

The smoke test starts the image and checks it from the outside: status codes, security headers, cache headers, compression, redirects, the sitemap, and the facts the copy has to keep straight.

To test a published image instead, pass its name. A registry image is pulled first, so you're never testing a stale local copy:

```bash
test/smoke.sh ghcr.io/yolorepair/site:latest
```

The package is private, so that needs a `docker login ghcr.io` first.

To poke at it yourself:

```bash
docker run --rm -p 8080:8080 yolorepair-site:test
```

Then open `http://localhost:8080`.

## Where things live

| Path | What's in it |
|---|---|
| `src/pages/` | One file per page. |
| `src/layouts/Base.astro` | The `<head>` for every page. Pages pass a title and a description. |
| `src/components/` | Header, footer, the logo mark. |
| `src/styles/` | All the CSS. `global.css` is shared; the rest are per page. |
| `src/site.js` | The page list, and the pages that are served but unlisted. The nav, the sitemap and `noindex` all read it. |
| `public/` | Files served as-is: favicons, the link-preview image, `robots.txt`. |
| `nginx/` | Server config: headers, caching, the 404, and the form's path to the relay. |
| `test/smoke.sh` | The smoke test. |
| `test/e2e.sh` | The end-to-end test: site, relay and a fake intake together. |
| `Dockerfile` | Two stages: Node builds the site, nginx serves it. |
| `relay/` | The relay: a small Go service for the repair-request form. Its own image. |

## The request form

`/request/` is a plain HTML form: name, email, what it is, what it's doing, drop-off or mail-in, and a return address in separate boxes (street, second line, city, state, ZIP) so a browser or password manager can fill it in. It posts to `/request/submit` on the same site, so the CSP's `form-action 'self'` still holds.

One small script, `src/scripts/address.js`, makes the address quicker. It's built into a file under `/_astro/`, since the CSP allows no inline script, and the form works the same without it:

- **Suggestions on the Street box.** It asks `/request/address` on the site itself, and picking a match fills street, city, state and ZIP.
- **A note on drop-off vs mail-in from the ZIP.** A Sacramento-area ZIP (`956`, `957`, `958`) pre-selects drop-off if nothing is picked yet. It never changes a choice someone made; when the two disagree it says so under the question.

**It's open.** `/request/` is in the nav and the sitemap, and every other page points at it, with the email address next to it as the fallback. `/request/thanks/` and `/request/problem/` are in `unlisted` in `src/site.js`: served, but `noindex` and out of the sitemap, since nobody should land on them from a search.

nginx hands two paths to services outside the site's container: `POST /request/submit` to the relay, and `GET /request/address` to a [Photon](https://github.com/komoot/photon) geocoder (open source, OpenStreetMap data, run in the same cluster — no paid API, and what someone types never leaves it). Nothing else leaves the container. Two settings say where they are:

| Setting | What it is |
|---|---|
| `RELAY_URL` | The relay's address, like `http://site-relay.shop.svc.cluster.local:8080`. Scheme, host and port, nothing else. Optional. |
| `PHOTON_URL` | Photon's address, like `http://photon.photon.svc.cluster.local:2322`. Same rules as `RELAY_URL`. Optional. |

The rule is that the form may break and the site may not:

- **`RELAY_URL` unset** — the form lands on the problem page, which says to email. This is how the image runs today.
- **`RELAY_URL` set to something that isn't a plain address** — same, and the start-up log says so.
- **Relay down, or its name doesn't resolve** — same, per request. nginx looks the name up when a form arrives, not at start, so a missing relay can't stop the site from starting.

Photon gets the same treatment. With `PHOTON_URL` unset, unusable, or Photon down, `/request/address` answers with an empty list and the boxes are plain text fields.

`/request/address` is locked down so it can't be used as a way into Photon:

- **Only `q` goes through.** The rest of Photon's query is fixed in nginx: houses and streets, five at most, biased toward the shop. Every other parameter is dropped. A `q` over 300 characters gets nothing.
- **Nothing about the visitor goes with it.** No cookies, no referrer, no user agent.
- **Rate-limited.** 4 a second per visitor (by `CF-Connecting-IP`, burst 20) and 20 a second for everyone (burst 60). `GET` only.
- **Kept out of the logs.** What someone types is part of their address, so that path has no access log and only critical errors.

That last one has a price: nginx's resolver doesn't read the search domains in `/etc/resolv.conf`. In Kubernetes, give `RELAY_URL` the full service name ending in `.svc.cluster.local`. A short name never resolves, and every form ends on the problem page.

The locations are written at container start by `nginx/40-relay-location.sh` and `nginx/41-address-location.sh` into `/tmp/relay-location.conf` and `/tmp/address-location.conf`, so `/tmp` has to be writable. It already has to be: nginx keeps its pid file there.

The smoke test covers all of the cases above against a stand-in relay and a stand-in Photon. The end-to-end test below runs the real relay; nothing here runs the real Photon.

### End to end

```bash
docker build -t yolorepair-site:test .
docker build -t yolorepair-site-relay:test relay/
test/e2e.sh
```

That puts the site image, the relay image and a fake intake (`test/e2e/fake-intake/`) on one Docker network. It reads the form off the site, fills in every field it finds, posts it, and checks what the fake intake received: the fields, the token, the idempotency key. Then the honeypot, a bad form, the rate limit, intake down and relay down. It needs `jq`.

Each image has its own smoke test. This one is for the two drifting apart — rename a field on the form and forget the relay, and this is what fails. Add a field to the form and it fails until the test knows what to type into it. CI runs it as the `e2e` job.

The fake intake only writes down what it's sent. Holding the relay to intake's real contract is the job of the relay's own tests.

## The relay

`relay/` is a second, separate service. It takes the repair-request form's POST, checks it, forwards it to the bench's intake system with a token that can write a request and read nothing, and answers with a redirect to a thank-you page or a problem page. It keeps nothing: no database, no disk, and nothing a person typed goes in its logs.

It isn't deployed yet. You need Go 1.27 to work on it.

```bash
cd relay
go test ./...
```

The tests run against a fake intake built from `relay/testdata/request-contract.json`. That file is intake's contract, copied from its repo. When intake changes it, copy it again; don't edit it here. A test fails if the relay's rules and the contract disagree.

To build the image and check it from the outside:

```bash
docker build -t yolorepair-site-relay:test relay/
relay/smoke.sh
```

CI runs all of that in the `relay` job and publishes the image as `ghcr.io/yolorepair/site-relay`, with the same tags and the same version number as the site's image.

It reads its settings from the environment and listens on 8080:

| Setting | What it is |
|---|---|
| `INTAKE_URL` | Where intake is. Required. |
| `INTAKE_SUBMIT_TOKEN` | The write-only token. Required. |
| `RATE_LIMIT_PER_IP` | Requests forwarded per sender per hour. Default 5. |
| `RATE_LIMIT_TOTAL` | Requests forwarded per hour, everyone together. Default 30. |
| `CHALLENGE` | `none`, the only value today. It's where a CAPTCHA would plug in. |

It won't start without the first two, or with a setting it can't read.

The form sends the return address in boxes (`address_line1`, `address_line2`, `address_city`, `address_state`, `address_zip`). Intake takes one `return_address`, so the relay joins them, one line each and `City, ST 95691` last, then checks the result against intake's 500-character cap. Each box has its own cap and must be one line. A form that sends the old single `return_address` box still works; one that sends both is turned away.

### Spam checks

Nothing third-party. Four things stand between a bot and the bench:

- **A hidden field.** The form has a `website` input people never see. A bot that fills it in is shown the thank-you page and nothing is sent.
- **A look at the text.** A description that is mostly links, or a name with a link in it, goes to the problem page. One or two links next to a real description are fine.
- **Rate limits.** So many per sender and so many overall, per hour. Over the limit goes to the problem page, which has the email address on it.
- **Size caps**, the same ones intake enforces.

The counters live in memory, so they reset on a restart and aren't shared between copies. The relay is meant to run as one copy. The sender's address comes from the `CF-Connecting-IP` header, which is only trustworthy because nothing but the site can reach the relay. That rule lives in the cluster's config, not here.

## Rules the build enforces

**No inline styles.** The Content Security Policy is `style-src 'self'` with no `'unsafe-inline'`. A `<style>` block or a `style=""` attribute won't apply — the browser drops it without a word. Put CSS in `src/styles/`. The smoke test fails if either shows up in the built HTML.

**Every URL ends in a slash.** `/repairs/`, not `/repairs`. Canonical tags, nav links and the sitemap all use that form, and nginx redirects the other one.

**Nothing third-party.** No CDN fonts, no analytics, no embeds. Adding one means loosening the CSP, and that's a decision, not a side effect.

## Adding a page

1. Create `src/pages/<name>.astro` and wrap the content in `Base`.
2. Add it to the list in `src/site.js`. That puts it in the nav and the sitemap.
3. Add `<name>` to the page loop in `test/smoke.sh`.

A page that shouldn't be found at all goes in `unlisted` in `src/site.js` instead: no nav link, `noindex`, no sitemap entry.

Shipping a page before its copy is written? Set `draft: true` on its entry in `src/site.js`. It's still served and linked, but it carries `noindex` and stays out of the sitemap. List it in `DRAFTS` in the smoke test too.

## How a change gets to the live site

1. Open a pull request. Three checks have to pass: `build` (the image builds for arm64 and amd64, and the smoke test passes), `hadolint` (the Dockerfile lints clean), and `html-and-links`. Two more run on every pull request but aren't required yet: `relay` tests and builds the relay, and `e2e` runs the form end to end.
2. Merge it. `main` is protected — nothing lands without those checks.
3. The merge publishes a new image to `ghcr.io/yolorepair/site`, tagged `latest` and with the commit SHA.
4. The cluster that hosts the site watches `latest` and rolls the new image out by itself.

This repo's job ends at step 3. The deployment config lives in a separate GitOps repo, and TLS is handled at the edge, which is why nginx here only speaks plain HTTP on 8080.

## Releases

Commits follow [Conventional Commits](https://www.conventionalcommits.org). [release-please](https://github.com/googleapis/release-please) reads them, keeps a release PR open with the next version and changelog, and on merge tags the release and publishes both images as `:X.Y.Z` and `:X.Y`.

Every merge still deploys. A release just gives a version something to pin to. `CHANGELOG.md` has the history.

## Dependency updates

Dependabot checks npm, the Docker base images, the relay's Go module and the GitHub Actions versions every Monday and opens one grouped PR per ecosystem. CI runs on each. Nothing merges them for you.

Two things are held on purpose: Node stays on 22 and nginx stays on its stable 1.30 line. Moving either is a decision, so Dependabot only offers patch-level bumps for nginx and nothing across a Node major.

## Brand

The logo, palette and font choices come from [`yolorepair/branding`](https://github.com/yolorepair/branding). This repo carries its own copy of the mark for now.

The fonts are Barlow Condensed and IBM Plex Mono, both under the SIL Open Font License, bundled from the [Fontsource](https://fontsource.org) packages.
