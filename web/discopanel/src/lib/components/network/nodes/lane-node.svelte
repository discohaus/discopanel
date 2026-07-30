<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { ArrowRightLeft, Waypoints } from '@lucide/svelte';
	import type { LaneNodeData } from '../topology-data';

	let { data, selected }: NodeProps = $props();
	let d = $derived(data as LaneNodeData);

	const DOT: Record<string, string> = {
		'topo-edge-ok': 'bg-status-ok',
		'topo-edge-busy': 'bg-status-busy',
		'topo-edge-sleep': 'bg-status-sleep',
		'topo-edge-idle': 'bg-status-idle'
	};
</script>

<div
	class="flex w-44 items-center gap-2 rounded-lg border bg-card px-3 py-2 transition-colors {selected
		? 'border-primary ring-1 ring-primary/40'
		: 'border-border'} {d.dimmed ? 'opacity-60' : ''}"
>
	{#if d.relay}
		<ArrowRightLeft class="size-3.5 shrink-0 text-muted-foreground" />
	{:else}
		<Waypoints class="size-3.5 shrink-0 text-primary" />
	{/if}
	<p class="min-w-0 flex-1 truncate font-mono text-xs">{d.label}</p>
	<span class="size-1.5 shrink-0 rounded-full {DOT[d.stateClass] ?? 'bg-status-idle'}"></span>
</div>
<Handle type="target" position={Position.Left} />
<Handle type="source" position={Position.Right} />
