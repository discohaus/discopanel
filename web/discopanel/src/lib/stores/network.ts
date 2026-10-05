import { writable } from 'svelte/store';
import { browser } from '$app/environment';

interface NetworkState {
	online: boolean;
	checking: boolean;
}

// Combine all dead server failures over a debounce period into one probe failure
const FAILURE_DEBOUNCE_MS = 1500;
const FAILURE_COOLDOWN_MS = 10000;
const PROBE_TIMEOUT_MS = 4000;

function createNetworkStore() {
	const { subscribe, update } = writable<NetworkState>({
		online: browser ? navigator.onLine : true,
		checking: false
	});

	let state: NetworkState;
	subscribe((s) => (state = s));

	let initialized = false;
	let pendingProbe: ReturnType<typeof setTimeout> | null = null;
	let lastProbeAt = 0;

	function cancelPendingProbe() {
		if (pendingProbe !== null) {
			clearTimeout(pendingProbe);
			pendingProbe = null;
		}
	}

	function setOnline(online: boolean) {
		update((s) => ({ ...s, online, checking: false }));
	}

	async function checkConnection(): Promise<boolean> {
		if (!browser || state.checking) return state.online;
		if (!navigator.onLine) {
			setOnline(false);
			return false;
		}

		update((s) => ({ ...s, checking: true }));
		const online = await fetch('/api/health', {
			cache: 'no-store',
			signal: AbortSignal.timeout(PROBE_TIMEOUT_MS)
		}).then(
			(response) => response.ok,
			() => false
		);

		setOnline(online);
		return online;
	}

	// Send health check probe to confirm server failure
	function reportFailure(): void {
		if (!browser || pendingProbe !== null) return;
		if (Date.now() - lastProbeAt < FAILURE_COOLDOWN_MS) return;

		pendingProbe = setTimeout(() => {
			pendingProbe = null;
			lastProbeAt = Date.now();
			checkConnection();
		}, FAILURE_DEBOUNCE_MS);
	}

	// A served request is proof the panel is reachable again
	function reportSuccess(): void {
		if (!browser) return;
		cancelPendingProbe();
		if (!state.online) {
			setOnline(true);
		}
	}

	function init() {
		if (!browser || initialized) return;
		initialized = true;

		window.addEventListener('online', () => {
			lastProbeAt = 0;
			checkConnection();
		});

		window.addEventListener('offline', () => {
			cancelPendingProbe();
			setOnline(false);
		});
	}

	return {
		subscribe,
		init,
		checkConnection,
		reportFailure,
		reportSuccess,
		isOnline: () => state.online
	};
}

export const networkStore = createNetworkStore();
