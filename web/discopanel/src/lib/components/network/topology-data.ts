import type { Edge, Node } from '@xyflow/svelte';
import {
	NetworkOwnerKind,
	NetworkReservationKind,
	ProxyRouteState,
	type GetNetworkTopologyResponse,
	type NetworkReservation,
	type ProxyRoute,
	type ProxyListenerWithCount
} from '$lib/proto/discopanel/v1/proxy_pb';
import {
	ModuleProtocol,
	ModuleStatus,
	NetworkTransport,
	type Module,
	type Server
} from '$lib/proto/discopanel/v1/storage_pb';
import { layoutColumns, type LayoutEdge, type LayoutItem } from './topology-layout';

// What the inspector panel is focused on
export type Selection =
	| { kind: 'overview' }
	| { kind: 'panel' }
	| { kind: 'listener'; id: string }
	| { kind: 'listener-create' }
	| { kind: 'entry'; port: number; transport: string }
	| { kind: 'lane'; port: number; protocol: ModuleProtocol }
	| { kind: 'route'; port: number; protocol: ModuleProtocol; hostname: string }
	| { kind: 'server'; id: string }
	| { kind: 'module'; id: string };

export interface ExposedPort {
	port: number;
	label: string;
	transport: string;
}

export interface SourceNodeData extends Record<string, unknown> {
	players: number;
	selection: Selection;
}

export interface EntryNodeData extends Record<string, unknown> {
	title: string;
	port: number;
	sub: string;
	active: boolean;
	selection: Selection;
}

export interface ListenerNodeData extends Record<string, unknown> {
	name: string;
	port: number;
	isDefault: boolean;
	enabled: boolean;
	autoCreated: boolean;
	// Panel listener, permanent and undeletable
	panel: boolean;
	state: 'active' | 'idle' | 'disabled';
	routeCount: number;
	selection: Selection;
}

export interface LaneNodeData extends Record<string, unknown> {
	port: number;
	protocol: ModuleProtocol;
	label: string;
	relay: boolean;
	stateClass: string;
	dimmed: boolean;
	selection: Selection;
}

export interface RouteNodeData extends Record<string, unknown> {
	hostname: string;
	port: number;
	protocol: ModuleProtocol;
	stateClass: string;
	connections: number;
	wakeable: boolean;
	live: boolean;
	stale: boolean;
	dimmed: boolean;
	selection: Selection;
}

export interface ActionNodeData extends Record<string, unknown> {
	label: string;
	selection: Selection;
}

export interface BackendNodeData extends Record<string, unknown> {
	kind: 'server' | 'module' | 'panel';
	name: string;
	favicon: string;
	statusServer: Server | null;
	moduleRunning: boolean;
	extraPorts: ExposedPort[];
	nested: boolean;
	parentName: string;
	selection: Selection;
}

export interface TopologyGraph {
	nodes: Node[];
	edges: Edge[];
}

const HEIGHTS = {
	source: 58,
	entry: 58,
	listener: 66,
	lane: 46,
	route: 58,
	backend: 66,
	action: 40
};

// Lane display order within one listener port
const LANE_ORDER: ModuleProtocol[] = [
	ModuleProtocol.MINECRAFT,
	ModuleProtocol.HTTP,
	ModuleProtocol.TCP,
	ModuleProtocol.UDP
];

const LANE_LABEL: Record<number, string> = {
	[ModuleProtocol.MINECRAFT]: 'minecraft',
	[ModuleProtocol.HTTP]: 'http',
	[ModuleProtocol.TCP]: 'tcp relay',
	[ModuleProtocol.UDP]: 'udp relay'
};

// Short lowercase label for a dispatch lane
export function laneLabel(protocol: ModuleProtocol): string {
	return LANE_LABEL[protocol] ?? 'tcp relay';
}

