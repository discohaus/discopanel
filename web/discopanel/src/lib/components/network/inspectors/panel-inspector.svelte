<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { CopyButton } from '$lib/components/app';
	import { PanelsTopLeft, X } from '@lucide/svelte';

	let {
		port,
		baseUrl,
		onClose
	}: {
		port: number;
		baseUrl: string;
		onClose: () => void;
	} = $props();

	let address = $derived.by(() => {
		const host = baseUrl || 'localhost';
		return port === 80 ? `http://${host}` : `http://${host}:${port}`;
	});
</script>

<div class="flex h-full min-h-0 flex-col">
	<div class="flex items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="flex min-w-0 items-center gap-2.5">
			<PanelsTopLeft class="size-4 shrink-0 text-primary" />
			<div class="min-w-0">
				<h3 class="truncate text-sm font-semibold">DiscoPanel</h3>
				<p class="text-xs text-muted-foreground">This web interface</p>
			</div>
		</div>
		<Button variant="ghost" size="icon" class="size-8" onclick={onClose} title="Back to overview">
			<X class="size-4" />
		</Button>
	</div>

	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
		<div>
			<span class="stat-label">Web address</span>
			<div
				class="mt-1.5 flex items-center justify-between gap-2 rounded-lg border bg-muted/40 py-1.5 pr-1.5 pl-3"
			>
				<p class="truncate font-mono text-sm" title={address}>{address}</p>
				<CopyButton text={address} label="Copy address" />
			</div>
		</div>

		<div class="space-y-2 text-sm">
			<div class="flex items-center justify-between gap-3">
				<span class="text-muted-foreground">Port</span>
				<span class="font-mono text-xs">:{port}/tcp</span>
			</div>
			<div class="flex items-center justify-between gap-3">
				<span class="text-muted-foreground">Serves</span>
				<span class="text-xs">Web UI and API</span>
			</div>
		</div>

		<p class="text-xs text-muted-foreground">Binds the host directly, does not pass the proxy</p>
	</div>
</div>
