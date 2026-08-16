<script lang="ts">
	import { rpcClient, rpcErrorMessage } from '$lib/api/rpc-client';
	import { Label } from '$lib/components/ui/label';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import HostnameListInput from '../hostname-list-input.svelte';
	import InspectorHeader from './inspector-header.svelte';
	import { notify } from '$lib/stores/activity.svelte';
	import {
		BadgeCheck,
		DoorOpen,
		Globe,
		Loader2,
		MousePointerClick,
		Network,
		RotateCcw,
		Save,
		Settings,
		TriangleAlert,
		Waypoints
	} from '@lucide/svelte';

	let {
		enabled,
		running,
		hostnames,
		catchAll,
		lobby,
		lobbyOnline,
		listenerCount,
		routeCount,
		hasProxiedWorkloads,
		onRequestDisable,
		onChanged
	}: {
		enabled: boolean;
		running: boolean;
		hostnames: string[];
		catchAll: boolean;
		lobby: boolean;
		lobbyOnline: boolean;
		listenerCount: number;
		routeCount: number;
		hasProxiedWorkloads: boolean;
		onRequestDisable: () => void;
		onChanged: () => Promise<void>;
	} = $props();

	let draftEnabled = $state(true);
	let draftHostnames = $state<string[]>([]);
	let draftCatchAll = $state(true);
	let draftLobby = $state(true);
	let draftLobbyOnline = $state(true);
	let saving = $state(false);

	// Draft reseeds whenever the saved config changes
	let seeded = $state('unseeded');
	$effect(() => {
		const snapshot = `${enabled}|${catchAll}|${lobby}|${lobbyOnline}|${hostnames.join(',')}`;
		if (seeded === snapshot) return;
		seeded = snapshot;
		draftEnabled = enabled;
		draftCatchAll = catchAll;
		draftLobby = lobby;
		draftLobbyOnline = lobbyOnline;
		draftHostnames = [...hostnames];
	});

	let dirty = $derived(
		draftEnabled !== enabled ||
			draftCatchAll !== catchAll ||
			draftLobby !== lobby ||
			draftLobbyOnline !== lobbyOnline ||
			draftHostnames.join(',') !== hostnames.join(',')
	);

	// Catch all only turns off once a name exists
	let catchAllLocked = $derived(draftCatchAll && draftHostnames.length === 0);

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
				hostnames: draftHostnames,
				catchAll: draftCatchAll,
				lobby: draftLobby,
				lobbyOnline: draftLobbyOnline
			});
			notify.success('Network settings saved');
			await onChanged();
			seeded = 'unseeded';
		} catch (error: unknown) {
			notify.error(rpcErrorMessage(error, 'Failed to save network settings'));
		} finally {
			saving = false;
		}
	}
</script>

<div class="flex h-full min-h-0 flex-col">
	<InspectorHeader title="Network Settings">
		{#snippet icon()}
			<Settings class="size-4 shrink-0 text-primary" />
		{/snippet}
	</InspectorHeader>

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

		{#if enabled && !running}
			<p class="flex items-start gap-2 text-xs text-status-busy">
				<TriangleAlert class="mt-0.5 size-3.5 shrink-0" />
				Routing is on but the proxy is not running yet
			</p>
		{/if}

		{#if draftEnabled}
			<div class="space-y-2">
				<Label for="panel-hostnames">Panel hostnames</Label>
				<HostnameListInput
					inputId="panel-hostnames"
					bind:hostnames={draftHostnames}
					disabled={saving}
				/>
			</div>

			<label class="flex cursor-pointer items-center justify-between gap-3 rounded-lg border p-3.5">
				<span class="text-sm">
					<span class="font-medium">Catch all</span>
					<span class="mt-0.5 block text-xs font-normal text-muted-foreground">
						{#if catchAllLocked}
							Add a panel hostname first to turn this off
						{:else}
							The panel answers addresses that are not listed
						{/if}
					</span>
				</span>
				<Switch
					checked={draftCatchAll}
					onCheckedChange={(next) => (draftCatchAll = next)}
					disabled={saving || catchAllLocked}
				/>
			</label>

			<label class="flex cursor-pointer items-center justify-between gap-3 rounded-lg border p-3.5">
				<span class="text-sm">
					<span class="flex items-center gap-2 font-medium">
						<DoorOpen class="size-4 text-primary" />
						The lobby
					</span>
					<span class="mt-0.5 block text-xs font-normal text-muted-foreground">
						Game joins on unlisted addresses land in a lobby with a portal for every server
					</span>
				</span>
				<Switch
					checked={draftLobby}
					onCheckedChange={(next) => (draftLobby = next)}
					disabled={saving}
				/>
			</label>

			{#if draftLobby}
				<label
					class="flex cursor-pointer items-center justify-between gap-3 rounded-lg border p-3.5"
				>
					<span class="text-sm">
						<span class="flex items-center gap-2 font-medium">
							<BadgeCheck class="size-4 text-primary" />
							Verified accounts
						</span>
						<span class="mt-0.5 block text-xs font-normal text-muted-foreground">
							Lobby visitors must be signed in to a real Minecraft account
						</span>
					</span>
					<Switch
						checked={draftLobbyOnline}
						onCheckedChange={(next) => (draftLobbyOnline = next)}
						disabled={saving}
					/>
				</label>
			{/if}
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

		<p class="flex items-start gap-2 text-xs text-muted-foreground">
			<MousePointerClick class="mt-0.5 size-3.5 shrink-0" />
			Click anything on the map to open its settings here
		</p>
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
