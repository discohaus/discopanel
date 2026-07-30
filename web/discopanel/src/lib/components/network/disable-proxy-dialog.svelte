<script lang="ts">
	import { rpcClient } from '$lib/api/rpc-client';
	import type {
		ProxiedModulePortImpact,
		ProxiedServerImpact,
		ProxiedServerPortImpact
	} from '$lib/proto/discopanel/v1/proxy_pb';
	import type { Module } from '$lib/proto/discopanel/v1/storage_pb';
	import {
		Dialog,
		DialogContent,
		DialogDescription,
		DialogFooter,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { serversStore } from '$lib/stores/servers';
	import { toast } from 'svelte-sonner';
	import { Loader2, Unplug } from '@lucide/svelte';

	let {
		open = $bindable(false),
		baseUrl,
		modules,
		usedPorts,
		onConverted
	}: {
		open?: boolean;
		baseUrl: string;
		modules: Module[];
		usedPorts: number[];
		onConverted: () => Promise<void>;
	} = $props();

	let loading = $state(false);
	let applying = $state(false);
	let applyError = $state('');
	let servers = $state<ProxiedServerImpact[]>([]);
	let modulePorts = $state<ProxiedModulePortImpact[]>([]);
	let serverPorts = $state<ProxiedServerPortImpact[]>([]);
	let ports = $state<Record<string, number>>({});

	let serverNames = $derived(new Map($serversStore.map((s) => [s.id, s.name])));
	let moduleNames = $derived(new Map(modules.map((m) => [m.id, m.name])));

	// Fresh preflight every time the dialog opens
	$effect(() => {
		if (!open) return;
		loading = true;
		applyError = '';
		rpcClient.proxy
			.getProxyDisableImpact({})
			.then((impact) => {
				servers = impact.servers;
				modulePorts = impact.modulePorts;
				serverPorts = impact.serverPorts;
				ports = Object.fromEntries(impact.servers.map((s) => [s.serverId, s.proposedPort]));
			})
			.catch((error: unknown) => {
				toast.error(error instanceof Error ? error.message : 'Failed to load disable preview');
				open = false;
			})
			.finally(() => (loading = false));
	});

	function portError(serverId: string): string {
		const port = ports[serverId];
		if (!port || port < 1 || port > 65535) return 'Port must be between 1 and 65535';
		for (const [id, p] of Object.entries(ports)) {
			if (id !== serverId && p === port) return 'Port picked twice';
		}
		if (usedPorts.includes(port) && servers.every((s) => s.proposedPort !== port)) {
			return 'Port is already reserved';
		}
		return '';
	}

	let hasErrors = $derived(servers.some((s) => portError(s.serverId)));
	let empty = $derived(
		servers.length === 0 && modulePorts.length === 0 && serverPorts.length === 0
	);

	async function apply() {
		applying = true;
		applyError = '';
		try {
			await rpcClient.proxy.updateProxyConfig({
				enabled: false,
				baseUrl,
				convertToDirect: true,
				assignments: servers.map((s) => ({
					serverId: s.serverId,
					hostname: s.hostname,
					proposedPort: ports[s.serverId] ?? s.proposedPort
				}))
			});
			toast.success('Proxy disabled, servers converted to direct ports');
			open = false;
			await onConverted();
		} catch (error: unknown) {
			applyError = error instanceof Error ? error.message : 'Failed to disable proxy';
		} finally {
			applying = false;
		}
	}
</script>

<Dialog bind:open>
	<DialogContent class="sm:max-w-lg">
		<DialogHeader>
			<DialogTitle>Disable proxy routing?</DialogTitle>
			<DialogDescription>Everything routed by hostname moves to a direct port.</DialogDescription>
		</DialogHeader>

		{#if loading}
			<div class="space-y-2">
				<Skeleton class="h-10 rounded-lg" />
				<Skeleton class="h-10 rounded-lg" />
			</div>
		{:else}
			<div class="max-h-80 space-y-3 overflow-y-auto">
				{#if empty}
					<div
						class="rounded-lg border border-dashed p-4 text-center text-sm text-muted-foreground"
					>
						Nothing routes through the proxy, it can turn off cleanly
					</div>
				{/if}
				{#if servers.length > 0}
					<div class="divide-y rounded-lg border">
						{#each servers as impact (impact.serverId)}
							{@const error = portError(impact.serverId)}
							<div class="flex items-center gap-3 px-3 py-2.5">
								<div class="min-w-0 flex-1">
									<p class="truncate text-sm font-medium">
										{serverNames.get(impact.serverId) ?? impact.serverId.slice(0, 8)}
									</p>
									<p class="truncate font-mono text-xs text-muted-foreground line-through">
										{impact.hostname}
									</p>
								</div>
								<div class="w-28 shrink-0">
									<Input
										type="number"
										min="1"
										max="65535"
										bind:value={ports[impact.serverId]}
										class="h-8 font-mono {error ? 'border-destructive' : ''}"
										aria-label="Direct port"
									/>
									{#if error}
										<p class="mt-1 text-[11px] text-destructive">{error}</p>
									{/if}
								</div>
							</div>
						{/each}
					</div>
				{/if}

				{#if serverPorts.length > 0}
					<div class="divide-y rounded-lg border">
						{#each serverPorts as impact (impact.serverId + impact.portName + impact.currentHostPort)}
							<div class="flex items-center justify-between gap-3 px-3 py-2 text-sm">
								<p class="min-w-0 truncate">
									{serverNames.get(impact.serverId) ?? impact.serverId.slice(0, 8)}
									<span class="text-xs text-muted-foreground">
										· {impact.portName || 'extra port'}
									</span>
								</p>
								<span class="shrink-0 font-mono text-xs">
									{#if impact.currentHostPort !== impact.proposedHostPort}
										:{impact.currentHostPort} → :{impact.proposedHostPort}
									{:else}
										:{impact.proposedHostPort}
									{/if}
								</span>
							</div>
						{/each}
					</div>
				{/if}

				{#if modulePorts.length > 0}
					<div class="divide-y rounded-lg border">
						{#each modulePorts as impact (impact.moduleId + impact.portName)}
							<div class="flex items-center justify-between gap-3 px-3 py-2 text-sm">
								<p class="min-w-0 truncate">
									{moduleNames.get(impact.moduleId) ?? impact.moduleId.slice(0, 8)}
									<span class="text-xs text-muted-foreground">· {impact.portName}</span>
								</p>
								<span class="shrink-0 font-mono text-xs">
									{#if impact.currentHostPort !== impact.proposedHostPort}
										:{impact.currentHostPort} → :{impact.proposedHostPort}
									{:else}
										:{impact.proposedHostPort}
									{/if}
								</span>
							</div>
						{/each}
					</div>
				{/if}
			</div>

			{#if applyError}
				<p class="text-xs text-destructive">{applyError}</p>
			{/if}

			{#if !empty}
				<p class="text-xs text-muted-foreground">Containers recreate with the new ports</p>
			{/if}
		{/if}

		<DialogFooter>
			<Button variant="outline" onclick={() => (open = false)} disabled={applying}>Cancel</Button>
			<Button variant="destructive" onclick={apply} disabled={applying || loading || hasErrors}>
				{#if applying}
					<Loader2 class="size-4 animate-spin" />
				{:else}
					<Unplug class="size-4" />
				{/if}
				Disable and convert
			</Button>
		</DialogFooter>
	</DialogContent>
</Dialog>
