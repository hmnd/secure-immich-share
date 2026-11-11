<script lang="ts">
	import { mdiAlertCircleOutline, mdiCheck, mdiClose, mdiLoading } from '@mdi/js';
	import { online } from 'svelte/reactivity/window';
	import Icon from '#lib/components/Icon.svelte';
	import type { Banner } from '#lib/progress.ts';

	interface Props {
		banner: Banner;
		/** Opens the share sheet for a waiting batch. */
		onaction?: () => void;
		/** Hides a finished or failed outcome. */
		ondismiss: () => void;
	}

	let { banner, onaction, ondismiss }: Props = $props();

	const KEEP_OPEN = 'Keep this page open until it’s finished.';
	const OFFLINE = 'Waiting for a connection…';
	const RESUMES = 'Saving carries on by itself once the phone is back online.';

	const waitsOffline = $derived(
		banner.kind === 'working' && banner.resumes && online.current === false
	);
</script>

<div class={['banner', banner.kind]}>
	<div class="row">
		{#if banner.kind === 'working'}
			<span class="icon"><Icon path={mdiLoading} spin /></span>
		{:else if banner.kind === 'done'}
			<span class="icon"><Icon path={mdiCheck} /></span>
		{:else if banner.kind === 'failed'}
			<span class="icon"><Icon path={mdiAlertCircleOutline} /></span>
		{/if}
		<div class="text" role="status">
			<p class="title">{waitsOffline ? OFFLINE : banner.title}</p>
			{#if banner.kind === 'working'}
				<p class="detail">{waitsOffline ? RESUMES : KEEP_OPEN}</p>
			{:else if banner.kind === 'waiting'}
				<p class="detail">Tap the button, then choose “Save” in the menu that opens.</p>
			{:else if banner.kind === 'done' && banner.detail}
				<p class="detail">{banner.detail}</p>
			{/if}
		</div>
		{#if banner.kind === 'done' || banner.kind === 'failed'}
			<button type="button" class="close" aria-label="Close" onclick={ondismiss}>
				<Icon path={mdiClose} size={20} />
			</button>
		{/if}
	</div>
	{#if banner.kind === 'waiting'}
		<button type="button" class="btn primary action" onclick={onaction}>
			{banner.action}
		</button>
	{/if}
	{#if banner.kind === 'working' || banner.kind === 'waiting'}
		<progress value={banner.progress?.done} max={banner.progress?.total} aria-label="Progress"
		></progress>
	{/if}
</div>

<style>
	.banner {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
	}

	.row {
		display: flex;
		align-items: center;
		gap: var(--space-3);
	}

	.text {
		flex: 1;
		min-width: 0;
	}

	.title {
		font-weight: 600;
		overflow-wrap: anywhere;
	}

	.detail {
		color: var(--color-muted);
		font-size: 0.875rem;
	}

	.icon {
		display: grid;
		flex: none;
	}

	.working .icon,
	.done .icon {
		color: var(--color-accent);
	}

	.failed .icon,
	.failed .title {
		color: var(--color-danger);
	}

	.action {
		width: 100%;
		min-height: 3rem;
	}

	.close {
		display: grid;
		flex: none;
		place-items: center;
		width: 2.75rem;
		height: 2.75rem;
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

	progress {
		width: 100%;
		height: 0.5rem;
		border: 0;
		border-radius: 999px;
		background: var(--color-border);
		appearance: none;
		overflow: hidden;
	}

	progress::-webkit-progress-bar {
		background: var(--color-border);
	}

	progress::-webkit-progress-value {
		background: var(--color-accent);
		transition: width 0.3s ease;
	}

	progress::-moz-progress-bar {
		background: var(--color-accent);
	}

	@media (min-width: 40rem) {
		.action {
			width: auto;
			align-self: flex-start;
		}
	}

	@media (prefers-reduced-motion: reduce) {
		progress::-webkit-progress-value {
			transition: none;
		}
	}
</style>
