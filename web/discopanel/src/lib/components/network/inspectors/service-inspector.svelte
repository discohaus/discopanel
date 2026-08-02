<script lang="ts">
	import { resolve } from '$app/paths';
	import { NetworkOwnerKind } from '$lib/proto/discopanel/v1/proxy_pb';
	import { ModuleProtocol } from '$lib/proto/discopanel/v1/storage_pb';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { AddressSelect } from '$lib/components/app';
	import { routeStateClass, routeStateLabel, routeStatsSummary } from '$lib/proxy-route';
	import { playerAddress } from '$lib/hostname';
	import { laneLabel, type LaneService } from '../topology-data';
	import { ArrowUpRight, X } from '@lucide/svelte';

	let {
		service,
		ownerName,
		serverId,
		onClose
	}: {
		service: LaneService;
		ownerName: string;
		serverId: string;
		onClose: () => void;
	} = $props();

	// Address shaped by the lane protocol
	function addressFor(name: string): string {
		if (service.protocol === ModuleProtocol.HTTP) {
			return service.port === 80 ? `http://${name}` : `http://${name}:${service.port}`;
		}
		if (service.protocol === ModuleProtocol.MINECRAFT) return playerAddress(name, service.port);
		return name;
	}

	let addressLabel = $derived(
		service.protocol === ModuleProtocol.HTTP ? 'Web address' : 'Player address'
	);
	// Per name state matches, one route speaks for the service
	let route = $derived(service.routes[0] ?? null);
	let stats = $derived(route ? routeStatsSummary(route) : '');
	let isModule = $derived(service.ownerKind === NetworkOwnerKind.MODULE);
</script>

<div class="flex h-full min-h-0 flex-col">
	<div class="flex items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="min-w-0">
			<h3 class="truncate text-sm font-semibold">{ownerName}</h3>
			<p class="text-xs text-muted-foreground">{laneLabel(service.protocol)} on port {service.port}</p>
		</div>
		<Button variant="ghost" size="icon" class="size-8" onclick={onClose} title="Back to overview">
			<X class="size-4" />
		</Button>
	</div>

	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
		{#if service.hostnames.length > 0}
			<div>
				<span class="stat-label">{addressLabel}</span>
				<div class="mt-1.5">
					<AddressSelect addresses={service.hostnames.map(addressFor)} />
				</div>
			</div>
		{/if}

		{#if service.staleHostnames.length > 0}
			<div
				class="rounded-lg border border-status-busy/30 bg-status-busy/10 p-3 text-xs text-status-busy"
			>
				Still serving {service.staleHostnames.join(', ')} without a reservation
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
