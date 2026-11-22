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
| `src/site.js` | The page list. The nav, the sitemap and `noindex` all read it. |
| `public/` | Files served as-is: favicons, the link-preview image, `robots.txt`. |
| `nginx/` | Server config: headers, caching, the 404. |
| `test/smoke.sh` | The smoke test. |
| `Dockerfile` | Two stages: Node builds the site, nginx serves it. |
| `relay/` | The relay: a small Go service for the repair-request form. Its own image. |

## The relay

`relay/` is a second, separate service. It takes the repair-request form's POST, checks it, forwards it to the bench's intake system with a token that can write a request and read nothing, and answers with a redirect to a thank-you page or a problem page. It keeps nothing: no database, no disk, and nothing a person typed goes in its logs.

It isn't deployed yet, and the site has no form yet. You need Go 1.27 to work on it.

```bash
cd relay
go test ./...
```

The tests run against a fake intake built from `relay/testdata/request-contract.json`. That file is intake's contract, copied from its repo. When intake changes it, copy it again; don't edit it here. A test fails if the relay's rules and the contract disagree.

To build the image:

```bash
docker build -t yolorepair-site-relay:test relay/
```

It reads `INTAKE_URL` and `INTAKE_SUBMIT_TOKEN` from the environment and won't start without both. It listens on 8080.

## Rules the build enforces

**No inline styles.** The Content Security Policy is `style-src 'self'` with no `'unsafe-inline'`. A `<style>` block or a `style=""` attribute won't apply — the browser drops it without a word. Put CSS in `src/styles/`. The smoke test fails if either shows up in the built HTML.

**Every URL ends in a slash.** `/repairs/`, not `/repairs`. Canonical tags, nav links and the sitemap all use that form, and nginx redirects the other one.

**Nothing third-party.** No CDN fonts, no analytics, no embeds. Adding one means loosening the CSP, and that's a decision, not a side effect.

## Adding a page

1. Create `src/pages/<name>.astro` and wrap the content in `Base`.
2. Add it to the list in `src/site.js`. That puts it in the nav and the sitemap.
3. Add `<name>` to the page loop in `test/smoke.sh`.

Shipping a page before its copy is written? Set `draft: true` on its entry in `src/site.js`. It's still served and linked, but it carries `noindex` and stays out of the sitemap. List it in `DRAFTS` in the smoke test too.

## How a change gets to the live site

1. Open a pull request. Three checks have to pass: `build` (the image builds for arm64 and amd64, and the smoke test passes), `hadolint` (the Dockerfile lints clean), and `html-and-links`.
2. Merge it. `main` is protected — nothing lands without those checks.
3. The merge publishes a new image to `ghcr.io/yolorepair/site`, tagged `latest` and with the commit SHA.
4. The cluster that hosts the site watches `latest` and rolls the new image out by itself.

This repo's job ends at step 3. The deployment config lives in a separate GitOps repo, and TLS is handled at the edge, which is why nginx here only speaks plain HTTP on 8080.

## Releases

Commits follow [Conventional Commits](https://www.conventionalcommits.org). [release-please](https://github.com/googleapis/release-please) reads them, keeps a release PR open with the next version and changelog, and on merge tags the release and publishes the image as `:X.Y.Z` and `:X.Y`.

Every merge still deploys. A release just gives a version something to pin to. No release has been cut yet, so there's no `CHANGELOG.md` to read.

## Dependency updates

Dependabot checks npm, the Docker base images and the GitHub Actions versions every Monday and opens one grouped PR per ecosystem. CI runs on each. Nothing merges them for you.

Two things are held on purpose: Node stays on 22 and nginx stays on its stable 1.30 line. Moving either is a decision, so Dependabot only offers patch-level bumps for nginx and nothing across a Node major.

## Brand

The logo, palette and font choices come from [`yolorepair/branding`](https://github.com/yolorepair/branding). This repo carries its own copy of the mark for now.

The fonts are Barlow Condensed and IBM Plex Mono, both under the SIL Open Font License, bundled from the [Fontsource](https://fontsource.org) packages.
