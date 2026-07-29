// Composes the full hostname a short name resolves to
export function composeHostname(host: string, baseUrl: string, useBase: boolean): string {
	const trimmed = host.trim().toLowerCase();
	if (!trimmed) return '';
	if (useBase && baseUrl && !trimmed.includes('.')) return `${trimmed}.${baseUrl}`;
	return trimmed;
}

// Splits a stored hostname into short name and base flag
export function splitHostname(full: string, baseUrl: string): { host: string; useBase: boolean } {
	if (baseUrl && full.toLowerCase().endsWith(`.${baseUrl.toLowerCase()}`)) {
		return { host: full.slice(0, full.length - baseUrl.length - 1), useBase: true };
	}
	// Unmatched hostnames stay untouched so saves never rewrite them
	return { host: full, useBase: false };
}

// Turns a display name into a hostname slug
export function hostnameSlug(name: string): string {
	return name
		.toLowerCase()
		.trim()
		.replace(/\s+/g, '-')
		.replace(/[^a-z0-9-]/g, '');
}
