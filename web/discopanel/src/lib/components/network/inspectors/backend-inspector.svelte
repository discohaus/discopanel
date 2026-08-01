<script lang="ts">
	import { resolve } from '$app/paths';
	import type { Module, Server } from '$lib/proto/discopanel/v1/storage_pb';
	import { Button } from '$lib/components/ui/button';
	import { CopyButton, ServerAvatar, StatusBadge } from '$lib/components/app';
	import { playerAddress } from '$lib/hostname';
	import { panelHost } from '$lib/utils/host';
	import type { ExposedPort } from '../topology-data';
	import { ArrowUpRight, Container, X } from '@lucide/svelte';

	let {
		server,
		module,
		listenPort,
		baseUrl,
		extraPorts,
		onClose
	}: {
		server: Server | null;
		module: Module | null;
		listenPort: number;
		baseUrl: string;
		extraPorts: ExposedPort[];
		onClose: () => void;
	} = $props();

	let name = $derived(server?.name ?? module?.name ?? '');
	let address = $derived.by(() => {
		if (!server) return '';
		if (server.proxyHostname) return playerAddress(server.proxyHostname, listenPort);
		if (!server.port) return '';
		return `${panelHost(baseUrl)}:${server.port}`;
	});
</script>

<div class="flex h-full min-h-0 flex-col">
	<div class="flex items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="flex min-w-0 items-center gap-2.5">
			{#if server}
				<ServerAvatar name={server.name} favicon={server.favicon} size="sm" />
			{:else}
				<Container class="size-4 text-muted-foreground" />
			{/if}
			<div class="min-w-0">
				<h3 class="truncate text-sm font-semibold">{name}</h3>
				<p class="text-xs text-muted-foreground">{server ? 'Server' : 'Module'}</p>
			</div>
		</div>
		<Button variant="ghost" size="icon" class="size-8" onclick={onClose} title="Back to overview">
			<X class="size-4" />
		</Button>
	</div>

	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
		{#if server}
			<div class="flex items-center justify-between gap-3">
				<span class="text-sm text-muted-foreground">Status</span>
				<StatusBadge status={server.status} />
			</div>
			{#if address}
				<div>
					<span class="stat-label">Player address</span>
					<div
						class="mt-1.5 flex items-center justify-between gap-2 rounded-lg border bg-muted/40 py-1.5 pr-1.5 pl-3"
					>
						<p class="truncate font-mono text-sm" title={address}>{address}</p>
						<CopyButton text={address} label="Copy address" />
					</div>
				</div>
			{/if}
		{/if}

		{#if module}
			<div class="flex items-center justify-between gap-3 text-sm">
				<span class="text-muted-foreground">Ports</span>
				<span class="font-mono text-xs">
					{module.ports
						.filter((p) => p.hostPort > 0)
						.map((p) => `${p.name} :${p.hostPort}`)
						.join(' · ') || 'none'}
				</span>
			</div>
		{/if}

		{#if extraPorts.length > 0}
			<div>
				<span class="stat-label">Exposed ports</span>
				<div class="mt-1.5 divide-y rounded-lg border">
					{#each extraPorts as ep (ep.port + ep.label)}
						<div class="flex items-center justify-between gap-3 px-3 py-2 text-xs">
							<span class="text-muted-foreground">{ep.label}</span>
							<span class="font-mono">:{ep.port}/{ep.transport}</span>
						</div>
					{/each}
				</div>
			</div>
		{/if}
	</div>

	{#if server}
		<div class="border-t bg-muted/20 px-4 py-3">
			<Button variant="outline" size="sm" href={resolve(`/servers/${server.id}`)}>
				Open server
				<ArrowUpRight class="size-3.5" />
			</Button>
		</div>
	{/if}
</div>
