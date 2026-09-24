<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { PageHeader, TabRail } from '$lib/components/app';
	import { Plus } from '@lucide/svelte';
	import TemplateManagement from './TemplateManagement.svelte';
	import ActiveModules from './ActiveModules.svelte';

	const TABS = [
		{ key: 'templates', label: 'Templates', desc: 'Reusable module definitions for any server' },
		{ key: 'active', label: 'Active instances', desc: 'Every module running across the panel' }
	] as const;

	// Url query param always decides the visible tab
	let activeTab = $derived.by(() => {
		const requested = page.url.searchParams.get('tab');
		return TABS.some((t) => t.key === requested) ? (requested as string) : 'templates';
	});

	function setTab(tab: string | undefined) {
		if (!tab || tab === activeTab) return;
		const base = resolve('/modules');
		const target = tab === 'templates' ? base : `${base}?tab=${tab}`;
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- base is resolved, only query varies
		goto(target, { noScroll: true, keepFocus: true });
	}

	let createOpen = $state(false);
	let templateCategory = $state<string | null>(null);
	let templateCategories = $state<string[]>([]);
</script>

<svelte:head>
	<title>Modules · DiscoPanel</title>
</svelte:head>

{#snippet categoryFilter()}
	<button
		class="h-6.5 rounded-md px-2.5 text-xs font-medium transition-colors {templateCategory === null
			? 'bg-primary/15 text-primary'
			: 'text-muted-foreground hover:bg-muted/60 hover:text-foreground'}"
		onclick={() => (templateCategory = null)}
	>
		All
	</button>
	{#each templateCategories as cat (cat)}
		<button
			class="h-6.5 rounded-md px-2.5 text-xs font-medium capitalize transition-colors {templateCategory ===
			cat
				? 'bg-primary/15 text-primary'
				: 'text-muted-foreground hover:bg-muted/60 hover:text-foreground'}"
			onclick={() => (templateCategory = cat)}
		>
			{cat}
		</button>
	{/each}
{/snippet}

<div class="flex min-h-0 flex-1 flex-col">
	<TabRail
		tabs={TABS}
		value={activeTab}
		onValueChange={setTab}
		submenu={activeTab === 'templates' && templateCategories.length > 0
			? categoryFilter
			: undefined}
	>
		{#snippet header()}
			<PageHeader
				title="Modules"
				description={TABS.find((t) => t.key === activeTab)?.desc ??
					'Companion services for servers'}
				class="pt-5 pb-4"
			/>
		{/snippet}
		{#snippet rail()}
			{#if activeTab === 'templates'}
				<button
					class="flex items-center gap-1.5 border-b-2 border-transparent px-3 pt-1.5 pb-2 text-sm font-medium text-primary transition-colors hover:text-primary/80"
					onclick={() => (createOpen = true)}
				>
					<Plus class="size-4" />
					Create template
				</button>
			{/if}
		{/snippet}
	</TabRail>

	<div class="min-h-0 flex-1 overflow-y-auto">
		<div class="mx-auto w-full max-w-6xl p-4 sm:p-6 2xl:max-w-7xl">
			{#if activeTab === 'templates'}
				<TemplateManagement
					bind:createOpen
					bind:category={templateCategory}
					bind:categories={templateCategories}
				/>
			{:else if activeTab === 'active'}
				<ActiveModules />
			{/if}
		</div>
	</div>
</div>
