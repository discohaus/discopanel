<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import {
		DropdownMenu,
		DropdownMenuContent,
		DropdownMenuItem,
		DropdownMenuSeparator,
		DropdownMenuTrigger
	} from '$lib/components/ui/dropdown-menu';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import {
		PageHeader,
		StatusBadge,
		ServerAvatar,
		EmptyState,
		ConfirmDialog,
		TabRail
	} from '$lib/components/app';
	import MetricsSparkline from '$lib/components/metrics-sparkline.svelte';
	import { routedAddresses } from '$lib/hostname';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { serversStore, sortServersByActivity, claimFullStats } from '$lib/stores/servers';
	import {
		tpsTone,
		TONE_TEXT,
		canStart,
		canStop,
		canRestart,
		isUp,
		statusMeta
	} from '$lib/server-status';
	import { loaderDisplayName } from '$lib/stores/loaders';
	import { formatUptime } from '$lib/utils/time';
	import { runServerAction, type ServerOp } from '$lib/server-actions';
	import { notify } from '$lib/stores/activity.svelte';
	import {
		Plus,
		Search,
		Play,
		Square,
		RotateCw,
		RefreshCcw,
		MoreHorizontal,
		Trash2,
		Server as ServerIcon,
		Users,
		Zap,
		MemoryStick,
		ArrowUpDown
	} from '@lucide/svelte';
	import { type Server, ServerStatus, ModLoader } from '$lib/proto/discopanel/v1/storage_pb';

	const DELETE_WARNING =
		'This permanently removes the container, all server files, the world, and every mod and configuration.\nThis cannot be undone.';

	type SortKey = 'activity' | 'name' | 'players' | 'status';
	const SORT_OPTIONS: { key: SortKey; label: string }[] = [
		{ key: 'activity', label: 'Activity' },
		{ key: 'name', label: 'Name' },
		{ key: 'players', label: 'Players' },
		{ key: 'status', label: 'Status' }
	];

	const FILTERS = [
		{ key: 'all', label: 'All' },
		{ key: 'active', label: 'Active' },
		{ key: 'stopped', label: 'Stopped' },
		{ key: 'issues', label: 'Issues' }
	] as const;

	let servers = $derived($serversStore);
	let searchQuery = $state('');
	let filter = $state<'all' | 'active' | 'stopped' | 'issues'>('all');
	let sortKey = $state<SortKey>('activity');
	let initialLoading = $state(true);
	let actioningId = $state('');
	let deleteTarget = $state<Server | null>(null);
	let deleteOpen = $state(false);
	let now = $state(new Date());
	let lobbyWaiting = $state<Record<string, number>>({});

	// Lobby holds per server show while worlds wake
	async function pollLobbyWaiting() {
		try {
			const status = await rpcClient.proxy.getProxyStatus({}, silentCallOptions);
			lobbyWaiting = status.lobbyWaiting;
		} catch {
			// Silent polls swallow transient failures
		}
	}

	onMount(() => {
		const release = claimFullStats();
		serversStore
			.fetchServers(servers.length > 0, true)
			.catch(() => {})
			.finally(() => (initialLoading = false));
		const clock = setInterval(() => (now = new Date()), 30000);
		pollLobbyWaiting();
		const lobbyPoll = setInterval(pollLobbyWaiting, 10000);
		return () => {
			release();
			clearInterval(clock);
			clearInterval(lobbyPoll);
		};
	});

	function inFilter(server: Server): boolean {
		const issue = server.status === ServerStatus.ERROR || server.status === ServerStatus.UNHEALTHY;
		switch (filter) {
			case 'active':
				return server.status !== ServerStatus.STOPPED && !issue;
			case 'stopped':
				return server.status === ServerStatus.STOPPED;
			case 'issues':
				return issue;
			default:
				return true;
		}
	}

	let counts = $derived.by(() => {
		const issues = servers.filter(
			(s) => s.status === ServerStatus.ERROR || s.status === ServerStatus.UNHEALTHY
		).length;
		const stopped = servers.filter((s) => s.status === ServerStatus.STOPPED).length;
		return { all: servers.length, active: servers.length - issues - stopped, stopped, issues };
	});

	let filterTabs = $derived(
		servers.length === 0
			? []
			: FILTERS.filter((f) => f.key === 'all' || counts[f.key] > 0).map((f) => ({
					...f,
					class:
						f.key === 'issues'
							? 'text-status-danger data-[state=active]:border-status-danger data-[state=active]:text-status-danger'
							: ''
				}))
	);

	function applySort(list: Server[]): Server[] {
		switch (sortKey) {
			case 'name':
				return [...list].sort((a, b) => a.name.localeCompare(b.name));
			case 'players':
				return [...list].sort((a, b) => (b.playersOnline || 0) - (a.playersOnline || 0));
			case 'status':
				return [...list].sort((a, b) =>
					statusMeta(a.status).label.localeCompare(statusMeta(b.status).label)
				);
			default:
				return sortServersByActivity([...list]);
		}
	}

	let visibleServers = $derived.by(() => {
		const sorted = applySort(servers.filter(inFilter));
		if (!searchQuery) return sorted;
		const q = searchQuery.toLowerCase();
		return sorted.filter(
			(s) =>
				s.name.toLowerCase().includes(q) ||
				s.description.toLowerCase().includes(q) ||
				s.mcVersion.toLowerCase().includes(q) ||
				$loaderDisplayName(s.modLoader).toLowerCase().includes(q)
		);
	});

	async function handleAction(action: ServerOp, server: Server) {
		actioningId = server.id;
		await runServerAction(action, server);
		actioningId = '';
	}

	function requestDelete(server: Server) {
		deleteTarget = server;
		deleteOpen = true;
	}

	async function confirmDelete() {
		if (!deleteTarget) return;
		try {
			await rpcClient.server.deleteServer({ id: deleteTarget.id }, silentCallOptions);
			serversStore.removeServer(deleteTarget.id);
			notify.success(`Deleted ${deleteTarget.name}`);
		} catch (error) {
			notify.error(error instanceof Error ? error.message : 'Failed to delete server');
		} finally {
			deleteTarget = null;
		}
	}

	function connectionLabel(server: Server): string {
		return routedAddresses(server).join(', ') || `:${server.port}`;
	}
