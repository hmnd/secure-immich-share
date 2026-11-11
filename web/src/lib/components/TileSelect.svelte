<script lang="ts">
	import { mdiCheckboxBlankCircleOutline, mdiCheckCircle, mdiMagnifyPlusOutline } from '@mdi/js';
	import type { Attachment } from 'svelte/attachments';
	import Icon from '#lib/components/Icon.svelte';
	import type { Selection } from '#lib/selection.svelte.ts';

	interface Props {
		selection: Selection;
		/** The tile's share position. */
		index: number;
		onopen: (index: number) => void;
	}

	let { selection, index, onopen }: Props = $props();

	const selected = $derived(selection.has(index));
	const candidate = $derived(selection.candidate(index));
	const tile = $derived({
		selectable: true,
		selecting: selection.active,
		selected,
		candidate
	});

	/** Reflects `classes` on the tile the controls sit in (server-rendered markup). */
	function parentClasses(classes: Record<string, boolean>): Attachment<HTMLElement> {
		return (node) => {
			const parent = node.parentElement;
			if (!parent) return;
			for (const [name, on] of Object.entries(classes)) parent.classList.toggle(name, on);
			return () => parent.classList.remove(...Object.keys(classes));
		};
	}
</script>

<button
	type="button"
	class="select"
	aria-label="Select photo {index + 1}"
	aria-pressed={selected}
	onclick={(event) => selection.pick(index, event.shiftKey)}
	{@attach parentClasses(tile)}
>
	<Icon path={selected || candidate ? mdiCheckCircle : mdiCheckboxBlankCircleOutline} />
</button>
{#if selection.active}
	<button
		type="button"
		class="zoom"
		aria-label="Open photo {index + 1}"
		title="Open"
		onclick={() => onopen(index)}
	>
		<Icon path={mdiMagnifyPlusOutline} />
	</button>
{/if}
