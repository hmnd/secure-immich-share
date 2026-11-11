<script lang="ts">
	import { plural } from '#lib/format.ts';
	import type { SaveJob } from '#lib/save-job.ts';
	import type { ZipJob, ZipState } from '#lib/zip-job.svelte.ts';

	interface Props {
		/** Null when the share can't be downloaded as a zip. */
		zip: ZipJob | null;
		/** Progress and the share sheet button live in the top bar; this button only starts it. */
		saveJob: SaveJob | null;
	}

	let { zip, saveJob }: Props = $props();

	interface ZipView {
		label: string;
		busy: boolean;
		note: string | null;
	}

	function zipView(state: ZipState, idle: string): ZipView {
		switch (state.kind) {
			case 'idle':
				return { label: idle, busy: false, note: null };
			case 'starting':
				return { label: 'Preparing…', busy: true, note: null };
			case 'waiting':
				return { label: 'Waiting…', busy: true, note: null };
			case 'preparing':
				return { label: `Preparing ${state.done}/${state.total}`, busy: true, note: null };
			case 'ready':
				return {
					label: idle,
					busy: false,
					note: state.skipped > 0 ? `${plural(state.skipped, 'item', 'items')} skipped` : null
				};
			case 'failed':
				return { label: idle, busy: false, note: state.message };
			default:
				return state satisfies never;
		}
	}

	const download = $derived(zip ? zipView(zip.state, 'Download all') : null);
</script>

{#if saveJob || download}
	<div class="buttons">
		{#if download}
			<button
				type="button"
				class={['btn', !saveJob && 'primary']}
				disabled={download.busy}
				onclick={() => zip?.start()}
			>
				{download.label}
			</button>
		{/if}
		{#if saveJob}
			<button
				type="button"
				class="btn primary"
				disabled={saveJob.busy}
				onclick={() => saveJob.save()}
			>
				{saveJob.busy ? 'Saving…' : 'Save all'}
			</button>
		{/if}
	</div>
	{#if download?.note}
		<p class="note" role="status">{download.note}</p>
	{/if}
{/if}

<style>
	.buttons {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: var(--space-2);
	}

	.note {
		color: var(--color-muted);
		font-size: 0.875rem;
	}
</style>