</script>

<svelte:head>
	<title>Servers · DiscoPanel</title>
</svelte:head>

<div class="flex min-h-0 flex-1 flex-col">
	<TabRail
		tabs={filterTabs}
		value={filter}
		onValueChange={(v) => (filter = (v as typeof filter) || 'all')}
	>
		{#snippet header()}
			<PageHeader title="Servers" description="Manage every server on this panel" class="pt-5 pb-4">
				<Button href={resolve('/servers/new')} size="sm" class="glow-primary h-8">
					<Plus class="size-4" />
					<span class="hidden sm:inline">New server</span>
				</Button>
			</PageHeader>
		{/snippet}
		{#snippet tab(t)}
			{t.label}
			<span class="tabular ml-1.5 text-xs text-muted-foreground">
				{counts[t.key as keyof typeof counts]}
			</span>
		{/snippet}
		{#snippet rail()}
			{#if servers.length > 0}
				<div class="flex flex-wrap items-center gap-2 pb-2">
					<div class="relative">
						<Search
							class="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
						/>
						<Input
							type="search"
							placeholder="Search servers..."
							class="h-8 w-48 pl-8 sm:w-64"
							bind:value={searchQuery}
						/>
					</div>
					<Select
						type="single"
						value={sortKey}
						onValueChange={(v) => (sortKey = (v as SortKey) || 'activity')}
					>
						<SelectTrigger class="h-8 w-auto gap-1.5 text-xs">
							<ArrowUpDown class="size-3.5 text-muted-foreground" />
							<span>{SORT_OPTIONS.find((o) => o.key === sortKey)?.label}</span>
						</SelectTrigger>
						<SelectContent>
							{#each SORT_OPTIONS as option (option.key)}
								<SelectItem value={option.key}>{option.label}</SelectItem>
							{/each}
						</SelectContent>
					</Select>
				</div>
			{/if}
		{/snippet}
	</TabRail>

	<div class="min-h-0 flex-1 overflow-y-auto">
		<div class="mx-auto w-full max-w-6xl space-y-5 p-4 sm:p-6 2xl:max-w-7xl">
			{#if initialLoading && servers.length === 0}
				<div class="space-y-3">
					<Skeleton class="h-9 rounded-lg" />
					<Skeleton class="h-64 rounded-lg" />
				</div>
			{:else if servers.length === 0}
				<div class="rounded-xl border bg-card">
					<EmptyState
						icon={ServerIcon}
						title="No servers yet"
						description="Create your first Minecraft server and invite your friends."
					>
						<Button href={resolve('/servers/new')} class="glow-primary">
							<Plus class="size-4" />
							Create server
						</Button>
					</EmptyState>
				</div>
			{:else if visibleServers.length === 0}
				<div class="rounded-xl border bg-card">
					<EmptyState
						icon={Search}
						title="No matching servers"
						description="Try a different search or filter."
					/>
				</div>
			{:else}
				<div class="overflow-hidden rounded-xl border bg-card">
					<div class="divide-y">
						{#each visibleServers as server (server.id)}
							{@const busy = actioningId === server.id}
							<div
								class="group relative flex items-center gap-3 px-3 py-3 transition-colors hover:bg-accent/40 sm:px-4"
							>
								<ServerAvatar name={server.name} favicon={server.favicon} size="md" />
								<div class="min-w-0 flex-1">
									<div class="flex items-center gap-2">
										<a
											href={resolve(`/servers/${server.id}`)}
											class="truncate text-sm font-medium after:absolute after:inset-0"
										>
											{server.name}
										</a>
										<StatusBadge status={server.status} />
									</div>
									<div
										class="mt-0.5 flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground"
									>
										<span>{server.mcVersion}</span>
										{#if server.modLoader !== ModLoader.VANILLA && $loaderDisplayName(server.modLoader)}
											<span>·</span>
											<span>{$loaderDisplayName(server.modLoader)}</span>
										{/if}
										<span>·</span>
										<span class="font-mono">{connectionLabel(server)}</span>
										{#if server.status === ServerStatus.RUNNING && server.lastStarted}
											<span>·</span>
											<span>up {formatUptime(server.lastStarted, now)}</span>
										{/if}
										{#if (lobbyWaiting[server.id] ?? 0) > 0}
											<span>·</span>
											<span class="text-primary"
												>{lobbyWaiting[server.id]} waiting in the lobby</span
											>
										{/if}
									</div>
								</div>

								<div class="tabular hidden shrink-0 items-center gap-4 text-xs md:flex">
									{#if isUp(server.status)}
										<span
											class="flex w-14 items-center gap-1.5 text-muted-foreground"
											title="Players online"
										>
											<Users class="size-3.5 shrink-0" />
											{server.playersOnline || 0}/{server.maxPlayers}
										</span>
										<span
											class="flex w-12 items-center gap-1.5 {server.tps > 0
												? TONE_TEXT[tpsTone(server.tps)]
												: 'text-muted-foreground/40'}"
											title="Ticks per second"
										>
											<Zap class="size-3.5 shrink-0" />
											{server.tps > 0 ? server.tps.toFixed(1) : '--'}
										</span>
										<span
											class="hidden w-14 items-center gap-1.5 text-muted-foreground lg:flex"
											title="Memory in use"
										>
											<MemoryStick class="size-3.5 shrink-0" />
											{Number(server.memoryUsage) > 0
												? `${(Number(server.memoryUsage) / 1024).toFixed(1)}G`
												: '--'}
										</span>
									{:else}
										<span class="w-14"></span>
										<span class="w-12"></span>
										<span class="hidden w-14 lg:block"></span>
									{/if}
								</div>

								{#if server.status === ServerStatus.RUNNING}
									<div class="hidden xl:block">
										<MetricsSparkline serverId={server.id} />
									</div>
								{:else}
									<div class="hidden w-24 xl:block"></div>
								{/if}

								<div
									class="relative z-10 flex shrink-0 items-center gap-1 opacity-80 transition-opacity group-hover:opacity-100"
								>
									{#if canStart(server.status)}
										<Button
											variant="ghost"
											size="icon"
											class="size-8 text-status-ok hover:bg-status-ok/10 hover:text-status-ok"
											disabled={busy}
											title="Start"
											onclick={() => handleAction('start', server)}
										>
											<Play class="size-4" />
										</Button>
									{:else if canStop(server.status)}
										<Button
											variant="ghost"
											size="icon"
											class="size-8 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
											disabled={busy}
											title="Stop"
											onclick={() => handleAction('stop', server)}
										>
											<Square class="size-4" />
										</Button>
									{/if}
									{#if canRestart(server.status)}
										<Button
											variant="ghost"
											size="icon"
											class="hidden size-8 sm:inline-flex"
											disabled={busy}
											title="Restart"
											onclick={() => handleAction('restart', server)}
										>
											<RotateCw class="size-4" />
										</Button>
									{/if}
									<DropdownMenu>
										<DropdownMenuTrigger>
											{#snippet child({ props })}
												<Button
													{...props}
													variant="ghost"
													size="icon"
													class="size-8"
													disabled={busy}
													title="More"
												>
													<MoreHorizontal class="size-4" />
												</Button>
											{/snippet}
										</DropdownMenuTrigger>
										<DropdownMenuContent align="end">
											<DropdownMenuItem
												onclick={() => handleAction('recreate', server)}
												disabled={busy}
											>
												<RefreshCcw class="mr-2 size-4" />
												Recreate container
											</DropdownMenuItem>
											<DropdownMenuSeparator />
											<DropdownMenuItem
												variant="destructive"
												onclick={() => requestDelete(server)}
												disabled={busy}
											>
												<Trash2 class="mr-2 size-4" />
												Delete server
											</DropdownMenuItem>
										</DropdownMenuContent>
									</DropdownMenu>
								</div>
							</div>
						{/each}
					</div>
				</div>
				<p class="text-xs text-muted-foreground">
					{visibleServers.length} of {servers.length}
					{servers.length === 1 ? 'server' : 'servers'}
				</p>
			{/if}
		</div>
	</div>
</div>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete {deleteTarget?.name ?? 'server'}?"
	description={DELETE_WARNING}
	confirmLabel="Delete server"
	destructive
	onConfirm={confirmDelete}
/>
