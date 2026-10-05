<script lang="ts">
	import type { Snippet } from 'svelte';
	import { cn, centerInRail } from '$lib/utils';
	import { Tabs, TabsList, TabsTrigger } from '$lib/components/ui/tabs';
	import { UNDERLINE_TAB } from '$lib/tabs';

	interface TabDef {
		key: string;
		label: string;
		class?: string;
	}

	let {
		tabs,
		value = $bindable(),
		onValueChange,
		stackRail = false,
		header,
		rail,
		submenu,
		tab
	}: {
		tabs: readonly TabDef[];
		value: string;
		onValueChange?: (value: string) => void;
		stackRail?: boolean;
		header?: Snippet;
		rail?: Snippet;
		submenu?: Snippet;
		tab?: Snippet<[TabDef]>;
	} = $props();

	let tabStrip = $state<HTMLElement | null>(null);

	function centerTab(event: MouseEvent) {
		centerInRail(
			tabStrip,
			(event.target as HTMLElement | null)?.closest('[data-slot="tabs-trigger"]')
		);
	}
</script>

<div class="shrink-0">
	<div class="border-b bg-card/40">
		<div class="mx-auto w-full max-w-6xl px-4 sm:px-6 2xl:max-w-7xl">
			{@render header?.()}
			{#if tabs.length > 0 || rail}
				<div
					class={cn(
						'flex gap-4',
						stackRail
							? 'flex-col items-stretch gap-2 sm:flex-row sm:items-end sm:justify-between sm:gap-4'
							: 'items-end justify-between'
					)}
				>
					<Tabs bind:value {onValueChange} class="min-w-0">
						<div class="overflow-x-auto" bind:this={tabStrip} onclick={centerTab}>
							<TabsList class="h-auto w-max justify-start gap-1 bg-transparent p-0">
								{#each tabs as t (t.key)}
									<TabsTrigger value={t.key} class="{UNDERLINE_TAB} {t.class ?? ''}">
										{#if tab}
											{@render tab(t)}
										{:else}
											{t.label}
										{/if}
									</TabsTrigger>
								{/each}
							</TabsList>
						</div>
					</Tabs>
					{#if rail}
						<div class={cn('flex items-end', stackRail ? 'min-w-0 sm:shrink-0' : 'shrink-0')}>
							{@render rail()}
						</div>
					{/if}
				</div>
			{/if}
		</div>
	</div>
	{#if submenu}
		<div class="mx-auto w-full max-w-6xl px-4 sm:px-6 2xl:max-w-7xl">
			<div
				class="flex w-fit max-w-full flex-wrap items-center gap-2 rounded-b-lg border-x border-b bg-card/40 px-3 py-2 shadow-sm"
			>
				{@render submenu()}
			</div>
		</div>
	{/if}
</div>
