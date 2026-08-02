<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { Badge } from '$lib/components/ui/badge';
	import { Alert, AlertDescription, AlertTitle } from '$lib/components/ui/alert';
	import { Progress } from '$lib/components/ui/progress';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { PageHeader, EmptyState, ConfirmDialog, TabRail } from '$lib/components/app';
	import { registerRefresh } from '$lib/stores/refresh';
	import ScrollToTop from '$lib/components/scroll-to-top.svelte';
	import { toast } from 'svelte-sonner';
	import {
		Star,
		Download,
		Search,
		RefreshCw,
		ExternalLink,
		AlertCircle,
		Settings,
		Upload,
		Package,
		Trash2,
		X
	} from '@lucide/svelte';
	import { create } from '@bufbuild/protobuf';
	import type { IndexedModpack } from '$lib/proto/discopanel/v1/storage_pb';
	import type {
		SearchModpacksRequest,
		SearchModpacksResponse,
		GetIndexerStatusResponse
	} from '$lib/proto/discopanel/v1/modpack_pb';
	import { SearchModpacksRequestSchema } from '$lib/proto/discopanel/v1/modpack_pb';
	import { rpcClient } from '$lib/api/rpc-client';
	import { loadModLoaders } from '$lib/stores/loaders';
	import { debounce } from 'lodash-es';
	import { uploadFile, cancelUpload, type UploadProgress } from '$lib/utils/chunked-upload';
	import { formatBytes } from '$lib/utils';

	let searchParams = $state<SearchModpacksRequest>(
		create(SearchModpacksRequestSchema, {
			query: '',
			gameVersion: '',
			modLoader: '',
			indexer: '',
			page: 1,
			pageSize: 20
		})
	);

	let searchResults = $state<SearchModpacksResponse | null>(null);
	let favorites = $state<IndexedModpack[]>([]);
	let uploadedPacks = $state<IndexedModpack[]>([]);
	let loading = $state(false);
	let syncing = $state(false);
	let view = $state<'browse' | 'favorites' | 'uploaded'>('browse');
	let indexerStatus = $state<GetIndexerStatusResponse | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);
	let uploading = $state(false);
	let uploadProgress = $state<UploadProgress | null>(null);
	let uploadAbortController = $state<AbortController | null>(null);
	// Default Modrinth because CurseForge needs an API key
	let selectedIndexer = $state('modrinth');
	let indexerName = $derived(selectedIndexer === 'fuego' ? 'CurseForge' : 'Modrinth');
	let deleteTarget = $state<IndexedModpack | null>(null);
	let deleteOpen = $state(false);

	// Dynamic filter options fetched from API
	let gameVersions = $state<string[]>([]);
	let modLoaders = $state<Array<{ value: string; label: string }>>([
		{ value: '', label: 'All loaders' }
	]);

	const VIEWS = [
		{ key: 'browse', label: 'Browse', desc: 'Browse and install modpacks for your servers' },
		{ key: 'favorites', label: 'Favorites', desc: 'Modpacks you starred' },
		{ key: 'uploaded', label: 'Uploaded', desc: 'Modpack ZIPs hosted on this panel' }
	] as const;

	let fuegoBlocked = $derived(
		selectedIndexer === 'fuego' && !!indexerStatus && !indexerStatus.indexersAvailable['fuego']
	);

	onMount(() => {
		initialLoad();
		return registerRefresh(refreshView);
	});

	async function initialLoad() {
		await Promise.all([
			checkIndexerStatus(),
			loadFavorites(),
			loadUploadedPacks(),
			loadMinecraftVersions(),
			loadLoaderFilters(),
			searchModpacks()
		]);

		if (searchResults && searchResults.total === 0 && !searchParams.query) {
			syncModpacks();
		}
	}

	function refreshView() {
		if (view === 'favorites') return loadFavorites();
		if (view === 'uploaded') return loadUploadedPacks();
		return Promise.all([searchModpacks(false), checkIndexerStatus()]);
	}

	// Switches between browse, favorites and uploaded views
	function setView(next: 'browse' | 'favorites' | 'uploaded') {
		view = next;
	}

	async function loadMinecraftVersions() {
		try {
			const response = await rpcClient.minecraft.getMinecraftVersions({});
			gameVersions = response.versions.map((v) => v.id);
		} catch (error) {
			console.error('Failed to load Minecraft versions:', error);
		}
	}

	async function loadLoaderFilters() {
		try {
			const loaders = await loadModLoaders();
			modLoaders = [
				{ value: '', label: 'All loaders' },
				...loaders.map((loader) => ({ value: loader.name, label: loader.displayName }))
			];
		} catch (error) {
			console.error('Failed to load mod loaders:', error);
		}
	}

	async function checkIndexerStatus() {
		try {
			const response = await rpcClient.modpack.getIndexerStatus({});
			indexerStatus = response;
		} catch (error) {
			console.error('Failed to check indexer status:', error);
		}
	}

	function isPackURL(q: string): boolean {
		return /^https?:\/\/(www\.)?(curseforge\.com|modrinth\.com)\//.test(q.trim());
	}

	async function openModpackURL(url: string) {
		try {
			const resp = await rpcClient.modpack.getModpackByURL({ url: url.trim() });
			if (resp.modpack) {
				goto(resolve(`/servers/new?modpack=${resp.modpack.id}`));
				return;
			}
			toast.error('No indexed modpack matches that link');
		} catch {
			toast.error('Modpack lookup failed');
		}
	}

	async function searchModpacks(resetPage = true) {
		if (isPackURL(searchParams.query)) {
			await openModpackURL(searchParams.query);
			return;
		}
		loading = true;
		try {
			// Fresh searches always restart at page one
			if (resetPage) {
				searchParams.page = 1;
			}
			const response = await rpcClient.modpack.searchModpacks({
				...searchParams,
				indexer: selectedIndexer
			});
			searchResults = response;
		} catch (error) {
			toast.error('Failed to search modpacks');
			console.error(error);
		} finally {
			loading = false;
		}
	}

	async function _syncModpacks() {
		syncing = true;
		try {
			const result = await rpcClient.modpack.syncModpacks({
				query: searchParams.query || '',
				gameVersion: searchParams.gameVersion || '',
				modLoader: searchParams.modLoader || '',
				indexer: selectedIndexer
			});
			toast.success(`Synced ${result.syncedCount} modpacks from ${indexerName}`);
			await searchModpacks();
		} catch (error) {
			toast.error(error instanceof Error ? error.message : 'Failed to sync modpacks');
			console.error(error);
		} finally {
			syncing = false;
		}
	}

	// Debounce sync so indexers do not rate limit us
	const syncModpacks = debounce(_syncModpacks, 1000, { leading: true, trailing: false });

	// Searches while typing, links wait for enter
	const autoSearch = debounce(() => {
		if (!isPackURL(searchParams.query)) searchModpacks(true);
	}, 350);

	function submitSearch() {
		autoSearch.cancel();
		searchModpacks(true);
	}

	async function toggleFavorite(modpack: IndexedModpack) {
		try {
			const result = await rpcClient.modpack.toggleFavorite({ id: modpack.id });

			if (searchResults) {
				searchResults.modpacks = searchResults.modpacks.map((m) =>
					m.id === modpack.id ? { ...m, isFavorited: result.isFavorited } : m
				);
			}

			uploadedPacks = uploadedPacks.map((m) =>
				m.id === modpack.id ? { ...m, isFavorited: result.isFavorited } : m
			);

			if (result.isFavorited) {
				if (!favorites.find((f) => f.id === modpack.id)) {
					favorites = [...favorites, { ...modpack, isFavorited: true }];
				}
				toast.success('Added to favorites');
			} else {
				favorites = favorites.filter((f) => f.id !== modpack.id);
				toast.success('Removed from favorites');
			}
		} catch (error) {
			toast.error('Failed to toggle favorite');
			console.error(error);
		}
	}

	async function loadFavorites() {
		try {
			const result = await rpcClient.modpack.listFavorites({});
			favorites = result.modpacks;
		} catch (error) {
			toast.error('Failed to load favorites');
			console.error(error);
		}
	}

	async function loadUploadedPacks() {
		try {
			// Manual indexer filter returns only uploads
			const result = await rpcClient.modpack.searchModpacks({
				query: '',
				gameVersion: '',
				modLoader: '',
				indexer: 'manual',
				page: 1,
				pageSize: 100
			});
			uploadedPacks = result.modpacks || [];
		} catch (error) {
			console.error('Failed to load uploaded packs:', error);
		}
	}

	function formatNumber(num: number): string {
		if (num >= 1000000) {
			return `${(num / 1000000).toFixed(1)}M`;
		} else if (num >= 1000) {
			return `${(num / 1000).toFixed(1)}K`;
		}
		return num.toString();
	}

	async function handleModpackUpload(event: Event) {
		const input = event.target as HTMLInputElement;
		const files = input.files;
		if (!files || files.length === 0) return;

		const file = files[0];
		if (!file.name.endsWith('.zip')) {
			toast.error('Please select a valid modpack ZIP file');
			return;
		}

		uploading = true;
		uploadAbortController = new AbortController();
		uploadProgress = null;

		try {
			const uploadResult = await uploadFile(file, {
				onProgress: (progress) => {
					uploadProgress = progress;
				},
				signal: uploadAbortController.signal
			});
			if (!uploadResult.sessionId) {
				throw new Error('Upload completed but no session ID returned');
			}

			const result = await rpcClient.modpack.importUploadedModpack({
				uploadSessionId: uploadResult.sessionId,
				name: file.name.replace(/\.zip$/i, ''),
				description: ''
			});

			toast.success(`Modpack "${result.modpack?.name}" uploaded successfully`);

			await Promise.all([searchModpacks(), loadUploadedPacks()]);
		} catch (error: unknown) {
			if (error instanceof Error && error.message === 'Upload cancelled') {
				toast.info('Upload cancelled');
			} else {
				toast.error(error instanceof Error ? error.message : 'Failed to upload modpack');
				console.error(error);
			}
		} finally {
			uploading = false;
			uploadProgress = null;
			uploadAbortController = null;
			input.value = '';
		}
	}

	function cancelCurrentUpload() {
		if (uploadAbortController) {
			uploadAbortController.abort();
		}
		if (uploadProgress?.sessionId) {
			cancelUpload(uploadProgress.sessionId).catch(() => {});
		}
	}

	function requestDelete(modpack: IndexedModpack) {
		deleteTarget = modpack;
		deleteOpen = true;
	}

	async function confirmDelete() {
		if (!deleteTarget) return;
		const modpack = deleteTarget;
		try {
			await rpcClient.modpack.deleteModpack({ id: modpack.id });

			toast.success(`Modpack "${modpack.name}" deleted successfully`);

			uploadedPacks = uploadedPacks.filter((m) => m.id !== modpack.id);
			favorites = favorites.filter((m) => m.id !== modpack.id);

			if (view === 'browse') {
				await searchModpacks();
			}
		} catch (error) {
			toast.error(error instanceof Error ? error.message : 'Failed to delete modpack');
		}
	}

	function indexerLabel(modpack: IndexedModpack): string {
		if (modpack.indexer === 'manual') return 'Manual upload';
		if (modpack.indexer === 'fuego') return 'CurseForge';
		return modpack.indexer;
	}

	// Uploaded packs list first in browse results
	let displayModpacks = $derived(
		view === 'favorites'
			? favorites
			: view === 'uploaded'
				? uploadedPacks
				: (() => {
						const results = searchResults?.modpacks || [];
						const uploaded: IndexedModpack[] = [];
						const indexed: IndexedModpack[] = [];
						results.forEach((m) => (m.indexer === 'manual' ? uploaded : indexed).push(m));
						return [...uploaded, ...indexed];
					})()
	);
