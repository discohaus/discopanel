<script lang="ts">
	import InspectorHeader from './inspector-header.svelte';
	import { Router } from '@lucide/svelte';

	let {
		gatewayIp,
		onBack
	}: {
		gatewayIp: string;
		onBack: () => void;
	} = $props();
</script>

<div class="flex h-full min-h-0 flex-col">
	<InspectorHeader title="Router" subtitle="The edge of your network" {onBack}>
		{#snippet icon()}
			<Router class="size-4 shrink-0 text-primary" />
		{/snippet}
	</InspectorHeader>

	<div class="min-h-0 flex-1 space-y-2 overflow-y-auto p-4 text-sm">
		<div class="flex items-center justify-between gap-3">
			<span class="text-muted-foreground">Gateway address</span>
			<span class="font-mono text-xs">{gatewayIp || 'Not detected'}</span>
		</div>
		<p class="pt-2 text-xs text-muted-foreground">
			Forwards internet traffic from your router to this machine
		</p>
	</div>
</div>
