<script lang="ts">
	import type { LinkKind } from '#lib/api/index.ts';
	import Lightbox from '#lib/components/Lightbox.svelte';
	import type { PhotoView } from '#lib/photo-view.svelte.ts';
	import type { Selection } from '#lib/selection.svelte.ts';

	interface Props {
		kind: LinkKind;
		ref: string;
		photos: PhotoView;
		/** Null when nothing can be selected. */
		selection: Selection | null;
	}

	let { kind, ref, photos, selection }: Props = $props();

	const view = $derived(photos.current);
</script>

<svelte:window onpopstate={() => photos.popstate()} />

{#if view.kind === 'open'}
	<Lightbox
		{kind}
		{ref}
		items={photos.items}
		{selection}
		index={view.index}
		onnavigate={(index) => photos.show(index)}
		onclose={() => photos.close()}
	/>
{/if}
