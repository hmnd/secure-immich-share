<script lang="ts">
	import {
		mdiCheck,
		mdiCheckboxBlankCircleOutline,
		mdiCheckboxMarkedCircle,
		mdiChevronLeft,
		mdiChevronRight,
		mdiClose,
		mdiDownload,
		mdiLoading,
		mdiMagnifyMinusOutline,
		mdiMagnifyPlusOutline,
		mdiMotionPauseOutline,
		mdiMotionPlayOutline,
		mdiReload
	} from '@mdi/js';
	import { flushSync } from 'svelte';
	import type { Attachment } from 'svelte/attachments';
	import { on } from 'svelte/events';
	import {
		getShareAsset,
		type LinkKind,
		type MediaVariant,
		type ShareItem
	} from '#lib/api/index.ts';
	import Icon from '#lib/components/Icon.svelte';
	import { download } from '#lib/download.ts';
	import { altText, formatDateTime } from '#lib/format.ts';
	import { ItemSave, type ItemSaveState } from '#lib/item-save.svelte.ts';
	import { Lazy } from '#lib/lazy.svelte.ts';
	import { LivePhoto } from '#lib/live-photo.svelte.ts';
	import { MediaLoader, NOT_AVAILABLE, type MediaState } from '#lib/media-loader.svelte.ts';
	import { fileName } from '#lib/saving.ts';
	import type { Selection } from '#lib/selection.svelte.ts';
	import { canSaveToPhone } from '#lib/share-sheet.ts';
	import { thumbHashToDataURL } from '#lib/thumbhash.ts';
	import { Zoom, type Hit } from '#lib/zoom.svelte.ts';

	interface Props {
		kind: LinkKind;
		ref: string;
		items: readonly ShareItem[];
		/** Null when nothing can be selected. */
		selection: Selection | null;
		index: number;
		onnavigate: (index: number) => void;
		onclose: () => void;
	}

	let { kind, ref, items, selection, index, onnavigate, onclose }: Props = $props();

	const SLIDE_GAP_PX = 16;
	const AXIS_SLOP_PX = 10;
	const COMMIT_FRACTION = 0.3;
	const FLING_PX_PER_MS = 0.4;
	const VELOCITY_WINDOW_MS = 100;
	const EDGE_RESISTANCE = 0.3;
	const SETTLE_MS = 250;

	interface Sample {
		x: number;
		t: number;
	}

	type Swiping = {
		kind: 'swiping';
		id: number;
		x: number;
		/** The track's offset when the finger caught it, before edge resistance. */
		start: number;
		samples: Sample[];
		offset: number;
	};

	/**
	 * A touch on the stage: undecided, swiping between photos, or ignored (vertical). Idle while
	 * the photo's zoom follows the pointers. A touch may catch the track mid-settle, holding it
	 * at `offset`.
	 */
	type Gesture =
		| { kind: 'idle' }
		| { kind: 'pending'; id: number; x: number; y: number; t: number; offset: number }
		| Swiping
		| { kind: 'vertical'; id: number; offset: number }
		| { kind: 'settling'; animation: Animation; offset: number };

	interface Slide {
		pos: -1 | 0 | 1;
		item: ShareItem;
	}

	interface ItemMedia {
		video: MediaLoader | null;
		live: LivePhoto | null;
		full: MediaLoader | null;
		download: MediaLoader | null;
		save: ItemSave | null;
	}

	type Stage =
		| { kind: 'image'; src: string; status: string | null }
		| { kind: 'video'; src: string }
		| { kind: 'live'; src: string; motion: string | null; status: string | null };

	interface Action {
		label: string;
		icon: string;
		disabled: boolean;
		busy?: boolean;
		/** Show the label beside the icon: the next tap matters. */
		hint?: boolean;
		/** A toggle's state. */
		pressed?: boolean;
	}

	let dialog: HTMLDialogElement | null = null;
	let viewport: HTMLDivElement | null = null;
	let track: HTMLDivElement | null = null;
	let gesture = $state.raw<Gesture>({ kind: 'idle' });
	/** The last pointer's type: hover styles are for mice only, as phones keep :hover after a tap. */
	let pointer = $state('');
	const offset = $derived(gesture.kind === 'idle' ? 0 : gesture.offset);

	const phone = canSaveToPhone();

	const item = $derived(items[index]);
	const slides = $derived(
		([-1, 0, 1] as const).flatMap((pos): Slide[] => {
			const neighbour = items[index + pos];
			return neighbour ? [{ pos, item: neighbour }] : [];
		})
	);
	const media = $derived(createMedia(kind, ref, item, index));
	/** Each photo opens fitted. */
	const zoom = $derived(
		new Zoom({
			ratio: item.ratio,
			area: () => viewport?.getBoundingClientRect() ?? null,
			onzoom: sharpen
		})
	);
	const zoomable = $derived(item.kind !== 'video');

	const detail = $derived.by(() => {
		const { id } = item;
		return new Lazy(() => getShareAsset(kind, ref, id));
	});

	function createMedia(kind: LinkKind, ref: string, item: ShareItem, index: number): ItemMedia {
		const loader = (variant: MediaVariant, auto = false) =>
			item.variants.includes(variant)
				? new MediaLoader({ kind, ref, id: item.id, variant }, auto)
				: null;
		const motion = item.kind === 'live' ? loader('video') : null;
		const full = item.kind === 'video' ? null : loader('full');
		const download = loader('download');
		const sources = [download, full].filter((l) => l !== null);
		return {
			video: item.kind === 'video' ? loader('video', true) : null,
			live: motion && new LivePhoto(motion),
			full,
			download,
			save: phone && download ? new ItemSave(sources, fileName(index)) : null
		};
	}

	function statusText(state: MediaState | undefined): string | null {
		if (!state) return NOT_AVAILABLE;
		switch (state.kind) {
			case 'idle':
			case 'ready':
				return null;
			case 'checking':
			case 'preparing':
				return 'Preparing…';
			case 'failed':
				return state.message;
			default:
				return state satisfies never;
		}
	}

	function action(state: MediaState, label: string, icon: string): Action {
		switch (state.kind) {
			case 'idle':
			case 'ready':
				return { label, icon, disabled: false };
			case 'checking':
			case 'preparing':
				return preparing(label);
			case 'failed':
				return failed(state.message, label, icon);
			default:
				return state satisfies never;
		}
	}

	function preparing(label: string): Action {
		return { label: `${label}: Preparing…`, icon: mdiLoading, disabled: true, busy: true };
	}

	function failed(message: string, label: string, icon: string): Action {
		return message === NOT_AVAILABLE
			? { label: `${label}: ${NOT_AVAILABLE}`, icon, disabled: true }
			: { label: `${label}: Retry`, icon: mdiReload, disabled: false };
	}

	const fullState = $derived(media.full?.state);
	const stillSrc = $derived(fullState?.kind === 'ready' ? fullState.url : item.previewUrl);

	const stage = $derived.by((): Stage => {
		switch (item.kind) {
			case 'image':
				return { kind: 'image', src: stillSrc, status: null };
			case 'video': {
				const state = media.video?.state;
				return state?.kind === 'ready'
					? { kind: 'video', src: state.url }
					: { kind: 'image', src: item.previewUrl, status: statusText(state) };
			}
			case 'live': {
				const live = media.live;
				if (live?.mode !== 'playing') {
					return { kind: 'live', src: stillSrc, motion: null, status: null };
				}
				const state = live.motion.state;
				return state.kind === 'ready'
					? { kind: 'live', src: stillSrc, motion: state.url, status: null }
					: { kind: 'live', src: stillSrc, motion: null, status: statusText(state) };
			}
			default:
				return item.kind satisfies never;
		}
	});

	function slideStage(slide: Slide): Stage {
		return slide.pos === 0 ? stage : { kind: 'image', src: slide.item.previewUrl, status: null };
	}

	const selectAction = $derived.by((): Action | null => {
		if (!selection?.selectable(index)) return null;
		const selected = selection.has(index);
		return {
			label: 'Select',
			icon: selected ? mdiCheckboxMarkedCircle : mdiCheckboxBlankCircleOutline,
			disabled: false,
			pressed: selected
		};
	});
	const counter = $derived(
		selection?.active
			? `${selection.count} selected · ${index + 1} / ${items.length}`
			: `${index + 1} / ${items.length}`
	);

	const zoomAction = $derived.by((): Action | null => {
		if (!zoomable) return null;
		if (!zoom.zoomed) return { label: 'Zoom in', icon: mdiMagnifyPlusOutline, disabled: false };
		const loading = fullState?.kind === 'checking' || fullState?.kind === 'preparing';
		return {
			label: 'Zoom out',
			icon: loading ? mdiLoading : mdiMagnifyMinusOutline,
			disabled: false,
			busy: loading
		};
	});
	function saveAction(state: ItemSaveState): Action {
		const label = 'Save to phone';
		switch (state.kind) {
			case 'idle':
				return { label, icon: mdiDownload, disabled: false };
			case 'preparing':
				return preparing(label);
			case 'ready':
				return { label: 'Tap to save', icon: mdiDownload, disabled: false, hint: true };
			case 'saved':
				return { label: 'Saved', icon: mdiCheck, disabled: false };
			case 'failed':
				return failed(state.message, label, mdiDownload);
			default:
				return state satisfies never;
		}
	}

	const downloadAction = $derived.by((): Action | null => {
		if (media.save) return saveAction(media.save.state);
		return media.download ? action(media.download.state, 'Download', mdiDownload) : null;
	});
	const liveAction = $derived.by((): Action | null => {
		const live = media.live;
		if (!live) return null;
		const state = live.motion.state;
		if (live.mode === 'playing' && state.kind === 'ready') {
			return { label: 'Stop live photo', icon: mdiMotionPauseOutline, disabled: false };
		}
		return action(state, 'Play live photo', mdiMotionPlayOutline);
	});

	const date = $derived(formatDateTime(detail.value?.takenAt ?? item.takenAt));
	const description = $derived(detail.value?.description ?? null);
	function background(item: ShareItem): string {
		const hash = item.thumbhash ? thumbHashToDataURL(item.thumbhash) : null;
		return [item.thumbUrl, hash]
			.filter(Boolean)
			.map((url) => `url("${url}")`)
			.join(', ');
	}

	const open: Attachment<HTMLDialogElement> = (node) => {
		dialog = node;
		node.showModal();
		return () => {
			dialog = null;
		};
	};

	/**
	 * Cancels the moves of the lightbox's own gestures. Otherwise Chrome may treat a swipe as a
	 * scroll, fling it on release and swallow the next tap (say, on Select) to stop the fling,
	 * and Safari may zoom the page on a pinch.
	 */
	const ownTouches: Attachment<HTMLElement> = (node) =>
		on(
			node,
			'touchmove',
			(event) => {
				const touch = event.changedTouches[0];
				const owned =
					event.touches.length > 1 ||
					zoom.active ||
					gesture.kind === 'swiping' ||
					(gesture.kind === 'pending' &&
						touch !== undefined &&
						Math.abs(touch.clientX - gesture.x) > Math.abs(touch.clientY - gesture.y));
				if (owned && event.cancelable) event.preventDefault();
			},
			{ passive: false }
		);

	const bindViewport: Attachment<HTMLDivElement> = (node) => {
		viewport = node;
		const off = on(
			node,
			'wheel',
			(event) => {
				if (zoomable) zoom.wheel(event);
			},
			{ passive: false }
		);
		return () => {
			off();
			viewport = null;
		};
	};

	const bindTrack: Attachment<HTMLDivElement> = (node) => {
		track = node;
		return () => {
			track = null;
			if (gesture.kind === 'settling') gesture.animation.cancel();
			gesture = { kind: 'idle' };
		};
	};

	function stop() {
		for (const loader of Object.values(media)) loader?.cancel();
	}

	function go(delta: number) {
		const next = index + delta;
		if (next < 0 || next >= items.length) return;
		halt();
		stop();
		onnavigate(next);
	}

	function close() {
		dialog?.close();
	}

	function handleClose() {
		stop();
		onclose();
	}

	function onkeydown(event: KeyboardEvent) {
		if (event.target instanceof HTMLMediaElement) return;
		if (event.key === 'ArrowLeft') {
			event.preventDefault();
			go(-1);
		} else if (event.key === 'ArrowRight') {
			event.preventDefault();
			go(1);
		}
	}

	/** Whether `event` belongs to the touch the gesture follows. */
	function follows(event: PointerEvent): boolean {
		return (
			(gesture.kind === 'pending' || gesture.kind === 'swiping' || gesture.kind === 'vertical') &&
			event.pointerId === gesture.id
		);
	}

	function hit(event: Event): Hit {
		const target = event.target instanceof Element ? event.target : null;
		if (target?.closest('button')) return 'button';
		return target?.closest('.frame') ? 'photo' : 'backdrop';
	}

	function onpointerdown(event: PointerEvent & { currentTarget: HTMLElement }) {
		// A finger catches the track mid-settle, to swipe on from where it is.
		const held = event.pointerType === 'touch' && event.isPrimary ? halt() : offset;
		if (gesture.kind === 'settling') return;
		if (gesture.kind !== 'swiping' && zoomable && zoom.down(event, hit(event))) {
			release(held);
			return;
		}
		if (zoom.active || event.pointerType !== 'touch') return;
		if (!event.isPrimary) {
			// A second finger that can't pinch: the swipe gives way.
			release(held);
			return;
		}
		const { pointerId: id, clientX: x, clientY: y, timeStamp: t } = event;
		gesture = { kind: 'pending', id, x, y, t, offset: held };
	}

	function onpointermove(event: PointerEvent & { currentTarget: HTMLElement }) {
		if (zoom.move(event) || !follows(event)) return;
		if (gesture.kind === 'pending') {
			const dx = event.clientX - gesture.x;
			const dy = event.clientY - gesture.y;
			if (Math.max(Math.abs(dx), Math.abs(dy)) < AXIS_SLOP_PX) return;
			if (Math.abs(dx) <= Math.abs(dy)) {
				gesture = { kind: 'vertical', id: gesture.id, offset: gesture.offset };
				return;
			}
			event.currentTarget.setPointerCapture(event.pointerId);
			const { id, x, t, offset } = gesture;
			const start = offset / give(offset);
			swipe({ kind: 'swiping', id, x, start, samples: [{ x, t }], offset }, event);
		} else if (gesture.kind === 'swiping') {
			swipe(gesture, event);
		}
	}

	/** How far the track follows the finger: less towards an end, where there's no photo. */
	function give(offset: number): number {
		return items[index + (offset < 0 ? 1 : -1)] ? 1 : EDGE_RESISTANCE;
	}

	function swipe(from: Swiping, event: PointerEvent) {
		const raw = from.start + event.clientX - from.x;
		gesture = {
			...from,
			samples: [
				...from.samples.filter((s) => event.timeStamp - s.t <= VELOCITY_WINDOW_MS),
				{ x: event.clientX, t: event.timeStamp }
			],
			offset: raw * give(raw)
		};
	}

	function onpointerup(event: PointerEvent) {
		if (zoom.up(event) || !follows(event)) return;
		if (gesture.kind === 'pending' || gesture.kind === 'vertical') {
			release(gesture.offset);
			return;
		}
		if (gesture.kind !== 'swiping' || !track) {
			gesture = { kind: 'idle' };
			return;
		}
		const { x, samples, offset } = gesture;
		const first = samples[0];
		const velocity = (event.clientX - first.x) / Math.max(1, event.timeStamp - first.t);
		// A flick goes its own way, even from a track caught on its way back.
		const flung =
			Math.abs(velocity) > FLING_PX_PER_MS && Math.sign(velocity) === Math.sign(event.clientX - x);
		const far = Math.abs(offset) > track.clientWidth * COMMIT_FRACTION;
		let delta: -1 | 0 | 1 = 0;
		if (flung) delta = velocity < 0 ? 1 : -1;
		else if (far) delta = offset < 0 ? 1 : -1;
		void settle(offset, items[index + delta] ? delta : 0);
	}

	function onpointercancel(event: PointerEvent) {
		if (zoom.up(event, true) || !follows(event)) return;
		if (gesture.kind === 'idle' || gesture.kind === 'settling') return;
		release(gesture.offset);
	}

	/** Lets go of the track, `offset` from rest: it slides back into place. */
	function release(offset: number) {
		if (offset === 0) gesture = { kind: 'idle' };
		else void settle(offset, 0);
	}

	/** Stops the track where it is mid-settle; returns its offset there. */
	function halt(): number {
		if (gesture.kind !== 'settling' || !track) return 0;
		const { m41 } = new DOMMatrixReadOnly(getComputedStyle(track).transform);
		gesture.animation.cancel();
		gesture = { kind: 'idle' };
		return m41;
	}

	/**
	 * Shows the photo `delta` away at once, then slides the track from where the finger left it
	 * into place. The bar's actions (such as Select) apply to the incoming photo meanwhile.
	 */
	async function settle(offset: number, delta: -1 | 0 | 1) {
		if (!track) return;
		// Where the finger left the incoming photo, relative to its resting place.
		const from = offset + delta * (track.clientWidth + SLIDE_GAP_PX);
		if (delta !== 0) {
			stop();
			onnavigate(index + delta);
		}
		const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
		const animation = track.animate(
			[{ transform: `translateX(${from}px)` }, { transform: 'translateX(0px)' }],
			{ duration: reduced ? 0 : SETTLE_MS, easing: 'cubic-bezier(0.2, 0, 0, 1)', fill: 'forwards' }
		);
		const settling: Gesture = { kind: 'settling', animation, offset: from };
		gesture = settling;
		// Lays the slides out around the new photo before the animation's first frame.
		flushSync();
		await animation.finished.catch(() => undefined);
		// Closed meanwhile: the track is gone and the gesture reset.
		if (gesture !== settling) return;
		gesture = { kind: 'idle' };
		flushSync();
		animation.cancel();
	}

	function onStageClick(event: MouseEvent) {
		if (!zoom.zoomed && hit(event) === 'backdrop') close();
	}

	function onStageDblClick(event: MouseEvent) {
		// Touches double tap through the zoom's own pointer handling. A zoomed photo captures the
		// mouse to pan, so its double clicks land on the stage.
		if (zoomable && pointer !== 'touch' && (zoom.zoomed || hit(event) === 'photo')) {
			zoom.toggle({ x: event.clientX, y: event.clientY });
		}
	}

	function toggleSelected() {
		selection?.toggle(index);
	}

	function toggleZoom() {
		zoom.toggle();
	}

	/** Loads the full-size photo for a closer look, unless there's none to be had. */
	function sharpen() {
		const full = media.full;
		if (!full) return;
		const { state } = full;
		if (state.kind === 'failed' && state.message === NOT_AVAILABLE) return;
		void full.start();
	}

	function toggleLive() {
		media.live?.toggle();
	}

	function save() {
		if (media.save) media.save.save();
		else void downloadFile();
	}

	async function downloadFile() {
		const loader = media.download;
		if (!loader) return;
		const state = await loader.start();
		if (state.kind === 'ready' && loader === media.download) download(state.url);
	}
