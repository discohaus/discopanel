<script lang="ts" module>
	let instance = 0;
</script>

<script lang="ts">
	import { cn } from '$lib/utils';

	let { class: className = '', spotlight = false }: { class?: string; spotlight?: boolean } =
		$props();

	// Unique ids keep multiple logos from colliding
	const uid = ++instance;
	const clipId = `disco-chip-${uid}`;

	// Grass block sprite rows from the discohaus identity system
	const sprite = [
		['#3c7f3c', '#55aa55', '#3c7f3c', '#55aa55'],
		['#55aa55', '#6c472a', '#55aa55', '#6c472a'],
		['#6c472a', '#8b5e3c', '#6c472a', '#8b5e3c'],
		['#8b5e3c', '#6c472a', '#8b5e3c', '#6c472a']
	];
</script>

<svg viewBox="0 0 160 160" fill="none" class={cn('shrink-0', className)} aria-hidden="true">
	<defs>
		<clipPath id={clipId}>
			<rect x="20" y="20" width="120" height="120" rx="24" />
		</clipPath>
	</defs>
	<!-- Four pins per side in the identity pin color -->
	<g class="pins">
		{#each [44, 68, 92, 116] as c (c)}
			<rect x={c - 6} y="6" width="12" height="20" rx="4" />
			<rect x={c - 6} y="134" width="12" height="20" rx="4" />
			<rect x="6" y={c - 6} width="20" height="12" rx="4" />
			<rect x="134" y={c - 6} width="20" height="12" rx="4" />
		{/each}
	</g>
	<!-- Chip body stays ink in both themes -->
	<rect class="chip" x="20" y="20" width="120" height="120" rx="24" />
	<g shape-rendering="crispEdges">
		{#each sprite as row, y (y)}
			{#each row as fill, x (x)}
				<rect x={40 + x * 20} y={40 + y * 20} width="20" height="20" {fill} />
			{/each}
		{/each}
	</g>
	{#if spotlight}
		<!-- Brand beams drift over the chip face -->
		<g clip-path="url(#{clipId})">
			<circle class="light light-a" r="40" />
			<circle class="light light-b" r="33" />
		</g>
	{/if}
</svg>

<style>
	.pins {
		fill: #6f6c7c;
	}

	.chip {
		fill: #26242e;
	}

	:global(.dark) .chip {
		stroke: oklch(1 0 0 / 0.22);
		stroke-width: 5;
	}

	.light {
		mix-blend-mode: screen;
		opacity: 0.45;
		filter: blur(16px);
	}

	.light-a {
		fill: #e05fc0;
		animation: disco-sweep-a 7s ease-in-out infinite;
	}

	.light-b {
		fill: #4fc9e8;
		animation: disco-sweep-b 9s ease-in-out infinite;
	}

	@keyframes disco-sweep-a {
		0%,
		100% {
			transform: translate(40px, 33px);
		}
		50% {
			transform: translate(120px, 113px);
		}
	}

	@keyframes disco-sweep-b {
		0%,
		100% {
			transform: translate(120px, 47px);
		}
		50% {
			transform: translate(33px, 120px);
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.light {
			animation: none;
			opacity: 0.25;
		}
		.light-a {
			transform: translate(60px, 53px);
		}
		.light-b {
			transform: translate(107px, 100px);
		}
	}
</style>
