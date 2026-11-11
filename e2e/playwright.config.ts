import { defineConfig, devices } from '@playwright/test';
import { BASE_URL } from './harness.ts';

export default defineConfig({
	testDir: './tests',
	globalSetup: './global-setup.ts',
	outputDir: './test-results',
	// First requests for a file strip and verify it with exiftool/ffmpeg.
	timeout: 30_000,
	expect: { timeout: 15_000 },
	fullyParallel: true,
	forbidOnly: !!process.env.CI,
	workers: process.env.CI ? 2 : undefined,
	reporter: [['list'], ['html', { open: 'never', outputFolder: './playwright-report' }]],
	use: {
		baseURL: BASE_URL,
		trace: 'retain-on-failure'
	},
	projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }]
});
