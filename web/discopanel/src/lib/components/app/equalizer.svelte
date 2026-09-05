<script lang="ts">
	import type { StatusTone } from '$lib/server-status';
	import { cn } from '$lib/utils';

	let {
		tone = 'ok',
		size = 'sm',
		bars = 4,
		animated = true,
		color = '',
		class: className = ''
	}: {
		tone?: StatusTone;
		size?: 'sm' | 'md' | 'lg';
		bars?: number;
		animated?: boolean;
		color?: string;
		class?: string;
	} = $props();

	const SIZES = {
		sm: { h: 'h-3.5', w: 'w-[3px]', gap: 'gap-[3px]' },
		md: { h: 'h-5', w: 'w-1', gap: 'gap-1' },
		lg: { h: 'h-9', w: 'w-1.5', gap: 'gap-1.5' }
	} as const;

	const TONE_COLOR: Record<StatusTone, string> = {
		ok: 'var(--status-ok)',
		busy: 'var(--status-busy)',
		warn: 'var(--status-warn)',
		danger: 'var(--status-danger)',
		sleep: 'var(--status-sleep)',
		idle: 'var(--status-idle)'
	};

	let s = $derived(SIZES[size]);
</script>

<span
	class={cn('equalizer inline-flex items-end', s.h, s.gap, className)}
	style="--eq-color: {color || TONE_COLOR[tone]}"
	aria-hidden="true"
>
	{#each Array(bars) as _, i (i)}
		<span
			class={cn('eq-bar rounded-full', s.w)}
			class:eq-animated={animated}
			style="animation-delay: {i * 0.18}s"
		></span>
	{/each}
</span>

<style>
	.eq-bar {
		background: var(--eq-color);
		height: 30%;
		opacity: 0.55;
	}

	.eq-bar.eq-animated {
		animation: eq-dance 1.6s ease-in-out infinite;
	}

	@keyframes eq-dance {
		0%,
		100% {
			height: 30%;
			opacity: 0.55;
		}
		25% {
			height: 100%;
			opacity: 1;
		}
		60% {
			height: 55%;
			opacity: 0.8;
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.eq-bar.eq-animated {
			animation: none;
			height: 60%;
			opacity: 0.8;
		}
	}
</style>
