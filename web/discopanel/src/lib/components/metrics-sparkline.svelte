<script lang="ts">
	import { onMount } from 'svelte';
	import { LineChart } from 'layerchart';
	import { scaleTime } from 'd3-scale';
	import { timestampFromDate, timestampDate } from '@bufbuild/protobuf/wkt';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';

	interface Point {
		ts: Date;
		value: number;
	}

	let {
		serverId,
		color = 'var(--metric-tps)'
	}: {
		serverId: string;
		color?: string;
	} = $props();

	let points = $state<Point[]>([]);

	onMount(async () => {
		try {
			const to = new Date();
			const from = new Date(to.getTime() - 60 * 60 * 1000);
			const response = await rpcClient.server.getServerMetricsHistory(
				{
					id: serverId,
					from: timestampFromDate(from),
					to: timestampFromDate(to),
					resolution: 0
				},
				silentCallOptions
			);
			points = response.samples
				.filter((s) => s.timestamp)
				.map((s) => ({ ts: timestampDate(s.timestamp!), value: s.tps }));
		} catch {
			// A card without a sparkline is fine
		}
	});
</script>

{#if points.length > 1 && points.some((p) => p.value > 0)}
	<div class="h-7 w-24" title="TPS, last hour">
		<LineChart
			data={points}
			x="ts"
			xScale={scaleTime()}
			series={[{ key: 'tps', value: (d: Point) => d.value, color }]}
			yDomain={[0, 20]}
			axis={false}
			grid={false}
			rule={false}
			tooltip={false}
			props={{ spline: { strokeWidth: 1.5 } }}
		/>
	</div>
{/if}
