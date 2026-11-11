import { ApiError } from './client.ts';

export function errorMessage(err: unknown): string {
	if (err instanceof ApiError) {
		if (err.code === 'timeout') return 'The server took too long to respond';
		if (err.status === 0) return 'Network unavailable';
		return err.message || err.code;
	}
	return err instanceof Error ? err.message : 'Unknown error';
}

export const isNotFound = (err: unknown) => err instanceof ApiError && err.status === 404;
