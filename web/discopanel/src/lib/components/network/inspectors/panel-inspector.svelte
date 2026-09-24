<script lang="ts">
	import { AddressSelect } from '$lib/components/app';
	import InspectorHeader from './inspector-header.svelte';
	import { PanelsTopLeft } from '@lucide/svelte';
	import { panelHost } from '$lib/utils/host';
	import { webUrl } from '$lib/hostname';

	let {
		port,
		hosts,
		onBack
	}: {
		port: number;
		// Hosts the panel answers on, configured else detected
		hosts: string[];
		onBack: () => void;
	} = $props();

	// Browser host fills in when detection has nothing
	let names = $derived(hosts.length > 0 ? hosts : [panelHost()]);

	let addresses = $derived(names.map((host) => webUrl(host, port)));
</script>

<div class="flex h-full min-h-0 flex-col">
	<InspectorHeader title="DiscoPanel" subtitle="Web UI on port {port}" {onBack}>
		{#snippet icon()}
			<PanelsTopLeft class="size-4 shrink-0 text-primary" />
		{/snippet}
	</InspectorHeader>

	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
		<div>
			<span class="stat-label">Answers on</span>
			<div class="mt-1.5">
				<AddressSelect {addresses} link label="address" />
			</div>
		</div>

		<div class="space-y-2 text-sm">
			<div class="flex items-center justify-between gap-3">
				<span class="text-muted-foreground">Listener</span>
				<span class="font-mono text-xs">:{port}/tcp</span>
			</div>
			<div class="flex items-center justify-between gap-3">
				<span class="text-muted-foreground">Serves</span>
				<span class="text-xs">Web UI and API</span>
			</div>
		</div>
	</div>
</div>
