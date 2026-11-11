import { test as base, expect, type ConsoleMessage } from '@playwright/test';
import { BROKEN_ID } from './immich.ts';

/** Logged for each CSP violation by the init script below. */
export const CSP_VIOLATION = 'CSP violation:';

/** The broken fixture's media fails verification on purpose, so its failed loads are expected. */
function expected(msg: ConsoleMessage): boolean {
	const text = msg.text();
	return text.startsWith('Failed to load resource') && msg.location().url.includes(BROKEN_ID);
}

/** Fails any test whose page logs an error, violates the CSP or throws. */
export const test = base.extend<{ consoleErrors: string[] }>({
	consoleErrors: [
		async ({ page }, use) => {
			const errors: string[] = [];
			page.on('console', (msg) => {
				if (msg.type() === 'error' && !expected(msg)) {
					errors.push(`${msg.text()} (${msg.location().url})`);
				}
			});
			page.on('pageerror', (err) => errors.push(`uncaught: ${err.message}`));
			await page.addInitScript((prefix) => {
				document.addEventListener('securitypolicyviolation', (e) => {
					console.error(`${prefix} ${e.violatedDirective} ${e.blockedURI} at ${e.sourceFile}`);
				});
			}, CSP_VIOLATION);
			await use(errors);
			expect(errors, 'console errors').toEqual([]);
		},
		{ auto: true }
	]
});

export { expect };
