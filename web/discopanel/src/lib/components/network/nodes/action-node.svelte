<script lang="ts">
	import { type NodeProps } from '@xyflow/svelte';
	import { Plus } from '@lucide/svelte';
	import type { ActionNodeData } from '../topology-data';

	let { data }: NodeProps = $props();
	let d = $derived(data as ActionNodeData);
</script>

<div
	class="flex w-56 cursor-pointer items-center gap-2.5 rounded-lg border border-dashed bg-card/60 px-3 py-2 text-muted-foreground transition-colors hover:border-primary/50 hover:text-foreground {d.active
		? 'border-primary text-foreground ring-1 ring-primary/40'
		: ''}"
>
	<Plus class="size-4 shrink-0" />
	<p class="truncate text-xs font-medium">{d.label}</p>
</div>
