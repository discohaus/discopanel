<script lang="ts">
	import { resolve } from '$app/paths';
	import type { Module, Server } from '$lib/proto/discopanel/v1/storage_pb';
	import { Button } from '$lib/components/ui/button';
	import { CopyButton, ServerAvatar, StatusBadge } from '$lib/components/app';
	import { playerAddress } from '$lib/hostname';
	import { panelHost } from '$lib/utils/host';
	import InspectorHeader from './inspector-header.svelte';
	import type { ExposedPort } from '../topology-data';
	import { ArrowUpRight, Container } from '@lucide/svelte';

	let {
		server,
		module,
		listenPort,
		extraPorts,
		onBack
	}: {
		server: Server | null;
		module: Module | null;
		listenPort: number;
		extraPorts: ExposedPort[];
		onBack: () => void;
	} = $props();

	let name = $derived(server?.name ?? module?.name ?? '');
	// Every routed name lists, unrouted falls back to the port
	let addresses = $derived.by(() => {
		if (!server) return [];
		if (server.proxyHostnames.length) {
			return server.proxyHostnames.map((h) => playerAddress(h, listenPort));
		}
		if (!server.port) return [];
		return [`${panelHost()}:${server.port}`];
	});
</script>

<div class="flex h-full min-h-0 flex-col">
	<InspectorHeader title={name} subtitle={server ? 'Server' : 'Module'} {onBack}>
		{#snippet icon()}
			{#if server}
				<ServerAvatar name={server.name} favicon={server.favicon} size="sm" />
			{:else}
				<Container class="size-4 shrink-0 text-primary" />
			{/if}
		{/snippet}
	</InspectorHeader>

	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
		{#if server}
			<div class="flex items-center justify-between gap-3">
				<span class="text-sm text-muted-foreground">Status</span>
				<StatusBadge status={server.status} />
			</div>
			{#if addresses.length > 0}
				<div>
					<span class="stat-label">Player address</span>
					<div class="mt-1.5 space-y-1.5">
						{#each addresses as address (address)}
							<div
								class="flex items-center justify-between gap-2 rounded-lg border bg-muted/40 py-1.5 pr-1.5 pl-3"
							>
								<p class="truncate font-mono text-sm" title={address}>{address}</p>
								<CopyButton text={address} label="Copy address" />
							</div>
						{/each}
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
