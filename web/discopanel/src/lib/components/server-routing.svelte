<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { resolve } from '$app/paths';
	import { rpcClient, rpcErrorMessage, silentCallOptions } from '$lib/api/rpc-client';
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
	import type { Server, ProxyListener, Module } from '$lib/proto/discopanel/v1/storage_pb';
	import { ModuleProtocol, ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import {
		BaseUrlSource,
		NetworkOwnerKind,
		type GetServerRoutingResponse,
		type ProxyRoute
	} from '$lib/proto/discopanel/v1/proxy_pb';
	import { routeStateLabel, routeStateClass, routeStatsSummary } from '$lib/proxy-route';
	import { composeHostname, splitHostname } from '$lib/hostname';
	import { laneLabel } from '$lib/components/network/topology-data';

	let { server, active = true }: { server: Server; active?: boolean } = $props();

	let loading = $state(true);
	let saving = $state(false);
	let routingInfo = $state<GetServerRoutingResponse | null>(null);
	let allRoutes = $state<ProxyRoute[]>([]);
	let allModules = $state<Module[]>([]);
	let listeners = $state<ProxyListener[]>([]);
	let rawUsedPorts = $state<number[]>([]);

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
	let modulesById = $derived(new Map(allModules.map((m) => [m.id, m])));

	// Own held direct port stays out of the conflict map
	let usedPorts = $derived(
		Object.fromEntries(
			rawUsedPorts.filter((p) => original.useProxy || p !== original.port).map((p) => [p, true])
		)
	);

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
			const split = splitHostname(full, routing.effectiveBaseUrl || '');
			useProxy = full !== '';
			hostname = split.host;
			useBaseUrl = full ? split.useBase : true;
			listenerId = routing.proxyListenerId || '';
			if (!listenerId && listeners.length > 0) {
				listenerId = (listeners.find((l) => l.isDefault) ?? listeners[0]).id;
			}

			rawUsedPorts = portData?.usedPorts?.map((p) => p.port) ?? [];
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
			const [routeData, moduleData] = await Promise.all([
				rpcClient.proxy.getProxyRoutes({}, silentCallOptions),
				rpcClient.module.listModules({}, silentCallOptions).catch(() => null)
			]);
			allRoutes = routeData.routes;
			allModules = moduleData?.modules ?? [];
		} catch {
			// Route list is optional context
		}
	}

	// Route states keep themselves fresh while the tab shows
	$effect(() => {
		if (!active) return;
		const timer = setInterval(async () => {
			try {
				const routeData = await rpcClient.proxy.getProxyRoutes({}, silentCallOptions);
				allRoutes = routeData.routes;
			} catch {
				// Poll failures keep the last snapshot
			}
		}, 5000);
		return () => clearInterval(timer);
	});

	async function refreshAvailablePort() {
		try {
			const portData = await rpcClient.server.getNextAvailablePort({});
			port = portData.port;
			rawUsedPorts = portData.usedPorts?.map((p) => p.port) ?? [];
			portError = '';
		} catch {
			// Keeps the typed port on failure
		}
	}

	let composed = $derived(
		composeHostname(hostname, routingInfo?.effectiveBaseUrl ?? '', useBaseUrl)
	);

	let selectedListenerPort = $derived(
		listeners.find((l) => l.id === listenerId)?.port ?? routingInfo?.listenPort ?? 0
	);

	// Instant domains resolve on their own, custom ones need DNS
	let needsDns = $derived.by(() => {
		if (!routingInfo || !composed) return false;
		// Preset sslip domains carry the address in the name
		if (composed.endsWith('.sslip.io')) return false;
		if (routingInfo.baseUrlSource !== BaseUrlSource.AUTO) return true;
		return !composed.endsWith(routingInfo.effectiveBaseUrl);
	});

	// Conflicts need the same port, lane, and hostname
	let hostnameError = $derived.by(() => {
		if (!useProxy || !composed) return '';
		const hostnameRegex =
			/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/i;
		if (!hostnameRegex.test(composed)) return 'Invalid hostname format';
		const conflict = allRoutes.find(
			(route) =>
				route.protocol === ModuleProtocol.MINECRAFT &&
				route.listenPort === selectedListenerPort &&
				route.hostname.toLowerCase() === composed &&
				!(route.ownerKind === NetworkOwnerKind.SERVER && route.ownerId === server.id)
		);
		if (conflict) return `Hostname already routed for minecraft on port ${conflict.listenPort}`;
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
			toast.error(rpcErrorMessage(error, 'Failed to save routing configuration'));
		} finally {
			saving = false;
		}
	}

	function discardChanges() {
		useProxy = original.useProxy;
		const split = splitHostname(original.hostname, routingInfo?.effectiveBaseUrl ?? '');
		hostname = split.host;
		useBaseUrl = original.hostname ? split.useBase : true;
		listenerId = original.listenerId;
		port = original.port;
		portError = '';
	}

	let routeLive = $derived(!!routingInfo?.currentRoute && server.status === ServerStatus.RUNNING);

	// Direct ports read plain, proxied ones read as lanes
	function portProtoLabel(p: { protocol: ModuleProtocol; proxyEnabled: boolean }): string {
		const label = laneLabel(p.protocol);
		return p.proxyEnabled ? label : label.replace(' relay', '');
	}

	// Resolved identity for one route row
	function routeOwner(route: ProxyRoute): { label: string; serverId: string; self: boolean } {
		if (route.ownerKind === NetworkOwnerKind.SERVER) {
			if (route.ownerId === server.id) return { label: 'This server', serverId: '', self: true };
			return {
				label: serverNames.get(route.ownerId) ?? route.ownerId.slice(0, 8),
				serverId: route.ownerId,
				self: false
			};
		}
		if (route.ownerKind === NetworkOwnerKind.MODULE) {
			const module = modulesById.get(route.ownerId);
			if (!module) return { label: route.ownerId.slice(0, 8), serverId: '', self: false };
			const parent = module.serverId === server.id ? 'this server' : module.serverName;
			return {
				label: parent ? `${module.name} · ${parent}` : module.name,
				serverId: '',
				self: module.serverId === server.id
			};
		}
		return { label: '', serverId: '', self: false };
	}

	// Own routes pinned first in the shared route table
	let sortedRoutes = $derived(
		[...allRoutes].sort((a, b) => {
			const selfA = routeOwner(a).self ? 0 : 1;
			const selfB = routeOwner(b).self ? 0 : 1;
			return (
				selfA - selfB ||
				a.listenPort - b.listenPort ||
				a.hostname.localeCompare(b.hostname) ||
				a.protocol - b.protocol
			);
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
			description="Turn on the proxy to route this server by hostname."
		>
			{#if showSettingsLink}
				<Button href="{resolve('/settings')}?tab=network" variant="outline">
					Open network settings
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
				baseUrl={routingInfo.effectiveBaseUrl}
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

			{#if useProxy && composed && !hostnameError && needsDns}
				<div class="border-t px-4 py-3">
					<Alert>
						<AlertCircle class="size-4" />
						<AlertDescription>
							Point a DNS record for <code class="font-mono">{composed}</code> at this machine
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
				{#each server.additionalPorts || [] as extra (extra.name + extra.hostPort)}
					<div class="flex items-center justify-between px-4 py-2.5">
						<div class="min-w-0">
							<p class="truncate text-sm font-medium">{extra.name || 'Additional port'}</p>
							<p class="text-xs text-muted-foreground">
								container {extra.containerPort} · {portProtoLabel(extra)}{extra.proxyEnabled
									? ' · proxied'
									: ''}
							</p>
						</div>
						<span class="tabular font-mono text-sm">{extra.hostPort}</span>
					</div>
				{/each}
			</div>
		</section>

		{#if sortedRoutes.length > 0}
			<section class="overflow-hidden rounded-xl border bg-card">
				<header class="border-b bg-muted/30 px-4 py-3">
					<h3 class="text-sm font-semibold">Active routes</h3>
					<p class="mt-0.5 text-xs text-muted-foreground">Everything the proxy currently serves</p>
				</header>
				<div class="divide-y">
					{#each sortedRoutes as route (`${route.listenPort}:${route.protocol}:${route.hostname}:${route.ownerId}:${route.portName}`)}
						{@const owner = routeOwner(route)}
						{@const stats = routeStatsSummary(route)}
						<div
							class="flex items-center justify-between gap-3 px-4 py-2.5 {owner.self
								? 'bg-primary/[0.03]'
								: ''}"
						>
							<div class="min-w-0">
								<div class="flex flex-wrap items-center gap-2">
									<p class="truncate font-mono text-sm">{route.hostname || 'any hostname'}</p>
									<span
										class="rounded-full border px-1.5 py-px font-mono text-[10px] text-muted-foreground"
									>
										{laneLabel(route.protocol)}
									</span>
									<span class="font-mono text-xs text-muted-foreground">:{route.listenPort}</span>
								</div>
								{#if owner.self && route.ownerKind === NetworkOwnerKind.SERVER}
									<p class="text-xs font-medium text-primary">This server</p>
								{:else if owner.serverId}
									<a
										href={resolve(`/servers/${owner.serverId}`)}
										class="text-xs text-muted-foreground hover:text-foreground hover:underline"
									>
										{owner.label}
									</a>
								{:else if owner.label}
									<p
										class="text-xs {owner.self
											? 'font-medium text-primary'
											: 'text-muted-foreground'}"
									>
										{owner.label}
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
