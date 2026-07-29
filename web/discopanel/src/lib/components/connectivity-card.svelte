<script lang="ts">
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Button } from '$lib/components/ui/button';
	import {
		Select,
		SelectContent,
		SelectItem,
		SelectTrigger
	} from '$lib/components/ui/select';
	import { CopyButton } from '$lib/components/app';
	import {
		ArrowRight,
		Cable,
		Container,
		Globe,
		Network,
		RefreshCw,
		Users
	} from '@lucide/svelte';
	import type { ProxyListener } from '$lib/proto/discopanel/v1/storage_pb';
	import { composeHostname, hostnameSlug } from '$lib/hostname';

	let {
		proxyEnabled = false,
		baseUrl = '',
		listeners = [],
		serverName = '',
		deriveHostname = false,
		routeActive = null,
		disabled = false,
		usedPorts = {},
		hostnameError = '',
		useProxy = $bindable(false),
		hostname = $bindable(''),
		useBaseUrl = $bindable(true),
		listenerId = $bindable(''),
		port = $bindable(25565),
		portError = $bindable(''),
		onAutoAssignPort = undefined
	}: {
		proxyEnabled?: boolean;
		baseUrl?: string;
		listeners?: ProxyListener[];
		serverName?: string;
		deriveHostname?: boolean;
		routeActive?: boolean | null;
		disabled?: boolean;
		usedPorts?: Record<number, boolean>;
		hostnameError?: string;
		useProxy?: boolean;
		hostname?: string;
		useBaseUrl?: boolean;
		listenerId?: string;
		port?: number;
		portError?: string;
		onAutoAssignPort?: () => void | Promise<void>;
	} = $props();

	let hostnameEdited = $state(false);

	let selectedListener = $derived(listeners.find((l) => l.id === listenerId));
	let composedHostname = $derived(composeHostname(hostname, baseUrl, useBaseUrl));
	let hostnameMissing = $derived(proxyEnabled && useProxy && hostname.trim().length === 0);
	let hasDot = $derived(hostname.includes('.'));
	let proxied = $derived(proxyEnabled && useProxy);

	// Address players type into their server list
	let addressPreview = $derived.by(() => {
		if (proxied) {
			const host = composedHostname || (baseUrl ? `your-hostname.${baseUrl}` : 'your-hostname');
			const listenPort = selectedListener?.port ?? 25565;
			return listenPort === 25565 ? host : `${host}:${listenPort}`;
		}
		return `localhost:${port}`;
	});

	// Follows the server name until edited by hand
	$effect(() => {
		if (deriveHostname && useProxy && !hostnameEdited) {
			hostname = hostnameSlug(serverName);
		}
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
		if (!hostname) {
			hostname = hostnameSlug(serverName) || 'minecraft-server';
		}
	}

	function chooseDirect() {
		useProxy = false;
		validatePort(port);
	}
</script>

{#snippet pathNode(icon: typeof Users, title: string, sub: string, tone: 'active' | 'muted')}
	{@const Icon = icon}
	<div
		class="flex min-w-0 flex-1 basis-40 items-center gap-2.5 rounded-lg border px-3 py-2 {tone ===
		'active'
			? 'border-primary/30 bg-primary/5'
			: 'bg-muted/30'}"
	>
		<Icon class="size-4 shrink-0 {tone === 'active' ? 'text-primary' : 'text-muted-foreground'}" />
		<div class="min-w-0">
			<p class="truncate text-xs font-medium">{title}</p>
			<p class="truncate font-mono text-[11px] text-muted-foreground">{sub}</p>
		</div>
	</div>
{/snippet}

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
			<p class="text-xs text-destructive">{portError}</p>
		{:else}
			<p class="text-xs text-muted-foreground">
				Pre-filled with a free port, the Minecraft default is 25565
			</p>
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
				<div class="flex flex-wrap items-center gap-2 text-sm font-medium">
					<Globe class="size-4 text-primary" />
					Proxy hostname
					<span
						class="rounded-full border border-primary/25 bg-primary/10 px-1.5 py-px text-[10px] font-medium text-primary"
					>
						Recommended
					</span>
				</div>
				<p class="mt-1 text-xs text-muted-foreground">
					Players join with a memorable address like {baseUrl
						? `survival.${baseUrl}`
						: 'play.example.com'}
				</p>
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
				<p class="mt-1 text-xs text-muted-foreground">
					Players join with this machine's address and a port number
				</p>
			</button>
		</div>

		{#if useProxy}
			<div class="grid gap-4 {listeners.length > 1 ? 'sm:grid-cols-[minmax(0,1fr)_20rem]' : ''}">
				<div class="space-y-2">
					<Label for="proxy_hostname">Hostname</Label>
					<div class="flex">
						<Input
							id="proxy_hostname"
							placeholder={baseUrl ? 'survival' : 'survival.example.com'}
							bind:value={hostname}
							oninput={(e) => (hostnameEdited = e.currentTarget.value.length > 0)}
							{disabled}
							class="min-w-0 flex-1 {baseUrl ? 'rounded-r-none' : ''} {hostnameMissing ||
							hostnameError
								? 'border-destructive'
								: ''}"
						/>
						{#if baseUrl}
							<button
								type="button"
								aria-pressed={useBaseUrl}
								title={useBaseUrl ? 'Base domain appended, click to detach' : 'Click to append the base domain'}
								onclick={() => (useBaseUrl = !useBaseUrl)}
								{disabled}
								class="inline-flex shrink-0 items-center rounded-r-md border border-l-0 border-input px-3 font-mono text-xs transition-colors {useBaseUrl &&
								!hasDot
									? 'bg-primary/10 text-foreground'
									: 'bg-muted/40 text-muted-foreground line-through'}"
							>
								.{baseUrl}
							</button>
						{/if}
					</div>
					{#if hostnameMissing}
						<p class="text-xs text-destructive">A hostname is required for proxy routing</p>
					{:else if hostnameError}
						<p class="text-xs text-destructive">{hostnameError}</p>
					{:else if baseUrl && useBaseUrl && hasDot}
						<p class="text-xs text-muted-foreground">
							Full domain entered, base domain not appended
						</p>
					{:else if baseUrl && useBaseUrl}
						<p class="text-xs text-muted-foreground">Base domain appended automatically</p>
					{:else}
						<p class="text-xs text-muted-foreground">Enter the full domain players will use</p>
					{/if}
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
				{/if}
			</div>
		{:else}
			{@render portField()}
		{/if}
	{:else}
		<div class="grid gap-4 sm:grid-cols-2">
			{@render portField()}
			<div class="rounded-lg border border-dashed p-3">
				<div class="flex items-center gap-2 text-sm font-medium">
					<Globe class="size-4 text-muted-foreground" />
					Proxy routing is off
				</div>
				<p class="mt-1 text-xs text-muted-foreground">
					With the proxy enabled, players join with a hostname like play.example.com instead of a
					port number
				</p>
			</div>
		</div>
	{/if}
</div>

<div class="border-t px-4 py-4">
	<span class="stat-label">Player address</span>
	<div
		class="mt-2 flex items-center justify-between gap-3 rounded-lg border bg-muted/40 py-2 pr-2 pl-4"
	>
		<p class="truncate font-mono text-lg" title={addressPreview}>{addressPreview}</p>
		<div class="flex shrink-0 items-center gap-2">
			{#if proxied}
				{#if routeActive === false}
					<span
						class="inline-flex items-center gap-1.5 rounded-full border border-status-busy/25 bg-status-busy/10 px-2 py-0.5 text-xs font-medium text-status-busy"
					>
						<Globe class="size-3" />
						Route activates on start
					</span>
				{:else}
					<span
						class="inline-flex items-center gap-1.5 rounded-full border border-status-ok/25 bg-status-ok/10 px-2 py-0.5 text-xs font-medium text-status-ok"
					>
						<Globe class="size-3" />
						Routed via proxy
					</span>
				{/if}
			{:else}
				<span
					class="inline-flex items-center gap-1.5 rounded-full border border-status-idle/25 bg-status-idle/10 px-2 py-0.5 text-xs font-medium text-status-idle"
				>
					<Cable class="size-3" />
					Direct connection
				</span>
			{/if}
			<CopyButton text={addressPreview} label="Copy address" />
		</div>
	</div>
	<p class="mt-2 text-xs text-muted-foreground">What players type into their multiplayer server list</p>
</div>

<div class="border-t bg-muted/20 px-4 py-3.5">
	<div class="flex flex-wrap items-center gap-2">
		{@render pathNode(Users, 'Players', proxied ? addressPreview : 'direct connect', 'active')}
		<ArrowRight class="size-3.5 shrink-0 text-muted-foreground/60" />
		{#if proxied}
			{@render pathNode(
				Network,
				selectedListener?.name || 'Proxy listener',
				`:${selectedListener?.port ?? 25565}`,
				'active'
			)}
			<ArrowRight class="size-3.5 shrink-0 text-muted-foreground/60" />
			{@render pathNode(Container, serverName.trim() || 'Server', 'container :25565', 'muted')}
		{:else}
			{@render pathNode(Container, serverName.trim() || 'Server', `container :${port}`, 'active')}
		{/if}
	</div>
</div>
