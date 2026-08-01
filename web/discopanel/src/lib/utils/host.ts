// Host the browser actually reached the panel on
export function panelHost(preferred?: string): string {
	return preferred || window.location.hostname;
}
