<script lang="ts">
	import type { Component, Snippet } from 'svelte';
	import { cn } from '$lib/utils';

	let {
		icon: Icon,
		title,
		description = '',
		children,
		class: className = ''
	}: {
		icon?: Component;
		title: string;
		description?: string;
		children?: Snippet;
		class?: string;
	} = $props();
</script>

<div class={cn('flex flex-col items-center justify-center px-6 py-14 text-center', className)}>
	{#if Icon}
		<div
			class="mb-4 flex size-12 items-center justify-center rounded-xl border border-border/60 bg-muted/40 text-muted-foreground"
		>
			<Icon class="size-6" />
		</div>
	{/if}
	<h3 class="text-sm font-semibold">{title}</h3>
	{#if description}
		<p class="mt-1 max-w-sm text-sm text-muted-foreground">{description}</p>
	{/if}
	{#if children}
		<div class="mt-4 flex items-center gap-2">
			{@render children()}
		</div>
	{/if}
</div>
