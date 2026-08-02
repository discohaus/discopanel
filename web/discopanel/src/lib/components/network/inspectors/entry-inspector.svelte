<script lang="ts">
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { AddressSelect } from '$lib/components/app';
	import { ArrowUpRight, X } from '@lucide/svelte';

	let {
		port,
		transport,
		ownerName,
		ownerLabel,
		serverId,
		detail,
		addresses = [],
		onClose
	}: {
		port: number;
		transport: string;
		ownerName: string;
		ownerLabel: string;
		serverId: string;
		detail: string;
		addresses?: string[];
		onClose: () => void;
	} = $props();

	let addressLabel = $derived(ownerLabel === 'Server' ? 'Player address' : 'Address');
</script>

<div class="flex h-full min-h-0 flex-col">
	<div class="flex items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="min-w-0">
			<h3 class="truncate font-mono text-sm font-semibold">:{port}</h3>
			<p class="text-xs text-muted-foreground">Direct host bind</p>
		</div>
		<Button variant="ghost" size="icon" class="size-8" onclick={onClose} title="Back to overview">
			<X class="size-4" />
		</Button>
	</div>

	<div class="min-h-0 flex-1 space-y-2 overflow-y-auto p-4 text-sm">
		{#if addresses.length > 0}
			<div class="pb-2">
				<span class="stat-label">{addressLabel}</span>
				<div class="mt-1.5">
					<AddressSelect {addresses} />
				</div>
			</div>
		{/if}
		<div class="flex items-center justify-between gap-3">
			<span class="text-muted-foreground">Port</span>
			<span class="font-mono text-xs">:{port}/{transport}</span>
		</div>
		<div class="flex items-center justify-between gap-3">
			<span class="text-muted-foreground">{ownerLabel}</span>
			{#if serverId}
				<a
					href={resolve(`/servers/${serverId}`)}
					class="inline-flex items-center gap-1 hover:underline"
				>
					{ownerName || 'unknown'}
					<ArrowUpRight class="size-3.5 text-muted-foreground" />
				</a>
			{:else}
				<span>{ownerName || 'unknown'}</span>
			{/if}
		</div>
		{#if detail}
			<div class="flex items-center justify-between gap-3">
				<span class="text-muted-foreground">Purpose</span>
				<span class="text-xs">{detail}</span>
			</div>
		{/if}
		<p class="pt-2 text-xs text-muted-foreground">Bypasses the proxy and binds the host directly</p>
	</div>
</div>
