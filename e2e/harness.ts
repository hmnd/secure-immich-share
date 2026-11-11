import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const E2E_DIR = dirname(fileURLToPath(import.meta.url));
export const ROOT = dirname(E2E_DIR);
export const SERVER_DIR = join(ROOT, 'server');
export const ARTIFACTS = join(E2E_DIR, '.artifacts');
export const SERVER_BIN = join(ROOT, 'bin', 'secure-immich-share');
/** The fake Immich's Go package, relative to server/. */
export const FAKE_IMMICH_PKG = './tests/cmd/fake-immich';
export const FAKE_IMMICH_BIN = join(ARTIFACTS, 'fake-immich');

export const PORT = Number(process.env.E2E_PORT ?? 4780);
export const IMMICH_PORT = Number(process.env.E2E_IMMICH_PORT ?? 4781);
/** PUBLIC_URL: API mutations must come from exactly this origin. */
export const BASE_URL = `http://localhost:${PORT}`;
export const IMMICH_URL = `http://127.0.0.1:${IMMICH_PORT}`;
