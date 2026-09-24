<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { Globe } from '@lucide/svelte';
	import type { InternetNodeData } from '../topology-data';

	let { data }: NodeProps = $props();
	let d = $derived(data as InternetNodeData);
</script>

<div
	class="w-56 rounded-lg border bg-card px-3 py-2.5 transition-colors {d.active
		? 'border-primary ring-1 ring-primary/40'
		: 'border-border'}"
>
	<div class="flex items-center gap-2">
		<Globe class="size-4 shrink-0 text-primary" />
		<p class="min-w-0 flex-1 truncate text-xs font-medium">Internet</p>
	</div>
	<div class="mt-1 flex items-center gap-2 pl-6">
		<span class="text-[11px] text-muted-foreground">public address</span>
		<span class="truncate font-mono text-[11px] text-muted-foreground">
			{d.publicIp || 'unknown'}
		</span>
	</div>
</div>
<Handle type="source" position={Position.Right} />
