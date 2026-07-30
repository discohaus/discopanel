<script lang="ts">
	import { rpcClient } from '$lib/api/rpc-client';
	import { BaseUrlSource } from '$lib/proto/discopanel/v1/proxy_pb';
	import { Input } from '$lib/components/ui/input';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import { CopyButton } from '$lib/components/app';
	import { toast } from 'svelte-sonner';
	import { Globe, Loader2, Network, RotateCcw, Save, Zap } from '@lucide/svelte';

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
	let domainMode = $state<'instant' | 'custom'>('instant');
	let customDomain = $state('');
	let saving = $state(false);

	// Saved snapshot drives dirty detection
	let seeded = $state('');
	$effect(() => {
		const snapshot = `${enabled}|${baseUrl}`;
		if (seeded === snapshot) return;
		seeded = snapshot;
		draftEnabled = enabled;
		domainMode = baseUrl ? 'custom' : 'instant';
		customDomain = baseUrl;
	});

	let draftBaseUrl = $derived(domainMode === 'custom' ? customDomain.trim() : '');
	let dirty = $derived(draftEnabled !== enabled || draftBaseUrl !== baseUrl);
	let instantDomain = $derived(baseUrlSource === BaseUrlSource.CUSTOM ? '' : effectiveBaseUrl);

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
		domainMode = baseUrl ? 'custom' : 'instant';
		customDomain = baseUrl;
	}

	async function save() {
		saving = true;
		try {
			await rpcClient.proxy.updateProxyConfig({
				enabled: draftEnabled,
				baseUrl: draftBaseUrl
			});
			toast.success('Proxy configuration saved');
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
				<div class="grid gap-2" role="radiogroup" aria-label="Base domain">
					<button
						type="button"
						role="radio"
						aria-checked={domainMode === 'instant'}
						class="rounded-lg border p-3 text-left transition-colors {domainMode === 'instant'
							? 'border-primary bg-primary/5'
							: 'hover:bg-accent/40'}"
						onclick={() => (domainMode = 'instant')}
					>
						<div class="flex items-center gap-2 text-sm font-medium">
							<Zap class="size-4 text-primary" />
							Built-in domain
						</div>
						{#if instantDomain}
							<div class="mt-1.5 flex items-center justify-between gap-2">
								<p class="truncate font-mono text-xs text-muted-foreground">{instantDomain}</p>
								<CopyButton text={instantDomain} label="Copy domain" />
							</div>
							<p class="mt-1 text-xs text-muted-foreground">
								Made from this machine's IP, works with zero setup
							</p>
						{:else if domainMode === 'instant'}
							<p class="mt-1 text-xs text-muted-foreground">No address detected yet</p>
						{/if}
					</button>
					<button
						type="button"
						role="radio"
						aria-checked={domainMode === 'custom'}
						class="rounded-lg border p-3 text-left transition-colors {domainMode === 'custom'
							? 'border-primary bg-primary/5'
							: 'hover:bg-accent/40'}"
						onclick={() => (domainMode = 'custom')}
					>
						<div class="flex items-center gap-2 text-sm font-medium">
							<Globe class="size-4 text-primary" />
							Domain you own
						</div>
						{#if domainMode === 'custom'}
							<Input
								type="text"
								bind:value={customDomain}
								placeholder="minecraft.example.com"
								class="mt-2 h-8"
								onclick={(e) => e.stopPropagation()}
							/>
							<p class="mt-1.5 text-xs text-muted-foreground">
								Needs a DNS record pointing at this machine
							</p>
						{:else}
							<p class="mt-1 text-xs text-muted-foreground">Use a domain you already bought</p>
						{/if}
					</button>
				</div>
			</div>
		{/if}
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
