<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import {
		PageHeader,
		StatTile,
		StatusBadge,
		ServerAvatar,
		EmptyState,
		AddressSelect
	} from '$lib/components/app';
	import MetricsSparkline from '$lib/components/metrics-sparkline.svelte';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { panelHost } from '$lib/utils/host';
	import { directAddresses, playerAddress } from '$lib/hostname';
	import { serversStore, sortServersByActivity, claimFullStats } from '$lib/stores/servers';
	import { currentUser, canAccessSettings } from '$lib/stores/auth';
	import {
		tpsTone,
		TONE_TEXT,
		TONE_BG,
		canStart,
		canStop,
		isUp,
		statusMeta
	} from '$lib/server-status';
	import { formatUptime, formatRelative } from '$lib/utils/time';
	import { formatBytes } from '$lib/utils';
	import { runServerAction } from '$lib/server-actions';
	import {
		Plus,
		Play,
		Square,
		ArrowRight,
		Terminal,
		Server as ServerIcon,
		Users,
		Zap,
		MemoryStick,
		HardDrive,
		AlertTriangle,
		MessageCircle,
		Github,
		BookOpen,
		Package,
		Settings,
		FileText,
		Loader2,
		Moon
	} from '@lucide/svelte';
	import { type Server, ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import type { GetHostnameSuggestionsResponse } from '$lib/proto/discopanel/v1/proxy_pb';

	let servers = $derived($serversStore);
	let user = $derived($currentUser);
	let showSettingsLink = $derived($canAccessSettings);
	let initialLoading = $state(true);
	let actioningId = $state('');
	let now = $state(new Date());
	let hostTotalMb = $state(0);
	let suggestions = $state<GetHostnameSuggestionsResponse | null>(null);

	onMount(() => {
		const release = claimFullStats();
		serversStore
			.fetchServers(servers.length > 0, true)
			.catch(() => {})
			.finally(() => (initialLoading = false));
		rpcClient.server
			.getHostMemory({})
			.then((r) => (hostTotalMb = Number(r.totalMb)))
			.catch(() => {});
		rpcClient.proxy
			.getHostnameSuggestions({ label: '' }, silentCallOptions)
			.then((r) => (suggestions = r))
			.catch(() => {});
		const clock = setInterval(() => (now = new Date()), 30000);
		return () => {
			release();
			clearInterval(clock);
		};
	});

	let stats = $derived.by(() => {
		const running = servers.filter((s) => s.status === ServerStatus.RUNNING);
		// Offline servers keep stale tps, trust only live ones
		const withTps = running.filter((s) => s.tps && s.tps > 0);
		return {
			total: servers.length,
			running: running.length,
			players: running.reduce((acc, s) => acc + (s.playersOnline || 0), 0),
			playerCapacity: running.reduce((acc, s) => acc + (s.maxPlayers || 0), 0),
			usedMemoryMb: running.reduce((acc, s) => acc + Number(s.memoryUsage || s.memory || 0), 0),
			totalMemoryMb: servers.reduce((acc, s) => acc + (s.memory || 0), 0),
			avgTps: withTps.length
				? withTps.reduce((acc, s) => acc + (s.tps || 0), 0) / withTps.length
				: 0
		};
	});

	// Any sampled server carries the shared volume numbers
	let hostDisk = $derived(servers.find((s) => Number(s.diskTotal) > 0));
	let diskPercent = $derived(
		hostDisk ? (Number(hostDisk.diskUsed) / Math.max(Number(hostDisk.diskTotal), 1)) * 100 : 0
	);
	// Host total anchors the bar, allocations as fallback
	let memDenomMb = $derived(hostTotalMb > 0 ? hostTotalMb : stats.totalMemoryMb);
	let memoryPercent = $derived(memDenomMb > 0 ? (stats.usedMemoryMb / memDenomMb) * 100 : 0);
	let allocatedPercent = $derived(memDenomMb > 0 ? (stats.totalMemoryMb / memDenomMb) * 100 : 0);

	function attentionReason(server: Server): string {
		if (server.status === ServerStatus.ERROR)
			return 'Crashed or failed to start, check the console';
		if (server.status === ServerStatus.UNHEALTHY) return 'Running but not responding normally';
		return `Struggling to keep up, ${server.tps.toFixed(1)} TPS`;
	}

	let attention = $derived(
		servers.filter(
			(s) =>
				s.status === ServerStatus.ERROR ||
				s.status === ServerStatus.UNHEALTHY ||
				(s.status === ServerStatus.RUNNING && s.tps > 0 && s.tps < 15)
		)
	);

	// Live grid shows anything up or on its way up
	let liveServers = $derived(
		sortServersByActivity([...servers]).filter(
			(s) =>
				isUp(s.status) ||
				s.status === ServerStatus.STARTING ||
				s.status === ServerStatus.PROVISIONING ||
				s.status === ServerStatus.PAUSED
		)
	);

	// Offline servers with history, most recently seen first
	let recentlyOffline = $derived(
		servers
			.filter((s) => s.status === ServerStatus.STOPPED)
			.sort((a, b) => Number(b.lastStarted?.seconds ?? 0n) - Number(a.lastStarted?.seconds ?? 0n))
			.slice(0, 4)
	);

	let greeting = $derived.by(() => {
		const hour = now.getHours();
		const timeOfDay =
			hour < 5 ? 'evening' : hour < 12 ? 'morning' : hour < 18 ? 'afternoon' : 'evening';
		return user ? `Good ${timeOfDay}, ${user.username}` : `Good ${timeOfDay}`;
	});

	let headline = $derived.by(() => {
		if (servers.length === 0) return 'Ready when you are';
		if (stats.running === 0) return 'Everything is quiet, all servers are stopped';
		const players = stats.players === 1 ? '1 player' : `${stats.players} players`;
		return `${stats.running} of ${stats.total} ${stats.total === 1 ? 'server' : 'servers'} running · ${players} online`;
	});

	// Routed names else every detected direct address
	function addresses(server: Server): string[] {
		if (server.proxyHostnames.length > 0) return server.proxyHostnames;
		if (suggestions) {
			const list = directAddresses(
				server.port,
				suggestions.lanIp,
				suggestions.publicIp,
				suggestions.suggestions.map((s) => s.hostname)
			);
			if (list.length > 0) return list;
		}
		return [playerAddress(panelHost(), server.port)];
	}

	async function power(server: Server, start: boolean) {
		actioningId = server.id;
		await runServerAction(start ? 'start' : 'stop', server);
		actioningId = '';
	}
</script>

<svelte:head>
	<title>Home · DiscoPanel</title>
</svelte:head>

{#snippet offlineRow(server: Server)}
	{@const busy = actioningId === server.id}
	<div
		class="group relative flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-accent/40"
	>
		<ServerAvatar name={server.name} favicon={server.favicon} size="sm" />
		<div class="min-w-0 flex-1">
			<a
				href={resolve(`/servers/${server.id}`)}
				class="truncate text-sm font-medium after:absolute after:inset-0"
			>
				{server.name}
			</a>
			<p class="text-xs text-muted-foreground">
				{server.lastStarted
					? `last online ${formatRelative(server.lastStarted, now)}`
					: 'never started'}
				· {server.mcVersion}
			</p>
		</div>
		<div class="relative z-10 shrink-0">
			{#if busy}
				<Loader2 class="mx-2 size-4 animate-spin text-muted-foreground" />
			{:else if canStart(server.status)}
				<Button
					variant="ghost"
					size="icon"
					class="size-7 text-status-ok hover:bg-status-ok/10 hover:text-status-ok"
					title="Start"
					onclick={() => power(server, true)}
				>
					<Play class="size-4" />
				</Button>
			{/if}
		</div>
	</div>
{/snippet}

<div class="border-b bg-card/40">
	<div class="mx-auto w-full max-w-6xl px-4 pt-5 sm:px-6 2xl:max-w-7xl">
		<PageHeader title={greeting} description={headline} class="pb-4" />
	</div>
</div>

<div class="mx-auto w-full max-w-6xl space-y-5 p-4 sm:p-6 2xl:max-w-7xl">
	{#if initialLoading && servers.length === 0}
		<div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
			{#each Array(4) as _, i (i)}
				<Skeleton class="h-24 rounded-lg" />
			{/each}
		</div>
		<div class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_17rem]">
			<Skeleton class="h-72 rounded-xl" />
			<Skeleton class="h-72 rounded-xl" />
		</div>
	{:else if servers.length === 0}
		<div class="rounded-xl border bg-card">
			<EmptyState
				icon={ServerIcon}
				title="Welcome to DiscoPanel"
				description="Spin up your first Minecraft server, or browse modpacks to find something your friends will love."
				class="py-20"
			>
				<Button href={resolve('/servers/new')} class="glow-primary">
					<Plus class="size-4" />
					Create your first server
				</Button>
				<Button href={resolve('/modpacks')} variant="outline">
					<Package class="size-4" />
					Browse modpacks
				</Button>
			</EmptyState>
		</div>
	{:else}
		<div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
			<StatTile
				label="Servers"
				icon={ServerIcon}
				value={String(stats.total)}
				sub="{stats.running} running"
			/>
			<StatTile
				label="Players online"
				icon={Users}
				value={String(stats.players)}
				sub={stats.running > 0
					? `of ${stats.playerCapacity} slots on running servers`
					: 'no servers running'}
			/>
			<StatTile
				label="Memory"
				icon={MemoryStick}
				value="{(stats.usedMemoryMb / 1024).toFixed(1)} GB"
				sub="committed of {(stats.totalMemoryMb / 1024).toFixed(1)} GB allocated"
			/>
			<StatTile
				label="Avg TPS"
				icon={Zap}
				value={stats.avgTps > 0 ? stats.avgTps.toFixed(1) : '--'}
				valueClass={stats.avgTps > 0 ? TONE_TEXT[tpsTone(stats.avgTps)] : ''}
				sub={stats.avgTps > 0 ? 'across running servers' : 'no tick data yet'}
			/>
		</div>

		{#if attention.length > 0}
			<section class="overflow-hidden rounded-xl border border-status-warn/30 bg-card">
				<header
					class="flex items-center gap-2 border-b border-status-warn/20 bg-status-warn/5 px-4 py-2.5"
				>
					<AlertTriangle class="size-4 text-status-warn" />
					<h2 class="text-sm font-semibold">
						Needs attention
						<span class="ml-1 font-normal text-muted-foreground">{attention.length}</span>
					</h2>
				</header>
				<div class="divide-y">
					{#each attention as server (server.id)}
						{@const busy = actioningId === server.id}
						<div
							class="group relative flex items-center gap-3 px-4 py-3 transition-colors hover:bg-accent/40"
						>
							<ServerAvatar name={server.name} favicon={server.favicon} size="md" />
							<div class="min-w-0 flex-1">
								<div class="flex flex-wrap items-center gap-2">
									<a
										href={resolve(`/servers/${server.id}`)}
										class="truncate text-sm font-medium after:absolute after:inset-0"
									>
										{server.name}
									</a>
									<StatusBadge status={server.status} />
								</div>
								<p class="mt-0.5 text-xs text-muted-foreground">{attentionReason(server)}</p>
							</div>
							<div class="relative z-10 flex shrink-0 items-center gap-1">
								<Button
									variant="outline"
									size="sm"
									class="h-7 gap-1.5 text-xs"
									href="{resolve(`/servers/${server.id}`)}?tab=console"
								>
									<Terminal class="size-3.5" />
									Console
								</Button>
								{#if busy}
									<Loader2 class="mx-2 size-4 animate-spin text-muted-foreground" />
								{:else if canStop(server.status)}
									<Button
										variant="ghost"
										size="icon"
										class="size-7 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
										title="Stop"
										onclick={() => power(server, false)}
									>
										<Square class="size-4" />
									</Button>
								{:else if canStart(server.status)}
									<Button
										variant="ghost"
										size="icon"
										class="size-7 text-status-ok hover:bg-status-ok/10 hover:text-status-ok"
										title="Start"
										onclick={() => power(server, true)}
									>
										<Play class="size-4" />
									</Button>
								{/if}
							</div>
						</div>
					{/each}
				</div>
			</section>
		{/if}

		<div class="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_17rem]">
			<div class="min-w-0 space-y-4">
				{#if liveServers.length > 0}
					<section>
						<div class="mb-2.5 flex items-baseline justify-between gap-2">
							<h2 class="flex items-center gap-2 text-sm font-semibold">
								<span class="glow-ok size-2 rounded-full bg-status-ok" aria-hidden="true"></span>
								Live now
							</h2>
							<a
								href={resolve('/servers')}
								class="inline-flex items-center gap-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
							>
								Manage all {stats.total}
								<ArrowRight class="size-3" />
							</a>
						</div>
						<div class="grid gap-3 sm:grid-cols-2">
							{#each liveServers as server (server.id)}
								{@const busy = actioningId === server.id}
								{@const meta = statusMeta(server.status)}
								{@const maxPlayers = server.maxPlayersSlp || server.maxPlayers}
								{@const fill =
									maxPlayers > 0 ? ((server.playersOnline || 0) / maxPlayers) * 100 : 0}
								<div
									class="group relative overflow-hidden rounded-xl border bg-card p-4 transition-all hover:border-primary/30 hover:shadow-sm"
								>
									<div class="flex items-start gap-3">
										<ServerAvatar name={server.name} favicon={server.favicon} size="md" />
										<div class="min-w-0 flex-1">
											<div class="flex items-center justify-between gap-2">
												<a
													href={resolve(`/servers/${server.id}`)}
													class="truncate text-sm font-semibold after:absolute after:inset-0"
												>
													{server.name}
												</a>
												<div class="relative z-10 flex shrink-0 items-center gap-1">
													{#if busy}
														<Loader2 class="size-4 animate-spin text-muted-foreground" />
													{:else if canStop(server.status)}
														<Button
															variant="ghost"
															size="icon"
															class="size-7 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 hover:bg-status-danger/10 hover:text-status-danger focus-visible:opacity-100"
															title="Stop"
															onclick={() => power(server, false)}
														>
															<Square class="size-3.5" />
														</Button>
													{/if}
												</div>
											</div>
											<div class="mt-0.5 flex items-center gap-1.5 text-xs {TONE_TEXT[meta.tone]}">
												{#if server.status === ServerStatus.PAUSED}
													<Moon class="size-3" />
												{/if}
												{meta.label}
												{#if server.status === ServerStatus.RUNNING && server.lastStarted}
													<span class="text-muted-foreground"
														>· up {formatUptime(server.lastStarted, now)}</span
													>
												{/if}
											</div>
										</div>
									</div>

									<div class="relative z-10 mt-3">
										<AddressSelect addresses={addresses(server)} />
									</div>

									<div class="mt-3 flex items-end justify-between gap-3">
										<div class="min-w-0 flex-1 space-y-1.5">
											<div class="flex items-center justify-between text-xs">
												<span class="flex items-center gap-1.5 text-muted-foreground">
													<Users class="size-3" />
													Players
												</span>
												<span class="tabular font-medium">
													{server.playersOnline || 0}<span class="text-muted-foreground"
														>/{maxPlayers}</span
													>
												</span>
											</div>
											<div class="h-1 overflow-hidden rounded-full bg-muted">
												<div
													class="h-full rounded-full transition-all duration-500 {TONE_BG[
														fill >= 90 ? 'danger' : fill >= 75 ? 'busy' : 'ok'
													]}"
													style="width: {Math.min(fill, 100)}%"
												></div>
											</div>
										</div>
										{#if isUp(server.status) && server.tps > 0}
											<span
												class="tabular flex shrink-0 items-center gap-1 text-xs font-medium {TONE_TEXT[
													tpsTone(server.tps)
												]}"
												title="Ticks per second"
											>
												<Zap class="size-3" />
												{server.tps.toFixed(1)}
											</span>
										{/if}
										{#if server.status === ServerStatus.RUNNING}
											<div class="shrink-0">
												<MetricsSparkline serverId={server.id} />
											</div>
										{/if}
									</div>
								</div>
							{/each}
						</div>
					</section>
				{:else}
					<section class="rounded-xl border bg-card">
						<EmptyState
							icon={Moon}
							title="All quiet"
							description="No servers are running right now. Start one below and it will show up here live."
							class="py-10"
						/>
						{#if recentlyOffline.length > 0}
							<div class="border-t">
								<div class="divide-y">
									{#each recentlyOffline as server (server.id)}
										{@render offlineRow(server)}
									{/each}
								</div>
							</div>
						{/if}
					</section>
				{/if}

				{#if liveServers.length > 0 && recentlyOffline.length > 0}
					<section class="overflow-hidden rounded-xl border bg-card">
						<header class="flex items-center justify-between border-b px-4 py-2.5">
							<h2 class="text-sm font-semibold">Waking these up?</h2>
							<a
								href={resolve('/servers')}
								class="inline-flex items-center gap-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
							>
								All servers
								<ArrowRight class="size-3" />
							</a>
						</header>
						<div class="divide-y">
							{#each recentlyOffline as server (server.id)}
								{@render offlineRow(server)}
							{/each}
						</div>
					</section>
				{/if}
			</div>

			<div class="space-y-4">
				<div class="rounded-xl border bg-card p-4">
					<h2 class="mb-3 text-sm font-semibold">Host resources</h2>
					{#if stats.totalMemoryMb > 0 || hostDisk}
						<div class="space-y-4">
							{#if stats.totalMemoryMb > 0}
								<div>
									<div class="mb-1.5 flex items-center justify-between text-xs">
										<span class="stat-label flex items-center gap-1.5">
											<MemoryStick class="size-3.5" />
											Memory
										</span>
										<span class="tabular text-muted-foreground">
											{(stats.usedMemoryMb / 1024).toFixed(1)} / {hostTotalMb > 0
												? Math.round(hostTotalMb / 1024)
												: (stats.totalMemoryMb / 1024).toFixed(1)} GB
										</span>
									</div>
									<div
										class="relative h-1.5 overflow-hidden rounded-full bg-muted"
										title="solid committed, light allocated"
									>
										<div
											class="absolute inset-y-0 left-0 rounded-full bg-primary/25 transition-all duration-500"
											style="width: {Math.min(allocatedPercent, 100)}%"
										></div>
										<div
											class="absolute inset-y-0 left-0 rounded-full transition-all duration-500 {TONE_BG[
												memoryPercent >= 90 ? 'danger' : memoryPercent >= 75 ? 'busy' : 'ok'
											]}"
											style="width: {Math.min(memoryPercent, 100)}%"
										></div>
									</div>
								</div>
							{/if}
							{#if hostDisk}
								<div>
									<div class="mb-1.5 flex items-center justify-between text-xs">
										<span class="stat-label flex items-center gap-1.5">
											<HardDrive class="size-3.5" />
											Disk
										</span>
										<span class="tabular text-muted-foreground">
											{formatBytes(Number(hostDisk.diskUsed), 0)} / {formatBytes(
												Number(hostDisk.diskTotal),
												0
											)}
										</span>
									</div>
									<div class="h-1.5 overflow-hidden rounded-full bg-muted">
										<div
											class="h-full rounded-full transition-all duration-500 {TONE_BG[
												diskPercent >= 90 ? 'danger' : diskPercent >= 75 ? 'busy' : 'ok'
											]}"
											style="width: {Math.min(diskPercent, 100)}%"
										></div>
									</div>
								</div>
							{/if}
						</div>
					{:else}
						<p class="text-xs text-muted-foreground">
							Start a server to see memory and disk usage.
						</p>
					{/if}
				</div>

				<div class="rounded-xl border bg-card p-4">
					<h2 class="mb-3 text-sm font-semibold">Quick actions</h2>
					<div class="space-y-1">
						<a
							href={resolve('/servers/new')}
							class="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm transition-colors hover:bg-accent"
						>
							<Plus class="size-4 text-primary" />
							Create a server
						</a>
						<a
							href={resolve('/modpacks')}
							class="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm transition-colors hover:bg-accent"
						>
							<Package class="size-4 text-muted-foreground" />
							Browse modpacks
						</a>
						{#if showSettingsLink}
							<a
								href={resolve('/settings')}
								class="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm transition-colors hover:bg-accent"
							>
								<Settings class="size-4 text-muted-foreground" />
								Panel settings
							</a>
						{/if}
						<a
							href={resolve('/docs/api')}
							class="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm transition-colors hover:bg-accent"
						>
							<FileText class="size-4 text-muted-foreground" />
							API reference
						</a>
					</div>
				</div>

				<div class="rounded-xl border bg-card p-4">
					<h2 class="mb-3 text-sm font-semibold">Community & help</h2>
					<div class="space-y-1">
						<a
							href="https://discord.gg/6Z9yKTbsrP"
							target="_blank"
							rel="noreferrer"
							class="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
						>
							<MessageCircle class="size-4" />
							Discord
						</a>
						<a
							href="https://github.com/discohaus/discopanel/issues"
							target="_blank"
							rel="noreferrer"
							class="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
						>
							<Github class="size-4" />
							Report an issue
						</a>
						<a
							href="https://github.com/discohaus/discopanel"
							target="_blank"
							rel="noreferrer"
							class="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
						>
							<BookOpen class="size-4" />
							Documentation
						</a>
					</div>
				</div>
			</div>
		</div>
	{/if}
</div>
