<script lang="ts">
	import { onMount } from 'svelte';
	import { slide } from 'svelte/transition';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Button } from '$lib/components/ui/button';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { AddressSelect } from '$lib/components/app';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { Cable, Globe, Network, RefreshCw } from '@lucide/svelte';
	import type { ProxyListener } from '$lib/proto/discopanel/v1/storage_pb';
	import type { GetHostnameSuggestionsResponse } from '$lib/proto/discopanel/v1/proxy_pb';
	import HostnameListInput from '$lib/components/network/hostname-list-input.svelte';
	import { fallbackAddresses, hostnameSlug, playerAddress } from '$lib/hostname';

	let {
		proxyEnabled = false,
		listeners = [],
		serverName = '',
		routeActive = null,
		disabled = false,
		usedPorts = {},
		hostnameError = '',
		showCatchAll = false,
		catchAllError = '',
		useProxy = $bindable(false),
		hostnames = $bindable([]),
		listenerId = $bindable(''),
		catchAll = $bindable(false),
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
		showCatchAll?: boolean;
		catchAllError?: string;
		useProxy?: boolean;
		hostnames?: string[];
		listenerId?: string;
		catchAll?: boolean;
		port?: number;
		portError?: string;
		onAutoAssignPort?: () => void | Promise<void>;
	} = $props();

	let selectedListener = $derived(listeners.find((l) => l.id === listenerId));
	let proxied = $derived(proxyEnabled && useProxy);

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
	let directAddrs = $derived(fallbackAddresses(port, suggestions));

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
				<Button
					type="button"
					variant="outline"
					class="shrink-0"
					onclick={onAutoAssignPort}
					{disabled}
				>
					<RefreshCw class="size-3.5" />
					Auto-assign
				</Button>
			{/if}
		</div>
		{#if portError}
			<p transition:slide={{ duration: 150 }} class="text-xs text-destructive">{portError}</p>
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
			<div class="space-y-2">
				<div class="flex items-center justify-between gap-2">
					{#if proxied && routeActive !== null}
						<span class="flex items-center gap-1.5 text-xs text-muted-foreground">
							<span class="size-2 rounded-full {routeActive ? 'bg-status-ok' : 'bg-status-busy'}"
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

			{#if listeners.length > 0 || showCatchAll}
				<div class="space-y-1">
					<div class="flex flex-wrap items-center justify-between gap-x-8 gap-y-2">
						{#if listeners.length > 1}
							<div class="flex items-center gap-2">
								<Label for="proxy_listener" class="shrink-0">Listener</Label>
								<Select
									type="single"
									value={listenerId}
									onValueChange={(v) => (listenerId = v || '')}
									{disabled}
								>
									<SelectTrigger id="proxy_listener" class="h-8 w-52">
										<span class="truncate">
											{selectedListener
												? `${selectedListener.name} (port ${selectedListener.port})`
												: 'Select a listener'}
										</span>
									</SelectTrigger>
									<SelectContent>
										{#each listeners as listener (listener.id)}
											<SelectItem value={listener.id}>
												{listener.name} (port {listener.port}){listener.isDefault
													? ' (default)'
													: ''}
											</SelectItem>
										{/each}
									</SelectContent>
								</Select>
							</div>
						{:else if listeners.length === 1}
							<div class="flex min-w-0 items-center gap-2 text-sm text-muted-foreground">
								<Network class="size-3.5 shrink-0" />
								<span class="min-w-0 truncate">{listeners[0].name}</span>
								<span class="shrink-0 font-mono text-xs">:{listeners[0].port}</span>
							</div>
						{/if}

						{#if showCatchAll}
							<label class="flex cursor-pointer items-center gap-2">
								<Checkbox bind:checked={catchAll} {disabled} />
								<span class="text-sm">Catch all</span>
								<span class="text-xs text-muted-foreground">
									players land here when their address matches nothing else
								</span>
							</label>
						{/if}
					</div>
					{#if showCatchAll && catchAllError}
						<p transition:slide={{ duration: 150 }} class="truncate text-xs text-destructive">
							{catchAllError}
						</p>
					{/if}
				</div>
			{/if}
		{:else}
			<div class="grid gap-4 sm:grid-cols-2">
				{@render portField()}
				<div class="space-y-2">
					<Label>Public Address</Label>
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
