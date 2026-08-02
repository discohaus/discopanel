import { ProxyRouteState, ProxyRouteStateSchema, type ProxyRoute } from '$lib/proto/discopanel/v1/proxy_pb';
import { enumLabel } from '$lib/proto-meta';
import { formatBytes } from '$lib/utils';

// Human label for a route's serving state
export function routeStateLabel(route: ProxyRoute): string {
	switch (route.state) {
		case ProxyRouteState.STARTING:
			return enumLabel(ProxyRouteStateSchema, ProxyRouteState.STARTING);
		case ProxyRouteState.OFFLINE:
			return route.wakeable ? 'Wakes on join' : enumLabel(ProxyRouteStateSchema, ProxyRouteState.OFFLINE);
		default:
			return route.active ? enumLabel(ProxyRouteStateSchema, ProxyRouteState.ONLINE) : 'Inactive';
	}
}

// Badge classes matching the status color tokens
export function routeStateClass(route: ProxyRoute): string {
	switch (route.state) {
		case ProxyRouteState.STARTING:
			return 'border-status-busy/25 bg-status-busy/10 text-status-busy';
		case ProxyRouteState.OFFLINE:
			return route.wakeable
				? 'border-status-sleep/25 bg-status-sleep/10 text-status-sleep'
				: 'border-status-idle/25 bg-status-idle/10 text-status-idle';
		default:
			return route.active
				? 'border-status-ok/25 bg-status-ok/10 text-status-ok'
				: 'border-status-idle/25 bg-status-idle/10 text-status-idle';
	}
}

// Text color token for a route's serving state
export function routeStateText(route: ProxyRoute): string {
	switch (route.state) {
		case ProxyRouteState.STARTING:
			return 'text-status-busy';
		case ProxyRouteState.OFFLINE:
			return route.wakeable ? 'text-status-sleep' : 'text-status-idle';
		default:
			return route.active ? 'text-status-ok' : 'text-status-idle';
	}
}

// Compact live traffic summary for a route row
export function routeStatsSummary(route: ProxyRoute): string {
	const parts: string[] = [];
	if (route.activeConnections > 0n) {
		parts.push(`${route.activeConnections} connected`);
	}
	if (route.totalConnections > 0n) {
		parts.push(`${route.totalConnections} total`);
	}
	const bytes = Number(route.bytesToBackend) + Number(route.bytesToClient);
	if (bytes > 0) {
		parts.push(formatBytes(bytes));
	}
	if (route.logins > 0n) {
		parts.push(`${route.logins} logins`);
	}
	if (route.statusPings > 0n) {
		parts.push(`${route.statusPings} pings`);
	}
	if (route.wakes > 0n) {
		parts.push(`${route.wakes} wakes`);
	}
	if (route.lastProtocolVersion > 0) {
		parts.push(`proto ${route.lastProtocolVersion}`);
	}
	return parts.join(' · ');
}
