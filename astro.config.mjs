import { defineConfig } from 'astro/config';

// Static output only: `astro build` writes plain files to dist/, and nginx
// serves them. No adapter, no server runtime.
export default defineConfig({
  site: 'https://yolorepair.com',
});
