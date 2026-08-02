<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { CopyButton } from '$lib/components/app';
	import { PanelsTopLeft, X } from '@lucide/svelte';
	import { panelHost } from '$lib/utils/host';

	let {
		port,
		hostnames,
		onClose
	}: {
		port: number;
		// Named hostnames the panel serves, base first
		hostnames: string[];
		onClose: () => void;
	} = $props();

	// Names shown, detected host fills an empty list
	let names = $derived(hostnames.length > 0 ? hostnames : [panelHost()]);

	function addressFor(host: string): string {
		const secure = window.location.protocol === 'https:';
		const proto = secure ? 'https' : 'http';
		return port === (secure ? 443 : 80) ? `${proto}://${host}` : `${proto}://${host}:${port}`;
	}
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
			<div class="mt-1.5 divide-y rounded-lg border">
				{#each names as name (name)}
					{@const address = addressFor(name)}
					<div class="flex items-center justify-between gap-2 py-1.5 pr-1.5 pl-3">
						<!-- eslint-disable svelte/no-navigation-without-resolve -- external URL -->
						<a
							href={address}
							target="_blank"
							rel="noopener noreferrer"
							class="truncate font-mono text-xs hover:underline"
							title={address}
						>
							{name}
						</a>
						<!-- eslint-enable svelte/no-navigation-without-resolve -->
						<CopyButton text={address} label="Copy address" />
					</div>
				{/each}
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