</script>

<svelte:head>
	<title>Modpacks · DiscoPanel</title>
</svelte:head>

{#snippet browseFilters()}
	<div class="relative">
		<Search class="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
		<Input
			type="search"
			placeholder="Search modpacks or paste a link..."
			class="h-8 w-52 pl-8 sm:w-72"
			bind:value={searchParams.query}
			oninput={autoSearch}
			onkeydown={(e) => e.key === 'Enter' && submitSearch()}
		/>
	</div>
	<Select
		type="single"
		value={searchParams.gameVersion}
		onValueChange={(v: string | undefined) => {
			searchParams.gameVersion = v || '';
			searchModpacks(true);
		}}
		disabled={loading}
	>
		<SelectTrigger class="h-8 w-32 text-xs">
			<span class="truncate">{searchParams.gameVersion || 'All versions'}</span>
		</SelectTrigger>
		<SelectContent>
			<SelectItem value="">All versions</SelectItem>
			{#each gameVersions as version (version)}
				<SelectItem value={version}>{version}</SelectItem>
			{/each}
		</SelectContent>
	</Select>
	<Select
		type="single"
		value={searchParams.modLoader}
		onValueChange={(v: string | undefined) => {
			searchParams.modLoader = v || '';
			searchModpacks(true);
		}}
		disabled={loading}
	>
		<SelectTrigger class="h-8 w-32 text-xs">
			<span class="truncate">
				{searchParams.modLoader
					? modLoaders.find((l) => l.value === searchParams.modLoader)?.label
					: 'All loaders'}
			</span>
		</SelectTrigger>
		<SelectContent>
			{#each modLoaders as loader (loader.value)}
				<SelectItem value={loader.value}>{loader.label}</SelectItem>
			{/each}
		</SelectContent>
	</Select>
	<Select
		type="single"
		value={selectedIndexer}
		onValueChange={(v: string | undefined) => {
			selectedIndexer = v || 'modrinth';
			// Undebounced so a fresh indexer always syncs
			_syncModpacks();
		}}
		disabled={syncing}
	>
		<SelectTrigger class="h-8 w-32 text-xs">
			<span class="truncate">{indexerName}</span>
		</SelectTrigger>
		<SelectContent>
			<SelectItem value="modrinth">Modrinth</SelectItem>
			<SelectItem value="fuego">CurseForge</SelectItem>
		</SelectContent>
	</Select>
	<Button
		variant="ghost"
		size="sm"
		class="h-8 text-muted-foreground hover:text-foreground"
		onclick={syncModpacks}
		disabled={syncing || fuegoBlocked}
	>
		<RefreshCw class="size-3.5 {syncing ? 'animate-spin' : ''}" />
		Sync {indexerName}
	</Button>
{/snippet}

{#snippet uploadActions()}
	<Button
		variant="ghost"
		size="sm"
		class="h-8 text-muted-foreground hover:text-foreground"
		onclick={() => fileInput?.click()}
		disabled={uploading}
	>
		<Upload class="size-3.5" />
		Upload modpack
	</Button>
{/snippet}

<div class="flex min-h-0 flex-1 flex-col">
	<TabRail
		tabs={VIEWS}
		value={view}
		onValueChange={(v) => setView((v as typeof view) || 'browse')}
		submenu={view === 'browse'
			? browseFilters
			: view === 'uploaded' && uploadedPacks.length > 0
				? uploadActions
				: undefined}
	>
		{#snippet header()}
			<PageHeader
				title="Modpacks"
				description={VIEWS.find((v) => v.key === view)?.desc ?? ''}
				class="pt-5 pb-4"
			/>
		{/snippet}
		{#snippet tab(t)}
			{t.label}
			{#if t.key === 'favorites'}
				<span class="tabular ml-1.5 text-xs text-muted-foreground">{favorites.length}</span>
			{:else if t.key === 'uploaded'}
				<span class="tabular ml-1.5 text-xs text-muted-foreground">{uploadedPacks.length}</span>
			{/if}
		{/snippet}
	</TabRail>

	<div data-scroll-root class="min-h-0 flex-1 overflow-y-auto">
		<div class="mx-auto w-full max-w-6xl space-y-5 p-4 sm:p-6 2xl:max-w-7xl">
			<input
				bind:this={fileInput}
				type="file"
				accept=".zip"
				onchange={handleModpackUpload}
				class="hidden"
			/>

			{#if fuegoBlocked}
				<Alert>
					<AlertCircle class="size-4" />
					<AlertTitle>CurseForge API key required</AlertTitle>
					<AlertDescription>
						<div class="space-y-2">
							<p>
								To sync modpacks from CurseForge, you need to <a
									href="https://console.curseforge.com/#/api-keys"
									target="_blank"
									rel="noopener noreferrer"
									class="font-medium underline underline-offset-4">generate a CurseForge API key</a
								> and add it to your server defaults.
							</p>
							<div class="flex items-center gap-2">
								<Button size="sm" variant="outline" href="{resolve('/settings')}#cfApiKey">
									<Settings class="size-4" />
									Configure in Settings
								</Button>
							</div>
						</div>
					</AlertDescription>
				</Alert>
			{/if}

			{#if uploading && uploadProgress}
				<div class="rounded-lg border bg-card p-4">
					<div class="mb-2 flex items-center justify-between">
						<span class="text-sm font-medium">Uploading modpack...</span>
						<div class="flex items-center gap-2">
							<span class="tabular text-sm text-muted-foreground">
								{uploadProgress.percentComplete.toFixed(0)}%
							</span>
							<Button
								size="icon"
								variant="ghost"
								class="size-6"
								onclick={cancelCurrentUpload}
								title="Cancel upload"
							>
								<X class="size-4" />
							</Button>
						</div>
					</div>
					<Progress value={uploadProgress.percentComplete} class="h-2" />
					<p class="mt-1 text-xs text-muted-foreground">
						{formatBytes(uploadProgress.bytesUploaded)} / {formatBytes(uploadProgress.totalBytes)}
					</p>
				</div>
			{/if}

			{#if view === 'browse' && (loading || syncing) && displayModpacks.length === 0}
				<div class="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
					{#each Array(6) as _, i (i)}
						<Skeleton class="h-48 rounded-lg" />
					{/each}
				</div>
			{:else if displayModpacks.length > 0}
				<div class="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
					{#each displayModpacks as modpack (modpack.id)}
						{@const loaders = modpack.modLoaders}
						{@const versions = modpack.gameVersions}
						<div
							class="flex flex-col rounded-xl border bg-card p-4 transition-all hover:border-primary/30 hover:shadow-sm"
						>
							<div class="flex items-start gap-3">
								{#if modpack.logoUrl}
									<img
										src={modpack.logoUrl}
										alt={modpack.name}
										loading="lazy"
										class="size-12 shrink-0 rounded-md border object-cover"
									/>
								{:else}
									<div
										class="flex size-12 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground"
									>
										<Package class="size-5" />
									</div>
								{/if}
								<div class="min-w-0 flex-1">
									<h3 class="line-clamp-1 text-sm font-semibold">{modpack.name}</h3>
									<div class="mt-1 flex flex-wrap items-center gap-1.5">
										<Badge variant="secondary" class="capitalize">{indexerLabel(modpack)}</Badge>
										{#if modpack.downloadCount > 0}
											<span class="inline-flex items-center gap-1 text-xs text-muted-foreground">
												<Download class="size-3" />
												<span class="tabular">{formatNumber(modpack.downloadCount)}</span>
											</span>
										{/if}
									</div>
								</div>
								<Button
									size="icon"
									variant="ghost"
									class="size-8 shrink-0"
									onclick={() => toggleFavorite(modpack)}
									title={modpack.isFavorited ? 'Remove from favorites' : 'Add to favorites'}
								>
									<Star
										class="size-4 {modpack.isFavorited
											? 'fill-primary text-primary'
											: 'text-muted-foreground'}"
									/>
								</Button>
							</div>

							{#if modpack.summary}
								<p class="mt-3 line-clamp-2 text-sm text-muted-foreground">{modpack.summary}</p>
							{/if}

							{#if loaders.length > 0 || versions.length > 0}
								<div class="mt-3 space-y-2">
									{#if loaders.length > 0}
										<div class="flex flex-wrap gap-1">
											{#each loaders as loader (loader)}
												<Badge variant="outline" class="capitalize">{loader}</Badge>
											{/each}
										</div>
									{/if}
									{#if versions.length > 0}
										<p class="text-xs text-muted-foreground">
											MC {versions.slice(0, 3).join(', ')}{versions.length > 3
												? ` +${versions.length - 3} more`
												: ''}
										</p>
									{/if}
								</div>
							{/if}

							<div class="mt-auto flex items-center gap-2 pt-4">
								{#if modpack.websiteUrl}
									<Button
										variant="outline"
										size="sm"
										href={modpack.websiteUrl}
										target="_blank"
										rel="noopener noreferrer"
									>
										<ExternalLink class="size-3.5" />
										View
									</Button>
								{/if}
								{#if modpack.indexer === 'manual'}
									<Button
										variant="outline"
										size="sm"
										class="text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
										onclick={() => requestDelete(modpack)}
									>
										<Trash2 class="size-3.5" />
										Delete
									</Button>
								{/if}
								<Button
									size="sm"
									class="ml-auto"
									onclick={() => goto(resolve(`/servers/new?modpack=${modpack.id}`))}
								>
									Install
								</Button>
							</div>
						</div>
					{/each}
				</div>
			{:else}
				<div class="rounded-lg border bg-card">
					{#if view === 'favorites'}
						<EmptyState
							icon={Star}
							title="No favorite modpacks yet"
							description="Browse the modpacks list and click the star icon to add favorites."
						>
							<Button variant="outline" size="sm" onclick={() => setView('browse')}>
								Browse modpacks
							</Button>
						</EmptyState>
					{:else if view === 'uploaded'}
						<EmptyState
							icon={Upload}
							title="No uploaded modpacks"
							description="Upload a modpack ZIP to host it here."
						>
							<Button
								variant="outline"
								size="sm"
								onclick={() => fileInput?.click()}
								disabled={uploading}
							>
								<Upload class="size-4" />
								Upload modpack
							</Button>
						</EmptyState>
					{:else if searchParams.query}
						<EmptyState
							icon={Search}
							title="No matching modpacks"
							description="Try a different search, or sync {indexerName} for fresh results."
						>
							<Button
								variant="outline"
								size="sm"
								onclick={syncModpacks}
								disabled={syncing || fuegoBlocked}
							>
								<RefreshCw class="size-4 {syncing ? 'animate-spin' : ''}" />
								Sync {indexerName}
							</Button>
						</EmptyState>
					{:else}
						<EmptyState
							icon={Package}
							title="No modpacks found"
							description="No modpacks found locally. Sync to fetch modpacks from {indexerName}."
						>
							<Button
								variant="outline"
								size="sm"
								onclick={syncModpacks}
								disabled={syncing || fuegoBlocked}
							>
								<RefreshCw class="size-4 {syncing ? 'animate-spin' : ''}" />
								Sync {indexerName}
							</Button>
						</EmptyState>
					{/if}
				</div>
			{/if}

			{#if view === 'browse' && searchResults && searchResults.total > searchResults.pageSize}
				<div class="flex items-center justify-center gap-3">
					<Button
						variant="outline"
						size="sm"
						disabled={(searchParams.page || 1) === 1 || loading}
						onclick={() => {
							searchParams.page = Math.max(1, (searchParams.page || 1) - 1);
							searchModpacks(false);
						}}
					>
						Previous
					</Button>
					<span class="tabular text-sm text-muted-foreground">
						Page {searchParams.page} of {Math.ceil(searchResults.total / searchResults.pageSize)}
					</span>
					<Button
						variant="outline"
						size="sm"
						disabled={(searchParams.page || 1) >=
							Math.ceil(searchResults.total / searchResults.pageSize) || loading}
						onclick={() => {
							searchParams.page = (searchParams.page || 1) + 1;
							searchModpacks(false);
						}}
					>
						Next
					</Button>
				</div>
			{/if}
		</div>
	</div>
</div>

<ConfirmDialog
	bind:open={deleteOpen}
	title={`Delete "${deleteTarget?.name ?? 'modpack'}"?`}
	description="This removes the uploaded modpack from DiscoPanel. This cannot be undone."
	confirmLabel="Delete modpack"
	destructive
	onConfirm={confirmDelete}
/>

<ScrollToTop />
