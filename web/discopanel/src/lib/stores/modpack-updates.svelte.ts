import { goto } from '$app/navigation';
import { SvelteSet } from 'svelte/reactivity';
import { resolve } from '$app/paths';
import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
import { rpcClient } from '$lib/api/rpc-client';
import { serversStore } from '$lib/stores/servers';
import { activity, notify } from '$lib/stores/activity.svelte';

// Server advertises an update the operator has not ignored
export function modpackUpdatePending(server: Server): boolean {
	return (
		!!server.modpackUpdateVersionId &&
		server.modpackUpdateVersionId !== server.modpackUpdateDismissedId
	);
}

// Settings tab anchor holding the modpack version picker
export function modpackSettingsHref(serverId: string): string {
	return resolve(`/servers/${serverId}`) + '?tab=settings#modpack';
}

// Marks the advertised version ignored until a newer one appears
export async function dismissModpackUpdate(server: Server): Promise<void> {
	const res = await rpcClient.server.dismissModpackUpdate({
		id: server.id,
		versionId: server.modpackUpdateVersionId
	});
	if (res.server) serversStore.updateServer(res.server);
}

const announced = new SvelteSet<string>();

// Announces each new advertised version once per session
export function watchModpackUpdates(): () => void {
	return serversStore.subscribe((servers) => {
		for (const server of servers) {
			if (!modpackUpdatePending(server)) continue;
			const key = `${server.id}:${server.modpackUpdateVersionId}`;
			if (announced.has(key)) continue;
			announced.add(key);
			notify.info(`${server.name} has a modpack update`, {
				description: server.modpackUpdateVersionName,
				duration: 12000,
				actions: [
					{
						label: 'Open',
						// eslint-disable-next-line svelte/no-navigation-without-resolve -- base is resolved, only query varies
						run: () => goto(modpackSettingsHref(server.id))
					},
					{
						label: 'Ignore',
						run: async (event) => {
							await dismissModpackUpdate(server);
							activity.remove(event.id);
						}
					}
				]
			});
		}
	});
}
