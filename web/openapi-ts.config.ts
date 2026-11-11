import { defineConfig } from '@hey-api/openapi-ts';

// Generates the API client from the server's contract (`just gen-api`).
export default defineConfig({
	input: '../api/openapi.json',
	output: {
		path: 'src/lib/api/generated',
		importFileExtension: '.ts'
	},
	plugins: [
		{ name: '@hey-api/client-fetch', throwOnError: true },
		'@hey-api/typescript',
		'zod',
		{ name: '@hey-api/sdk', validator: { response: 'zod' } }
	]
});
