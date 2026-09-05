import tailwindcss from '@tailwindcss/vite';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';
import { execSync } from 'child_process';
import { readFileSync, existsSync } from 'fs';
import { join } from 'path';
import { homedir } from 'os';

// Get version from env for CI builds || version file || git tag for local
function getVersion() {
	if (process.env.APP_VERSION) {
		return process.env.APP_VERSION;
	}

	// Check version file stored in home
	try {
		const versionFile = join(homedir(), '.discopanel');
		if (existsSync(versionFile)) {
			const version = readFileSync(versionFile, 'utf8').trim();
			if (version) return version;
		}
	} catch {
		/* ignore */
	}

	// Derive version from git tags
	try {
		const version = execSync('git describe --tags --always').toString().trim();
		if (version) return version;
	} catch {
		/* ignore */
	}

	return 'dev'; // default
}

export default defineConfig({
	plugins: [tailwindcss(), sveltekit()],
	define: {
		__APP_VERSION__: JSON.stringify(getVersion())
	},
	server: {
		proxy: {
			'/api': {
				target: 'http://localhost:8080',
				changeOrigin: true
			},
			// Proxy Connect RPC service paths
			'/discopanel.v1': {
				target: 'http://localhost:8080',
				changeOrigin: true
			},
			// Proxy gRPC reflection service
			'/grpc.reflection': {
				target: 'http://localhost:8080',
				changeOrigin: true
			},
			// Proxy Connect service paths
			'/connect': {
				target: 'http://localhost:8080',
				changeOrigin: true
			},
			// Keeps browser Host so backend origin check passes
			'/ws': {
				target: 'ws://localhost:8080',
				ws: true
			}
		}
	},
	optimizeDeps: {
		include: ['monaco-editor']
	},
	build: {
		rollupOptions: {
			onwarn(warning, warn) {
				if (warning.code === 'UNUSED_EXTERNAL_IMPORT') {
					return;
				}
				warn(warning);
			}
		}
	}
});
