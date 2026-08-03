<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { Globe, Lock, MoonStar } from '@lucide/svelte';
	import { certificatesStore } from '$lib/stores/certificates.svelte';
	import type { ServiceNodeData } from '../topology-data';

	// Lock follows certificate coverage of the shown name
	certificatesStore.ensure();

	let { data }: NodeProps = $props();
	let d = $derived(data as ServiceNodeData);
	let secured = $derived(d.http && certificatesStore.isSecured(d.summary));

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
	] ?? 'border-l-status-idle'} {d.active ? 'border-primary ring-1 ring-primary/40' : ''} {d.dimmed
		? 'opacity-60'
		: ''}"
>
	<div class="flex items-center gap-2">
		<Globe class="size-3.5 shrink-0 text-muted-foreground" />
		<p class="min-w-0 truncate font-mono text-xs" title={d.names}>{d.summary}</p>
		{#if d.nameCount > 1}
			<span class="shrink-0 font-mono text-[10px] text-muted-foreground" title={d.names}>
				+{d.nameCount - 1}
			</span>
		{/if}
		<span class="min-w-0 flex-1"></span>
		{#if secured}
			<Lock class="size-3 shrink-0 text-emerald-500" aria-label="Secure" />
		{/if}
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
	<div class="mt-1 flex items-center gap-2 pl-5.5">
		<span class="text-[11px] text-muted-foreground">route</span>
		<span class="font-mono text-[11px] text-muted-foreground">:{d.port}</span>
	</div>
</div>
<Handle type="target" position={Position.Left} />
<Handle type="source" position={Position.Right} />
