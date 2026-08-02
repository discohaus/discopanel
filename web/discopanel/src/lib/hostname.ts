// Routing pattern hostnames must pass, mirrors the backend
const hostnamePattern =
	/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/;

// Reports whether a hostname passes the routing pattern
export function validHostname(hostname: string): boolean {
	return hostnamePattern.test(hostname.trim().toLowerCase());
}

// Shortest shared summary for a hostname set
export function hostnameSummary(hostnames: string[]): string {
	if (hostnames.length === 0) return '';
	if (hostnames.length === 1) return hostnames[0];
	const split = hostnames.map((h) => h.split('.'));
	const shared: string[] = [];
	for (let i = 0; ; i++) {
		const part = split[0][i];
		if (!part || !split.every((p) => p.length > i + 1 && p[i] === part)) break;
		shared.push(part);
	}
	if (shared.length > 0) return shared.join('.') + '.*';
	return `${hostnames.length} hostnames`;
}

// Instant domain names resolve without any DNS setup
const instantSuffixes = ['.sslip.io', '.traefik.me'];
export function needsDnsSetup(hostname: string): boolean {
	const name = hostname.trim().toLowerCase();
	return !instantSuffixes.some((suffix) => name.endsWith(suffix));
}

// Player facing address, default port stays implicit
export function playerAddress(host: string, listenerPort?: number): string {
	if (!host) return '';
	if (listenerPort && listenerPort !== 25565) return `${host}:${listenerPort}`;
	return host;
}

// Turns a display name into a hostname slug
export function hostnameSlug(name: string): string {
	return name
		.toLowerCase()
		.trim()
		.replace(/\s+/g, '-')
		.replace(/[^a-z0-9-]/g, '');
}
