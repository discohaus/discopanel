<script lang="ts" generics="T extends string">
	import type { Component, Snippet } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { cn, centerInRail } from '$lib/utils';
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
		sidebarClass = 'md:w-64 bg-card/40',
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

	let nav = $state<HTMLElement | null>(null);

	function selectSection(id: T, event: MouseEvent) {
		activeSection = id;
		centerInRail(nav, event.currentTarget);
	}
</script>

<div class="flex h-full min-h-0 flex-col md:flex-row">
	<aside class={cn('flex shrink-0 flex-col border-b md:border-r md:border-b-0', sidebarClass)}>
		<!-- Mobile title row collapses to a close button -->
		<div class="flex items-center justify-between gap-3 border-b md:hidden">
			<div class="min-w-0 flex-1">
				{@render sidebarHeader?.()}
			</div>
			<Button variant="ghost" size="icon" class="mr-2 size-8 shrink-0" onclick={onclose}>
				<X class="size-4" />
				<span class="sr-only">Close</span>
			</Button>
		</div>
		<div class="hidden md:block">
			{@render sidebarHeader?.()}
		</div>

		<nav
			bind:this={nav}
			class="flex shrink-0 gap-1 overflow-x-auto p-2 md:min-h-0 md:flex-1 md:flex-col md:space-y-1 md:overflow-x-visible md:overflow-y-auto md:p-3"
		>
			{#each navItems as item (item.id)}
				{@const Icon = item.icon}
				<button
					type="button"
					onclick={(event) => selectSection(item.id, event)}
					class={cn(
						'flex w-auto shrink-0 items-center gap-2.5 rounded-md px-3 py-2 text-left text-sm whitespace-nowrap transition-colors md:w-full',
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

		<div class="hidden md:block">
			{@render sidebarFooter?.()}
		</div>
	</aside>

	<div class="flex min-h-0 min-w-0 flex-1 flex-col">
		<div
			class="hidden items-start justify-between gap-4 border-b px-4 py-3 sm:px-6 sm:py-4 md:flex"
		>
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

		<div class="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
			{@render children()}

			<!-- Sidebar footer moves below the content when the rail goes horizontal -->
			{#if sidebarFooter}
				<div class="mt-6 md:hidden">
					{@render sidebarFooter()}
				</div>
			{/if}
		</div>

		<div class="flex flex-wrap items-center justify-end gap-2 border-t px-4 py-3 sm:px-6 sm:py-4">
			{@render footer()}
		</div>
	</div>
</div>
