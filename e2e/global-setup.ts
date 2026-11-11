import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import {
	createWriteStream,
	existsSync,
	mkdirSync,
	readdirSync,
	readFileSync,
	statSync
} from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, relative } from 'node:path';
import {
	ARTIFACTS,
	BASE_URL,
	FAKE_IMMICH_BIN,
	FAKE_IMMICH_PKG,
	IMMICH_PORT,
	IMMICH_URL,
	PORT,
	ROOT,
	SERVER_BIN,
	SERVER_DIR
} from './harness.ts';

const BUILD_INPUTS = [
	'server',
	'web/src',
	'web/package.json',
	'web/pnpm-lock.yaml',
	'web/vite.config.ts'
];
const IGNORED = new Set(['node_modules', 'testdata', 'dist', 'tests']);
const START_TIMEOUT_MS = 120_000;

function newestMtime(path: string): number {
	const stat = statSync(path, { throwIfNoEntry: false });
	if (!stat) return 0;
	if (!stat.isDirectory()) return stat.mtimeMs;
	let newest = 0;
	for (const entry of readdirSync(path)) {
		if (IGNORED.has(entry)) continue;
		newest = Math.max(newest, newestMtime(join(path, entry)));
	}
	return newest;
}

function run(cmd: string, args: string[], cwd = ROOT) {
	execFileSync(cmd, args, { cwd, stdio: 'inherit' });
}

/** Builds the binary unless it is newer than every source it embeds (E2E_SKIP_BUILD=1 skips). */
function build() {
	if (process.env.E2E_SKIP_BUILD !== '1') {
		const built = statSync(SERVER_BIN, { throwIfNoEntry: false })?.mtimeMs ?? 0;
		const newest = Math.max(...BUILD_INPUTS.map((p) => newestMtime(join(ROOT, p))));
		if (built < newest) {
			if (!existsSync(join(ROOT, 'web', 'node_modules'))) run('just', ['web-install']);
			run('just', ['build']);
		}
	}
	// Go's build cache makes this quick when nothing changed.
	run('mise', ['exec', '--', 'go', 'build', '-o', FAKE_IMMICH_BIN, FAKE_IMMICH_PKG], SERVER_DIR);
}

interface Proc {
	child: ChildProcess;
	log: string;
	exited: Promise<number | null>;
}

function start(bin: string, args: string[], env: NodeJS.ProcessEnv, name: string): Proc {
	const log = join(ARTIFACTS, `${name}.log`);
	const out = createWriteStream(log);
	const child = spawn(bin, args, { env, stdio: ['ignore', 'pipe', 'pipe'] });
	child.stdout?.pipe(out);
	child.stderr?.pipe(out);
	const exited = new Promise<number | null>((resolve) => child.once('exit', resolve));
	return { child, log, exited };
}

/** Polls `url` until `ready` accepts the response; fails early if `proc` exits. */
async function waitFor(proc: Proc, url: string, ready: (res: Response) => boolean) {
	let exited = false;
	void proc.exited.then(() => (exited = true));
	const deadline = Date.now() + START_TIMEOUT_MS;
	while (Date.now() < deadline) {
		if (exited) {
			throw new Error(`${proc.log}:\n${readFileSync(proc.log, 'utf8').slice(-4000)}`);
		}
		try {
			if (ready(await fetch(url, { signal: AbortSignal.timeout(2000) }))) return;
		} catch {
			// not listening yet
		}
		await new Promise((resolve) => setTimeout(resolve, 200));
	}
	throw new Error(`timed out waiting for ${url}; see ${relative(ROOT, proc.log)}`);
}

async function stop(proc: Proc) {
	if (proc.child.exitCode !== null || proc.child.signalCode !== null) return;
	proc.child.kill('SIGTERM');
	const killed = setTimeout(() => proc.child.kill('SIGKILL'), 10_000);
	await proc.exited;
	clearTimeout(killed);
}

export default async function globalSetup(): Promise<() => Promise<void>> {
	mkdirSync(ARTIFACTS, { recursive: true });
	build();

	const cacheDir = await mkdtemp(join(tmpdir(), 'sis-e2e-'));
	const env: NodeJS.ProcessEnv = {
		...process.env,
		IMMICH_URL,
		PUBLIC_URL: BASE_URL,
		COOKIE_KEY: randomBytes(32).toString('base64'),
		CACHE_DIR: cacheDir,
		LISTEN_ADDR: `127.0.0.1:${PORT}`
	};

	const procs: Proc[] = [];
	const teardown = async () => {
		await Promise.all(procs.reverse().map(stop));
		await rm(cacheDir, { recursive: true, force: true });
	};
	try {
		const immich = start(
			FAKE_IMMICH_BIN,
			['-addr', `127.0.0.1:${IMMICH_PORT}`],
			env,
			'fake-immich'
		);
		procs.push(immich);
		await waitFor(immich, `${IMMICH_URL}/`, () => true);

		const server = start(SERVER_BIN, ['serve'], env, 'server');
		procs.push(server);
		await waitFor(server, `${BASE_URL}/healthz`, (res) => res.ok);
	} catch (err) {
		await teardown();
		throw err;
	}
	return teardown;
}
