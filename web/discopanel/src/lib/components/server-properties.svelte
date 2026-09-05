<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { SvelteURL } from 'svelte/reactivity';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { rpcClient } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import { Loader2, Save, RotateCcw, Search, AlertCircle, X, FileSliders } from '@lucide/svelte';
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import { ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import type { ServerProperty } from '$lib/proto/discopanel/v1/properties_pb';
	import PropertyField from '$lib/components/properties/property-field.svelte';
	import { PropertiesForm, categorySlug } from '$lib/components/properties/properties-form.svelte';
	import { EmptyState } from '$lib/components/app';
	import { copyToClipboard } from '$lib/utils/clipboard';

	interface Props {
		server: Server;
		onUpdate?: () => void;
	}

	let { server, onUpdate }: Props = $props();

	const form = new PropertiesForm(true);

	let loading = $state(true);
	let saving = $state(false);
	let searchQuery = $state('');
	let activeCategory = $state('');
	let highlighted = $state<string | null>(null);

	let running = $derived(server.status === ServerStatus.RUNNING);
	let stopped = $derived(server.status === ServerStatus.STOPPED);
	let searching = $derived(searchQuery.trim().length > 0);

	// Reload from scratch when viewing a different server
	let previousServerId = $state(server.id);
	$effect(() => {
		if (server.id !== previousServerId) {
			previousServerId = server.id;
			untrack(() => {
				searchQuery = '';
				activeCategory = '';
				loadProperties();
			});
		}
	});

	onMount(() => {
		loadProperties().then(checkUrlHash);
		window.addEventListener('hashchange', checkUrlHash);
		return () => window.removeEventListener('hashchange', checkUrlHash);
	});

	async function loadProperties() {
		loading = true;
		try {
			const response = await rpcClient.properties.getServerProperties({ serverId: server.id });
			form.process(response.categories);
			if (
				!form.visibleCategories.some((c) => categorySlug(c.name) === activeCategory) &&
				form.visibleCategories.length > 0
			) {
				activeCategory = categorySlug(form.visibleCategories[0].name);
			}
		} catch (error) {
			notify.error('Failed to load server properties');
			console.error(error);
		} finally {
			loading = false;
		}
	}

	async function save() {
		if (!form.dirty || saving) return;
		saving = true;
		try {
			const response = await rpcClient.properties.updateServerProperties({
				serverId: server.id,
				updates: form.buildUpdates()
			});
			form.process(response.categories);
			notify.success(
				stopped ? 'Properties saved' : 'Properties saved. Restart the server to apply.'
			);
			onUpdate?.();
		} catch (error) {
			notify.error('Failed to save properties');
			console.error(error);
		} finally {
			saving = false;
		}
	}

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

	function selectCategory(slug: string) {
		activeCategory = slug;
		searchQuery = '';
		const url = new SvelteURL(window.location.href);
		url.hash = slug;
		window.history.replaceState({}, '', `${url.pathname}${url.search}${url.hash}`);
	}

	function flashField(id: string) {
		setTimeout(() => {
			const element = document.getElementById(id);
			if (element) {
				element.scrollIntoView({ behavior: 'smooth', block: 'center' });
				highlighted = id;
				setTimeout(() => (highlighted = null), 3000);
			}
		}, 100);
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

		const category = form.visibleCategories.find((c) => categorySlug(c.name) === hash);
		if (category) {
			activeCategory = hash;
			return;
		}

		for (const cat of form.visibleCategories) {
			if (cat.properties.some((p) => p.key === hash)) {
				activeCategory = categorySlug(cat.name);
				flashField(hash);
				return;
			}
		}
	}
</script>

{#if loading}
	<div class="flex min-h-0 flex-1 items-center justify-center rounded-xl border bg-card">
		<Loader2 class="size-8 animate-spin text-muted-foreground" />
	</div>
{:else if form.visibleCategories.length === 0}
	<div class="flex min-h-0 flex-1 flex-col justify-center rounded-xl border bg-card">
		<EmptyState
			icon={FileSliders}
			title="No properties found"
			description="Unable to load the properties for this server."
		/>
	</div>
{:else}
	<div class="flex min-h-0 flex-1 flex-col gap-4 md:flex-row md:gap-6">
		<aside class="flex shrink-0 flex-col md:min-h-0 md:w-52">
			<div class="relative mb-3 shrink-0">
				<Search class="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
				<Input placeholder="Search properties..." class="h-8 pl-8" bind:value={searchQuery} />
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
				<span class="stat-label hidden px-2.5 pb-1 md:block">Categories</span>
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
			{#if !stopped}
				<p
					class="flex shrink-0 items-center gap-2 rounded-lg border border-status-busy/25 bg-status-busy/5 px-3 py-2 text-xs text-muted-foreground"
				>
					<AlertCircle class="size-3.5 shrink-0 text-status-busy" />
					{#if running}
						Read-only while the server runs. Stop it to make changes.
					{:else}
						Changes apply the next time the server starts.
					{/if}
				</p>
			{/if}

			{#if searching && visibleFieldCount === 0}
				<div
					class="flex min-h-0 flex-1 items-center justify-center rounded-xl border bg-card text-sm text-muted-foreground"
				>
					No properties match "{searchQuery}"
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
										locked={running}
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
						<Button size="sm" onclick={save} disabled={saving} class="min-w-28">
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
