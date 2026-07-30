<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { Users } from '@lucide/svelte';
	import type { SourceNodeData } from '../topology-data';

	let { data }: NodeProps = $props();
	let d = $derived(data as SourceNodeData);
</script>

<div
	class="flex w-56 items-center gap-2.5 rounded-lg border border-primary/30 bg-primary/5 px-3 py-2.5"
>
	<Users class="size-4 shrink-0 text-primary" />
	<div class="min-w-0">
		<p class="truncate text-xs font-medium">Players</p>
		<p class="truncate font-mono text-[11px] text-muted-foreground">
			{d.players} online
		</p>
	</div>
</div>
<Handle type="source" position={Position.Right} />
