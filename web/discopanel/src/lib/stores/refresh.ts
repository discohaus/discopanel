type Refresher = () => unknown;

const refreshers = new Set<Refresher>();

export function registerRefresh(fn: Refresher): () => void {
	refreshers.add(fn);
	return () => {
		refreshers.delete(fn);
	};
}

export async function runPageRefreshers(): Promise<void> {
	await Promise.allSettled([...refreshers].map((fn) => Promise.resolve(fn())));
}
