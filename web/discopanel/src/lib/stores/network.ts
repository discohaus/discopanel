import { writable } from 'svelte/store';
import { browser } from '$app/environment';

interface NetworkState {
	online: boolean;
	checking: boolean;
	lastChecked: Date | null;
}

// A dead server drops every in-flight request at once, so failed calls get
// coalesced into one probe instead of a burst.
const FAILURE_DEBOUNCE_MS = 1500;
const FAILURE_COOLDOWN_MS = 10000;

function createNetworkStore() {
	const initialOnline = browser ? navigator.onLine : true;
	const { subscribe, update } = writable<NetworkState>({
		online: initialOnline,
		checking: false,
		lastChecked: null
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

	async function checkConnection(): Promise<boolean> {
		if (!browser || state.checking) return state.online;

		update((s) => ({ ...s, checking: true }));

		if (!navigator.onLine) {
			update((s) => ({ ...s, online: false, checking: false, lastChecked: new Date() }));
			return false;
		}

		try {
			const controller = new AbortController();
			const timeoutId = setTimeout(() => controller.abort(), 4000);
			const response = await fetch('/api/health', {
				method: 'GET',
				cache: 'no-store',
				signal: controller.signal
			});
			clearTimeout(timeoutId);

			const isOk = response.ok;
			update((s) => ({ ...s, online: isOk, checking: false, lastChecked: new Date() }));
			return isOk;
		} catch {
			update((s) => ({ ...s, online: false, checking: false, lastChecked: new Date() }));
			return false;
		}
	}

	// Called when a request failed at the transport level. The error itself
	// never decides, a health probe does, so a single slow or broken call
	// cannot take the whole UI offline.
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
		if (state.online) return;
		update((s) => ({ ...s, online: true, checking: false, lastChecked: new Date() }));
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
			update((s) => ({ ...s, online: false, checking: false, lastChecked: new Date() }));
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
