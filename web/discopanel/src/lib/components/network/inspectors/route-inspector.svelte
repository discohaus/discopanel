<script lang="ts">
	import { resolve } from '$app/paths';
	import { NetworkOwnerKind, type ProxyRoute } from '$lib/proto/discopanel/v1/proxy_pb';
	import { ModuleProtocol } from '$lib/proto/discopanel/v1/storage_pb';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { CopyButton } from '$lib/components/app';
	import { routeStateClass, routeStateLabel, routeStatsSummary } from '$lib/proxy-route';
	import { playerAddress } from '$lib/hostname';
	import { laneLabel } from '../topology-data';
	import { ArrowUpRight, X } from '@lucide/svelte';

	let {
		hostname,
		port,
		protocol,
		route,
		stale = false,
		ownerName,
		serverId,
		onClose
	}: {
		hostname: string;
		port: number;
		protocol: ModuleProtocol;
		route: ProxyRoute | null;
		stale?: boolean;
		ownerName: string;
		serverId: string;
		onClose: () => void;
	} = $props();

	// Address shaped by the lane protocol
	let address = $derived.by(() => {
		if (!hostname) return '';
		if (protocol === ModuleProtocol.HTTP) {
			return port === 80 ? `http://${hostname}` : `http://${hostname}:${port}`;
		}
		if (protocol === ModuleProtocol.MINECRAFT) return playerAddress(hostname, port);
		return '';
	});
	let addressLabel = $derived(protocol === ModuleProtocol.HTTP ? 'Web address' : 'Player address');
	let stats = $derived(route ? routeStatsSummary(route) : '');
	let isModule = $derived(route?.ownerKind === NetworkOwnerKind.MODULE);
</script>

<div class="flex h-full min-h-0 flex-col">
	<div class="flex items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="min-w-0">
			<h3 class="truncate font-mono text-sm font-semibold">{hostname || 'any hostname'}</h3>
			<p class="text-xs text-muted-foreground">{laneLabel(protocol)} route on port {port}</p>
		</div>
		<Button variant="ghost" size="icon" class="size-8" onclick={onClose} title="Back to overview">
			<X class="size-4" />
		</Button>
	</div>

	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
		{#if address}
			<div>
				<span class="stat-label">{addressLabel}</span>
				<div
					class="mt-1.5 flex items-center justify-between gap-2 rounded-lg border bg-muted/40 py-1.5 pr-1.5 pl-3"
				>
					<p class="truncate font-mono text-sm" title={address}>{address}</p>
					<CopyButton text={address} label="Copy address" />
				</div>
			</div>
		{:else if !hostname}
			<p class="text-xs text-muted-foreground">Serves every hostname reaching this lane</p>
		{/if}

		{#if stale}
			<div
				class="rounded-lg border border-status-busy/30 bg-status-busy/10 p-3 text-xs text-status-busy"
			>
				Still serving but no longer reserved
			</div>
		{/if}

		<div class="space-y-2 text-sm">
			<div class="flex items-center justify-between gap-3">
				<span class="text-muted-foreground">State</span>
				{#if route}
					<Badge variant="outline" class="text-xs {routeStateClass(route)}">
						{routeStateLabel(route)}
					</Badge>
				{:else}
					<Badge variant="outline" class="text-xs">Not serving</Badge>
				{/if}
			</div>
			<div class="flex items-center justify-between gap-3">
				<span class="text-muted-foreground">{isModule ? 'Module' : 'Server'}</span>
				{#if serverId}
					<a
						href={resolve(`/servers/${serverId}`)}
						class="inline-flex items-center gap-1 text-sm hover:underline"
					>
						{ownerName}
						<ArrowUpRight class="size-3.5 text-muted-foreground" />
					</a>
				{:else}
					<span>{ownerName}</span>
				{/if}
			</div>
			{#if route?.backendHost}
				<div class="flex items-center justify-between gap-3">
					<span class="text-muted-foreground">Backend</span>
					<span class="font-mono text-xs">{route.backendHost}:{route.backendPort}</span>
				</div>
			{/if}
			{#if route?.portName}
				<div class="flex items-center justify-between gap-3">
					<span class="text-muted-foreground">Port name</span>
					<span class="font-mono text-xs">{route.portName}</span>
				</div>
			{/if}
		</div>

		{#if stats}
			<div>
				<span class="stat-label">Traffic</span>
				<p class="mt-1.5 text-xs text-muted-foreground tabular-nums">{stats}</p>
			</div>
		{/if}
	</div>
</div>
