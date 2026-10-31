/// <reference types="vitest/config" />
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import react from '@vitejs/plugin-react';
import { defineConfig, type Plugin } from 'vite';

// Serves the MSW worker from node_modules during `dev:mock`, so it is never
// copied into public/ and cannot end up in a production build.
function mockWorker(): Plugin {
  return {
    name: 'webee-mock-worker',
    configureServer(server) {
      server.middlewares.use('/mockServiceWorker.js', (_req, res) => {
        res.setHeader('Content-Type', 'text/javascript');
        res.end(readFileSync(resolve(import.meta.dirname, 'node_modules/msw/lib/mockServiceWorker.js')));
      });
    },
  };
}

export default defineConfig(({ command, mode }) => {
  const mock = mode === 'mock';
  if (mock && command === 'build') {
    throw new Error('Mock mode is for local development only; it must not be built.');
  }
  return {
    plugins: [react(), ...(mock ? [mockWorker()] : [])],
    define: { __USE_MOCKS__: JSON.stringify(mock) },
    server: {
      // Matches APP_BASE_URL in .env.example, which emailed links point to.
      port: 3000,
      strictPort: true,
      // Same-origin in development: the session cookie is SameSite=Lax and the
      // API refuses unsafe cross-origin requests.
      proxy: mock ? undefined : { '/api': process.env.API_PROXY_TARGET ?? 'http://localhost:8080' },
    },
    test: {
      environment: 'jsdom',
      setupFiles: ['./src/test/setup.ts'],
      css: false,
    },
  };
});
