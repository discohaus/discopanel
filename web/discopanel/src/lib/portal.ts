// Moves overlay nodes to body so nothing clips them
export function portal(node: HTMLElement) {
	document.body.appendChild(node);
	return { destroy: () => node.remove() };
}
