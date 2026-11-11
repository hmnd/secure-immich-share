import { createSubscriber } from 'svelte/reactivity';

/** A value loaded the first time it's read reactively; undefined until then or if loading fails. */
export class Lazy<T> {
	#value = $state.raw<T>();
	#subscribe: () => void;

	constructor(load: () => Promise<T>) {
		let started = false;
		this.#subscribe = createSubscriber(() => {
			if (started) return;
			started = true;
			load().then(
				(value) => (this.#value = value),
				() => {}
			);
		});
	}

	get value(): T | undefined {
		this.#subscribe();
		return this.#value;
	}
}
