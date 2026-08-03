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
	ServerStatus,
	type Module,
	type Server
} from '$lib/proto/discopanel/v1/storage_pb';
import { hostnameSummary } from '$lib/hostname';
import {
	contentBounds,
	layoutColumns,
	zoneRect,
	type LayoutEdge,
	type LayoutItem,
	type ZoneItem
} from './topology-layout';

// What the inspector panel is focused on
export type Selection =
	| { kind: 'overview' }
	| { kind: 'internet' }
	| { kind: 'router' }
	| { kind: 'panel' }
	| { kind: 'listener'; id: string }
	| { kind: 'listener-create' }
	| { kind: 'entry'; port: number; transport: string }
	| { kind: 'lane'; port: number; protocol: ModuleProtocol }
	| { kind: 'service'; port: number; ownerKind: NetworkOwnerKind; ownerId: string }
	| { kind: 'server'; id: string }
	| { kind: 'module'; id: string };

export interface ExposedPort {
	port: number;
	label: string;
	transport: string;
}

// Focus ring flag since xyflow selected nodes drag together
export interface ActiveFlag {
	active?: boolean;
}

export interface InternetNodeData extends Record<string, unknown>, ActiveFlag {
	publicIp: string;
	selection: Selection;
}

export interface RouterNodeData extends Record<string, unknown>, ActiveFlag {
	gatewayIp: string;
	selection: Selection;
}

export interface ZoneNodeData extends Record<string, unknown> {
	label: string;
	sub: string;
	width: number;
	height: number;
	selection: Selection;
}

export interface EntryNodeData extends Record<string, unknown>, ActiveFlag {
	title: string;
	port: number;
	sub: string;
	bound: boolean;
	live: boolean;
	selection: Selection;
}

// One protocol chip rendered inside a listener card
export interface LaneChip {
	protocol: ModuleProtocol;
	label: string;
	stateClass: string;
	relay: boolean;
	selection: Selection;
}

export interface ListenerNodeData extends Record<string, unknown>, ActiveFlag {
	name: string;
	port: number;
	isDefault: boolean;
	enabled: boolean;
	autoCreated: boolean;
	// Panel listener, permanent and undeletable
	panel: boolean;
	state: 'active' | 'idle' | 'disabled';
	lanes: LaneChip[];
	// Chip protocol currently focused in the inspector
	activeLane: ModuleProtocol | null;
	onSelect: (sel: Selection) => void;
	selection: Selection;
}

export interface ServiceNodeData extends Record<string, unknown>, ActiveFlag {
	// Shortest name fronts the card, rest counted
	summary: string;
	// Full set for the hover tooltip
	names: string;
	nameCount: number;
	staleCount: number;
	port: number;
	stateClass: string;
	connections: number;
	wakeable: boolean;
	live: boolean;
	dimmed: boolean;
	http: boolean;
	selection: Selection;
}

export interface ActionNodeData extends Record<string, unknown>, ActiveFlag {
	label: string;
	selection: Selection;
}

export interface BackendNodeData extends Record<string, unknown>, ActiveFlag {
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
	internet: 58,
	router: 58,
	entry: 58,
	listener: 64,
	listenerLanes: 92,
	service: 58,
	backend: 66,
	action: 40
};

