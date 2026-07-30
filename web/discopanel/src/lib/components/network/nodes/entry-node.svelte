<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { Cable, PanelsTopLeft } from '@lucide/svelte';
	import type { EntryNodeData } from '../topology-data';

	let { data, selected }: NodeProps = $props();
	let d = $derived(data as EntryNodeData);
</script>

<div
	class="flex w-56 items-center gap-2.5 rounded-lg border bg-card px-3 py-2.5 transition-colors {selected
		? 'border-primary ring-1 ring-primary/40'
		: d.active
			? 'border-border'
			: 'border-dashed opacity-70'}"
>
	{#if d.variant === 'panel'}
		<PanelsTopLeft class="size-4 shrink-0 text-primary" />
	{:else}
		<Cable class="size-4 shrink-0 {d.active ? 'text-foreground' : 'text-muted-foreground'}" />
	{/if}
	<div class="min-w-0">
		<p class="truncate font-mono text-xs font-medium">{d.title}</p>
		<p class="truncate text-[11px] text-muted-foreground">{d.sub}</p>
	</div>
</div>
<Handle type="target" position={Position.Left} />
<Handle type="source" position={Position.Right} />
