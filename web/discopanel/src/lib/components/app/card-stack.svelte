<script lang="ts" generics="T">
	import type { Snippet } from 'svelte';
	import { fade, fly } from 'svelte/transition';
	import { flip } from 'svelte/animate';

	let {
		items = [],
		visible = 3,
		columns = 1,
		depth = 3,
		slotHeight,
		gap = '0.5rem',
		itemKey,
		card,
		empty = undefined
	}: {
		items?: T[];
		// Rows fully shown at once
		visible?: number;
		columns?: number;
		// Max card edges drawn per side
		depth?: number;
		// Fixed height of one card slot
		slotHeight: string;
		gap?: string;
		itemKey: (item: T) => string | number;
		card: Snippet<[T, number]>;
		empty?: Snippet;
	} = $props();

	let offset = $state(0);
	let slideDir = $state(1);
	let pageSize = $derived(Math.max(1, visible * columns));
	let maxOffset = $derived(Math.max(0, items.length - pageSize));

	// Shrinking lists pull the window back up
	$effect(() => {
		if (offset > maxOffset) offset = maxOffset;
	});

	let shown = $derived(items.slice(offset, offset + pageSize));
	let before = $derived(Math.min(offset, items.length));
	let after = $derived(Math.max(0, items.length - offset - pageSize));

	function page(dir: number) {
		slideDir = dir;
		offset = Math.min(maxOffset, Math.max(0, offset + dir * pageSize));
	}
</script>

{#snippet slivers(count: number)}
	<span class="flex min-w-0 flex-1 flex-col justify-center gap-[3px]">
		{#each Array.from({ length: Math.min(depth, count) }) as _, layer (layer)}
			<span
				class="h-[2px] rounded-full transition-colors {layer === 0
					? 'bg-border group-hover:bg-primary/50'
					: 'bg-border/50 group-hover:bg-primary/30'}"
			></span>
		{/each}
	</span>
{/snippet}

{#snippet edge(dir: number, count: number)}
	<button
		type="button"
		class="group flex h-4 w-full items-center gap-2 px-2 {count > 0
			? 'cursor-pointer'
			: 'invisible'}"
		onclick={() => page(dir)}
		disabled={count === 0}
		tabindex={count > 0 ? 0 : -1}
		aria-label={dir > 0 ? `Show ${count} more` : `Show ${count} previous`}
	>
		{@render slivers(count)}
		<span
			class="shrink-0 text-[10px] leading-none text-muted-foreground transition-colors group-hover:text-foreground"
		>
			{count} more
		</span>
		{@render slivers(count)}
	</button>
{/snippet}

<div class="rounded-lg border bg-card/50 p-1">
	{@render edge(-1, before)}
	<div
		class="relative overflow-hidden"
		style="height: calc({visible} * {slotHeight} + {visible - 1} * {gap});"
	>
		{#key offset}
			<div
				class="absolute inset-0 grid"
				style="grid-template-columns: repeat({columns}, minmax(0, 1fr)); grid-template-rows: repeat({visible}, {slotHeight}); gap: {gap};"
				in:fly={{ y: slideDir * 16, duration: 160 }}
				out:fly={{ y: slideDir * -16, duration: 160 }}
			>
				{#if items.length === 0 && empty}
					<div class="min-h-0 min-w-0" style="grid-column: 1 / -1; grid-row: 1 / -1;">
						{@render empty()}
					</div>
				{:else}
					{#each shown as item, i (itemKey(item))}
						<div
							class="min-h-0 min-w-0 overflow-hidden"
							animate:flip={{ duration: 150 }}
							in:fade={{ duration: 120 }}
							out:fade={{ duration: 100 }}
						>
							{@render card(item, offset + i)}
						</div>
					{/each}
				{/if}
			</div>
		{/key}
	</div>
	{@render edge(1, after)}
</div>
