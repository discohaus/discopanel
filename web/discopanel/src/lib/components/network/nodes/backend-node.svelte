<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { Container } from '@lucide/svelte';
	import { ServerAvatar, StatusDot } from '$lib/components/app';
	import type { BackendNodeData } from '../topology-data';

	let { data, selected }: NodeProps = $props();
	let d = $derived(data as BackendNodeData);
</script>

<div
	class="rounded-lg border bg-card px-3 py-2.5 transition-colors {d.nested
		? 'w-52 border-dashed'
		: 'w-60'} {selected ? 'border-primary ring-1 ring-primary/40' : 'border-border'}"
>
	<div class="flex items-center gap-2.5">
		{#if d.kind === 'server'}
			<ServerAvatar name={d.name} favicon={d.favicon} size="sm" />
		{:else}
			<Container class="size-4 shrink-0 text-muted-foreground" />
		{/if}
		<p class="min-w-0 flex-1 truncate text-xs font-medium">{d.name}</p>
		{#if d.kind === 'server' && d.statusServer}
			<StatusDot status={d.statusServer.status} />
		{:else if d.kind === 'module'}
			<span
				class="size-2 shrink-0 rounded-full {d.moduleRunning ? 'bg-status-ok' : 'bg-status-idle'}"
			></span>
		{/if}
	</div>
	<div class="mt-1 flex items-center gap-2 pl-8">
		{#if d.kind === 'module'}
			<span class="rounded-full border px-1.5 text-[10px] text-muted-foreground">module</span>
			{#if d.parentName}
				<span class="truncate text-[11px] text-muted-foreground">{d.parentName}</span>
			{/if}
		{:else}
			<span class="text-[11px] text-muted-foreground">server</span>
		{/if}
		{#if d.extraPorts.length > 0}
			<span class="rounded-full border px-1.5 text-[10px] text-muted-foreground tabular-nums">
				+{d.extraPorts.length}
				{d.extraPorts.length === 1 ? 'port' : 'ports'}
			</span>
		{/if}
	</div>
</div>
<Handle type="target" position={Position.Left} />
