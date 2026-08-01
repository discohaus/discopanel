<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import {
		SvelteFlow,
		Background,
		Controls,
		MiniMap,
		type Node,
		type Edge,
		type NodeTypes
	} from '@xyflow/svelte';
	import '@xyflow/svelte/dist/style.css';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { registerRefresh } from '$lib/stores/refresh';
	import { serversStore } from '$lib/stores/servers';
	import {
		NetworkOwnerKind,
		NetworkReservationKind,
		type GetNetworkTopologyResponse,
		type ProxyListenerWithCount
	} from '$lib/proto/discopanel/v1/proxy_pb';
	import { NetworkTransport, type Module } from '$lib/proto/discopanel/v1/storage_pb';
	import { Button } from '$lib/components/ui/button';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { EmptyState } from '$lib/components/app';
	import { PaneGroup, Pane, PaneResizer } from 'paneforge';
	import { Network, RotateCcw } from '@lucide/svelte';
	import { buildGraph, laneLabel, type ExposedPort, type Selection } from './topology-data';
	import SourceNode from './nodes/source-node.svelte';
	import EntryNode from './nodes/entry-node.svelte';
	import ListenerNode from './nodes/listener-node.svelte';
	import LaneNode from './nodes/lane-node.svelte';
	import RouteNode from './nodes/route-node.svelte';
	import BackendNode from './nodes/backend-node.svelte';
	import ActionNode from './nodes/action-node.svelte';
	import ProxyInspector from './inspectors/proxy-inspector.svelte';
	import PanelInspector from './inspectors/panel-inspector.svelte';
	import ListenerInspector from './inspectors/listener-inspector.svelte';
	import LaneInspector from './inspectors/lane-inspector.svelte';
	import EntryInspector from './inspectors/entry-inspector.svelte';
	import RouteInspector from './inspectors/route-inspector.svelte';
	import BackendInspector from './inspectors/backend-inspector.svelte';
	import DisableProxyDialog from './disable-proxy-dialog.svelte';

	const nodeTypes: NodeTypes = {
		source: SourceNode,
		entry: EntryNode,
		listener: ListenerNode,
		lane: LaneNode,
		route: RouteNode,
		backend: BackendNode,
		action: ActionNode
	};

	let loading = $state(true);
	let loadError = $state(false);
	let topology = $state<GetNetworkTopologyResponse | null>(null);
	let listeners = $state<ProxyListenerWithCount[]>([]);
	let modules = $state<Module[]>([]);
	let baseUrl = $state('');
	let strictHttps = $state(false);
	let selection = $state<Selection>({ kind: 'overview' });
	let disableOpen = $state(false);

	let nodes = $state.raw<Node[]>([]);
	let edges = $state.raw<Edge[]>([]);

	let dnsProviderName = $state('');
	let dnsProviderFor = '';

	// Graph rebuilds whenever any input changes
	$effect(() => {
		if (!topology) return;
		const graph = buildGraph(
			topology,
			listeners,
			$serversStore,
			modules,
			selection,
			dnsProviderName
		);
		nodes = graph.nodes;
		edges = graph.edges;
	});

	// Provider detection follows the effective base domain
	$effect(() => {
		const domain = topology?.effectiveBaseUrl ?? '';
		if (!domain || domain.endsWith('.sslip.io') || dnsProviderFor === domain) return;
		dnsProviderFor = domain;
		rpcClient.proxy
			.getDnsSetup({ domain }, silentCallOptions)
			.then((res) => {
				dnsProviderName = res.providerFound ? res.providerName : '';
			})
			.catch(() => {
				dnsProviderName = '';
			});
	});

	let usedPorts = $derived(topology ? [...new Set(topology.reservations.map((r) => r.port))] : []);
	let hasProxiedWorkloads = $derived(
		topology?.reservations.some(
			(r) => r.kind === NetworkReservationKind.ROUTED || r.kind === NetworkReservationKind.RELAY
		) ?? false
	);
	let hasAnything = $derived($serversStore.length > 0 || modules.length > 0);
	let routeCount = $derived(topology?.routes.length ?? 0);

	onMount(() => {
		loadAll();
		const interval = setInterval(pollSilently, 10_000);
		const unregister = registerRefresh(loadAll);
		return () => {
			clearInterval(interval);
			unregister();
		};
	});

	async function loadAll() {
		try {
			const [topo, lst, mods, status] = await Promise.all([
				rpcClient.proxy.getNetworkTopology({}),
				rpcClient.proxy.getProxyListeners({}),
				rpcClient.module.listModules({}),
				rpcClient.proxy.getProxyStatus({})
			]);
			topology = topo;
			listeners = lst.listeners;
			modules = mods.modules;
			baseUrl = status.baseUrl;
			strictHttps = status.strictHttps;
			loadError = false;
		} catch {
			loadError = true;
		} finally {
			loading = false;
		}
	}

	async function pollSilently() {
		try {
			const [topo, lst, mods, status] = await Promise.all([
				rpcClient.proxy.getNetworkTopology({}, silentCallOptions),
				rpcClient.proxy.getProxyListeners({}, silentCallOptions),
				rpcClient.module.listModules({}, silentCallOptions),
				rpcClient.proxy.getProxyStatus({}, silentCallOptions)
			]);
			topology = topo;
			listeners = lst.listeners;
			modules = mods.modules;
			baseUrl = status.baseUrl;
			strictHttps = status.strictHttps;
			loadError = false;
		} catch {
			// Silent polls swallow transient failures
		}
	}

	function retry() {
		loading = true;
		loadError = false;
		loadAll();
	}

	// Selection rides on node data, no id parsing
	function selectNode(node: Node) {
		const sel = (node.data as { selection?: Selection }).selection;
		if (sel) selection = sel;
	}

	// Inspector context resolved from the current selection
	let selectedListener = $derived.by(() => {
		if (selection.kind !== 'listener') return null;
		const id = selection.id;
		return listeners.find((l) => l.listener?.id === id) ?? null;
	});
	let selectedRoute = $derived.by(() => {
		if (selection.kind !== 'route' || !topology) return null;
		const sel = selection;
		return (
			topology.routes.find(
				(r) =>
					r.listenPort === sel.port &&
					r.protocol === sel.protocol &&
					r.hostname.toLowerCase() === sel.hostname
			) ?? null
		);
	});
	let selectedRouteReservation = $derived.by(() => {
		if (selection.kind !== 'route' || !topology) return null;
		const sel = selection;
		return (
			topology.reservations.find(
				(r) =>
					r.kind === NetworkReservationKind.ROUTED &&
					r.port === sel.port &&
					r.protocol === sel.protocol &&
					r.hostname === sel.hostname
			) ?? null
		);
	});
	let selectedRouteOwner = $derived.by(() => {
		if (selection.kind !== 'route' || !topology) return { name: '', serverId: '' };
		const res = selectedRouteReservation;
		const ownerKind = selectedRoute?.ownerKind ?? res?.ownerKind;
		const ownerId = selectedRoute?.ownerId || res?.ownerId || '';
		if (ownerKind === NetworkOwnerKind.MODULE) {
			const module = modules.find((m) => m.id === ownerId);
			return {
				name: module?.name ?? ownerId.slice(0, 8),
				serverId: module?.serverId ?? ''
			};
		}
		const server = $serversStore.find((s) => s.id === ownerId);
		return { name: server?.name ?? ownerId.slice(0, 8), serverId: server?.id ?? '' };
	});
	let selectedLane = $derived.by(() => {
		if (selection.kind !== 'lane' || !topology) return null;
		const sel = selection;
		const routes = topology.reservations.filter(
			(r) =>
				r.kind === NetworkReservationKind.ROUTED &&
				r.port === sel.port &&
				r.protocol === sel.protocol
		);
		const relay = topology.reservations.find(
			(r) =>
				r.kind === NetworkReservationKind.RELAY &&
				r.port === sel.port &&
				r.protocol === sel.protocol
		);
		let ownerName = '';
		let serverId = '';
		if (relay) {
			if (relay.ownerKind === NetworkOwnerKind.MODULE) {
				const module = modules.find((m) => m.id === relay.ownerId);
				ownerName = module?.name ?? relay.ownerId.slice(0, 8);
				serverId = module?.serverId ?? '';
			} else {
				const server = $serversStore.find((s) => s.id === relay.ownerId);
				ownerName = server?.name ?? relay.ownerId.slice(0, 8);
				serverId = server?.id ?? '';
			}
		}
		return {
			port: sel.port,
			label: laneLabel(sel.protocol),
			relay: !!relay,
			routeCount: routes.length,
			ownerName,
			serverId
		};
	});
	let selectedEntry = $derived.by(() => {
		if (selection.kind !== 'entry' || !topology) return null;
		const sel = selection;
		const res = topology.reservations.find(
			(r) =>
				r.kind === NetworkReservationKind.EXCLUSIVE &&
				r.port === sel.port &&
				(r.transport === NetworkTransport.UDP ? 'udp' : 'tcp') === sel.transport
		);
		if (!res)
			return {
				port: sel.port,
				transport: sel.transport,
				ownerName: '',
				ownerLabel: 'Bound by',
				serverId: '',
				detail: ''
			};
		let ownerName = '';
		let serverId = '';
		let ownerLabel = 'Bound by';
		if (res.ownerKind === NetworkOwnerKind.MODULE) {
			ownerLabel = 'Module';
			const module = modules.find((m) => m.id === res.ownerId);
			ownerName = module?.name ?? res.ownerId.slice(0, 8);
			serverId = module?.serverId ?? '';
		} else if (res.ownerKind === NetworkOwnerKind.SERVER) {
			ownerLabel = 'Server';
			const server = $serversStore.find((s) => s.id === res.ownerId);
			ownerName = server?.name ?? res.ownerId.slice(0, 8);
			serverId = server?.id ?? '';
		}
		return {
			port: sel.port,
			transport: sel.transport,
			ownerName,
			ownerLabel,
			serverId,
			detail: res.detail
		};
	});
	let selectedServer = $derived.by(() => {
		if (selection.kind !== 'server') return null;
		const id = selection.id;
		return $serversStore.find((s) => s.id === id) ?? null;
	});
	let selectedModule = $derived.by(() => {
		if (selection.kind !== 'module') return null;
		const id = selection.id;
		return modules.find((m) => m.id === id) ?? null;
	});
	let selectedExtraPorts = $derived.by((): ExposedPort[] => {
		if (!topology) return [];
		if (selection.kind !== 'server' && selection.kind !== 'module') return [];
		const ownerKind =
			selection.kind === 'server' ? NetworkOwnerKind.SERVER : NetworkOwnerKind.MODULE;
		const id = selection.id;
		const gamePort = selection.kind === 'server' ? (selectedServer?.port ?? 0) : 0;
		return topology.reservations
			.filter((r) => {
				if (r.ownerKind !== ownerKind || r.ownerId !== id) return false;
				if (r.kind !== NetworkReservationKind.EXCLUSIVE) return false;
				// Game port and rcon shadow stay off the list
				if (gamePort > 0 && (r.port === gamePort || r.port === gamePort + 10)) return false;
				return true;
			})
			.map((r) => ({
				port: r.port,
				label: r.detail || 'port',
				transport: r.transport === NetworkTransport.UDP ? 'udp' : 'tcp'
			}));
	});
	let selectedServerListenPort = $derived.by(() => {
		if (!selectedServer?.proxyListenerId) return 0;
		return (
			listeners.find((l) => l.listener?.id === selectedServer?.proxyListenerId)?.listener?.port ?? 0
		);
	});

	function backToOverview() {
		selection = { kind: 'overview' };
	}

	async function onMutated() {
		await loadAll();
	}

	async function onConverted() {
		selection = { kind: 'overview' };
		await loadAll();
		serversStore.fetchServers(true);
	}
