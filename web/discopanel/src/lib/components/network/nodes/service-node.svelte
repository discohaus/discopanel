<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { Globe, MoonStar } from '@lucide/svelte';
	import type { ServiceNodeData } from '../topology-data';

	let { data, selected }: NodeProps = $props();
	let d = $derived(data as ServiceNodeData);

	const BORDER: Record<string, string> = {
		'topo-edge-ok': 'border-l-status-ok',
		'topo-edge-busy': 'border-l-status-busy',
		'topo-edge-sleep': 'border-l-status-sleep',
		'topo-edge-idle': 'border-l-status-idle'
	};
</script>

<div
	class="w-64 rounded-lg border border-l-2 bg-card px-3 py-2.5 transition-colors {BORDER[
		d.stateClass
	] ?? 'border-l-status-idle'} {selected ? 'border-primary ring-1 ring-primary/40' : ''} {d.dimmed
		? 'opacity-60'
		: ''}"
>
	<div class="flex items-center gap-2">
		<Globe class="size-3.5 shrink-0 text-muted-foreground" />
		<p class="min-w-0 flex-1 truncate font-mono text-xs" title={d.summary}>{d.summary}</p>
		{#if d.staleCount > 0}
			<span class="text-[10px] font-medium text-status-busy">stale</span>
		{/if}
		{#if d.wakeable}
			<MoonStar class="size-3 shrink-0 text-status-sleep" />
		{/if}
		{#if d.connections > 0}
			<span class="text-[10px] font-medium text-status-ok tabular-nums">{d.connections}</span>
		{/if}
		{#if d.live && d.connections === 0}
			<span class="size-1.5 shrink-0 rounded-full bg-status-ok"></span>
		{/if}
	</div>
	<p class="mt-1 pl-5.5 font-mono text-[11px] text-muted-foreground">
		:{d.port}{d.nameCount > 1 ? ` · ${d.nameCount} names` : ''}
	</p>
</div>
<Handle type="target" position={Position.Left} />
<Handle type="source" position={Position.Right} />
