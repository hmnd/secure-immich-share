<script lang="ts">
	import { mdiClose } from '@mdi/js';
	import { cubicOut } from 'svelte/easing';
	import { MediaQuery } from 'svelte/reactivity';
	import { fly } from 'svelte/transition';
	import Icon from '#lib/components/Icon.svelte';
	import ProgressBanner from '#lib/components/ProgressBanner.svelte';
	import { holdScroll } from '#lib/hold-scroll.ts';
	import type { PhotoView } from '#lib/photo-view.svelte.ts';
	import { fileBanner, saveBanner, zipBanner, type Banner } from '#lib/progress.ts';
	import type { SaveJob } from '#lib/save-job.ts';
	import type { DownloadState, SelectionActions } from '#lib/selection-actions.svelte.ts';

	interface Props {
		/** Null when nothing in the share can be selected. */
		actions: SelectionActions | null;
		/** Saving to the phone, from the header or the selection; null when this device can't. */
		saveJob: SaveJob | null;
		/** The bar waits for the viewer to close, so it slides in where it can be seen. */
		photos: PhotoView;
	}

	let { actions, saveJob, photos }: Props = $props();

	function downloadBanner(download: DownloadState): Banner | null {
		switch (download.kind) {
			case 'none':
				return null;
			case 'zip':
				return zipBanner(download.state);
			case 'file':
				return fileBanner(download.state);
			default:
				return download satisfies never;
		}
	}

	const count = $derived(actions?.selection.count ?? 0);
	const saving = $derived(saveJob && saveBanner(saveJob));
	const downloading = $derived(actions && downloadBanner(actions.download));
	const busy = $derived(actions ? actions.busy : (saveJob?.busy ?? false));
	const shown = $derived(photos.isClosed && (count > 0 || saving !== null || downloading !== null));

	const reducedMotion = new MediaQuery('(prefers-reduced-motion: reduce)');
	/** Small screens dock the bar at the bottom, within thumb reach. */
	const atBottom = new MediaQuery('(max-width: 39.99rem)');

	function onbeforeunload(event: BeforeUnloadEvent) {
		if (busy) event.preventDefault();
	}
</script>

<svelte:window {onbeforeunload} />

<!-- Always there, so holdScroll keeps the page still as the sheet comes and goes at the top. -->
<div class="top-bar" {@attach !atBottom.current && holdScroll}>
	{#if shown}
		<div
			class="sheet"
			in:fly={{
				y: atBottom.current ? '100%' : '-100%',
				opacity: 1,
				duration: reducedMotion.current ? 0 : 200,
				easing: cubicOut
			}}
		>
			{#if actions && count > 0}
				<div class="part selection">
					<button
						type="button"
						class="close"
						aria-label="Clear selection"
						title="Clear selection"
						onclick={() => actions.selection.clear()}
					>
						<Icon path={mdiClose} size={20} />
					</button>
					<p class="count" role="status">{count} selected</p>
					<div class="buttons">
						<button
							type="button"
							class={['btn', !saveJob && 'primary']}
							disabled={busy}
							onclick={() => actions.startDownload()}
						>
							{actions.downloadLabel}
						</button>
						{#if saveJob}
							<button
								type="button"
								class="btn primary"
								disabled={busy}
								onclick={() => actions.save()}
							>
								Save to phone
							</button>
						{/if}
					</div>
				</div>
			{/if}
			{#if saveJob && saving}
				<div class="part">
					<ProgressBanner
						banner={saving}
						onaction={() => saveJob.save()}
						ondismiss={() => saveJob.dismiss()}
					/>
				</div>
			{/if}
			{#if actions && downloading}
				<div class="part">
					<ProgressBanner banner={downloading} ondismiss={() => actions.dismissDownload()} />
				</div>
			{/if}
		</div>
	{/if}
</div>

<style>
	.top-bar {
		position: sticky;
		top: 0;
		z-index: 10;
	}

	.sheet {
		display: flex;
		flex-direction: column;
		padding-top: env(safe-area-inset-top);
		border-bottom: 1px solid var(--color-border);
		background: var(--color-surface);
		box-shadow: 0 2px 8px rgb(0 0 0 / 0.15);
	}

	.part {
		padding: var(--space-3) var(--space-4);
	}

	.part + .part {
		border-top: 1px solid var(--color-border);
	}

	.selection {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2) var(--space-3);
	}

	.count {
		flex: 1;
		font-weight: 600;
		white-space: nowrap;
	}

	.buttons {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.close {
		display: grid;
		flex: none;
		place-items: center;
		width: 2.75rem;
		height: 2.75rem;
		margin: calc(-1 * var(--space-2)) 0;
		padding: 0;
		border: 0;
		border-radius: 999px;
		background: transparent;
		color: var(--color-muted);
		cursor: pointer;
	}

	.close:hover {
		background: var(--color-border);
	}

	@media (max-width: 39.99rem) {
		/* Last in the body's column, after the footer, so it sticks to the bottom on any page length. */
		.top-bar {
			top: auto;
			bottom: 0;
			order: 1;
		}

		.sheet {
			padding-top: 0;
			padding-bottom: env(safe-area-inset-bottom);
			border-top: 1px solid var(--color-border);
			border-bottom: 0;
			box-shadow: 0 -2px 8px rgb(0 0 0 / 0.15);
		}

		.buttons {
			flex: 1 1 100%;
		}

		.buttons .btn {
			flex: 1;
		}
	}

	@media (min-width: 40rem) {
		.part {
			padding-inline: var(--space-6);
		}
	}
</style>
