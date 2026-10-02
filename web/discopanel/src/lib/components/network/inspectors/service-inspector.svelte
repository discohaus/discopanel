<script lang="ts">
	import { resolve } from '$app/paths';
	import { NetworkOwnerKind } from '$lib/proto/discopanel/v1/proxy_pb';
	import { ModuleProtocol } from '$lib/proto/discopanel/v1/storage_pb';
	import { Badge } from '$lib/components/ui/badge';
	import { AddressSelect } from '$lib/components/app';
	import InspectorHeader from './inspector-header.svelte';
	import { routeStateClass, routeStateLabel, routeStatsSummary } from '$lib/proxy-route';
	import { playerAddress, webUrl } from '$lib/hostname';
	import { laneLabel, type LaneService } from '../topology-data';
	import { ArrowUpRight, Globe } from '@lucide/svelte';

	let {
		service,
		ownerName,
		serverId,
		onBack
	}: {
		service: LaneService;
		ownerName: string;
		serverId: string;
		onBack: () => void;
	} = $props();

	let isHttp = $derived(service.protocols.includes(ModuleProtocol.HTTP));
	let isMinecraft = $derived(service.protocols.includes(ModuleProtocol.MINECRAFT));

	// Addresses shaped by every lane the service rides
	let addresses = $derived.by(() => {
		const out: string[] = [];
		for (const name of service.hostnames) {
			if (isMinecraft) out.push(playerAddress(name, service.port));
			if (isHttp) {
				out.push(webUrl(name, service.port));
			}
			if (!isMinecraft && !isHttp) out.push(name);
		}
		return out;
	});

	let addressLabel = $derived(isMinecraft ? 'Player address' : isHttp ? 'Web address' : 'Address');
	// Per name state matches, one route speaks for the service
	let route = $derived(service.routes[0] ?? null);
	let stats = $derived(route ? routeStatsSummary(route) : '');
	let isModule = $derived(service.ownerKind === NetworkOwnerKind.MODULE);
</script>

<div class="flex h-full min-h-0 flex-col">
	<InspectorHeader
		title={ownerName}
		subtitle="{service.protocols.map(laneLabel).join(' + ')} on port {service.port}"
		{onBack}
	>
		{#snippet icon()}
			<Globe class="size-4 shrink-0 text-primary" />
		{/snippet}
	</InspectorHeader>

	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
		{#if service.hostnames.length > 0}
			<div>
				<span class="stat-label">{addressLabel}</span>
				<div class="mt-1.5">
					<AddressSelect {addresses} />
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
