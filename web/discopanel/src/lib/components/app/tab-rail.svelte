<script lang="ts">
	import type { Snippet } from 'svelte';
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
		header,
		rail,
		submenu,
		tab
	}: {
		tabs: readonly TabDef[];
		value: string;
		onValueChange?: (value: string) => void;
		header?: Snippet;
		rail?: Snippet;
		submenu?: Snippet;
		tab?: Snippet<[TabDef]>;
	} = $props();
</script>

<div class="shrink-0">
	<div class="border-b bg-card/40">
		<div class="mx-auto w-full max-w-6xl px-4 sm:px-6 2xl:max-w-7xl">
			{@render header?.()}
			{#if tabs.length > 0 || rail}
				<div class="flex items-end justify-between gap-4">
					<Tabs bind:value {onValueChange} class="min-w-0">
						<div class="overflow-x-auto">
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
						<div class="flex shrink-0 items-end">
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
