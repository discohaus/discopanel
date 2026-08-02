<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { AddressSelect } from '$lib/components/app';
	import { PanelsTopLeft, X } from '@lucide/svelte';
	import { panelHost } from '$lib/utils/host';

	let {
		port,
		hosts,
		onClose
	}: {
		port: number;
		// Hosts the panel answers on, configured else detected
		hosts: string[];
		onClose: () => void;
	} = $props();

	// Browser host fills in when detection has nothing
	let names = $derived(hosts.length > 0 ? hosts : [panelHost()]);

	function addressFor(host: string): string {
		const secure = window.location.protocol === 'https:';
		const proto = secure ? 'https' : 'http';
		return port === (secure ? 443 : 80) ? `${proto}://${host}` : `${proto}://${host}:${port}`;
	}

	let addresses = $derived(names.map(addressFor));
</script>

<div class="flex h-full min-h-0 flex-col">
	<div class="flex items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="flex min-w-0 items-center gap-2.5">
			<PanelsTopLeft class="size-4 shrink-0 text-primary" />
			<div class="min-w-0">
				<h3 class="truncate text-sm font-semibold">DiscoPanel</h3>
				<p class="text-xs text-muted-foreground">Web UI on port {port}</p>
			</div>
		</div>
		<Button variant="ghost" size="icon" class="size-8" onclick={onClose} title="Back to overview">
			<X class="size-4" />
		</Button>
	</div>

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
