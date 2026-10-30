<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { Cable, Globe } from '@lucide/svelte';
	import type { EntryNodeData } from '../topology-data';

	let { data }: NodeProps = $props();
	let d = $derived(data as EntryNodeData);
	// Port nodes read as code, named nodes as prose
	let mono = $derived(d.port > 0);
</script>

<div
	class="flex w-56 items-center gap-2.5 rounded-lg border bg-card px-3 py-2.5 transition-colors {d.active
		? 'border-primary ring-1 ring-primary/40'
		: d.bound
			? 'border-border'
			: 'border-dashed opacity-70'}"
>
	{#if mono}
		<Cable class="size-4 shrink-0 {d.bound ? 'text-foreground' : 'text-muted-foreground'}" />
	{:else}
		<Globe class="size-4 shrink-0 {d.bound ? 'text-foreground' : 'text-muted-foreground'}" />
	{/if}
	<div class="min-w-0 flex-1">
		<p class="truncate text-xs font-medium {mono ? 'font-mono' : ''}">{d.title}</p>
		<p class="truncate text-[11px] text-muted-foreground">{d.sub}</p>
	</div>
	<span class="size-2 shrink-0 rounded-full {d.live ? 'bg-status-ok' : 'bg-status-idle'}"></span>
</div>
<Handle type="target" position={Position.Left} />
<Handle type="source" position={Position.Right} />
