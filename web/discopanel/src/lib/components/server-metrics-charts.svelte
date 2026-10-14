<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { timestampFromDate, timestampDate } from '@bufbuild/protobuf/wkt';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { wsClient } from '$lib/stores/websocket.svelte';
	import { Button } from '$lib/components/ui/button';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import MetricsChart from '$lib/components/metrics-chart.svelte';
	import type { Server, MetricsSample, ServerAction } from '$lib/proto/discopanel/v1/storage_pb';
	import { ServerActionKind } from '$lib/proto/discopanel/v1/storage_pb';

	let { server }: { server: Server } = $props();

	const ranges = [
		{ key: '1h', label: '1H', hours: 1 },
		{ key: '6h', label: '6H', hours: 6 },
		{ key: '24h', label: '24H', hours: 24 },
		{ key: '7d', label: '7D', hours: 168 }
	] as const;

	// Only server lifecycle kinds show as markers
	const markerKinds: ServerActionKind[] = [
		ServerActionKind.SERVER_CREATE,
		ServerActionKind.SERVER_START,
		ServerActionKind.SERVER_STOP,
		ServerActionKind.SERVER_PAUSE,
		ServerActionKind.SERVER_WAKE,
		ServerActionKind.SERVER_CRASH,
		ServerActionKind.SERVER_OOM,
		ServerActionKind.SERVER_BOOT_FAILED
	];

	let range = $state<(typeof ranges)[number]>(ranges[0]);
	let samples = $state<MetricsSample[]>([]);
	let events = $state<ServerAction[]>([]);
	let rangeFrom = $state(new Date());
	let rangeTo = $state(new Date());
	let loading = $state(true);
	let unsubscribeMetrics: (() => void) | null = null;

	function markerTone(kind: ServerActionKind): string {
		switch (kind) {
			case ServerActionKind.SERVER_CRASH:
			case ServerActionKind.SERVER_OOM:
			case ServerActionKind.SERVER_BOOT_FAILED:
				return 'bg-red-400';
			case ServerActionKind.SERVER_START:
				return 'bg-emerald-400';
			default:
				return 'bg-zinc-400';
		}
	}

	// Samples are filtered on ingest so timestamp always exists
	function sampleTs(s: MetricsSample): Date {
		return timestampDate(s.timestamp!);
	}

	async function loadEvents(from: Date) {
		try {
			const res = await rpcClient.server.getServerActions(
				{ id: server.id, afterId: 0n },
				silentCallOptions
			);
			events = res.actions.filter(
				(a) =>
					a.timestamp &&
					markerKinds.includes(a.kind) &&
					timestampDate(a.timestamp).getTime() >= from.getTime()
			);
		} catch {
			events = [];
		}
	}

	function markerLeft(a: ServerAction): number {
		if (!a.timestamp) return 0;
		const span = rangeTo.getTime() - rangeFrom.getTime();
		if (span <= 0) return 0;
		return Math.min(
			100,
			Math.max(0, ((timestampDate(a.timestamp).getTime() - rangeFrom.getTime()) / span) * 100)
		);
	}

	function markerTitle(a: ServerAction): string {
		if (!a.timestamp) return a.message;
		const ts = timestampDate(a.timestamp);
		return `${ts.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })} ${a.message}`;
	}

	// Tick charts show only after tick data is observed
	let tickCapable = $derived(
		server.tps > 0 || server.mspt > 0 || samples.some((s) => s.tps > 0 || s.mspt > 0)
	);

	// Heap chart appears once the agent has reported JVM heap
	let heapCapable = $derived(
		(server.agentConnected && server.heapUsedMb > 0) || samples.some((s) => s.heapUsedMb > 0)
	);

	async function loadHistory() {
		loading = true;
		try {
			const to = new Date();
			const from = new Date(to.getTime() - range.hours * 60 * 60 * 1000);
			rangeFrom = from;
			rangeTo = to;
			const response = await rpcClient.server.getServerMetricsHistory(
				{
					id: server.id,
					from: timestampFromDate(from),
					to: timestampFromDate(to),
					resolution: 0
				},
				silentCallOptions
			);
			samples = response.samples.filter((s) => s.timestamp !== undefined);
			await loadEvents(from);
		} catch (error) {
			console.error('Failed to load metrics history:', error);
		} finally {
			loading = false;
		}
	}

	function selectRange(r: (typeof ranges)[number]) {
		if (range.key === r.key) return;
		range = r;
		loadHistory();
	}

	function appendLive(serverId: string, sample: MetricsSample) {
		if (serverId !== server.id) return;
		if (!sample.timestamp) return;
		const cutoff = Date.now() - range.hours * 60 * 60 * 1000;
		samples = [...samples.filter((s) => sampleTs(s).getTime() >= cutoff), sample];
	}

	// Reload history and swap subscriptions per server
	let subscribedId = '';
	$effect(() => {
		if (server.id === subscribedId) return;
		if (subscribedId) wsClient.unsubscribeMetrics(subscribedId);
		subscribedId = server.id;
		samples = [];
		loadHistory();
		wsClient.subscribeMetrics(server.id);
	});

	onMount(() => {
		unsubscribeMetrics = wsClient.onMetrics(appendLive);
	});

	onDestroy(() => {
		unsubscribeMetrics?.();
		if (subscribedId) wsClient.unsubscribeMetrics(subscribedId);
	});

	function formatMemory(v: number): string {
		return v >= 1024 ? `${(v / 1024).toFixed(1)}G` : `${Math.round(v)}M`;
	}

	let tpsPoints = $derived(samples.map((s) => ({ ts: sampleTs(s), value: s.tps })));
	let msptPoints = $derived(samples.map((s) => ({ ts: sampleTs(s), value: s.mspt })));
	let playerPoints = $derived(samples.map((s) => ({ ts: sampleTs(s), value: s.players })));
	let cpuPoints = $derived(samples.map((s) => ({ ts: sampleTs(s), value: s.cpuPercent })));
	let memoryPoints = $derived(samples.map((s) => ({ ts: sampleTs(s), value: s.memoryMb })));
	let heapPoints = $derived(samples.map((s) => ({ ts: sampleTs(s), value: s.heapUsedMb })));
