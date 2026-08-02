import type { NetworkPort } from '$lib/proto/discopanel/v1/storage_pb';
import { ModuleProtocol, ModuleProtocolSchema } from '$lib/proto/discopanel/v1/storage_pb';
import { enumLabel } from '$lib/proto-meta';
import { isRelayProtocol } from '$lib/components/network/topology-data';

// Validates host port range and duplicates per row
export function portRowErrors(
	ports: NetworkPort[],
	usedPorts: Record<number, boolean> = {}
): string[] {
	return ports.map((port, index) => {
		const value = Number(port.hostPort);
		if (!value) return '';
		if (value < 1 || value > 65535) return 'Port must be between 1 and 65535';
		if (!port.proxyEnabled && usedPorts[value]) return `Port ${value} is already in use`;
		const duplicate = ports.some((p, i) => {
			if (i === index || Number(p.hostPort) !== value) return false;
			if (p.protocol !== port.protocol) return false;
			// Routed rows may share a port on different hostnames
			const routed = port.proxyEnabled && p.proxyEnabled && !isRelayProtocol(port.protocol);
			if (routed) return (p.hostnames ?? []).join(',') === (port.hostnames ?? []).join(',');
			return true;
		});
		if (duplicate)
			return `Duplicate port ${value}/${enumLabel(ModuleProtocolSchema, port.protocol || ModuleProtocol.TCP)}`;
		return '';
	});
}
