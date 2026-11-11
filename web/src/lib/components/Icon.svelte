<script lang="ts">
	interface Props {
		path: string;
		size?: number;
		spin?: boolean;
	}

	let { path, size = 24, spin = false }: Props = $props();
</script>

<svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true" class={{ spin }}>
	<path d={path} />
</svg>

<style>
	svg {
		flex: none;
		fill: currentColor;
	}

	.spin {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(1turn);
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.spin {
			animation-duration: 3s;
		}
	}
</style>
