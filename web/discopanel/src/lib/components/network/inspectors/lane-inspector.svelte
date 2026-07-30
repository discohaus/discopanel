<script lang="ts">
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { ArrowUpRight, X } from '@lucide/svelte';

	let {
		port,
		label,
		relay,
		routeCount,
		ownerName,
		serverId,
		onClose
	}: {
		port: number;
		label: string;
		relay: boolean;
		routeCount: number;
		ownerName: string;
		serverId: string;
		onClose: () => void;
	} = $props();
</script>

<div class="flex h-full min-h-0 flex-col">
	<div class="flex items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="min-w-0">
			<h3 class="truncate font-mono text-sm font-semibold">{label}</h3>
			<p class="text-xs text-muted-foreground">Dispatch lane on port {port}</p>
		</div>
		<Button variant="ghost" size="icon" class="size-8" onclick={onClose} title="Back to overview">
			<X class="size-4" />
		</Button>
	</div>

	<div class="min-h-0 flex-1 space-y-2 overflow-y-auto p-4 text-sm">
		<div class="flex items-center justify-between gap-3">
			<span class="text-muted-foreground">Port</span>
			<span class="font-mono text-xs">:{port}</span>
		</div>
		<div class="flex items-center justify-between gap-3">
			<span class="text-muted-foreground">Protocol</span>
			<span class="font-mono text-xs">{label}</span>
		</div>
		{#if relay}
			<div class="flex items-center justify-between gap-3">
				<span class="text-muted-foreground">Forwards to</span>
				{#if serverId}
					<a
						href={resolve(`/servers/${serverId}`)}
						class="inline-flex items-center gap-1 hover:underline"
					>
						{ownerName || 'unknown'}
						<ArrowUpRight class="size-3.5 text-muted-foreground" />
					</a>
				{:else}
					<span>{ownerName || 'nothing bound'}</span>
				{/if}
			</div>
			<p class="pt-2 text-xs text-muted-foreground">Relays raw traffic without reading hostnames</p>
		{:else}
			<div class="flex items-center justify-between gap-3">
				<span class="text-muted-foreground">Routes</span>
				<span class="tabular text-xs">{routeCount}</span>
			</div>
			<p class="pt-2 text-xs text-muted-foreground">Matches hostnames after protocol detection</p>
		{/if}
	</div>
</div>
