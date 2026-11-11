import { errorMessage } from '#lib/api/index.ts';
import { download } from '#lib/download.ts';
import { NOT_AVAILABLE, type MediaLoader } from '#lib/media-loader.svelte.ts';
import { hasActivation, loadShareable, shareFiles, shareLimits } from '#lib/share-sheet.ts';

export type ItemSaveState =
	| { kind: 'idle' }
	| { kind: 'preparing' }
	/** In memory, waiting for a tap: the one that started it expired. */
	| { kind: 'ready'; file: File; url: string }
	| { kind: 'saved' }
	| { kind: 'failed'; message: string };

type State =
	Exclude<ItemSaveState, { kind: 'preparing' }> | { kind: 'preparing'; abort: AbortController };

type Ready = Extract<State, { kind: 'ready' }>;

/** Saves one item through the share sheet, or downloads it when the sheet won't take it. */
export class ItemSave {
	readonly #loaders: MediaLoader[];
	readonly #name: string;
	#state = $state.raw<State>({ kind: 'idle' });

	constructor(loaders: MediaLoader[], name: string) {
		this.#loaders = loaders;
		this.#name = name;
	}

	get state(): ItemSaveState {
		return this.#state;
	}

	/** Call from a tap handler. */
	save(): void {
		const state = this.#state;
		switch (state.kind) {
			case 'ready':
				this.#share(state);
				return;
			case 'preparing':
				return;
			case 'idle':
			case 'saved':
			case 'failed':
				break;
			default:
				state satisfies never;
		}
		const abort = new AbortController();
		this.#state = { kind: 'preparing', abort };
		const variants = this.#loaders.map((loader) => (signal: AbortSignal) => loader.prepare(signal));
		void loadShareable(variants, this.#name, shareLimits(), abort.signal).then(
			(loaded) => {
				if (!this.#preparing(abort)) return;
				switch (loaded.kind) {
					case 'file': {
						const ready: Ready = { kind: 'ready', file: loaded.file, url: loaded.url };
						this.#state = ready;
						if (hasActivation()) this.#share(ready);
						break;
					}
					case 'download':
						download(loaded.url);
						this.#state = { kind: 'idle' };
						break;
					case 'unavailable':
						this.#state = { kind: 'failed', message: NOT_AVAILABLE };
						break;
					default:
						loaded satisfies never;
				}
			},
			(err: unknown) => {
				if (this.#preparing(abort)) this.#state = { kind: 'failed', message: errorMessage(err) };
			}
		);
	}

	cancel(): void {
		const state = this.#state;
		if (state.kind === 'preparing') state.abort.abort();
		if (state.kind === 'preparing' || state.kind === 'ready') this.#state = { kind: 'idle' };
	}

	#preparing(abort: AbortController): boolean {
		const state = this.#state;
		return state.kind === 'preparing' && state.abort === abort;
	}

	#share(ready: Ready): void {
		void shareFiles([ready.file]).then((outcome) => {
			if (this.#state !== ready) return;
			switch (outcome) {
				case 'shared':
					this.#state = { kind: 'saved' };
					break;
				case 'dismissed':
					break;
				case 'failed':
					download(ready.url);
					this.#state = { kind: 'idle' };
					break;
				default:
					outcome satisfies never;
			}
		});
	}
}