</script>

<div class="flex min-h-0 flex-1 flex-col">
	{#if loading}
		<div class="space-y-4 p-4 sm:p-6">
			<Skeleton class="h-10 rounded-lg" />
			<Skeleton class="h-[28rem] rounded-xl" />
		</div>
	{:else if loadError && !topology}
		<div class="flex flex-1 items-center justify-center p-6">
			<EmptyState
				icon={Network}
				title="Could not load the network"
				description="The topology request failed, try again."
			>
				<Button size="sm" variant="outline" onclick={retry}>
					<RotateCcw class="size-4" />
					Retry
				</Button>
			</EmptyState>
		</div>
	{:else if topology}
		<div class="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2.5 sm:px-6">
			<div class="flex items-center gap-2 text-xs text-muted-foreground">
				<span
					class="size-2 rounded-full {topology.proxyEnabled && topology.proxyRunning
						? 'bg-status-ok'
						: 'bg-status-idle'}"
				></span>
				{#if topology.proxyEnabled}
					{topology.proxyRunning ? 'Running' : 'Not running'} · {listeners.length}
					{listeners.length === 1 ? 'listener' : 'listeners'} · {routeCount}
					{routeCount === 1 ? 'route' : 'routes'}
				{:else}
					Proxy off
				{/if}
			</div>
		</div>

		<PaneGroup direction="horizontal" class="min-h-0 flex-1 max-lg:!flex-col">
			<Pane defaultSize={70} minSize={40} class="max-lg:!flex-none max-lg:!basis-[26rem]">
				{#if hasAnything || listeners.length > 0}
					<div class="topology-flow h-full">
						<SvelteFlow
							{nodes}
							{edges}
							{nodeTypes}
							fitView
							minZoom={0.4}
							maxZoom={1.5}
							nodesDraggable={false}
							nodesConnectable={false}
							onnodeclick={({ node }) => selectNode(node)}
							onpaneclick={backToOverview}
						>
							<Background gap={24} />
							<Controls showLock={false} />
							<MiniMap class="max-lg:hidden" />
						</SvelteFlow>
					</div>
				{:else}
					<div class="flex h-full items-center justify-center">
						<EmptyState
							icon={Network}
							title="Nothing on the network yet"
							description="Create a server and it appears here with its route."
						>
							<Button size="sm" href={resolve('/servers/new')}>Create server</Button>
						</EmptyState>
					</div>
				{/if}
			</Pane>
			<PaneResizer class="w-px bg-border transition-colors hover:bg-primary/40 max-lg:hidden" />
			<Pane defaultSize={30} minSize={22} class="max-lg:!flex-auto">
				<div class="h-full min-h-0 border-l bg-card max-lg:border-t max-lg:border-l-0">
					{#if selection.kind === 'panel'}
						<PanelInspector
							port={topology.panelPort}
							baseUrl={topology.effectiveBaseUrl}
							onClose={backToOverview}
						/>
					{:else if selection.kind === 'listener' && selectedListener}
						<ListenerInspector
							target={selectedListener}
							{listeners}
							{usedPorts}
							onDone={onMutated}
							onClose={backToOverview}
						/>
					{:else if selection.kind === 'listener-create'}
						<ListenerInspector
							target={null}
							{listeners}
							{usedPorts}
							onDone={onMutated}
							onClose={backToOverview}
						/>
					{:else if selection.kind === 'lane' && selectedLane}
						<LaneInspector
							port={selectedLane.port}
							label={selectedLane.label}
							relay={selectedLane.relay}
							routeCount={selectedLane.routeCount}
							ownerName={selectedLane.ownerName}
							serverId={selectedLane.serverId}
							onClose={backToOverview}
						/>
					{:else if selection.kind === 'entry' && selectedEntry}
						<EntryInspector
							port={selectedEntry.port}
							transport={selectedEntry.transport}
							ownerName={selectedEntry.ownerName}
							ownerLabel={selectedEntry.ownerLabel}
							serverId={selectedEntry.serverId}
							detail={selectedEntry.detail}
							onClose={backToOverview}
						/>
					{:else if selection.kind === 'route'}
						<RouteInspector
							hostname={selection.hostname}
							port={selection.port}
							protocol={selection.protocol}
							route={selectedRoute}
							stale={!selectedRouteReservation && !!selectedRoute}
							ownerName={selectedRouteOwner.name}
							serverId={selectedRouteOwner.serverId}
							onClose={backToOverview}
						/>
					{:else if (selection.kind === 'server' && selectedServer) || (selection.kind === 'module' && selectedModule)}
						<BackendInspector
							server={selectedServer}
							module={selectedModule}
							listenPort={selectedServerListenPort}
							baseUrl={topology.effectiveBaseUrl}
							extraPorts={selectedExtraPorts}
							onClose={backToOverview}
						/>
					{:else}
						<ProxyInspector
							enabled={topology.proxyEnabled}
							running={topology.proxyRunning}
							{baseUrl}
							effectiveBaseUrl={topology.effectiveBaseUrl}
							baseUrlSource={topology.baseUrlSource}
							{strictHttps}
							listenerCount={listeners.length}
							{routeCount}
							{hasProxiedWorkloads}
							initialPanel={page.url.searchParams.get('panel') ?? ''}
							onRequestDisable={() => (disableOpen = true)}
							onChanged={onMutated}
						/>
					{/if}
				</div>
			</Pane>
		</PaneGroup>
	{/if}
</div>

<DisableProxyDialog
	bind:open={disableOpen}
	{baseUrl}
	{strictHttps}
	{modules}
	{usedPorts}
	{onConverted}
/>
