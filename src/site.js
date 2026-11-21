// The site's pages, in nav order. One list feeds the nav, the layout and the
// sitemap filter in astro.config.mjs.
//
// draft: the page exists but its copy doesn't. A draft is served and linked
// from the nav, but carries noindex and stays out of the sitemap. Delete the
// flag when the page has real content.
export const pages = [
  { path: '/', label: 'Home' },
  { path: '/repairs/', label: 'Repairs' },
  { path: '/how-it-works/', label: 'How it works', draft: true },
  { path: '/contact/', label: 'Contact', draft: true },
];

export const draftPaths = pages.filter((p) => p.draft).map((p) => p.path);
