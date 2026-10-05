<script lang="ts">
	import InspectorHeader from './inspector-header.svelte';
	import { Globe } from '@lucide/svelte';

	let {
		publicIp,
		onBack
	}: {
		publicIp: string;
		onBack: () => void;
	} = $props();
</script>

<div class="flex h-full min-h-0 flex-col">
	<InspectorHeader title="Internet" subtitle="Everyone outside your network" {onBack}>
		{#snippet icon()}
			<Globe class="size-4 shrink-0 text-primary" />
		{/snippet}
	</InspectorHeader>

	<div class="min-h-0 flex-1 space-y-2 overflow-y-auto p-4 text-sm">
		<div class="flex items-center justify-between gap-3">
			<span class="text-muted-foreground">Public address</span>
			<span class="font-mono text-xs">{publicIp || 'Not detected'}</span>
		</div>
		<p class="pt-2 text-xs text-muted-foreground">
			Players outside your network reach you through this address
		</p>
	</div>
</div>
