<script lang="ts">
	import { type NodeProps } from '@xyflow/svelte';
	import type { ZoneNodeData } from '../topology-data';

	let { data }: NodeProps = $props();
	let d = $derived(data as ZoneNodeData);
</script>

<div
	class="rounded-2xl border border-border/50 bg-muted/10"
	style="width: {d.width}px; height: {d.height}px;"
>
	<div class="flex items-baseline gap-2 px-4 pt-3">
		<span class="text-[10px] font-semibold tracking-[0.14em] text-muted-foreground uppercase">
			{d.label}
		</span>
		<span class="truncate text-[10px] text-muted-foreground/60">{d.sub}</span>
	</div>
</div>
