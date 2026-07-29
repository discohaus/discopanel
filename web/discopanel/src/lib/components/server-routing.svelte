<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { resolve } from '$app/paths';
	import { rpcClient } from '$lib/api/rpc-client';
	import { toast } from 'svelte-sonner';
	import { Button } from '$lib/components/ui/button';
	import { Alert, AlertDescription } from '$lib/components/ui/alert';
	import { Badge } from '$lib/components/ui/badge';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { EmptyState } from '$lib/components/app';
	import ConnectivityCard from '$lib/components/connectivity-card.svelte';
	import { serversStore } from '$lib/stores/servers';
	import { canAccessSettings } from '$lib/stores/auth';
	import {
		Loader2,
		Save,
		AlertCircle,
		Network,
		ArrowUpRight,
		RotateCcw,
		RefreshCw
	} from '@lucide/svelte';
	import type { Server, ProxyListener } from '$lib/proto/discopanel/v1/storage_pb';
	import { ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import type { GetServerRoutingResponse, ProxyRoute } from '$lib/proto/discopanel/v1/proxy_pb';
	import { routeStateLabel, routeStateClass, routeStatsSummary } from '$lib/proxy-route';
	import { composeHostname, splitHostname } from '$lib/hostname';

	let { server, active = true }: { server: Server; active?: boolean } = $props();

	let loading = $state(true);
	let saving = $state(false);
	let routingInfo = $state<GetServerRoutingResponse | null>(null);
	let allRoutes = $state<ProxyRoute[]>([]);
	let listeners = $state<ProxyListener[]>([]);
	let usedPorts = $state<Record<number, boolean>>({});

	let useProxy = $state(false);
	let hostname = $state('');
	let useBaseUrl = $state(true);
	let listenerId = $state('');
	let port = $state(25565);
	let portError = $state('');
	let original = $state({ useProxy: false, hostname: '', listenerId: '', port: 25565 });

	let showSettingsLink = $derived($canAccessSettings);

	// Resolves route server ids to friendly names
	let serverNames = $derived(new Map($serversStore.map((s) => [s.id, s.name])));

	// Reload whenever the tab shows a different server
	let loadedServerId = $state('');
	$effect(() => {
		if (active && server.id !== loadedServerId) {
			loadedServerId = server.id;
			untrack(() => {
				loading = true;
				loadAll();
			});
		}
	});

	onMount(() => {
		if (active && server.id !== loadedServerId) {
			loadedServerId = server.id;
			loadAll();
		}
	});

	async function loadAll() {
		await Promise.all([loadRoutingInfo(), loadAllRoutes()]);
	}

	async function loadRoutingInfo() {
		try {
			loading = true;
			const [routing, listenerData, portData] = await Promise.all([
				rpcClient.proxy.getServerRouting({ serverId: server.id }),
				rpcClient.proxy.getProxyListeners({}).catch(() => null),
				rpcClient.server.getNextAvailablePort({}).catch(() => null)
			]);
			routingInfo = routing;
			listeners =
				listenerData?.listeners
					.map((l) => l.listener)
					.filter((l): l is ProxyListener => l !== undefined && l.enabled) ?? [];

			const full = routing.proxyHostname || '';
			const split = splitHostname(full, routing.baseUrl || '');
			useProxy = full !== '';
			hostname = split.host;
			useBaseUrl = full ? split.useBase : true;
			listenerId = routing.proxyListenerId || '';
			if (!listenerId && listeners.length > 0) {
				listenerId = (listeners.find((l) => l.isDefault) ?? listeners[0]).id;
			}

			// Own direct port stays out of the conflict map
			usedPorts = Object.fromEntries(
				portData?.usedPorts
					?.filter((p) => useProxy || p.port !== server.port)
					.map((p) => [p.port, true]) ?? []
			);
			// Proxied rows carry no usable host port yet
			port = useProxy ? portData?.port || 25565 : server.port;
			portError = '';
			original = { useProxy, hostname: full, listenerId, port: server.port };
		} catch {
			toast.error('Failed to load routing information');
		} finally {
			loading = false;
		}
	}

	async function loadAllRoutes() {
		try {
			const response = await rpcClient.proxy.getProxyRoutes({});
			allRoutes = response.routes;
		} catch {
			// Route list is optional context
		}
	}

	async function refreshAvailablePort() {
		try {
			const portData = await rpcClient.server.getNextAvailablePort({});
			port = portData.port;
			usedPorts = Object.fromEntries(
				portData.usedPorts
					?.filter((p) => original.useProxy || p.port !== original.port)
					.map((p) => [p.port, true]) ?? []
			);
			portError = '';
		} catch {
			// Keeps the typed port on failure
		}
	}

	let composed = $derived(composeHostname(hostname, routingInfo?.baseUrl ?? '', useBaseUrl));

	let hostnameError = $derived.by(() => {
		if (!useProxy || !composed) return '';
		const hostnameRegex =
			/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/i;
		if (!hostnameRegex.test(composed)) return 'Invalid hostname format';
		const conflict = allRoutes.find(
			(route) => route.hostname.toLowerCase() === composed && route.serverId !== server.id
		);
		if (conflict) return 'Hostname already in use by another server';
		return '';
	});

	let modeChanged = $derived(useProxy !== original.useProxy);
	let hasChanges = $derived(
		modeChanged ||
			(useProxy && (composed !== original.hostname || listenerId !== original.listenerId)) ||
			(!useProxy && port !== original.port)
	);
	// Everything except a pure hostname edit rebuilds the container
	let willRecreate = $derived(
		!!server.containerId &&
			(modeChanged ||
				(useProxy && original.useProxy && listenerId !== original.listenerId) ||
				(!useProxy && !original.useProxy && port !== original.port))
	);
	let canSave = $derived(
		hasChanges &&
			!saving &&
			!hostnameError &&
			!(useProxy && !hostname.trim()) &&
			!(!useProxy && !!portError)
	);

	async function saveRouting() {
		saving = true;
		try {
			await rpcClient.proxy.updateServerRouting({
				serverId: server.id,
				proxyHostname: useProxy ? composed : '',
				proxyListenerId: useProxy ? listenerId : '',
				port: useProxy ? undefined : port
			});
			toast.success(useProxy ? 'Routing saved' : 'Direct connection saved');
			await loadAll();
		} catch (error: unknown) {
			if (error instanceof Error && error.message.includes('Conflict')) {
				toast.error('Hostname already in use by another server');
			} else {
				toast.error('Failed to save routing configuration');
			}
		} finally {
			saving = false;
		}
	}

	function discardChanges() {
		useProxy = original.useProxy;
		const split = splitHostname(original.hostname, routingInfo?.baseUrl ?? '');
		hostname = split.host;
		useBaseUrl = original.hostname ? split.useBase : true;
		listenerId = original.listenerId;
		port = original.port;
		portError = '';
	}

	let routeLive = $derived(
		!!routingInfo?.currentRoute && server.status === ServerStatus.RUNNING
	);

	// Own route pinned first in the shared route table
	let sortedRoutes = $derived(
		[...allRoutes].sort((a, b) => {
			if (a.serverId === server.id) return -1;
			if (b.serverId === server.id) return 1;
			return a.hostname.localeCompare(b.hostname);
		})
	);
</script>

{#if loading}
	<div class="space-y-4">
		<Skeleton class="h-44 rounded-xl" />
		<Skeleton class="h-56 rounded-xl" />
	</div>
{:else if !routingInfo?.proxyEnabled}
	<div class="rounded-xl border bg-card">
		<EmptyState
			icon={Network}
			title="Proxy routing is disabled"
			description="With the proxy enabled, players connect through a hostname like play.example.com instead of a port number."
		>
			{#if showSettingsLink}
				<Button href="{resolve('/settings')}?tab=routing" variant="outline">
					Open routing settings
					<ArrowUpRight class="size-3.5" />
				</Button>
			{/if}
		</EmptyState>
	</div>
{:else}
	<div class="space-y-4">
		<section class="overflow-hidden rounded-xl border bg-card">
			<header class="border-b bg-muted/30 px-4 py-3">
				<h3 class="text-sm font-semibold">Connectivity</h3>
				<p class="mt-0.5 text-xs text-muted-foreground">How players reach this server</p>
			</header>

			<ConnectivityCard
				proxyEnabled={routingInfo.proxyEnabled}
				baseUrl={routingInfo.baseUrl}
				{listeners}
				serverName={server.name}
				routeActive={hasChanges ? null : routeLive}
				disabled={saving}
				{usedPorts}
				{hostnameError}
				bind:useProxy
				bind:hostname
				bind:useBaseUrl
				bind:listenerId
				bind:port
				bind:portError
				onAutoAssignPort={refreshAvailablePort}
			/>

			{#if useProxy && composed && !hostnameError}
				<div class="border-t px-4 py-3">
					<Alert>
						<AlertCircle class="size-4" />
						<AlertDescription>
							<p class="mb-1 font-medium">DNS configuration required</p>
							<p class="text-sm">
								Add a DNS record pointing
								<code class="font-mono">{composed}</code> to this machine's IP address.
							</p>
						</AlertDescription>
					</Alert>
				</div>
			{/if}

			{#if hasChanges}
				<div class="flex flex-wrap items-center justify-between gap-3 border-t px-4 py-3">
					{#if willRecreate}
						<p class="flex items-center gap-1.5 text-xs text-muted-foreground">
							<RefreshCw class="size-3.5 shrink-0" />
							Saving recreates the server container to apply the new network setup
						</p>
					{:else}
						<span></span>
					{/if}
					<div class="flex items-center gap-2">
						<Button variant="outline" size="sm" onclick={discardChanges} disabled={saving}>
							<RotateCcw class="size-4" />
							Discard
						</Button>
						<Button size="sm" onclick={saveRouting} disabled={!canSave}>
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
		</section>

		<section class="overflow-hidden rounded-xl border bg-card">
			<header class="flex items-baseline justify-between gap-2 border-b bg-muted/30 px-4 py-3">
				<div>
					<h3 class="text-sm font-semibold">Exposed ports</h3>
					<p class="mt-0.5 text-xs text-muted-foreground">
						Ports this server publishes on the host
					</p>
				</div>
				<a
					href="{resolve(`/servers/${server.id}`)}?tab=settings#network"
					class="shrink-0 text-xs text-primary hover:underline"
				>
					Manage ports
				</a>
			</header>
			<div class="divide-y">
				<div class="flex items-center justify-between px-4 py-2.5">
					<div class="min-w-0">
						<p class="text-sm font-medium">Game port</p>
						<p class="text-xs text-muted-foreground">
							{original.useProxy ? 'Reached through the proxy listener' : 'Primary Minecraft port'}
						</p>
					</div>
					<span class="tabular font-mono text-sm">{server.port}</span>
				</div>
				{#each server.additionalPorts || [] as port (port.name + port.hostPort)}
					<div class="flex items-center justify-between px-4 py-2.5">
						<div class="min-w-0">
							<p class="truncate text-sm font-medium">{port.name || 'Additional port'}</p>
							<p class="text-xs text-muted-foreground">
								container {port.containerPort} · {port.protocol || 'tcp'}
							</p>
						</div>
						<span class="tabular font-mono text-sm">{port.hostPort}</span>
					</div>
				{/each}
			</div>
		</section>

		{#if sortedRoutes.length > 0}
			<section class="overflow-hidden rounded-xl border bg-card">
				<header class="border-b bg-muted/30 px-4 py-3">
					<h3 class="text-sm font-semibold">Active routes</h3>
					<p class="mt-0.5 text-xs text-muted-foreground">
						Every hostname the proxy currently serves
					</p>
				</header>
				<div class="divide-y">
					{#each sortedRoutes as route (route.serverId)}
						{@const isSelf = route.serverId === server.id}
						{@const stats = routeStatsSummary(route)}
						<div
							class="flex items-center justify-between gap-3 px-4 py-2.5 {isSelf
								? 'bg-primary/[0.03]'
								: ''}"
						>
							<div class="min-w-0">
								<p class="truncate font-mono text-sm">{route.hostname}</p>
								{#if isSelf}
									<p class="text-xs font-medium text-primary">This server</p>
								{:else if serverNames.get(route.serverId)}
									<a
										href={resolve(`/servers/${route.serverId}`)}
										class="text-xs text-muted-foreground hover:text-foreground hover:underline"
									>
										{serverNames.get(route.serverId)}
									</a>
								{:else}
									<p class="font-mono text-xs text-muted-foreground">
										{route.serverId.slice(0, 8)}
									</p>
								{/if}
								{#if stats}
									<p class="text-xs text-muted-foreground tabular-nums">{stats}</p>
								{/if}
							</div>
							<Badge variant="outline" class="text-xs {routeStateClass(route)}">
								{routeStateLabel(route)}
							</Badge>
						</div>
					{/each}
				</div>
			</section>
		{/if}
	</div>
{/if}