// True when the protocol forwards without hostnames
export function isRelayProtocol(protocol: ModuleProtocol): boolean {
	return protocol !== ModuleProtocol.MINECRAFT && protocol !== ModuleProtocol.HTTP;
}

function transportLabel(t: NetworkTransport): string {
	return t === NetworkTransport.UDP ? 'udp' : 'tcp';
}

// Edge tone derived from a live route's state
function edgeClass(route: ProxyRoute | undefined, running: boolean): string {
	if (!route || !running) return 'topo-edge-idle';
	switch (route.state) {
		case ProxyRouteState.STARTING:
			return 'topo-edge-busy';
		case ProxyRouteState.OFFLINE:
			return route.wakeable ? 'topo-edge-sleep' : 'topo-edge-idle';
		default:
			return 'topo-edge-ok';
	}
}

function routeId(port: number, protocol: ModuleProtocol, hostname: string): string {
	return `route:${port}:${protocol}:${hostname}`;
}

function liveKey(port: number, protocol: ModuleProtocol, hostname: string): string {
	return `${port}:${protocol}:${hostname.toLowerCase()}`;
}

// Builds the flow graph from topology plus catalog data
export function buildGraph(
	topology: GetNetworkTopologyResponse,
	listeners: ProxyListenerWithCount[],
	servers: Server[],
	modules: Module[],
	selection: Selection,
	dnsProvider = ''
): TopologyGraph {
	const nodes: Node[] = [];
	const edges: Edge[] = [];
	const items: LayoutItem[] = [];
	const layoutEdges: LayoutEdge[] = [];

	const serversById = new Map(servers.map((s) => [s.id, s]));
	const modulesById = new Map(modules.map((m) => [m.id, m]));
	const running = topology.proxyRunning;

	const selectionKey = JSON.stringify(selection);
	const isSelected = (sel: Selection) => JSON.stringify(sel) === selectionKey;

	let order = 0;
	const nodeIds = new Set<string>();
	const addNode = (
		id: string,
		type: string,
		column: number,
		height: number,
		band: number,
		data: Record<string, unknown>,
		opts?: { group?: string; indent?: boolean }
	) => {
		if (nodeIds.has(id)) return;
		nodeIds.add(id);
		nodes.push({
			id,
			type,
			position: { x: 0, y: 0 },
			data,
			selected: isSelected(data.selection as Selection),
			draggable: false,
			connectable: false,
			selectable: false
		});
		items.push({ id, column, height, order: order++, band, ...opts });
	};

	const addEdge = (source: string, target: string, cls: string, animated: boolean) => {
		const id = `${source}~${target}`;
		if (edges.some((e) => e.id === id)) return;
		edges.push({ id, source, target, class: cls, animated, selectable: false });
		layoutEdges.push({ source, target });
	};

	// Live routes indexed by port, protocol, and hostname
	const liveRoutes = new Map<string, ProxyRoute>();
	for (const route of topology.routes) {
		liveRoutes.set(liveKey(route.listenPort, route.protocol, route.hostname), route);
	}

	// Players source node totals online players
	const players = servers.reduce((sum, s) => sum + (s.playersOnline || 0), 0);
	addNode('players', 'source', 0, HEIGHTS.source, 0, {
		players,
		selection: { kind: 'overview' }
	} satisfies SourceNodeData);

	// Detected provider resolving the base domain
	if (dnsProvider) {
		addNode('dns-provider', 'entry', 0, HEIGHTS.entry, 0, {
			title: dnsProvider,
			port: 0,
			sub: 'dns provider',
			active: true,
			selection: { kind: 'overview' }
		} satisfies EntryNodeData);
	}

	addNode(
		'panel',
		'backend',
		4,
		HEIGHTS.backend,
		0,
		{
			kind: 'panel',
			name: 'DiscoPanel',
			favicon: '',
			statusServer: null,
			moduleRunning: true,
			extraPorts: [],
			nested: false,
			parentName: '',
			selection: { kind: 'panel' }
		} satisfies BackendNodeData,
		{ group: 'panel' }
	);

	// Reservations split by kind before building lanes
	const socketRes: NetworkReservation[] = [];
	const routedRes: NetworkReservation[] = [];
	const relayRes: NetworkReservation[] = [];
	const exclusiveRes: NetworkReservation[] = [];
	for (const res of topology.reservations) {
		switch (res.kind) {
			case NetworkReservationKind.SOCKET:
				socketRes.push(res);
				break;
			case NetworkReservationKind.ROUTED:
				routedRes.push(res);
				break;
			case NetworkReservationKind.RELAY:
				relayRes.push(res);
				break;
			case NetworkReservationKind.EXCLUSIVE:
				exclusiveRes.push(res);
				break;
		}
	}

	// Listener nodes join socket reservations with their rows
	const rowsById = new Map(
		listeners.filter((l) => l.listener).map((l) => [l.listener!.id, l.listener!])
	);
	const listenerNodeByPort = new Map<number, string>();
	const listenerEnabledByPort = new Map<number, boolean>();
	const seenRows = new Set<string>();
	const listenerEntries: {
		id: string;
		port: number;
		name: string;
		enabled: boolean;
		isDefault: boolean;
		autoCreated: boolean;
		panel: boolean;
	}[] = [];
	for (const res of socketRes) {
		const isPanel = res.ownerKind === NetworkOwnerKind.PANEL;
		const row = rowsById.get(res.ownerId);
		seenRows.add(res.ownerId);
		listenerEntries.push({
			id: `listener:${res.ownerId}`,
			port: res.port,
			name: isPanel ? 'DiscoPanel' : row?.name || res.detail || `Port ${res.port}`,
			enabled: row?.enabled ?? true,
			isDefault: row?.isDefault ?? false,
			autoCreated: row?.autoCreated ?? false,
			panel: isPanel
		});
	}
	for (const lwc of listeners) {
		const row = lwc.listener;
		if (!row || seenRows.has(row.id)) continue;
		listenerEntries.push({
			id: `listener:${row.id}`,
			port: row.port,
			name: row.name,
			enabled: row.enabled,
			isDefault: row.isDefault,
			autoCreated: row.autoCreated,
			panel: row.id === 'panel'
		});
	}

	// Lanes keyed per port and protocol
	interface Lane {
		port: number;
		protocol: ModuleProtocol;
		routes: NetworkReservation[];
		relayOwner?: NetworkReservation;
		liveStates: ProxyRoute[];
	}
	const lanes = new Map<string, Lane>();
	const laneFor = (port: number, protocol: ModuleProtocol): Lane => {
		const key = `${port}:${protocol}`;
		let lane = lanes.get(key);
		if (!lane) {
			lane = { port, protocol, routes: [], liveStates: [] };
			lanes.set(key, lane);
		}
		return lane;
	};
	for (const res of routedRes) laneFor(res.port, res.protocol).routes.push(res);
	for (const res of relayRes) laneFor(res.port, res.protocol).relayOwner = res;
	for (const route of topology.routes) {
		laneFor(route.listenPort, route.protocol).liveStates.push(route);
	}

	// Ports carrying lanes always show a listener node
	const lanePorts = new Set([...lanes.values()].map((l) => l.port));
	for (const port of lanePorts) {
		if (listenerEntries.some((l) => l.port === port)) continue;
		listenerEntries.push({
			id: `listener:port:${port}`,
			port,
			name: `Port ${port}`,
			enabled: true,
			isDefault: false,
			autoCreated: true,
			panel: false
		});
	}
	listenerEntries.sort((a, b) => a.port - b.port);

	const laneList = [...lanes.values()].sort(
		(a, b) => a.port - b.port || LANE_ORDER.indexOf(a.protocol) - LANE_ORDER.indexOf(b.protocol)
	);
	const laneCountByPort = new Map<number, number>();
	for (const lane of laneList) {
		laneCountByPort.set(lane.port, (laneCountByPort.get(lane.port) ?? 0) + 1);
	}

	for (const entry of listenerEntries) {
		listenerNodeByPort.set(entry.port, entry.id);
		listenerEnabledByPort.set(entry.port, entry.enabled);
		const portLive = topology.routes.some((r) => r.listenPort === entry.port);
		const state = !entry.enabled ? 'disabled' : running && portLive ? 'active' : 'idle';
		const rowId = entry.id.startsWith('listener:port:') ? '' : entry.id.slice('listener:'.length);
		addNode(entry.id, 'listener', 1, HEIGHTS.listener, 0, {
			name: entry.name,
			port: entry.port,
			isDefault: entry.isDefault,
			enabled: entry.enabled,
			autoCreated: entry.autoCreated,
			panel: entry.panel,
			state,
			routeCount: laneCountByPort.get(entry.port) ?? 0,
			selection: entry.panel
				? { kind: 'panel' }
				: rowId
					? { kind: 'listener', id: rowId }
					: { kind: 'overview' }
		} satisfies ListenerNodeData);
		const cls =
			entry.enabled && topology.proxyEnabled && running ? 'topo-edge-ok' : 'topo-edge-idle';
		addEdge('players', entry.id, cls, false);
	}

	// Add listener affordance closes the listener column
	if (topology.proxyEnabled) {
		addNode('add-listener', 'action', 1, HEIGHTS.action, 0, {
			label: 'Add listener',
			selection: { kind: 'listener-create' }
		} satisfies ActionNodeData);
	}

	// Backend targets collected while wiring lanes
	const backendBand = new Map<string, number>();
	const backendTargets = new Map<string, { kind: 'server' | 'module'; id: string }>();
	const targetBackend = (ownerKind: NetworkOwnerKind, ownerId: string, band: number): string => {
		// Panel routes land on the fixed panel backend node
		if (ownerKind === NetworkOwnerKind.PANEL) {
			return 'panel';
		}
		const kind = ownerKind === NetworkOwnerKind.SERVER ? 'server' : 'module';
		const nodeId = `${kind}:${ownerId}`;
		backendTargets.set(nodeId, { kind, id: ownerId });
		backendBand.set(nodeId, Math.min(backendBand.get(nodeId) ?? 1, band));
		return nodeId;
	};

	// Lane class prefers the healthiest live route
	const laneClass = (lane: Lane): string => {
		if (!running) return 'topo-edge-idle';
		let cls = 'topo-edge-idle';
		for (const live of lane.liveStates) {
			const c = edgeClass(live, running);
			if (c === 'topo-edge-ok') return c;
			if (c !== 'topo-edge-idle') cls = c;
		}
		return cls;
	};

	for (const lane of laneList) {
		const listenerNode = listenerNodeByPort.get(lane.port);
		if (!listenerNode) continue;
		const dimmed = listenerEnabledByPort.get(lane.port) === false;
		const laneNodeId = `lane:${lane.port}:${lane.protocol}`;
		const cls = dimmed ? 'topo-edge-idle' : laneClass(lane);
		addNode(laneNodeId, 'lane', 2, HEIGHTS.lane, 0, {
			port: lane.port,
			protocol: lane.protocol,
			label: laneLabel(lane.protocol),
			relay: isRelayProtocol(lane.protocol),
			stateClass: cls,
			dimmed,
			selection: { kind: 'lane', port: lane.port, protocol: lane.protocol }
		} satisfies LaneNodeData);
		addEdge(listenerNode, laneNodeId, cls, false);

		if (isRelayProtocol(lane.protocol)) {
			// Relay lanes forward straight to one backend
			const owner = lane.relayOwner;
			if (owner) {
				const backend = targetBackend(owner.ownerKind, owner.ownerId, 0);
				const live = liveRoutes.get(liveKey(lane.port, lane.protocol, ''));
				const animated = Number(live?.activeConnections ?? 0n) > 0;
				addEdge(laneNodeId, backend, cls, animated);
			} else {
				for (const live of lane.liveStates) {
					if (
						live.ownerKind !== NetworkOwnerKind.SERVER &&
						live.ownerKind !== NetworkOwnerKind.MODULE
					) {
						continue;
					}
					const backend = targetBackend(live.ownerKind, live.ownerId, 0);
					addEdge(laneNodeId, backend, cls, Number(live.activeConnections) > 0);
				}
			}
			continue;
		}

		// Panel named claim collapses into its catch all node
		const panelCatchAll = lane.routes.some(
			(r) => r.ownerKind === NetworkOwnerKind.PANEL && !r.hostname
		);
		const laneRoutes = panelCatchAll
			? lane.routes.filter((r) => !(r.ownerKind === NetworkOwnerKind.PANEL && r.hostname))
			: lane.routes;

		// Hostname routes sorted with the catch all last
		const sorted = [...laneRoutes].sort((a, b) =>
			(a.hostname || '~').localeCompare(b.hostname || '~')
		);
		const reservedKeys = new Set<string>();
		for (const res of sorted) {
			reservedKeys.add(liveKey(lane.port, lane.protocol, res.hostname));
			const live = liveRoutes.get(liveKey(lane.port, lane.protocol, res.hostname));
			const routeCls = dimmed ? 'topo-edge-idle' : edgeClass(live, running);
			const id = routeId(lane.port, lane.protocol, res.hostname);
			addNode(id, 'route', 3, HEIGHTS.route, 0, {
				hostname: res.hostname,
				port: lane.port,
				protocol: lane.protocol,
				stateClass: routeCls,
				connections: Number(live?.activeConnections ?? 0n),
				wakeable: live?.wakeable ?? false,
				live: !!live,
				stale: false,
				dimmed,
				selection: {
					kind: 'route',
					port: lane.port,
					protocol: lane.protocol,
					hostname: res.hostname
				}
			} satisfies RouteNodeData);
			const animated = Number(live?.activeConnections ?? 0n) > 0;
			addEdge(laneNodeId, id, routeCls, animated);
			const backend = targetBackend(res.ownerKind, res.ownerId, 0);
			addEdge(id, backend, routeCls, animated);
		}

		// Live routes without reservations render as stale
		for (const live of lane.liveStates) {
			const key = liveKey(lane.port, lane.protocol, live.hostname);
			if (reservedKeys.has(key)) continue;
			const hostname = live.hostname.toLowerCase();
			const id = routeId(lane.port, lane.protocol, hostname);
			const routeCls = dimmed ? 'topo-edge-idle' : edgeClass(live, running);
			addNode(id, 'route', 3, HEIGHTS.route, 0, {
				hostname,
				port: lane.port,
				protocol: lane.protocol,
				stateClass: routeCls,
				connections: Number(live.activeConnections),
				wakeable: live.wakeable,
				live: true,
				stale: true,
				dimmed,
				selection: { kind: 'route', port: lane.port, protocol: lane.protocol, hostname }
			} satisfies RouteNodeData);
			addEdge(laneNodeId, id, routeCls, false);
			if (
				live.ownerKind === NetworkOwnerKind.SERVER ||
				live.ownerKind === NetworkOwnerKind.MODULE
			) {
				const backend = targetBackend(live.ownerKind, live.ownerId, 0);
				addEdge(id, backend, routeCls, false);
			}
		}
	}

	// Direct binds sit in their own band below
	const extraPorts = new Map<string, ExposedPort[]>();
	const pushExtra = (owner: string, entry: ExposedPort) => {
		const list = extraPorts.get(owner) ?? [];
		list.push(entry);
		extraPorts.set(owner, list);
	};
	const directEntries: NetworkReservation[] = [];
	for (const res of exclusiveRes) {
		if (res.ownerKind === NetworkOwnerKind.SERVER) {
			const server = serversById.get(res.ownerId);
			// Rcon shadow binds stay off the map
			if (server && res.port === server.port + 10 && res.transport === NetworkTransport.TCP) {
				continue;
			}
			directEntries.push(res);
			if (server && res.port !== server.port) {
				pushExtra(`server:${res.ownerId}`, {
					port: res.port,
					label: res.detail || 'port',
					transport: transportLabel(res.transport)
				});
			}
		} else if (res.ownerKind === NetworkOwnerKind.MODULE) {
			directEntries.push(res);
			pushExtra(`module:${res.ownerId}`, {
				port: res.port,
				label: res.detail || 'port',
				transport: transportLabel(res.transport)
			});
		}
	}
	directEntries.sort((a, b) => a.port - b.port || a.transport - b.transport);
	for (const res of directEntries) {
		const transport = transportLabel(res.transport);
		const id = `entry:${res.port}:${transport}`;
		const owner =
			res.ownerKind === NetworkOwnerKind.SERVER
				? serversById.get(res.ownerId)
				: modulesById.get(res.ownerId);
		addNode(id, 'entry', 1, HEIGHTS.entry, 1, {
			title: `:${res.port}`,
			port: res.port,
			sub: `direct ${transport}`,
			active: !!owner,
			selection: { kind: 'entry', port: res.port, transport }
		} satisfies EntryNodeData);
		addEdge('players', id, 'topo-edge-idle', false);
		const backend = targetBackend(res.ownerKind, res.ownerId, 1);
		addEdge(id, backend, 'topo-edge-idle', false);
	}

	// Server owned modules pull their parent onto the map
	for (const [nodeId, target] of [...backendTargets]) {
		if (target.kind !== 'module') continue;
		const module = modulesById.get(target.id);
		if (!module?.serverId || !serversById.has(module.serverId)) continue;
		const parentId = `server:${module.serverId}`;
		if (!backendTargets.has(parentId)) {
			backendTargets.set(parentId, { kind: 'server', id: module.serverId });
			backendBand.set(parentId, backendBand.get(nodeId) ?? 0);
		}
	}

	// Backends land last, modules nest under their server
	for (const [nodeId, target] of backendTargets) {
		const band = backendBand.get(nodeId) ?? 0;
		if (target.kind === 'server') {
			const server = serversById.get(target.id);
			addNode(
				nodeId,
				'backend',
				4,
				HEIGHTS.backend,
				band,
				{
					kind: 'server',
					name: server?.name ?? target.id.slice(0, 8),
					favicon: server?.favicon ?? '',
					statusServer: server ?? null,
					moduleRunning: false,
					extraPorts: extraPorts.get(nodeId) ?? [],
					nested: false,
					parentName: '',
					selection: { kind: 'server', id: target.id }
				} satisfies BackendNodeData,
				{ group: nodeId }
			);
		} else {
			const module = modulesById.get(target.id);
			const parent = module?.serverId ? serversById.get(module.serverId) : undefined;
			addNode(
				nodeId,
				'backend',
				4,
				HEIGHTS.backend,
				band,
				{
					kind: 'module',
					name: module?.name ?? target.id.slice(0, 8),
					favicon: '',
					statusServer: null,
					moduleRunning: module?.status === ModuleStatus.RUNNING,
					extraPorts: extraPorts.get(nodeId) ?? [],
					nested: !!parent,
					parentName: parent?.name ?? '',
					selection: { kind: 'module', id: target.id }
				} satisfies BackendNodeData,
				{ group: parent ? `server:${parent.id}` : nodeId, indent: !!parent }
			);
		}
	}

	const positions = layoutColumns(items, layoutEdges);
	for (const node of nodes) {
		const pos = positions.get(node.id);
		if (pos) node.position = pos;
	}

	return { nodes, edges };
}
