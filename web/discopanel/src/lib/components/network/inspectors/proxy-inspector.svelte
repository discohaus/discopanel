<script lang="ts">
	import { rpcClient, rpcErrorMessage } from '$lib/api/rpc-client';
	import { Label } from '$lib/components/ui/label';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import HostnameListInput from '../hostname-list-input.svelte';
	import { toast } from 'svelte-sonner';
	import { Globe, Loader2, Network, RotateCcw, Save, Waypoints, X } from '@lucide/svelte';

	let {
		enabled,
		running,
		hostnames,
		listenerCount,
		routeCount,
		hasProxiedWorkloads,
		onRequestDisable,
		onChanged,
		onClose
	}: {
		enabled: boolean;
		running: boolean;
		hostnames: string[];
		listenerCount: number;
		routeCount: number;
		hasProxiedWorkloads: boolean;
		onRequestDisable: () => void;
		onChanged: () => Promise<void>;
		onClose: () => void;
	} = $props();

	let draftEnabled = $state(true);
	let draftHostnames = $state<string[]>([]);
	let saving = $state(false);

	// Draft reseeds whenever the saved config changes
	let seeded = $state('unseeded');
	$effect(() => {
		const snapshot = `${enabled}|${hostnames.join(',')}`;
		if (seeded === snapshot) return;
		seeded = snapshot;
		draftEnabled = enabled;
		draftHostnames = [...hostnames];
	});

	let dirty = $derived(
		draftEnabled !== enabled || draftHostnames.join(',') !== hostnames.join(',')
	);

	function toggleEnabled(next: boolean) {
		// Turning off proxied workloads goes through the convert dialog
		if (!next && enabled && hasProxiedWorkloads) {
			onRequestDisable();
			return;
		}
		draftEnabled = next;
	}

	function discard() {
		seeded = 'unseeded';
	}

	async function save() {
		saving = true;
		try {
			await rpcClient.proxy.updateProxyConfig({
				enabled: draftEnabled,
				hostnames: draftHostnames
			});
			toast.success('Network settings saved');
			await onChanged();
			seeded = 'unseeded';
		} catch (error: unknown) {
			toast.error(rpcErrorMessage(error, 'Failed to save network settings'));
		} finally {
			saving = false;
		}
	}
</script>

<div class="flex h-full min-h-0 flex-col">
	<div class="flex items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="min-w-0">
			<h3 class="text-sm font-semibold">Proxy</h3>
			<p class="text-xs text-muted-foreground">
				{#if !draftEnabled}
					Off, servers bind host ports directly
				{:else if running}
					Running · {listenerCount}
					{listenerCount === 1 ? 'listener' : 'listeners'} · {routeCount}
					{routeCount === 1 ? 'route' : 'routes'}
				{:else}
					Enabled but not running
				{/if}
			</p>
		</div>
		<Button variant="ghost" size="icon" class="size-8" onclick={onClose} title="Close">
			<X class="size-4" />
		</Button>
	</div>

	<div class="min-h-0 flex-1 space-y-5 overflow-y-auto p-4">
		<label class="flex cursor-pointer items-center justify-between gap-3 rounded-lg border p-3.5">
			<span class="text-sm">
				<span class="flex items-center gap-2 font-medium">
					<Waypoints class="size-4 text-primary" />
					Proxy routing
				</span>
				<span class="mt-0.5 block text-xs font-normal text-muted-foreground">
					Route servers and services by hostname on shared ports
				</span>
			</span>
			<Switch checked={draftEnabled} onCheckedChange={toggleEnabled} disabled={saving} />
		</label>

		{#if draftEnabled}
			<div class="space-y-2">
				<Label for="panel-hostnames">Panel hostnames</Label>
				<HostnameListInput inputId="panel-hostnames" bind:hostnames={draftHostnames} disabled={saving} />
			</div>
		{/if}

		<div class="space-y-2 text-sm">
			<div class="flex items-center justify-between gap-3">
				<span class="flex items-center gap-2 text-muted-foreground">
					<Network class="size-3.5" />
					Listeners
				</span>
				<span class="tabular text-xs">{listenerCount}</span>
			</div>
			<div class="flex items-center justify-between gap-3">
				<span class="flex items-center gap-2 text-muted-foreground">
					<Globe class="size-3.5" />
					Active routes
				</span>
				<span class="tabular text-xs">{routeCount}</span>
			</div>
		</div>

		<p class="text-xs text-muted-foreground">Click anything on the map to inspect it</p>
	</div>

	{#if dirty}
		<div class="flex items-center justify-end gap-2 border-t bg-muted/20 px-4 py-3">
			<Button variant="outline" size="sm" onclick={discard} disabled={saving}>
				<RotateCcw class="size-4" />
				Discard
			</Button>
			<Button size="sm" onclick={save} disabled={saving}>
				{#if saving}
					<Loader2 class="size-4 animate-spin" />
				{:else}
					<Save class="size-4" />
				{/if}
				Save changes
			</Button>
		</div>
	{/if}
</div>
