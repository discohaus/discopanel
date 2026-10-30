<script lang="ts">
	import type { Snippet } from 'svelte';
	import { cn } from '$lib/utils';

	let {
		title,
		description = '',
		children,
		class: className = ''
	}: {
		title: string;
		description?: string;
		children?: Snippet;
		class?: string;
	} = $props();
</script>

<div class={cn('flex flex-wrap items-center justify-between gap-3', className)}>
	<div class="min-w-0">
		<h1 class="truncate text-xl font-semibold tracking-tight">{title}</h1>
		{#if description}
			<p class="mt-0.5 text-sm text-muted-foreground">{description}</p>
		{/if}
	</div>
	{#if children}
		<div class="flex shrink-0 items-center gap-2">
			{@render children()}
		</div>
	{/if}
</div>
