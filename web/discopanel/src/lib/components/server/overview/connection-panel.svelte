<script lang="ts">
	import { onMount } from 'svelte';
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import type { GetProxyStatusResponse } from '$lib/proto/discopanel/v1/proxy_pb';
	import { isUp, TONE_BG, type StatusTone } from '$lib/server-status';
	import { AddressSelect, MotdText } from '$lib/components/app';
	import { Users, Radio } from '@lucide/svelte';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { serverAddresses } from '$lib/hostname';

	let { server }: { server: Server } = $props();

	// Detected host addresses feed unrouted servers
	let proxyStatus = $state<GetProxyStatusResponse | null>(null);
	onMount(async () => {
		try {
			proxyStatus = await rpcClient.proxy.getProxyStatus({}, silentCallOptions);
		} catch {
			// Detection failures keep the browser host fallback
		}
	});

	// Every routed name joins, unrouted lists direct addresses
	let addresses = $derived(serverAddresses(server, proxyStatus));
	let up = $derived(isUp(server.status));
	let maxPlayers = $derived(server.maxPlayersSlp || server.maxPlayers);
	let fillPercent = $derived(maxPlayers > 0 ? (server.playersOnline / maxPlayers) * 100 : 0);

	// Capacity bar tone shifts as the server fills up
	let fillTone = $derived<StatusTone>(
		fillPercent >= 90 ? 'danger' : fillPercent >= 75 ? 'busy' : 'ok'
	);
</script>

<div class="flex flex-col overflow-hidden rounded-xl border bg-card">
	<div class="flex items-center justify-between px-5 pt-4">
		<span class="stat-label">Connection</span>
		{#if up && server.slpAvailable && Number(server.slpLatencyMs) > 0}
			<span
				class="flex items-center gap-1 text-xs text-muted-foreground"
				title="Status ping latency"
			>
				<Radio class="size-3" />
				{Number(server.slpLatencyMs)}ms
			</span>
		{/if}
	</div>

	<div class="space-y-3 px-5 py-4">
		<AddressSelect {addresses} />

		{#if server.motd}
			<div
				class="rounded-lg bg-terminal px-3 py-2.5 text-xs leading-relaxed text-terminal-foreground transition-colors duration-300"
			>
				<MotdText motd={server.motd} />
			</div>
		{/if}
	</div>

	<div class="mt-auto space-y-2 border-t px-5 py-4">
		<div class="flex items-center justify-between">
			<span class="stat-label flex items-center gap-1.5">
				<Users class="size-3.5" />
				Players
			</span>
			{#if up}
				<span class="tabular text-sm font-semibold">{server.playersOnline}/{maxPlayers}</span>
			{:else}
				<span class="font-mono text-sm text-muted-foreground">--</span>
			{/if}
		</div>
		{#if up}
			<div class="h-1.5 overflow-hidden rounded-full bg-muted">
				<div
					class="h-full rounded-full transition-all duration-500 {TONE_BG[fillTone]}"
					style="width: {Math.min(fillPercent, 100)}%"
				></div>
			</div>
			{#if server.playerSample && server.playerSample.length > 0}
				<div class="flex flex-wrap gap-1.5 pt-1">
					{#each server.playerSample as playerName (playerName)}
						<span
							class="inline-flex items-center gap-1.5 rounded-md border bg-muted/40 py-0.5 pr-2 pl-1 text-xs"
						>
							<img
								src="https://mc-heads.net/avatar/{playerName}/16"
								alt=""
								class="pixelated size-4 rounded-sm"
								onerror={(e) => {
									(e.currentTarget as HTMLImageElement).style.display = 'none';
								}}
							/>
							{playerName}
						</span>
					{/each}
				</div>
			{:else}
				<p class="text-xs text-muted-foreground">No players online right now</p>
			{/if}
		{:else}
			<p class="text-xs text-muted-foreground">Server offline</p>
		{/if}
	</div>
</div>