</script>

<div class="overflow-hidden rounded-xl border bg-card">
	<div class="p-4 sm:p-5">
		<div class="mb-4 flex items-center justify-between">
			<div>
				<h3 class="stat-label">Metrics</h3>
				<p class="mt-0.5 text-xs text-muted-foreground">
					Sampled every 30 seconds while the server runs
				</p>
			</div>
			<div class="flex gap-1">
				{#each ranges as r (r.key)}
					<Button
						variant={range.key === r.key ? 'secondary' : 'ghost'}
						size="sm"
						class="h-7 px-2 text-xs"
						onclick={() => selectRange(r)}
					>
						{r.label}
					</Button>
				{/each}
			</div>
		</div>

		{#if events.length > 0 && samples.length > 0}
			<div class="mb-3 flex items-center gap-2">
				<span class="shrink-0 text-[10px] tracking-wide text-muted-foreground uppercase">
					Events
				</span>
				<div class="relative h-3 flex-1 rounded bg-muted/40">
					{#each events as a (a.id)}
						<span
							class="absolute top-1/2 size-2 -translate-x-1/2 -translate-y-1/2 rounded-full {markerTone(
								a.kind
							)}"
							style="left: {markerLeft(a)}%"
							title={markerTitle(a)}
						></span>
					{/each}
				</div>
			</div>
		{/if}

		{#if loading && samples.length === 0}
			<div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
				{#each Array(3) as _, i (i)}
					<Skeleton class="h-40 rounded-lg" />
				{/each}
			</div>
		{:else if samples.length === 0}
			<div class="py-10 text-center text-sm text-muted-foreground">
				No metrics recorded for this range yet. Charts fill in while the server runs.
			</div>
		{:else}
			<div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
				{#if tickCapable}
					<MetricsChart
						title="TPS"
						color="var(--metric-tps)"
						points={tpsPoints}
						yDomain={[0, 20]}
						format={(v) => v.toFixed(1)}
					/>
					<MetricsChart
						title="Tick time"
						color="var(--metric-mspt)"
						points={msptPoints}
						format={(v) => v.toFixed(1)}
						unit="ms"
					/>
				{/if}
				<MetricsChart
					title="Players"
					color="var(--metric-players)"
					points={playerPoints}
					format={(v) => Math.round(v).toString()}
				/>
				<MetricsChart
					title="CPU"
					color="var(--metric-cpu)"
					points={cpuPoints}
					format={(v) => v.toFixed(0)}
					unit="%"
				/>
				{#if heapCapable}
					<MetricsChart
						title="Used"
						color="var(--metric-heap)"
						points={heapPoints}
						format={formatMemory}
					/>
				{/if}
				<MetricsChart
					title={heapCapable ? 'Container' : 'Memory'}
					color="var(--metric-memory)"
					points={memoryPoints}
					format={formatMemory}
				/>
			</div>
		{/if}
	</div>
</div>