</script>

<svelte:window {onkeydown} />

{#snippet actionButton(action: Action, onclick: () => void)}
	<button
		type="button"
		class={['icon', { hint: action.hint }]}
		aria-label={action.label}
		title={action.label}
		aria-busy={action.busy}
		aria-pressed={action.pressed}
		disabled={action.disabled}
		{onclick}
	>
		<Icon path={action.icon} spin={action.busy} />
		{#if action.hint}
			<span>{action.label}</span>
		{/if}
	</button>
{/snippet}

<dialog
	class="viewer"
	{@attach open}
	aria-label={altText(item)}
	data-pointer={pointer}
	onclose={handleClose}
	onpointerover={(event) => (pointer = event.pointerType)}
	onpointerdown={(event) => (pointer = event.pointerType)}
>
	<div class="viewer-bar top">
		<span class="counter">{counter}</span>
		<div class="actions">
			{#if selectAction}
				{@render actionButton(selectAction, toggleSelected)}
			{/if}
			{#if liveAction}
				{@render actionButton(liveAction, toggleLive)}
			{/if}
			{#if zoomAction}
				{@render actionButton(zoomAction, toggleZoom)}
			{/if}
			{#if downloadAction}
				{@render actionButton(downloadAction, save)}
			{/if}
			<button type="button" class="icon" aria-label="Close" title="Close" onclick={close}>
				<Icon path={mdiClose} />
			</button>
		</div>
	</div>

	<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
	<div
		class={['viewer-stage', 'stage', { zoomed: zoom.zoomed, panning: zoom.panning }]}
		{@attach bindViewport}
		{@attach ownTouches}
		onclick={onStageClick}
		ondblclick={onStageDblClick}
		{onpointerdown}
		{onpointermove}
		{onpointerup}
		{onpointercancel}
	>
		<div class="track" {@attach bindTrack} style:transform="translateX({offset}px)">
			{#each slides as slide (slide.item.id)}
				{@const current = slide.pos === 0}
				{@const shown = slideStage(slide)}
				<div
					class="slide"
					style:left="calc({slide.pos} * (100% + {SLIDE_GAP_PX}px))"
					aria-hidden={!current}
					inert={!current}
				>
					<div
						class={['frame', { smooth: current && zoom.smooth }]}
						style:--ratio={slide.item.ratio}
						style:background-image={background(slide.item)}
						style:translate={current ? `${zoom.x}px ${zoom.y}px` : undefined}
						style:scale={current ? zoom.scale : undefined}
					>
						{#if shown.kind === 'video'}
							<!-- svelte-ignore a11y_media_has_caption -->
							<video
								src={shown.src}
								poster={slide.item.previewUrl}
								controls
								playsinline
								autoplay
								onerror={() => media.video?.fail()}
							></video>
						{:else}
							<img src={shown.src} alt={altText(slide.item)} draggable="false" />
							{#if shown.kind === 'live' && shown.motion}
								<video
									src={shown.motion}
									muted
									autoplay
									playsinline
									onended={() => media.live?.still()}
									onerror={() => media.live?.still()}
								></video>
							{/if}
							{#if shown.status}
								<p class="status" role="status">{shown.status}</p>
							{/if}
						{/if}
					</div>
				</div>
			{/each}
		</div>
		<button
			type="button"
			class="icon nav prev"
			aria-label="Previous"
			disabled={index === 0}
			onclick={() => go(-1)}
		>
			<Icon path={mdiChevronLeft} size={32} />
		</button>
		<button
			type="button"
			class="icon nav next"
			aria-label="Next"
			disabled={index === items.length - 1}
			onclick={() => go(1)}
		>
			<Icon path={mdiChevronRight} size={32} />
		</button>
	</div>

	<div class="viewer-bar bottom">
		<div class="info">
			{#if date}
				<p class="date">{date}</p>
			{/if}
			{#if description}
				<p class="description">{description}</p>
			{/if}
		</div>
	</div>
</dialog>

<style>
	/* The look comes from the page's viewer styles (pages.css); these add the dialog and swiping. */
	dialog {
		width: 100%;
		max-width: none;
		height: 100%;
		max-height: none;
		margin: 0;
		padding: 0;
		border: 0;
	}

	dialog:not([open]) {
		display: none;
	}

	dialog::backdrop {
		background: #000;
	}

	.icon:disabled {
		cursor: default;
	}

	.icon:disabled:not([aria-busy='true']) {
		opacity: 0.5;
	}

	.icon[aria-pressed='true'] {
		color: var(--color-accent);
	}

	.icon.hint {
		grid-auto-flow: column;
		gap: var(--space-1);
		width: auto;
		padding: 0 var(--space-3) 0 var(--space-2);
		background: var(--color-accent);
		color: var(--color-on-accent);
		font-size: 0.875rem;
		font-weight: 500;
		white-space: nowrap;
	}

	.nav:disabled {
		visibility: hidden;
	}

	.stage {
		touch-action: none;
		user-select: none;
		-webkit-user-select: none;
	}

	.stage.zoomed {
		cursor: grab;
	}

	.stage.panning {
		cursor: grabbing;
	}

	.track {
		position: absolute;
		inset: 0;
		will-change: transform;
	}

	.slide {
		position: absolute;
		top: 0;
		width: 100%;
		height: 100%;
		display: grid;
		place-items: center;
	}

	.frame {
		background-position: center;
		background-repeat: no-repeat;
		background-size: cover;
	}

	.frame.smooth {
		transition:
			scale 250ms cubic-bezier(0.2, 0, 0, 1),
			translate 250ms cubic-bezier(0.2, 0, 0, 1);
	}

	@media (prefers-reduced-motion: reduce) {
		.frame.smooth {
			transition: none;
		}
	}

	.status {
		position: absolute;
		top: 50%;
		left: 50%;
		padding: var(--space-1) var(--space-3);
		border-radius: 999px;
		background: var(--color-overlay);
		font-size: 0.875rem;
		white-space: nowrap;
		transform: translate(-50%, -50%);
	}
</style>
