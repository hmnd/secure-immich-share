<script lang="ts">
	import type { SaveActions } from '#lib/save-actions.svelte.ts';

	interface Props {
		/** The chooser and progress live in the top bar; this button only starts a save. */
		actions: SaveActions;
	}

	let { actions }: Props = $props();
</script>

{#if actions.available('all')}
	<button
		type="button"
		class="btn primary"
		disabled={actions.busy}
		onclick={() => actions.save('all')}
	>
		{actions.busy ? 'Saving…' : actions.label('all')}
	</button>
{/if}
