import { writable, derived } from 'svelte/store';
import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
import { ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
import { create } from '@bufbuild/protobuf';
import { ListServersRequestSchema } from '$lib/proto/discopanel/v1/server_pb';

// Pages that need docker-level stats hold a claim while mounted
let fullStatsClaims = 0;

export function claimFullStats(): () => void {
	fullStatsClaims++;
	let released = false;
	return () => {
		if (!released) {
			released = true;
			fullStatsClaims--;
		}
	};
}

function createServersStore() {
	const { subscribe, set, update } = writable<Server[]>([]);

	// Guards against stale responses landing after newer ones
	let fetchSeq = 0;
	let appliedSeq = 0;

	return {
		subscribe,
		set,
		fetchServers: async (skipLoading = false, fullStats?: boolean) => {
			const seq = ++fetchSeq;
			try {
				const wantFull = fullStats ?? fullStatsClaims > 0;
				const request = create(ListServersRequestSchema, { fullStats: wantFull });
				const callOptions = skipLoading ? silentCallOptions : undefined;
				const response = await rpcClient.server.listServers(request, callOptions);
				if (seq > appliedSeq) {
					appliedSeq = seq;
					set(response.servers);
				}
				return response.servers;
			} catch (error) {
				console.error('Failed to fetch servers:', error);
				throw error;
			}
		},
		updateServer: (server: Server) => {
			update((servers) => {
				const index = servers.findIndex((s) => s.id === server.id);
				if (index !== -1) {
					return [...servers.slice(0, index), server, ...servers.slice(index + 1)];
				}
				return [...servers, server];
			});
		},
		removeServer: (id: string) => {
			update((servers) => servers.filter((s) => s.id !== id));
		},
		addServer: (server: Server) => {
			update((servers) => [...servers, server]);
		}
	};
}

export const serversStore = createServersStore();

function getTimestampMs(ts: { seconds: bigint } | undefined): number {
	if (!ts) return 0;
	return Number(ts.seconds) * 1000;
}

// Pins newest first, then players, then running, then idle
export function sortServersByActivity(servers: Server[]): Server[] {
	if (servers.length <= 1) return servers;

	// Find the most recently created or updated server to pin
	let pinnedIdx = 0;
	let pinnedTime = 0;
	for (let i = 0; i < servers.length; i++) {
		const created = getTimestampMs(servers[i].createdAt);
		const updated = getTimestampMs(servers[i].updatedAt);
		const latest = Math.max(created, updated);
		if (latest > pinnedTime) {
			pinnedTime = latest;
			pinnedIdx = i;
		}
	}

	const pinned = servers[pinnedIdx];
	const rest = servers.filter((_, i) => i !== pinnedIdx);

	rest.sort((a, b) => {
		const aRunning = a.status === ServerStatus.RUNNING ? 1 : 0;
		const bRunning = b.status === ServerStatus.RUNNING ? 1 : 0;
		// Running servers first
		if (aRunning !== bRunning) return bRunning - aRunning;

		// Both running: sort by players online desc
		if (aRunning && bRunning) {
			const playerDiff = (b.playersOnline || 0) - (a.playersOnline || 0);
			if (playerDiff !== 0) return playerDiff;
			// Tiebreak by lastStarted (most recent first)
			return getTimestampMs(b.lastStarted) - getTimestampMs(a.lastStarted);
		}

		// Both not running: sort by updatedAt desc
		return getTimestampMs(b.updatedAt) - getTimestampMs(a.updatedAt);
	});

	return [pinned, ...rest];
}

export const activitySortedServers = derived(serversStore, ($servers) =>
	sortServersByActivity([...$servers])
);
