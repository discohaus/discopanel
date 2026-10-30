<script lang="ts">
	import type { Component, Snippet } from 'svelte';
	import { cn } from '$lib/utils';

	let {
		label,
		value,
		sub = '',
		icon: Icon,
		valueClass = '',
		children,
		class: className = ''
	}: {
		label: string;
		value: string;
		sub?: string;
		icon?: Component;
		valueClass?: string;
		children?: Snippet;
		class?: string;
	} = $props();
</script>

<div
	class={cn(
		'flex items-center justify-between gap-3 rounded-lg border bg-card px-4 py-3',
		className
	)}
>
	<div class="min-w-0">
		<div class="stat-label flex items-center gap-1.5">
			{#if Icon}
				<Icon class="size-3.5" />
			{/if}
			{label}
		</div>
		<div class={cn('tabular mt-1 truncate text-xl font-semibold', valueClass)}>{value}</div>
		{#if sub}
			<div class="mt-0.5 truncate text-xs text-muted-foreground">{sub}</div>
		{/if}
	</div>
	{#if children}
		<div class="shrink-0">
			{@render children()}
		</div>
	{/if}
</div>
