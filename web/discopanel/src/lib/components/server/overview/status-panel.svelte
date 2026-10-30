<script lang="ts">
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import { ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import { statusMeta, TONE_TEXT } from '$lib/server-status';
	import { formatUptime } from '$lib/utils/time';
	import { Equalizer } from '$lib/components/app';
	import ServerPerformance from '$lib/components/server-performance.svelte';

	let { server, now = new Date() }: { server: Server; now?: Date } = $props();

	let meta = $derived(statusMeta(server.status));
	let animated = $derived(server.status !== ServerStatus.STOPPED && meta.tone !== 'danger');
</script>

<div class="flex flex-col overflow-hidden rounded-xl border bg-card">
	<div class="flex items-center justify-between px-5 pt-4">
		<span class="stat-label">Status</span>
		{#if server.status === ServerStatus.RUNNING && server.lastStarted}
			<span class="tabular text-xs text-muted-foreground">
				up {formatUptime(server.lastStarted, now)}
			</span>
		{/if}
	</div>
	<div class="flex flex-1 flex-col items-center justify-center gap-3 px-5 py-6">
		<Equalizer tone={meta.tone} size="lg" bars={5} {animated} />
		<div class="text-center">
			<div class="text-xl font-semibold {TONE_TEXT[meta.tone]}">{meta.label}</div>
			<p class="mt-1 text-xs text-muted-foreground">{meta.desc}</p>
		</div>
	</div>
	<ServerPerformance {server} />
</div>
