import prettier from 'eslint-config-prettier';
import path from 'node:path';
import js from '@eslint/js';
import svelte from 'eslint-plugin-svelte';
import { defineConfig, includeIgnoreFile } from 'eslint/config';
import globals from 'globals';
import oxlint from 'eslint-plugin-oxlint';
import ts from 'typescript-eslint';
import local from './lint/local.js';

const gitignorePath = path.resolve(import.meta.dirname, '.gitignore');

export default defineConfig(
	includeIgnoreFile(gitignorePath),
	{ ignores: ['src/lib/api/generated/'] },
	js.configs.recommended,
	ts.configs.recommended,
	svelte.configs.recommended,
	prettier,
	svelte.configs.prettier,
	{
		languageOptions: { globals: globals.browser },
		// TypeScript checks undefined names (typescript-eslint recommends this off).
		rules: { 'no-undef': 'off' }
	},
	{
		files: ['*.config.js', '*.config.ts', 'lint/**'],
		languageOptions: { globals: globals.node }
	},
	{
		files: ['**/*.svelte', '**/*.svelte.ts', '**/*.svelte.js'],
		languageOptions: {
			parserOptions: {
				extraFileExtensions: ['.svelte'],
				parser: ts.parser
			}
		}
	},
	// oxlint runs these; registered so eslint-disable comments naming them resolve.
	{ plugins: { local } },
	...oxlint.buildFromOxlintConfigFile(path.resolve(import.meta.dirname, '.oxlintrc.json'))
);
