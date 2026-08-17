import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
import { panelHost } from '$lib/utils/host';

// Detected addresses and base domain, proxy status fits
export interface AddressSource {
	lanIp: string;
	publicIp: string;
	effectiveBaseUrl: string;
}

// Hand mirror of NormalizeHostname and ValidHostname in reservations.go
const hostnamePattern =
	/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/;

// Reports whether a hostname passes the routing pattern
export function validHostname(hostname: string): boolean {
	return hostnamePattern.test(hostname.trim().toLowerCase());
}

// Any name speaks for the set, canonical sort picks one
export function hostnameSummary(hostnames: string[]): string {
	if (hostnames.length === 0) return '';
	return [...hostnames].sort()[0];
}

// Dotted quad shape with every octet in range
function validIp(s: string): boolean {
	return /^\d{1,3}(\.\d{1,3}){3}$/.test(s) && s.split('.').every((p) => Number(p) <= 255);
}

// Ip a name carries, itself or dashed in its first label
function embeddedIp(hostname: string): string {
	const host = hostname.trim().toLowerCase();
	if (validIp(host)) return host;
	const label = host.split('.')[0] ?? '';
	const direct = label.replaceAll('-', '.');
	if (validIp(direct)) return direct;
	const parts = label.split('-');
	if (parts.length < 4) return '';
	const tail = parts.slice(-4).join('.');
	return validIp(tail) ? tail : '';
}

// Names carrying their own address resolve without setup
export function needsDnsSetup(hostname: string): boolean {
	return embeddedIp(hostname) === '';
}

// Dash join keeps address bases one wildcard label
export function joinHostname(label: string, base: string): string {
	if (!base) return '';
	if (!label) return base;
	return embeddedIp(base) ? `${label}-${base}` : `${label}.${base}`;
}

// Suggested names for one label under the base domain
export function suggestionsFor(label: string, base: string): string[] {
	const name = joinHostname(label.trim().toLowerCase(), base.trim().toLowerCase());
	return name && validHostname(name) ? [name] : [];
}

// True when the browser itself rides https
function browserSecure(): boolean {
	return typeof window !== 'undefined' && window.location.protocol === 'https:';
}

// Web link, scheme mirrors the browser
export function webUrl(hostname: string, port: number): string {
	const scheme = browserSecure() ? 'https' : 'http';
	const defaultPort = browserSecure() ? 443 : 80;
	return port && port !== defaultPort ? `${scheme}://${hostname}:${port}` : `${scheme}://${hostname}`;
}

// Upgrades an http link when the browser rides https
export function mirrorScheme(url: string): string {
	if (!browserSecure()) return url;
	return url.replace(/^http:\/\//, 'https://');
}

// Lan or public reachability tag for one address
export function addressScope(address: string): string {
	let host = address.trim().toLowerCase();
	host = host.replace(/^[a-z][a-z0-9+.-]*:\/\//, '');
	host = host.split('/')[0];
	const withPort = host.match(/^(.+):(\d{1,5})$/);
	if (withPort) host = withPort[1];
	const ip = embeddedIp(host);
	if (!ip) return '';
	const parts = ip.split('.').map(Number);
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

// Routed names joined with the server's listener port
export function routedAddresses(server: Server): string[] {
	return server.proxyHostnames.map((h) => playerAddress(h, server.proxyPort));
}

// Detected direct addresses else the browser host
export function fallbackAddresses(port: number, src: AddressSource | null): string[] {
	if (src) {
		const list = directAddresses(port, src.lanIp, src.publicIp, [src.effectiveBaseUrl]);
		if (list.length > 0) return list;
	}
	return [playerAddress(panelHost(), port)];
}

// Routed names else every reachable direct address
export function serverAddresses(server: Server, src: AddressSource | null): string[] {
	const routed = routedAddresses(server);
	return routed.length > 0 ? routed : fallbackAddresses(server.port, src);
}

// Turns a display name into a hostname slug
export function hostnameSlug(name: string): string {
	return name
		.toLowerCase()
		.trim()
		.replace(/\s+/g, '-')
		.replace(/[^a-z0-9-]/g, '');
}
