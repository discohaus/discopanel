<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { SvelteURL } from 'svelte/reactivity';
	import { Input } from '$lib/components/ui/input';
	import { Button } from '$lib/components/ui/button';
	import { notify } from '$lib/stores/activity.svelte';
	import { Save, RotateCcw, Loader2, Search, X, FileSliders } from '@lucide/svelte';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import type { PropertyCategory, ServerProperty } from '$lib/proto/discopanel/v1/properties_pb';
	import PropertyField from '$lib/components/properties/property-field.svelte';
	import { PropertiesForm, categorySlug } from '$lib/components/properties/properties-form.svelte';
	import { EmptyState } from '$lib/components/app';

	interface Props {
		categories: PropertyCategory[];
		onSave: (updates: Record<string, string>) => Promise<void>;
		saving?: boolean;
	}

	let { categories, onSave, saving = false }: Props = $props();

	const form = new PropertiesForm(false);

	let activeCategory = $state<string>('');
	let highlighted = $state<string | null>(null);
	let searchQuery = $state('');
	let searching = $derived(searchQuery.trim().length > 0);

	// Reprocess whenever the parent hands new categories down
	let previousCategories = $state<PropertyCategory[] | undefined>(undefined);
	$effect(() => {
		if (categories && categories !== previousCategories) {
			untrack(() => {
				previousCategories = categories;
				form.process(categories);
				if (!activeCategory && form.visibleCategories.length > 0) {
					activeCategory = categorySlug(form.visibleCategories[0].name);
				}
			});
		}
	});

	onMount(() => {
		checkUrlHash();
		window.addEventListener('hashchange', checkUrlHash);
		return () => window.removeEventListener('hashchange', checkUrlHash);
	});

	function matchesSearch(prop: ServerProperty, q: string): boolean {
		return (
			prop.label.toLowerCase().includes(q) ||
			prop.key.toLowerCase().includes(q) ||
			prop.envVar.toLowerCase().includes(q) ||
			prop.description.toLowerCase().includes(q)
		);
	}

	// Search flattens across categories, otherwise show the active one
	let visibleGroups = $derived.by(() => {
		if (searching) {
			const q = searchQuery.trim().toLowerCase();
			return form.visibleCategories
				.map((cat) => ({
					name: cat.name,
					properties: cat.properties.filter((p) => matchesSearch(p, q))
				}))
				.filter((g) => g.properties.length > 0);
		}
		const cat = form.visibleCategories.find((c) => categorySlug(c.name) === activeCategory);
		return cat ? [{ name: cat.name, properties: cat.properties }] : [];
	});

	let visibleFieldCount = $derived(visibleGroups.reduce((acc, g) => acc + g.properties.length, 0));

	async function handleSave() {
		if (!form.dirty) return;
		await onSave(form.buildUpdates());
	}

	function selectCategory(slug: string) {
		activeCategory = slug;
		searchQuery = '';
		const url = new SvelteURL(window.location.href);
		url.hash = slug;
		window.history.replaceState({}, '', `${url.pathname}${url.search}${url.hash}`);
	}

	async function copyFieldLink(key: string) {
		const url = new SvelteURL(window.location.href);
		url.hash = key;
		const success = await copyToClipboard(url.toString());
		if (success) notify.success('Link copied to clipboard');
	}

	function checkUrlHash() {
		const hash = window.location.hash.slice(1);
		if (!hash) return;

		setTimeout(() => {
			const matchingCategory = form.visibleCategories.find((c) => categorySlug(c.name) === hash);
			if (matchingCategory) {
				activeCategory = hash;
				return;
			}

			for (const cat of form.visibleCategories) {
				if (cat.properties.some((p) => p.key === hash)) {
					activeCategory = categorySlug(cat.name);
					setTimeout(() => {
						const element = document.getElementById(hash);
						if (element) {
							element.scrollIntoView({ behavior: 'smooth', block: 'center' });
							highlighted = hash;
							setTimeout(() => (highlighted = null), 3000);
						}
					}, 100);
					return;
				}
			}
		}, 50);
	}
</script>

