<script lang="ts" module>
	import type {
		AddressCandidate,
		CheckNetworkReachabilityResponse
	} from '$lib/proto/discopanel/v1/proxy_pb';

	// Probe results survive inspector remounts
	let cachedProbe: { result: CheckNetworkReachabilityResponse; at: number } | null = null;
	const PROBE_CACHE_MS = 5 * 60 * 1000;
</script>

<script lang="ts">
	import { onMount } from 'svelte';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { AddressSource, BaseUrlSource } from '$lib/proto/discopanel/v1/proxy_pb';
	import { Input } from '$lib/components/ui/input';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import { toast } from 'svelte-sonner';
	import {
		AlertTriangle,
		Check,
		Globe,
		Loader2,
		Network,
		RadioTower,
		RotateCcw,
		Save,
		Zap
	} from '@lucide/svelte';

	let {
		enabled,
		running,
		baseUrl,
		effectiveBaseUrl,
		baseUrlSource,
		listenerCount,
		routeCount,
		hasProxiedWorkloads,
		onRequestDisable,
		onChanged
	}: {
		enabled: boolean;
		running: boolean;
		baseUrl: string;
		effectiveBaseUrl: string;
		baseUrlSource: BaseUrlSource;
		listenerCount: number;
		routeCount: number;
		hasProxiedWorkloads: boolean;
		onRequestDisable: () => void;
		onChanged: () => Promise<void>;
	} = $props();

	let draftEnabled = $state(false);
	let customDomain = $state('');
	let saving = $state(false);
	let candidates = $state<AddressCandidate[]>([]);
	let probing = $state(false);
	let probe = $state<CheckNetworkReachabilityResponse | null>(cachedProbe?.result ?? null);

	// Saved snapshot drives dirty detection
	let seeded = $state('');
	$effect(() => {
		const snapshot = `${enabled}|${baseUrl}`;
		if (seeded === snapshot) return;
		seeded = snapshot;
		draftEnabled = enabled;
		customDomain = baseUrl;
	});

	let draftBaseUrl = $derived(customDomain.trim().toLowerCase());
	let dirty = $derived(draftEnabled !== enabled || draftBaseUrl !== baseUrl);
	let autoDomain = $derived(candidates[0]?.domain ?? (baseUrlSource === BaseUrlSource.AUTO ? effectiveBaseUrl : ''));

	let probeFailures = $derived(
		probe?.ports.filter((p) => p.checked && !p.reachable) ?? []
	);
	let probeUnknown = $derived(probe?.ports.filter((p) => !p.checked) ?? []);

	onMount(() => {
		loadCandidates();
		if (!cachedProbe || Date.now() - cachedProbe.at > PROBE_CACHE_MS) {
			runProbe(true);
		}
	});

	async function loadCandidates() {
		try {
			const res = await rpcClient.proxy.getNetworkAddresses({}, silentCallOptions);
			candidates = res.candidates;
		} catch {
			// Presets are optional sugar
		}
	}

	async function runProbe(silent = false) {
		if (probing) return;
		probing = true;
		try {
			const result = await rpcClient.proxy.checkNetworkReachability(
				{},
				silent ? silentCallOptions : undefined
			);
			probe = result;
			cachedProbe = { result, at: Date.now() };
		} catch {
			if (!silent) toast.error('Reachability check failed');
		} finally {
			probing = false;
		}
	}

	function toggleEnabled(next: boolean) {
		if (!next && enabled && hasProxiedWorkloads) {
			// Guided convert handles the disable end to end
			onRequestDisable();
			return;
		}
		draftEnabled = next;
	}

	function discard() {
		draftEnabled = enabled;
		customDomain = baseUrl;
	}

	// Soft echo validation, warns without blocking the save
	async function validateSavedDomain(domain: string) {
		try {
			const result = await rpcClient.proxy.checkNetworkReachability(
				{ target: domain },
				silentCallOptions
			);
			const failed = result.ports.filter((p) => p.checked && !p.reachable);
			if (failed.length > 0) {
				toast.warning(
					`${domain} saved, but ${failed.length} ${failed.length === 1 ? 'port is' : 'ports are'} not reachable through it yet`
				);
			}
		} catch {
			toast.warning(`${domain} saved, but it does not resolve to this machine yet`);
		}
	}

	async function save() {
		saving = true;
		try {
			await rpcClient.proxy.updateProxyConfig({
				enabled: draftEnabled,
				baseUrl: draftBaseUrl
			});
			toast.success('Proxy configuration saved');
			if (draftEnabled && draftBaseUrl) {
				validateSavedDomain(draftBaseUrl);
			}
			await onChanged();
		} catch {
			toast.error('Failed to save proxy configuration');
		} finally {
			saving = false;
		}
	}
</script>

