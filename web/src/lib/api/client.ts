import { ZodError, z } from 'zod';
import { client } from './generated/client.gen.ts';

// zod's JIT uses `new Function`, which `script-src 'self'` blocks. Schemas
// decide at construction, so this must run before zod.gen.ts is evaluated
// (index.ts imports this module before the SDK).
z.config({ jitless: true });

export class ApiError extends Error {
	readonly status: number;
	readonly code: string;
	/** `Retry-After` in seconds (429/503), when the server sent one. */
	readonly retryAfter: number | null;

	constructor(status: number, code: string, message: string, retryAfter: number | null = null) {
		super(message);
		this.name = 'ApiError';
		this.status = status;
		this.code = code;
		this.retryAfter = retryAfter;
	}
}

const REQUEST_TIMEOUT_MS = 15_000;

const timeoutError = () => new ApiError(0, 'timeout', 'Request timed out');

/** `fetch` that gives up after `ms` (including reading the body) with `ApiError(0, 'timeout')`. */
function timedFetch(ms: number): typeof fetch {
	return async (input, init) => {
		const timeout = AbortSignal.timeout(ms);
		const signals = [
			init?.signal ?? (input instanceof Request ? input.signal : null),
			timeout
		].filter((s): s is AbortSignal => !!s);
		try {
			return await fetch(input, { ...init, signal: AbortSignal.any(signals) });
		} catch (err) {
			if (timeout.aborted) throw timeoutError();
			throw err;
		}
	};
}

client.setConfig({
	baseUrl: '',
	throwOnError: true,
	credentials: 'same-origin',
	fetch: timedFetch(REQUEST_TIMEOUT_MS)
});

const requestInterceptor = client.interceptors.request.use((request) => {
	if (!request.headers.has('Accept')) request.headers.set('Accept', 'application/json');
	return request;
});

/** The single place API failures become `ApiError`s. */
const errorInterceptor = client.interceptors.error.use((error, response) => {
	if (error instanceof ApiError) return error;
	if (error instanceof DOMException && error.name === 'TimeoutError') return timeoutError();
	if (error instanceof DOMException && error.name === 'AbortError') return error;
	if (!response) {
		return new ApiError(
			0,
			'network_error',
			error instanceof Error ? error.message : 'Network error'
		);
	}
	if (error instanceof ZodError) {
		console.error('[api] invalid_response', response.url, error.issues);
		return new ApiError(response.status, 'invalid_response', 'Unexpected response from the server');
	}
	const retry = Number(response.headers.get('Retry-After'));
	const retryAfter = Number.isFinite(retry) && retry > 0 ? retry : null;
	const body = (error ?? {}) as { error?: { code?: unknown; message?: unknown } };
	const { code, message } = body.error ?? {};
	if (typeof code === 'string') {
		return new ApiError(
			response.status,
			code,
			typeof message === 'string' ? message : code,
			retryAfter
		);
	}
	return new ApiError(
		response.status,
		`http_${response.status}`,
		response.statusText || `HTTP ${response.status}`,
		retryAfter
	);
});

type ResponseValidator = (data: unknown) => Promise<unknown>;

const reportedDrift = new Set<string>();

/**
 * Contract drift is strict in dev (the ZodError becomes an `invalid_response`
 * ApiError) and tolerant in prod: the issue is logged once per operation and
 * the unvalidated data is used (validators never change the data).
 */
function tolerant(validator: ResponseValidator | undefined, key: string) {
	if (!validator || import.meta.env.DEV) return validator;
	return async (data: unknown) => {
		try {
			await validator(data);
		} catch (err) {
			if (!(err instanceof ZodError)) throw err;
			if (!reportedDrift.has(key)) {
				reportedDrift.add(key);
				console.error('[api] invalid_response', key, err.issues);
			}
		}
	};
}

// The generated SDK passes its zod validator to these methods, looked up on
// the client at call time.
const methods = ['get', 'post'] as const;
const originals = methods.map((m) => client[m]);
for (const method of methods) {
	const send = client[method];
	client[method] = ((options: Parameters<typeof send>[0]) =>
		send({
			...options,
			responseValidator: tolerant(
				options.responseValidator as ResponseValidator | undefined,
				`${method.toUpperCase()} ${options.url}`
			)
		})) as typeof send;
}

import.meta.hot?.dispose(() => {
	client.interceptors.request.eject(requestInterceptor);
	client.interceptors.error.eject(errorInterceptor);
	methods.forEach((m, i) => (client[m] = originals[i] as never));
});
