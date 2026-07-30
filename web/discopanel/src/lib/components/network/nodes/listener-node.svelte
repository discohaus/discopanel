<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { Network, Star } from '@lucide/svelte';
	import type { ListenerNodeData } from '../topology-data';

	let { data, selected }: NodeProps = $props();
	let d = $derived(data as ListenerNodeData);

	const DOT: Record<string, string> = {
		active: 'bg-status-ok',
		idle: 'bg-status-idle',
		disabled: 'bg-status-idle opacity-40'
	};
</script>

<div
	class="w-56 rounded-lg border bg-card px-3 py-2.5 transition-colors {selected
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
		<span class="font-mono text-[11px] text-muted-foreground">:{d.port}</span>
		{#if d.autoCreated}
			<span class="rounded-full border px-1.5 text-[10px] text-muted-foreground">auto</span>
		{/if}
		<span class="text-[11px] text-muted-foreground">
			{d.routeCount > 0 ? `${d.routeCount} ${d.routeCount === 1 ? 'lane' : 'lanes'}` : 'no lanes'}
		</span>
	</div>
</div>
<Handle type="target" position={Position.Left} />
<Handle type="source" position={Position.Right} />
