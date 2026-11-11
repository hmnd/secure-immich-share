import type { MediaLoader } from '#lib/media-loader.svelte.ts';

/** A live photo's motion clip: still until played, and still again once it stops or ends. */
export class LivePhoto {
	readonly motion: MediaLoader;
	#mode = $state<'still' | 'playing'>('still');

	constructor(motion: MediaLoader) {
		this.motion = motion;
	}

	get mode(): 'still' | 'playing' {
		return this.#mode;
	}

	/** Plays the clip, preparing it first if needed; stops it while it plays. */
	toggle(): void {
		if (this.#mode === 'playing' && this.motion.state.kind === 'ready') {
			this.#mode = 'still';
			return;
		}
		this.#mode = 'playing';
		void this.motion.start();
	}

	still(): void {
		this.#mode = 'still';
	}

	cancel(): void {
		this.motion.cancel();
		this.#mode = 'still';
	}
}
