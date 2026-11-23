import { defineConfig } from 'astro/config';
import sitemap from '@astrojs/sitemap';
import { noindexPaths } from './src/site.js';

// Static output only: `astro build` writes plain files to dist/, and nginx
// serves them. No adapter, no server runtime.
export default defineConfig({
  site: 'https://yolorepair.com',
  // Every page is a directory with an index.html, so its URL ends in a slash.
  // Canonicals, nav links and the sitemap all use that form.
  trailingSlash: 'always',
  build: {
    // Astro inlines small stylesheets by default. The CSP has no
    // 'unsafe-inline', so every stylesheet has to be a file.
    inlineStylesheets: 'never',
  },
  integrations: [
    sitemap({
      filter: (url) => !noindexPaths.includes(new URL(url).pathname),
    }),
  ],
});