<div class="flex h-full min-h-0 flex-col">
	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
		<div class="flex items-center justify-between gap-3">
			<div class="flex min-w-0 items-center gap-3">
				<div
					class="flex size-9 shrink-0 items-center justify-center rounded-lg border {draftEnabled
						? 'border-primary/30 bg-primary/10 text-primary'
						: 'bg-muted/40 text-muted-foreground'}"
				>
					<Network class="size-4.5" />
				</div>
				<div class="min-w-0">
					<h3 class="text-sm font-semibold">Proxy routing</h3>
					<p class="text-xs text-muted-foreground">
						{#if enabled}
							{running ? 'Running' : 'Not running'} · {listenerCount}
							{listenerCount === 1 ? 'listener' : 'listeners'} · {routeCount}
							{routeCount === 1 ? 'route' : 'routes'}
						{:else}
							Off, servers use direct ports
						{/if}
					</p>
				</div>
			</div>
			<Switch
				checked={draftEnabled}
				onCheckedChange={toggleEnabled}
				disabled={saving}
				aria-label="Enable proxy"
			/>
		</div>

		{#if draftEnabled}
			<div class="space-y-2">
				<span class="stat-label">Domain</span>
				<Input
					type="text"
					bind:value={customDomain}
					placeholder="minecraft.example.com"
					class="font-mono text-sm"
				/>
				<p class="text-xs text-muted-foreground">
					{#if draftBaseUrl}
						Server hostnames live under this domain
					{:else if autoDomain}
						Empty follows the best detected address
					{:else}
						No address detected yet
					{/if}
				</p>

				{#if candidates.length > 0}
					<div class="overflow-hidden rounded-lg border">
						<button
							type="button"
							class="flex w-full items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-accent/40 {!draftBaseUrl
								? 'bg-primary/5'
								: ''}"
							onclick={() => (customDomain = '')}
						>
							<Zap class="size-3.5 shrink-0 text-primary" />
							<span class="min-w-0 flex-1">
								<span class="block text-xs font-medium">Automatic</span>
								{#if autoDomain}
									<span class="block truncate font-mono text-[11px] text-muted-foreground">
										{autoDomain}
									</span>
								{/if}
							</span>
							{#if !draftBaseUrl}
								<Check class="size-3.5 shrink-0 text-primary" />
							{/if}
						</button>
						{#each candidates as candidate (candidate.ip)}
							{@const active = draftBaseUrl === candidate.domain}
							<button
								type="button"
								class="flex w-full items-center gap-2 border-t px-3 py-2 text-left transition-colors hover:bg-accent/40 {active
									? 'bg-primary/5'
									: ''}"
								onclick={() => (customDomain = candidate.domain)}
							>
								<Globe class="size-3.5 shrink-0 text-muted-foreground" />
								<span class="min-w-0 flex-1 truncate font-mono text-xs">{candidate.domain}</span>
								<span class="shrink-0 rounded-full border px-1.5 text-[10px] text-muted-foreground">
									{candidate.source === AddressSource.PUBLIC ? 'public' : 'lan'}
								</span>
								{#if active}
									<Check class="size-3.5 shrink-0 text-primary" />
								{/if}
							</button>
						{/each}
					</div>
					<p class="text-xs text-muted-foreground">Pick a preset or point your own DNS here</p>
				{/if}
			</div>
		{/if}

		<div class="space-y-2">
			<div class="flex items-center justify-between gap-2">
				<span class="stat-label">Reachability</span>
				<Button size="sm" variant="ghost" class="h-7 px-2 text-xs" onclick={() => runProbe()} disabled={probing}>
					{#if probing}
						<Loader2 class="size-3.5 animate-spin" />
					{:else}
						<RadioTower class="size-3.5" />
					{/if}
					Check now
				</Button>
			</div>
			{#if probe}
				{#if probeFailures.length > 0}
					<div
						class="flex items-start gap-2 rounded-lg border border-status-busy/30 bg-status-busy/10 p-2.5 text-xs text-status-busy"
					>
						<AlertTriangle class="mt-px size-3.5 shrink-0" />
						<span>
							{probeFailures.length}
							{probeFailures.length === 1 ? 'port is' : 'ports are'} not reachable from outside, check
							your router's port forwarding
						</span>
					</div>
				{:else if probe.ports.some((p) => p.checked)}
					<p class="flex items-center gap-1.5 text-xs text-status-ok">
						<Check class="size-3.5" />
						Checked ports answer at {probe.ip}
					</p>
				{/if}
				<div class="divide-y rounded-lg border">
					{#each probe.ports as port (`${port.port}/${port.transport}`)}
						<div class="flex items-center justify-between gap-2 px-3 py-1.5 text-xs">
							<span class="min-w-0 truncate">
								<span class="font-mono">:{port.port}</span>
								{#if port.detail}
									<span class="text-muted-foreground"> · {port.detail}</span>
								{/if}
							</span>
							{#if !port.checked}
								<span class="shrink-0 text-muted-foreground">unknown</span>
							{:else if port.reachable}
								<Check class="size-3.5 shrink-0 text-status-ok" />
							{:else}
								<AlertTriangle class="size-3.5 shrink-0 text-status-busy" />
							{/if}
						</div>
					{/each}
				</div>
			{:else if probing}
				<p class="text-xs text-muted-foreground">Probing bound ports</p>
			{:else}
				<p class="text-xs text-muted-foreground">No probe has run yet</p>
			{/if}
		</div>
	</div>

	{#if dirty}
		<div class="flex items-center justify-end gap-2 border-t bg-muted/20 px-4 py-3">
			<Button variant="outline" size="sm" disabled={saving} onclick={discard}>
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
