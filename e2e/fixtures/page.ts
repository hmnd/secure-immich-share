import { expect, type Page } from '@playwright/test';
import { BASE_URL } from '../harness.ts';
import { PUBLIC_PATH } from './immich.ts';

/** Opens the public gallery and waits for the islands to lay out the grid. */
export async function openGallery(page: Page): Promise<void> {
	await page.goto(PUBLIC_PATH);
	await expect(page.locator('.grid.measured')).toBeVisible();
}

export const galleryURL = () => `${BASE_URL}${PUBLIC_PATH}`;
export const photoURL = (id: string) => `${BASE_URL}${PUBLIC_PATH}?photo=${id}`;
