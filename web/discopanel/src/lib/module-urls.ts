import { mirrorScheme } from '$lib/hostname';
import type { Module } from '$lib/proto/discopanel/v1/storage_pb';

// Alias templates whose host slot fans out per hostname
const HOST_ALIASES = ['{{host.hostname}}', '{{server.proxy_hostnames}}'];

// Port name a url template binds to
const PORT_ALIAS = /\{\{module\.ports\.([^.}]+)\./;

// Fills alias templates from one module's resolved values
function resolveAliases(input: string, vals: Record<string, string>): string {
	return input.replace(/\{\{[^}]+\}\}/g, (match) => vals[match] ?? match);
}

// Host templates expand into one url per routed hostname
export function moduleUrls(input: string, module: Module, vals: Record<string, string>): string[] {
	const portName = PORT_ALIAS.exec(input)?.[1];
	const port = module.ports.find((p) => p.name === portName);
	// Port hostnames win, empty falls back to server names
	const hostnames =
		port && port.hostnames.length > 0 ? port.hostnames : (module.serverProxyHostnames ?? []);
	const hostAlias = HOST_ALIASES.find((alias) => input.includes(alias));
	if (hostAlias && hostnames.length > 0) {
		return hostnames.map((name) =>
			mirrorScheme(resolveAliases(input.replaceAll(hostAlias, name), vals))
		);
	}
	return [mirrorScheme(resolveAliases(input, vals))];
}
