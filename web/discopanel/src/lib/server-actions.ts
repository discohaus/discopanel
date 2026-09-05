import { rpcClient } from '$lib/api/rpc-client';
import { serversStore } from '$lib/stores/servers';
import { notify } from '$lib/stores/activity.svelte';
import type { Server } from '$lib/proto/discopanel/v1/storage_pb';

export type ServerOp = 'start' | 'stop' | 'restart' | 'recreate';

const ACTION_VERBS: Record<ServerOp, string> = {
	start: 'Starting',
	stop: 'Stopping',
	restart: 'Restarting',
	recreate: 'Recreating'
};

export async function runServerAction(
	action: ServerOp,
	server: Pick<Server, 'id' | 'name'>,
	refetch: () => Promise<unknown> = () => serversStore.fetchServers(true)
): Promise<void> {
	try {
		switch (action) {
			case 'start':
				await rpcClient.server.startServer({ id: server.id });
				break;
			case 'stop':
				await rpcClient.server.stopServer({ id: server.id });
				break;
			case 'restart':
				await rpcClient.server.restartServer({ id: server.id });
				break;
			case 'recreate':
				await rpcClient.server.recreateServer({ id: server.id });
				break;
		}
		notify.success(`${ACTION_VERBS[action]} ${server.name}...`);
		await refetch();
	} catch {
		// Interceptor already toasts the failure
	}
}
