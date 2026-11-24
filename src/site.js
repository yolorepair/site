// The site's pages, in nav order. One list feeds the nav, the layout and the
// sitemap filter in astro.config.mjs.
//
// draft: the page exists but its copy doesn't. A draft is served and linked
// from the nav, but carries noindex and stays out of the sitemap. Delete the
// flag when the page has real content.
export const pages = [
  { path: '/', label: 'Home' },
  { path: '/repairs/', label: 'Repairs' },
  { path: '/how-it-works/', label: 'How it works' },
  { path: '/request/', label: 'Request a repair' },
  { path: '/contact/', label: 'Contact' },
];

// Served, but not meant to be found: no nav link, noindex, out of the sitemap.
// The form's thanks and problem pages live here, since nobody should land on
// them from a search.
export const unlisted = ['/request/thanks/', '/request/problem/'];

export const noindexPaths = [
  ...pages.filter((p) => p.draft).map((p) => p.path),
  ...unlisted,
];
