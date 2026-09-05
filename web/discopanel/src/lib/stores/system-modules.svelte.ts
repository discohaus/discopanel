import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
import type { Module } from '$lib/proto/discopanel/v1/storage_pb';
import { ModuleStatus } from '$lib/proto/discopanel/v1/storage_pb';

const POLL_MS = 30000;

// Polls panel-owned global modules for the app shell
class SystemModulesStore {
	modules = $state<Module[]>([]);
	loaded = $state(false);
	private timer: ReturnType<typeof setInterval> | null = null;

	running = $derived(this.modules.filter((m) => m.status === ModuleStatus.RUNNING));

	start() {
		if (this.timer) return;
		this.fetch();
		this.timer = setInterval(() => this.fetch(), POLL_MS);
	}

	stop() {
		if (this.timer) {
			clearInterval(this.timer);
			this.timer = null;
		}
		this.modules = [];
		this.loaded = false;
	}

	refresh() {
		if (this.timer) this.fetch();
	}

	private async fetch() {
		try {
			const response = await rpcClient.module.listModules({ fullStats: true }, silentCallOptions);
			// No server attachment means panel-managed global module
			this.modules = response.modules.filter((m) => !m.serverId);
			this.loaded = true;
		} catch {
			/* Shell indicator only, errors stay quiet */
		}
	}
}

export const systemModules = new SystemModulesStore();
