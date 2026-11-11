import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

const backend = process.env.BACKEND_URL ?? 'http://localhost:8080';

// The Go server renders every page; this build is the islands that enhance
// them (src/islands.ts), named in the manifest the server reads. In dev, open
// Vite's port: it serves the islands with hot reload and proxies everything
// else to the server (run it with VITE_DEV=true).
export default defineConfig({
	plugins: [svelte()],
	appType: 'custom',
	build: {
		outDir: 'build',
		manifest: true,
		rolldownOptions: { input: 'src/islands.ts' }
	},
	server: {
		proxy: {
			'^/(?!@vite/|@id/|@fs/|src/|node_modules/)': backend
		}
	}
});
