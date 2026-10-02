<script lang="ts">
	import type { Snippet } from 'svelte';
	import { cn } from '$lib/utils';

	let {
		title,
		description = '',
		action,
		children,
		contentClass = '',
		class: className = ''
	}: {
		title: string;
		description?: string;
		action?: Snippet;
		children: Snippet;
		contentClass?: string;
		class?: string;
	} = $props();
</script>

<section class={cn('overflow-hidden rounded-xl border bg-card', className)}>
	<header class="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="min-w-0">
			<h3 class="text-sm font-semibold">{title}</h3>
			{#if description}
				<p class="mt-0.5 text-xs text-muted-foreground">{description}</p>
			{/if}
		</div>
		{#if action}
			<div class="flex shrink-0 items-center gap-2">
				{@render action()}
			</div>
		{/if}
	</header>
	<div class={cn('p-4', contentClass)}>
		{@render children()}
	</div>
</section>
