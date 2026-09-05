<script lang="ts">
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import { tpsTone, TONE_TEXT, TONE_BG } from '$lib/server-status';
	import { formatBytes } from '$lib/utils';
	import { AlertTriangle } from '@lucide/svelte';

	let { server }: { server: Server } = $props();

	let agentHeap = $derived(server.agentConnected && server.heapMaxMb > 0);
	let memUsageMb = $derived(Number(server.memoryUsage));
	let cpuAvg = $derived(
		server.cpuCores > 0 ? server.cpuPercent / server.cpuCores : server.cpuPercent
	);
	let diskUsage = $derived(Number(server.diskUsage));
	let diskUsed = $derived(Number(server.diskUsed));
	let diskTotal = $derived(Number(server.diskTotal));
	let worldSize = $derived(Number(server.worldSize));
</script>

<div class="flex flex-col overflow-hidden rounded-xl border bg-card">
	<div class="px-5 pt-4">
		<span class="stat-label">Resources</span>
	</div>
	<div class="space-y-4 px-5 py-4">
		<div>
			<div class="mb-1.5 flex items-baseline justify-between">
				<span class="text-xs font-medium text-muted-foreground">Memory</span>
				{#if memUsageMb > 0}
					<span class="tabular font-mono text-xs">
						{(memUsageMb / 1024).toFixed(2)} / {(server.memory / 1024).toFixed(1)} GB
					</span>
				{:else if agentHeap}
					<span class="tabular font-mono text-xs">
						{(server.heapUsedMb / 1024).toFixed(2)} / {(server.memory / 1024).toFixed(1)} GB
					</span>
				{:else}
					<span class="tabular font-mono text-xs text-muted-foreground">
						{(server.memory / 1024).toFixed(1)} GB allocated
					</span>
				{/if}
			</div>
			<div
				class="relative h-2 overflow-hidden rounded-full bg-muted"
				title={agentHeap && memUsageMb > 0 ? 'solid Used, light Container' : undefined}
			>
				{#if memUsageMb > 0}
					<div
						class="absolute inset-y-0 left-0 rounded-full bg-primary/25 transition-all duration-500"
						style="width: {Math.min((memUsageMb / server.memory) * 100, 100)}%"
					></div>
				{/if}
				{#if agentHeap}
					<div
						class="absolute inset-y-0 left-0 rounded-full bg-primary transition-all duration-500"
						style="width: {Math.min((server.heapUsedMb / server.memory) * 100, 100)}%"
					></div>
				{:else if memUsageMb > 0}
					<div
						class="absolute inset-y-0 left-0 rounded-full bg-primary transition-all duration-500"
						style="width: {Math.min((memUsageMb / server.memory) * 100, 100)}%"
					></div>
				{/if}
			</div>
			{#if agentHeap}
				<p class="mt-1 text-[11px] text-muted-foreground">
					Used {(server.heapUsedMb / 1024).toFixed(2)}G
				</p>
			{/if}
		</div>

		<div>
			<div class="mb-1.5 flex items-baseline justify-between">
				<span class="text-xs font-medium text-muted-foreground">CPU</span>
				{#if server.cpuPercent > 0}
					<span class="tabular font-mono text-xs">{cpuAvg.toFixed(1)}%</span>
				{:else}
					<span class="font-mono text-xs text-muted-foreground">--</span>
				{/if}
			</div>
			<div
				class="h-2 overflow-hidden rounded-full bg-muted"
				title={server.cpuCores > 0
					? `${server.cpuPercent.toFixed(0)}% total across ${server.cpuCores} cores`
					: undefined}
			>
				{#if server.cpuPercent > 0}
					<div
						class="h-full rounded-full bg-primary transition-all duration-500"
						style="width: {Math.min(cpuAvg, 100)}%"
					></div>
				{/if}
			</div>
			<div class="mt-1 flex items-center justify-between gap-2">
				{#if server.cpuCores > 0 && server.cpuPercent > 0}
					<p class="text-[11px] text-muted-foreground">
						{(server.cpuPercent / 100).toFixed(1)} of {server.cpuCores} cores in use
					</p>
				{:else}
					<span></span>
				{/if}
				{#if server.cpuThrottlePercent > 5}
					<span
						class="flex items-center gap-1 text-[11px] text-status-warn"
						title="Share of recent CPU periods where the container hit its quota"
					>
						<AlertTriangle class="size-3" />
						throttled {server.cpuThrottlePercent.toFixed(0)}%
					</span>
				{/if}
			</div>
		</div>

		<div>
			<div class="mb-1.5 flex items-baseline justify-between">
				<span class="text-xs font-medium text-muted-foreground">Storage</span>
				{#if diskUsage > 0}
					<span class="tabular font-mono text-xs">{formatBytes(diskUsage, 1)}</span>
				{:else}
					<span class="font-mono text-xs text-muted-foreground">--</span>
				{/if}
			</div>
			<div
				class="relative h-2 overflow-hidden rounded-full bg-muted"
				title={diskTotal > 0
					? `this server ${formatBytes(diskUsage)}, volume ${formatBytes(diskUsed)} of ${formatBytes(diskTotal)} used`
					: 'measuring disk usage'}
			>
				{#if diskTotal > 0}
					<div
						class="absolute inset-y-0 left-0 rounded-full bg-muted-foreground/25"
						style="width: {Math.min((diskUsed / diskTotal) * 100, 100)}%"
					></div>
					{#if diskUsage > 0}
						<div
							class="absolute inset-y-0 left-0 rounded-full bg-primary transition-all duration-500"
							style="width: {Math.min(Math.max((diskUsage / diskTotal) * 100, 2), 100)}%"
						></div>
					{/if}
				{/if}
			</div>
			<p class="mt-1 flex items-center justify-between text-[11px] text-muted-foreground">
				<span>
					{#if worldSize > 0}
						world {formatBytes(worldSize, 1)}
					{:else if diskUsage > 0}
						no world saved yet
					{:else}
						measuring server data...
					{/if}
				</span>
				{#if diskTotal > 0}
					<span>{formatBytes(diskTotal - diskUsed, 1)} free of {formatBytes(diskTotal, 1)}</span>
				{/if}
			</p>
		</div>

		<div>
			<div class="mb-1.5 flex items-baseline justify-between">
				<span class="text-xs font-medium text-muted-foreground">TPS</span>
				{#if server.agentConnected && server.tps > 0}
					<span class="tabular font-mono text-xs font-semibold {TONE_TEXT[tpsTone(server.tps)]}">
						{server.tps.toFixed(1)}
						{#if server.mspt > 0}
							<span class="ml-1 font-normal text-muted-foreground"
								>{server.mspt.toFixed(1)}ms/tick</span
							>
						{/if}
					</span>
				{:else}
					<span class="font-mono text-xs text-muted-foreground">--</span>
				{/if}
			</div>
			<div class="h-2 overflow-hidden rounded-full bg-muted">
				{#if server.agentConnected && server.tps > 0}
					<div
						class="h-full rounded-full transition-all duration-500 {TONE_BG[tpsTone(server.tps)]}"
						style="width: {Math.min((server.tps / 20) * 100, 100)}%"
					></div>
				{/if}
			</div>
		</div>
	</div>
</div>
