import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';
import { fileURLToPath, URL } from 'node:url';

const repoRoot = fileURLToPath(new URL('../..', import.meta.url));

export default defineConfig(({ mode }) => {
  // The repository keeps one .env at its root, shared with the API, so the dev
  // proxy always points at the port the API actually listens on.
  const env = loadEnv(mode, repoRoot, '');
  const apiTarget = `http://localhost:${env.VITE_API_PORT || '8080'}`;

  return {
    plugins: [react()],
    resolve: {
      alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
    },
    server: {
      port: 5173,
      // The API is same-origin in production. Proxying in development keeps it
      // same-origin there too, so session cookies behave identically.
      proxy: {
        '/api': { target: apiTarget, changeOrigin: false },
        '/healthz': { target: apiTarget, changeOrigin: false },
        '/readyz': { target: apiTarget, changeOrigin: false },
      },
    },
  };
});
