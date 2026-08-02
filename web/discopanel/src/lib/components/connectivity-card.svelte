<script lang="ts">
	import { onMount } from 'svelte';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Button } from '$lib/components/ui/button';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { AddressSelect } from '$lib/components/app';
	import { panelHost } from '$lib/utils/host';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { Cable, Globe, Network, RefreshCw } from '@lucide/svelte';
	import type { ProxyListener } from '$lib/proto/discopanel/v1/storage_pb';
	import type { GetHostnameSuggestionsResponse } from '$lib/proto/discopanel/v1/proxy_pb';
	import HostnameListInput from '$lib/components/network/hostname-list-input.svelte';
	import { directAddresses, hostnameSlug, playerAddress } from '$lib/hostname';

	let {
		proxyEnabled = false,
		listeners = [],
		serverName = '',
		routeActive = null,
		disabled = false,
		usedPorts = {},
		hostnameError = '',
		useProxy = $bindable(false),
		hostnames = $bindable([]),
		listenerId = $bindable(''),
		port = $bindable(25565),
		portError = $bindable(''),
		onAutoAssignPort = undefined
	}: {
		proxyEnabled?: boolean;
		listeners?: ProxyListener[];
		serverName?: string;
		routeActive?: boolean | null;
		disabled?: boolean;
		usedPorts?: Record<number, boolean>;
		hostnameError?: string;
		useProxy?: boolean;
		hostnames?: string[];
		listenerId?: string;
		port?: number;
		portError?: string;
		onAutoAssignPort?: () => void | Promise<void>;
	} = $props();

	let selectedListener = $derived(listeners.find((l) => l.id === listenerId));
	let proxied = $derived(proxyEnabled && useProxy);

	// Rows double as the joinable player addresses
	function addressFor(name: string): string {
		return playerAddress(name, selectedListener?.port);
	}

	// Detected host addresses feed the direct port list
	let suggestions = $state<GetHostnameSuggestionsResponse | null>(null);
	onMount(async () => {
		try {
			suggestions = await rpcClient.proxy.getHostnameSuggestions({ label: '' }, silentCallOptions);
		} catch {
			// Detection failures keep the browser host fallback
		}
	});

	// Direct ports answer on ips and instant domains alike
	let directAddrs = $derived.by(() => {
		const list = suggestions
			? directAddresses(
					port,
					suggestions.lanIp,
					suggestions.publicIp,
					suggestions.suggestions.map((s) => s.hostname)
				)
			: [];
		return list.length > 0 ? list : [playerAddress(panelHost(), port)];
	});

	function validatePort(p: number) {
		if (p < 1 || p > 65535) {
			portError = 'Port must be between 1 and 65535';
			return;
		}
		if (usedPorts[p]) {
			portError = 'This port is already in use';
			return;
		}
		portError = '';
	}

	function chooseProxy() {
		useProxy = true;
		portError = '';
	}

	function chooseDirect() {
		useProxy = false;
		validatePort(port);
	}
</script>

