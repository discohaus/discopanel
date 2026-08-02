<script lang="ts">
	import { rpcClient } from '$lib/api/rpc-client';
	import { registerRefresh } from '$lib/stores/refresh';
	import { formatBytes } from '$lib/utils';
	import { Button } from '$lib/components/ui/button';
	import { ConfirmDialog } from '$lib/components/app';
	import { toast } from 'svelte-sonner';
	import { Archive, RotateCcw } from '@lucide/svelte';
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import type { Backup } from '$lib/proto/discopanel/v1/server_pb';
	import { ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import { timestampDate } from '@bufbuild/protobuf/wkt';

	let { server, active = false }: { server: Server; active?: boolean } = $props();

	let backups = $state<Backup[]>([]);
	let loading = $state(true);
	let restoreTarget = $state<Backup | null>(null);
	let restoreOpen = $state(false);
	let restoring = $state(false);

	let hasLoaded = false;
	let previousServerId = $state(server.id);

	let serverStopped = $derived(
		server.status === ServerStatus.STOPPED || server.status === ServerStatus.ERROR
	);

	async function loadBackups() {
		try {
			const res = await rpcClient.server.listBackups({ id: server.id });
			backups = res.backups;
		} catch {
			backups = [];
		} finally {
			loading = false;
		}
	}

	function requestRestore(backup: Backup) {
		restoreTarget = backup;
		restoreOpen = true;
	}

	async function confirmRestore() {
		if (!restoreTarget) return;
		restoring = true;
		try {
			const res = await rpcClient.server.restoreBackup({
				id: server.id,
				fileName: restoreTarget.fileName
			});
			toast.success(res.message);
			await loadBackups();
		} catch (e) {
			toast.error(e instanceof Error ? e.message : 'Restore failed');
		} finally {
			restoring = false;
			restoreOpen = false;
			restoreTarget = null;
		}
	}


	function formatWhen(backup: Backup): string {
		if (!backup.createdAt) return '';
		return timestampDate(backup.createdAt).toLocaleString(undefined, {
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}

	// Reset state when server changes
	$effect(() => {
		if (server.id !== previousServerId) {
			previousServerId = server.id;
			backups = [];
			loading = true;
			hasLoaded = false;
		}
	});

	$effect(() => {
		if (active && !hasLoaded) {
			hasLoaded = true;
			loadBackups();
		}
	});

	$effect(() => {
		if (!active) return;
		return registerRefresh(loadBackups);
	});
</script>

<div class="rounded-xl border bg-card p-4">
	<div class="mb-3">
		<h3 class="stat-label">World backups</h3>
		<p class="mt-0.5 text-xs text-muted-foreground">
			{serverStopped ? 'Restore rewinds the world' : 'Stop the server to restore'}
		</p>
	</div>

	{#if loading}
		<p class="py-4 text-center text-xs text-muted-foreground">Loading...</p>
	{:else if backups.length === 0}
		<p class="py-4 text-center text-xs text-muted-foreground">
			No backups yet, add a backup task to create them
		</p>
	{:else}
		<div class="max-h-64 space-y-1 overflow-y-auto">
			{#each backups as backup (backup.fileName)}
				<div class="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-xs">
					<Archive class="size-3.5 shrink-0 text-muted-foreground" />
					<span class="min-w-0 flex-1 truncate font-mono">{backup.fileName}</span>
					<span class="tabular shrink-0 text-muted-foreground">{formatBytes(Number(backup.size), 1)}</span>
					<span class="tabular shrink-0 text-muted-foreground">{formatWhen(backup)}</span>
					<Button
						variant="outline"
						size="sm"
						class="h-6 shrink-0 px-2 text-xs"
						disabled={!serverStopped || restoring}
						onclick={() => requestRestore(backup)}
					>
						<RotateCcw class="size-3" />
						Restore
					</Button>
				</div>
			{/each}
		</div>
	{/if}
</div>

<ConfirmDialog
	bind:open={restoreOpen}
	title="Restore {restoreTarget?.fileName ?? 'backup'}?"
	description="The current world is snapshotted first, then replaced by this backup."
	confirmLabel="Restore"
	onConfirm={confirmRestore}
/>