// Zone bands keyed to the columns they wrap
const ZONE_COLUMNS: Record<string, number[]> = {
	'zone:internet': [0],
	'zone:router': [1],
	'zone:machine': [2, 3],
	'zone:containers': [4]
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

function liveKey(port: number, protocol: ModuleProtocol, hostname: string): string {
	return `${port}:${protocol}:${hostname.toLowerCase()}`;
}

// One service and every name it answers on one port
export interface LaneService {
	key: string;
	port: number;
	protocols: ModuleProtocol[];
	ownerKind: NetworkOwnerKind;
	ownerId: string;
	relay: boolean;
	hostnames: string[];
	catchAll: boolean;
	// Live names lacking a reservation
	staleHostnames: string[];
	live: boolean;
	// Counters are service level, never sum across names
	connections: number;
	wakeable: boolean;
	routes: ProxyRoute[];
}

// Groups reservations and live routes per service and port
export function groupServices(
	reservations: NetworkReservation[],
	routes: ProxyRoute[],
	port?: number
): LaneService[] {
	const map = new Map<string, LaneService>();
	const get = (
		p: number,
		protocol: ModuleProtocol,
		ownerKind: NetworkOwnerKind,
		ownerId: string,
		relay: boolean
	): LaneService => {
		const key = `${p}:${ownerKind}:${ownerId}:${relay}`;
		let svc = map.get(key);
		if (!svc) {
			svc = {
				key,
				port: p,
				protocols: [],
				ownerKind,
				ownerId,
				relay,
				hostnames: [],
				catchAll: false,
				staleHostnames: [],
				live: false,
				connections: 0,
				wakeable: false,
				routes: []
			};
			map.set(key, svc);
		}
		if (!svc.protocols.includes(protocol)) svc.protocols.push(protocol);
		return svc;
	};

	for (const res of reservations) {
		if (port !== undefined && res.port !== port) continue;
		if (res.kind === NetworkReservationKind.ROUTED) {
			const svc = get(res.port, res.protocol, res.ownerKind, res.ownerId, false);
			if (!res.hostname) {
				svc.catchAll = true;
			} else if (!svc.hostnames.includes(res.hostname)) {
				svc.hostnames.push(res.hostname);
			}
		} else if (res.kind === NetworkReservationKind.RELAY) {
			get(res.port, res.protocol, res.ownerKind, res.ownerId, true);
		}
	}

	for (const route of routes) {
		if (port !== undefined && route.listenPort !== port) continue;
		const relay = isRelayProtocol(route.protocol);
		const svc = get(route.listenPort, route.protocol, route.ownerKind, route.ownerId, relay);
		svc.live = true;
		svc.routes.push(route);
		svc.connections = Math.max(svc.connections, Number(route.activeConnections));
		if (route.wakeable) svc.wakeable = true;
		const name = route.hostname.toLowerCase();
		if (relay || !name) continue;
		if (!svc.hostnames.includes(name) && !svc.staleHostnames.includes(name)) {
			svc.staleHostnames.push(name);
		}
	}

	for (const svc of map.values()) {
		svc.hostnames.sort();
		svc.staleHostnames.sort();
		svc.protocols.sort((a, b) => LANE_ORDER.indexOf(a) - LANE_ORDER.indexOf(b));
	}
	return [...map.values()];
}

// Service tone prefers the healthiest live route
function serviceClass(svc: LaneService, running: boolean): string {
	if (!running) return 'topo-edge-idle';
	let cls = 'topo-edge-idle';
	for (const route of svc.routes) {
		const c = edgeClass(route, running);
		if (c === 'topo-edge-ok') return c;
		if (c !== 'topo-edge-idle') cls = c;
	}
	return cls;
}

// Builds the flow graph from topology plus catalog data
export function buildGraph(
	topology: GetNetworkTopologyResponse,
	listeners: ProxyListenerWithCount[],
	servers: Server[],
	modules: Module[],
	selection: Selection,
	moved: Record<string, { x: number; y: number }>,
	onSelect: (sel: Selection) => void
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
	const activeIds: string[] = [];
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
		const active = isSelected(data.selection as Selection);
		if (active) activeIds.push(id);
		nodes.push({
			id,
			type,
			position: { x: 0, y: 0 },
			// Column and height ride along for live zone refits
			data: { ...data, active, col: column, h: height },
			draggable: type !== 'action',
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

	// Merged hostname groups per service drive the route column
	const services = groupServices(topology.reservations, topology.routes);

	const anyActive = topology.proxyEnabled && running;
	const trunkCls = anyActive ? 'topo-edge-ok' : 'topo-edge-idle';

	// Outside world flows in through the router
	addNode('internet', 'internet', 0, HEIGHTS.internet, 0, {
		publicIp: topology.publicIp,
		selection: { kind: 'internet' }
	} satisfies InternetNodeData);
	addNode('router', 'router', 1, HEIGHTS.router, 0, {
		gatewayIp: topology.gatewayIp,
		selection: { kind: 'router' }
	} satisfies RouterNodeData);
	addEdge('internet', 'router', trunkCls, false);

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
		relayOwner?: NetworkReservation;
		liveStates: ProxyRoute[];
	}
	const lanes = new Map<string, Lane>();
	const laneFor = (port: number, protocol: ModuleProtocol): Lane => {
		const key = `${port}:${protocol}`;
		let lane = lanes.get(key);
		if (!lane) {
			lane = { port, protocol, liveStates: [] };
			lanes.set(key, lane);
		}
		return lane;
	};
	for (const res of routedRes) laneFor(res.port, res.protocol);
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

	for (const entry of listenerEntries) {
		listenerNodeByPort.set(entry.port, entry.id);
		listenerEnabledByPort.set(entry.port, entry.enabled);
		const portLive = topology.routes.some((r) => r.listenPort === entry.port);
		const state = !entry.enabled ? 'disabled' : running && portLive ? 'active' : 'idle';
		const rowId = entry.id.startsWith('listener:port:') ? '' : entry.id.slice('listener:'.length);
		const chips: LaneChip[] = laneList
			.filter((lane) => lane.port === entry.port)
			.map((lane) => ({
				protocol: lane.protocol,
				label: laneLabel(lane.protocol),
				stateClass: entry.enabled ? laneClass(lane) : 'topo-edge-idle',
				relay: isRelayProtocol(lane.protocol),
				selection: { kind: 'lane', port: lane.port, protocol: lane.protocol }
			}));
		addNode(
			entry.id,
			'listener',
			2,
			chips.length > 0 ? HEIGHTS.listenerLanes : HEIGHTS.listener,
			0,
			{
				name: entry.name,
				port: entry.port,
				isDefault: entry.isDefault,
				enabled: entry.enabled,
				autoCreated: entry.autoCreated,
				panel: entry.panel,
				state,
				lanes: chips,
				activeLane:
					selection.kind === 'lane' && selection.port === entry.port ? selection.protocol : null,
				onSelect,
				selection: rowId ? { kind: 'listener', id: rowId } : { kind: 'overview' }
			} satisfies ListenerNodeData
		);
		const cls =
			entry.enabled && topology.proxyEnabled && running ? 'topo-edge-ok' : 'topo-edge-idle';
		addEdge('router', entry.id, cls, false);
	}

	// Add listener affordance closes the listener column
	if (topology.proxyEnabled) {
		addNode('add-listener', 'action', 2, HEIGHTS.action, 0, {
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

	// Relay lanes forward straight from listener to backend
	for (const lane of laneList) {
		if (!isRelayProtocol(lane.protocol)) continue;
		const listenerNode = listenerNodeByPort.get(lane.port);
		if (!listenerNode) continue;
		const dimmed = listenerEnabledByPort.get(lane.port) === false;
		const cls = dimmed ? 'topo-edge-idle' : laneClass(lane);
		const owner = lane.relayOwner;
		if (owner) {
			const backend = targetBackend(owner.ownerKind, owner.ownerId, 0);
			const live = liveRoutes.get(liveKey(lane.port, lane.protocol, ''));
			const animated = Number(live?.activeConnections ?? 0n) > 0;
			addEdge(listenerNode, backend, cls, animated);
		} else {
			for (const live of lane.liveStates) {
				if (
					live.ownerKind !== NetworkOwnerKind.SERVER &&
					live.ownerKind !== NetworkOwnerKind.MODULE
				) {
					continue;
				}
				const backend = targetBackend(live.ownerKind, live.ownerId, 0);
				addEdge(listenerNode, backend, cls, Number(live.activeConnections) > 0);
			}
		}
	}

	// One node per service, names collapse to a summary
	const routedServices = services
		.filter((svc) => !svc.relay)
		.sort((a, b) => a.port - b.port || (a.hostnames[0] ?? '~').localeCompare(b.hostnames[0] ?? '~'));
	for (const svc of routedServices) {
		const listenerNode = listenerNodeByPort.get(svc.port);
		if (!listenerNode) continue;
		const dimmed = listenerEnabledByPort.get(svc.port) === false;
		const routeCls = dimmed ? 'topo-edge-idle' : serviceClass(svc, running);
		const animated = svc.connections > 0;
		const backend = targetBackend(svc.ownerKind, svc.ownerId, 0);
		const allNames = [...svc.hostnames, ...svc.staleHostnames];

		// Catch all services forward straight to their backend
		if (allNames.length === 0) {
			addEdge(listenerNode, backend, routeCls, animated);
			continue;
		}

		const id = `service:${svc.key}`;
		addNode(id, 'service', 3, HEIGHTS.service, 0, {
			summary: hostnameSummary(allNames),
			names: allNames.join(', '),
			nameCount: allNames.length,
			staleCount: svc.staleHostnames.length,
			port: svc.port,
			stateClass: routeCls,
			connections: svc.connections,
			wakeable: svc.wakeable,
			live: svc.live,
			dimmed,
			http: svc.protocols.includes(ModuleProtocol.HTTP),
			selection: {
				kind: 'service',
				port: svc.port,
				ownerKind: svc.ownerKind,
				ownerId: svc.ownerId
			}
		} satisfies ServiceNodeData);
		addEdge(listenerNode, id, routeCls, animated);
		addEdge(id, backend, routeCls, animated);
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
		// Path tone follows the owner container state
		let live = false;
		let cls = 'topo-edge-idle';
		if (res.ownerKind === NetworkOwnerKind.SERVER) {
			const server = serversById.get(res.ownerId);
			live = server?.status === ServerStatus.RUNNING;
			if (server?.status === ServerStatus.STARTING) cls = 'topo-edge-busy';
		} else {
			const module = modulesById.get(res.ownerId);
			live = module?.status === ModuleStatus.RUNNING;
			if (module?.status === ModuleStatus.STARTING) cls = 'topo-edge-busy';
		}
		if (live) cls = 'topo-edge-ok';
		addNode(id, 'entry', 2, HEIGHTS.entry, 1, {
			title: `:${res.port}`,
			port: res.port,
			sub: `direct port · ${transport}`,
			bound: !!owner,
			live,
			selection: { kind: 'entry', port: res.port, transport }
		} satisfies EntryNodeData);
		addEdge('router', id, cls, false);
		const backend = targetBackend(res.ownerKind, res.ownerId, 1);
		addEdge(id, backend, cls, false);
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

	// Selection lights its traffic paths, everything else dims
	if (selection.kind !== 'overview') {
		let seeds = activeIds;
		// Lane chips light their whole listener subtree
		if (selection.kind === 'lane') {
			const listenerNode = listenerNodeByPort.get(selection.port);
			seeds = listenerNode ? [listenerNode] : [];
		}
		if (seeds.length > 0) {
			const bySource = new Map<string, Edge[]>();
			const byTarget = new Map<string, Edge[]>();
			for (const edge of edges) {
				bySource.set(edge.source, [...(bySource.get(edge.source) ?? []), edge]);
				byTarget.set(edge.target, [...(byTarget.get(edge.target) ?? []), edge]);
			}
			const onPath = new Set<string>();
			const walk = (starts: string[], down: boolean) => {
				const pending = [...starts];
				const seen = new Set(pending);
				while (pending.length > 0) {
					const id = pending.pop()!;
					for (const edge of (down ? bySource.get(id) : byTarget.get(id)) ?? []) {
						onPath.add(edge.id);
						const next = down ? edge.target : edge.source;
						if (!seen.has(next)) {
							seen.add(next);
							pending.push(next);
						}
					}
				}
			};
			walk(seeds, true);
			walk(seeds, false);
			for (const edge of edges) {
				if (onPath.has(edge.id)) {
					// Animation direction mirrors traffic flow
					edge.animated = true;
				} else {
					edge.animated = false;
					edge.class = `${edge.class} topo-edge-dim`;
				}
			}
		}
	}

	// Dragged nodes keep their saved spots after rebuilds
	const positions = layoutColumns(items, layoutEdges);
	for (const [id, pos] of Object.entries(moved)) {
		if (nodeIds.has(id)) positions.set(id, { x: pos.x, y: pos.y });
	}
	for (const node of nodes) {
		const pos = positions.get(node.id);
		if (pos) node.position = pos;
	}

	// Zone bands wrap each stage of the traffic path
	const bounds = contentBounds(items, positions);
	const zoneSpecs = [
		{ id: 'zone:internet', label: 'Internet', sub: 'public network' },
		{ id: 'zone:router', label: 'Router', sub: 'network edge' },
		{ id: 'zone:machine', label: 'This machine', sub: topology.lanIp || 'local network' },
		{ id: 'zone:containers', label: 'Containers', sub: 'docker network' }
	];
	const zoneNodes: Node[] = [];
	for (const spec of zoneSpecs) {
		const rect = zoneRect(ZONE_COLUMNS[spec.id], items, positions, bounds.top, bounds.bottom);
		if (!rect) continue;
		zoneNodes.push({
			id: spec.id,
			type: 'zone',
			position: { x: rect.x, y: rect.y },
			zIndex: -1,
			data: {
				label: spec.label,
				sub: spec.sub,
				width: rect.width,
				height: rect.height,
				selection: { kind: 'overview' }
			} satisfies ZoneNodeData,
			draggable: false,
			connectable: false,
			selectable: false
		});
	}

	return { nodes: [...zoneNodes, ...nodes], edges };
}

// Refits zone bands to wherever nodes sit right now
export function updateZoneNodes(all: Node[], dragging: Node[]): Node[] {
	const positions = new Map<string, { x: number; y: number }>();
	const zoneItems: ZoneItem[] = [];
	for (const node of all) {
		if (node.type === 'zone') continue;
		positions.set(node.id, node.position);
		zoneItems.push({
			id: node.id,
			column: Number(node.data.col ?? 0),
			height: Number(node.data.h ?? 0)
		});
	}
	// Drag events carry fresher positions than the bound list
	for (const node of dragging) {
		if (positions.has(node.id)) positions.set(node.id, node.position);
	}
	const bounds = contentBounds(zoneItems, positions);
	return all.map((node) => {
		if (node.type !== 'zone') return node;
		const columns = ZONE_COLUMNS[node.id];
		if (!columns) return node;
		const rect = zoneRect(columns, zoneItems, positions, bounds.top, bounds.bottom);
		if (!rect) return node;
		return {
			...node,
			position: { x: rect.x, y: rect.y },
			data: { ...node.data, width: rect.width, height: rect.height }
		};
	});
}
