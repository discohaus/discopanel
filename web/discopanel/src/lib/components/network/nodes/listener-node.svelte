<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { Network, Star } from '@lucide/svelte';
	import type { LaneChip, ListenerNodeData } from '../topology-data';

	let { data }: NodeProps = $props();
	let d = $derived(data as ListenerNodeData);

	const DOT: Record<string, string> = {
		active: 'bg-status-ok',
		idle: 'bg-status-idle',
		disabled: 'bg-status-idle opacity-40'
	};

	const CHIP_DOT: Record<string, string> = {
		'topo-edge-ok': 'bg-status-ok',
		'topo-edge-busy': 'bg-status-busy',
		'topo-edge-sleep': 'bg-status-sleep',
		'topo-edge-idle': 'bg-status-idle'
	};

	function pickLane(event: MouseEvent, chip: LaneChip) {
		event.stopPropagation();
		d.onSelect(chip.selection);
	}
</script>

<div
	class="w-56 rounded-lg border bg-card px-3 py-2.5 transition-colors {d.active
		? 'border-primary ring-1 ring-primary/40'
		: d.enabled
			? 'border-border'
			: 'border-dashed opacity-70'}"
>
	<div class="flex items-center gap-2">
		<Network class="size-4 shrink-0 text-primary" />
		<p class="min-w-0 flex-1 truncate text-xs font-medium">{d.name}</p>
		{#if d.isDefault}
			<Star class="size-3 shrink-0 text-primary" />
		{/if}
		<span class="size-2 shrink-0 rounded-full {DOT[d.state]}"></span>
	</div>
	<div class="mt-1 flex items-center gap-2 pl-6">
		<span class="text-[11px] text-muted-foreground">listener</span>
		<span class="font-mono text-[11px] text-muted-foreground">:{d.port}</span>
		{#if d.panel}
			<span class="text-[11px] text-muted-foreground">panel</span>
		{/if}
		{#if d.autoCreated}
			<span class="text-[11px] text-muted-foreground">auto</span>
		{/if}
		{#if d.lanes.length === 0}
			<span class="text-[11px] text-muted-foreground">no lanes</span>
		{/if}
	</div>
	{#if d.lanes.length > 0}
		<div class="mt-1.5 flex flex-wrap items-center gap-1 pl-6">
			{#each d.lanes as chip (chip.protocol)}
				<button
					type="button"
					class="nodrag flex items-center gap-1 rounded-md border px-1.5 py-0.5 font-mono text-[10px] transition-colors {chip.protocol ===
					d.activeLane
						? 'border-primary text-foreground ring-1 ring-primary/40'
						: 'border-border text-muted-foreground hover:border-primary/50 hover:text-foreground'}"
					onclick={(e) => pickLane(e, chip)}
				>
					{chip.label}
					<span class="size-1.5 rounded-full {CHIP_DOT[chip.stateClass] ?? 'bg-status-idle'}"
					></span>
				</button>
			{/each}
		</div>
	{/if}
</div>
<Handle type="target" position={Position.Left} />
<Handle type="source" position={Position.Right} />