{#snippet portField()}
	<div class="space-y-2">
		<Label for="server_port">Server port</Label>
		<div class="flex items-center gap-2">
			<Input
				id="server_port"
				type="number"
				min="1"
				max="65535"
				bind:value={port}
				oninput={(e) => validatePort(Number(e.currentTarget.value))}
				{disabled}
				class="flex-1 {portError ? 'border-destructive' : ''}"
			/>
			{#if onAutoAssignPort}
				<Button type="button" variant="outline" class="shrink-0" onclick={onAutoAssignPort} {disabled}>
					<RefreshCw class="size-3.5" />
					Auto-assign
				</Button>
			{/if}
		</div>
		{#if portError}
			<p class="text-xs text-destructive">{portError}</p>
		{/if}
	</div>
{/snippet}

<div class="space-y-4 p-4">
	{#if proxyEnabled}
		<div class="grid gap-3 sm:grid-cols-2" role="radiogroup" aria-label="Connection mode">
			<button
				type="button"
				role="radio"
				aria-checked={useProxy}
				class="rounded-lg border p-4 text-left transition-colors {useProxy
					? 'border-primary bg-primary/5'
					: 'hover:bg-accent/40'}"
				onclick={chooseProxy}
				{disabled}
			>
				<div class="flex items-center gap-2 text-sm font-medium">
					<Globe class="size-4 text-primary" />
					Hostnames
				</div>
				<p class="mt-1 text-xs text-muted-foreground">Players join by hostname</p>
			</button>
			<button
				type="button"
				role="radio"
				aria-checked={!useProxy}
				class="rounded-lg border p-4 text-left transition-colors {!useProxy
					? 'border-primary bg-primary/5'
					: 'hover:bg-accent/40'}"
				onclick={chooseDirect}
				{disabled}
			>
				<div class="flex items-center gap-2 text-sm font-medium">
					<Cable class="size-4 text-primary" />
					Direct port
				</div>
				<p class="mt-1 text-xs text-muted-foreground">Players join by IP and port</p>
			</button>
		</div>

		{#if useProxy}
			<div class="grid gap-4 {listeners.length > 0 ? 'sm:grid-cols-[minmax(0,1fr)_18rem]' : ''}">
				<div class="space-y-2">
					<div class="flex items-center justify-between gap-2">
						<Label for="proxy_hostnames">Player addresses</Label>
						{#if proxied && routeActive !== null}
							<span class="flex items-center gap-1.5 text-xs text-muted-foreground">
								<span
									class="size-2 rounded-full {routeActive ? 'bg-status-ok' : 'bg-status-busy'}"
								></span>
								{routeActive ? 'Routed via proxy' : 'Route activates on start'}
							</span>
						{/if}
					</div>
					<HostnameListInput
						inputId="proxy_hostnames"
						bind:hostnames
						label={hostnameSlug(serverName)}
						placeholder="survival.example.com"
						{disabled}
						error={hostnameError}
						{addressFor}
						copyable
						requireLabel
					/>
				</div>

				{#if listeners.length > 1}
					<div class="space-y-2">
						<Label for="proxy_listener">Proxy listener</Label>
						<Select
							type="single"
							value={listenerId}
							onValueChange={(v) => (listenerId = v || '')}
							{disabled}
						>
							<SelectTrigger id="proxy_listener" class="w-full">
								<span class="truncate">
									{selectedListener
										? `${selectedListener.name} (port ${selectedListener.port})`
										: 'Select a listener'}
								</span>
							</SelectTrigger>
							<SelectContent>
								{#each listeners as listener (listener.id)}
									<SelectItem value={listener.id}>
										{listener.name} (port {listener.port}){listener.isDefault ? ' (default)' : ''}
									</SelectItem>
								{/each}
							</SelectContent>
						</Select>
					</div>
				{:else if listeners.length === 1}
					<div class="space-y-2">
						<Label for="proxy_listener_static">Proxy listener</Label>
						<div
							id="proxy_listener_static"
							class="flex h-9 items-center gap-2 rounded-md border bg-muted/40 px-3 text-sm"
						>
							<Network class="size-3.5 shrink-0 text-muted-foreground" />
							<span class="min-w-0 truncate">{listeners[0].name}</span>
							<span class="ml-auto shrink-0 font-mono text-xs text-muted-foreground">
								:{listeners[0].port}
							</span>
						</div>
					</div>
				{/if}
			</div>
		{:else}
			<div class="grid gap-4 sm:grid-cols-2">
				{@render portField()}
				<div class="space-y-2">
					<Label>Player address</Label>
					<AddressSelect addresses={directAddrs} />
				</div>
			</div>
		{/if}
	{:else}
		<div class="grid gap-4 sm:grid-cols-2">
			{@render portField()}
			<div class="rounded-lg border border-dashed p-3">
				<div class="flex items-center gap-2 text-sm font-medium">
					<Globe class="size-4 text-muted-foreground" />
					Proxy routing is off
				</div>
				<p class="mt-1 text-xs text-muted-foreground">Enable the proxy to use hostnames</p>
			</div>
		</div>
	{/if}
</div>