{#if form.visibleCategories.length === 0}
	<div class="flex min-h-0 flex-1 flex-col justify-center rounded-xl border bg-card">
		<EmptyState
			icon={FileSliders}
			title="No properties found"
			description="Unable to load the default server properties."
		/>
	</div>
{:else}
	<div class="flex min-h-0 flex-1 flex-col gap-4 md:flex-row md:gap-6">
		<aside class="flex shrink-0 flex-col md:min-h-0 md:w-52">
			<div class="relative mb-3 shrink-0">
				<Search class="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
				<Input placeholder="Search settings..." class="h-8 pl-8" bind:value={searchQuery} />
				{#if searching}
					<button
						class="absolute top-1/2 right-2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
						onclick={() => (searchQuery = '')}
						title="Clear search"
					>
						<X class="size-3.5" />
					</button>
				{/if}
			</div>
			<nav
				class="flex gap-1 overflow-x-auto pb-1 md:min-h-0 md:flex-1 md:flex-col md:overflow-x-visible md:overflow-y-auto md:pb-0"
				class:opacity-50={searching}
			>
				<span class="stat-label hidden px-2.5 pb-1 md:block">Defaults</span>
				{#each form.visibleCategories as category (category.name)}
					{@const slug = categorySlug(category.name)}
					{@const isActive = !searching && activeCategory === slug}
					{@const modCount = form.modifiedCountBySlug.get(slug) ?? 0}
					<button
						class="flex shrink-0 items-center justify-between gap-2 rounded-md px-2.5 py-1.5 text-left text-sm whitespace-nowrap transition-colors md:w-full
							{isActive
							? 'bg-accent font-medium text-foreground'
							: 'text-muted-foreground hover:bg-accent/50 hover:text-foreground'}"
						onclick={() => selectCategory(slug)}
					>
						<span class="truncate">{category.name}</span>
						{#if modCount > 0}
							<span
								class="tabular inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-status-busy/15 px-1 text-[11px] font-semibold text-status-busy"
							>
								{modCount}
							</span>
						{/if}
					</button>
				{/each}
			</nav>
		</aside>

		<div class="flex min-h-0 min-w-0 flex-1 flex-col gap-3">
			{#if searching && visibleFieldCount === 0}
				<div
					class="flex min-h-0 flex-1 items-center justify-center rounded-xl border bg-card text-sm text-muted-foreground"
				>
					No settings match "{searchQuery}"
				</div>
			{:else}
				<section class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border bg-card">
					<div class="min-h-0 flex-1 overflow-y-auto">
						{#each visibleGroups as group (group.name)}
							<header
								class="sticky top-0 z-10 flex flex-wrap items-baseline justify-between gap-2 border-b bg-card px-4 py-2.5 [&:not(:first-child)]:border-t"
							>
								<h3 class="text-sm font-semibold">{group.name}</h3>
								<span class="text-xs text-muted-foreground">
									{group.properties.length}
									{group.properties.length === 1 ? 'setting' : 'settings'}
								</span>
							</header>
							<div class="divide-y">
								{#each group.properties as prop (prop.key)}
									<PropertyField
										{form}
										{prop}
										locked={saving}
										highlighted={highlighted === prop.key}
										onCopyLink={copyFieldLink}
									/>
								{/each}
							</div>
						{/each}
					</div>
				</section>
			{/if}

			{#if form.dirty}
				<div
					class="flex shrink-0 flex-wrap items-center justify-between gap-3 rounded-xl border bg-card px-4 py-3"
				>
					<span class="text-sm font-medium">
						{form.modifiedKeys.size} unsaved {form.modifiedKeys.size === 1 ? 'change' : 'changes'}
					</span>
					<div class="flex items-center gap-2">
						<Button variant="outline" size="sm" onclick={() => form.reset()} disabled={saving}>
							<RotateCcw class="size-4" />
							Discard
						</Button>
						<Button size="sm" onclick={handleSave} disabled={saving} class="min-w-28">
							{#if saving}
								<Loader2 class="size-4 animate-spin" />
							{:else}
								<Save class="size-4" />
							{/if}
							Save changes
						</Button>
					</div>
				</div>
			{/if}
		</div>
	</div>
{/if}
