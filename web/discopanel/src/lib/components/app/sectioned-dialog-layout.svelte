<script lang="ts" generics="T extends string">
	import type { Component, Snippet } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { cn } from '$lib/utils';
	import { X } from '@lucide/svelte';

	interface NavItem {
		id: T;
		label: string;
		icon: Component;
	}

	let {
		activeSection = $bindable(),
		navItems,
		title,
		description,
		sidebarClass = 'w-64 bg-card/40',
		onclose,
		sidebarHeader,
		sidebarFooter,
		navExtra,
		banner,
		children,
		footer
	}: {
		activeSection: T;
		navItems: NavItem[];
		title: string;
		description: string;
		sidebarClass?: string;
		onclose: () => void;
		sidebarHeader?: Snippet;
		sidebarFooter?: Snippet;
		// Renders trailing extras inside a nav button
		navExtra?: Snippet<[T]>;
		banner?: Snippet;
		children: Snippet;
		footer: Snippet;
	} = $props();
</script>

<div class="flex h-full min-h-0">
	<aside class={cn('flex shrink-0 flex-col border-r', sidebarClass)}>
		{@render sidebarHeader?.()}

		<nav class="flex-1 space-y-1 overflow-y-auto p-3">
			{#each navItems as item (item.id)}
				{@const Icon = item.icon}
				<button
					type="button"
					onclick={() => (activeSection = item.id)}
					class={cn(
						'flex w-full items-center gap-2.5 rounded-md px-3 py-2 text-left text-sm transition-colors',
						activeSection === item.id
							? 'bg-accent font-medium text-foreground'
							: 'text-muted-foreground hover:bg-accent/40 hover:text-foreground'
					)}
				>
					<Icon class="size-4 shrink-0" />
					{item.label}
					{@render navExtra?.(item.id)}
				</button>
			{/each}
		</nav>

		{@render sidebarFooter?.()}
	</aside>

	<div class="flex min-w-0 flex-1 flex-col">
		<div class="flex items-start justify-between gap-4 border-b px-6 py-4">
			<div class="min-w-0">
				<h2 class="text-lg font-semibold tracking-tight">{title}</h2>
				<p class="mt-0.5 text-sm text-muted-foreground">{description}</p>
			</div>
			<Button variant="ghost" size="icon" class="size-8 shrink-0" onclick={onclose}>
				<X class="size-4" />
				<span class="sr-only">Close</span>
			</Button>
		</div>

		{@render banner?.()}

		<div class="min-h-0 flex-1 overflow-y-auto p-6">
			{@render children()}
		</div>

		<div class="flex items-center justify-end gap-2 border-t px-6 py-4">
			{@render footer()}
		</div>
	</div>
</div>
