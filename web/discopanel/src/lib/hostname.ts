// Routing pattern hostnames must pass, mirrors the backend
const hostnamePattern =
	/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/;

// Reports whether a hostname passes the routing pattern
export function validHostname(hostname: string): boolean {
	return hostnamePattern.test(hostname.trim().toLowerCase());
}

// Shortest name stands in for the whole set
export function hostnameSummary(hostnames: string[]): string {
	if (hostnames.length === 0) return '';
	return [...hostnames].sort((a, b) => a.length - b.length || a.localeCompare(b))[0];
}

// Instant domain names resolve without any DNS setup
const instantSuffixes = ['.sslip.io'];
export function needsDnsSetup(hostname: string): boolean {
	const name = hostname.trim().toLowerCase();
	return !instantSuffixes.some((suffix) => name.endsWith(suffix));
}

// Lan or public reachability tag for one address
export function addressScope(address: string): string {
	let host = address.trim().toLowerCase();
	host = host.replace(/^[a-z][a-z0-9+.-]*:\/\//, '');
	host = host.split('/')[0];
	const withPort = host.match(/^(.+):(\d{1,5})$/);
	if (withPort) host = withPort[1];
	let ip = host;
	for (const suffix of instantSuffixes) {
		if (!host.endsWith(suffix)) continue;
		const label = host.slice(0, -suffix.length).split('.').pop() ?? '';
		ip = label.replaceAll('-', '.');
		break;
	}
	if (!/^\d{1,3}(\.\d{1,3}){3}$/.test(ip)) return '';
	const parts = ip.split('.').map(Number);
	if (parts.some((p) => p > 255)) return '';
	const privateIp =
		parts[0] === 10 ||
		parts[0] === 127 ||
		(parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31) ||
		(parts[0] === 192 && parts[1] === 168);
	return privateIp ? 'LAN' : 'Public';
}

// Player facing address, default port stays implicit
export function playerAddress(host: string, listenerPort?: number): string {
	if (!host) return '';
	if (listenerPort && listenerPort !== 25565) return `${host}:${listenerPort}`;
	return host;
}

// Every joinable address for one direct host port
export function directAddresses(
	port: number,
	lanIp: string,
	publicIp: string,
	names: string[]
): string[] {
	const out: string[] = [];
	const seen = new Set<string>();
	for (const host of [lanIp, publicIp, ...names]) {
		if (!host || seen.has(host)) continue;
		seen.add(host);
		out.push(playerAddress(host, port));
	}
	return out;
}

// Turns a display name into a hostname slug
export function hostnameSlug(name: string): string {
	return name
		.toLowerCase()
		.trim()
		.replace(/\s+/g, '-')
		.replace(/[^a-z0-9-]/g, '');
}
