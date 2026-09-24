<script lang="ts">
	import { resolve } from '$app/paths';
	import { rpcClient } from '$lib/api/rpc-client';
	import {
		NetworkOwnerKind,
		type GetNetworkTopologyResponse,
		type ProxyListenerWithCount
	} from '$lib/proto/discopanel/v1/proxy_pb';
	import type { Module, Server } from '$lib/proto/discopanel/v1/storage_pb';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
	import { ConfirmDialog, ListInput } from '$lib/components/app';
	import InspectorHeader from './inspector-header.svelte';
	import { notify } from '$lib/stores/activity.svelte';
	import { groupServices, laneLabel } from '../topology-data';
	import { AlertTriangle, ArrowUpRight, Loader2, Network, Plus, Save, Trash2, Zap } from '@lucide/svelte';

	let {
		target,
		listeners,
		usedPorts,
		topology,
		servers,
		modules,
		onDone,
		onBack
	}: {
		target: ProxyListenerWithCount | null;
		listeners: ProxyListenerWithCount[];
		usedPorts: number[];
		topology: GetNetworkTopologyResponse | null;
		servers: Server[];
		modules: Module[];
		onDone: () => Promise<void>;
		onBack: () => void;
	} = $props();

	let editing = $derived(target?.listener ?? null);
	let workloadCount = $derived(target?.workloadCount ?? 0);
	// Panel listener follows the server config, form stays hidden
	let isPanel = $derived(editing?.id === 'panel');

	let name = $state('');
	let description = $state('');
	let port = $state(25565);
	let enabled = $state(true);
	let isDefault = $state(false);
	const DISMISS_CIDR_WARNING_KEY = 'discopanel:dismiss_proxy_cidr_warning';

	let portError = $state('');
	let saving = $state(false);
	let deleteOpen = $state(false);
	let proxyWarningOpen = $state(false);
	let dontWarnAgain = $state(false);

	let ingressProxyProtocol = $state(false);
	let trustedProxies = $state<string[]>([]);

	// Sole default cannot be unset, another must take over
	let defaultLocked = $derived(!!editing?.isDefault);

	// Seed the draft whenever the focused listener changes
	let seededId = $state<string | null>('unseeded');
	$effect(() => {
		const id = editing?.id ?? null;
		if (seededId === id) return;
		seededId = id;
		if (editing) {
			name = editing.name;
			description = editing.description;
			port = editing.port;
			enabled = editing.enabled;
			isDefault = editing.isDefault;
			ingressProxyProtocol = editing.ingressProxyProtocol ?? false;
			trustedProxies = editing.trustedProxies ? [...editing.trustedProxies] : [];
		} else {
			name = '';
			description = '';
			port = nextFreePort();
			enabled = true;
			isDefault = listeners.length === 0;
			ingressProxyProtocol = false;
			trustedProxies = [];
		}
		portError = '';
	});

	// One service row grouping every hostname it answers on
	interface ServiceRow {
		key: string;
		name: string;
		serverId: string;
		lane: string;
		hostnames: string[];
		catchAll: boolean;
		relay: boolean;
		live: boolean;
	}

	// Routing table grouped per service instead of per hostname
	let serviceRows = $derived.by((): ServiceRow[] => {
		if (!editing || !topology) return [];
		return groupServices(topology.reservations, topology.routes, editing.port)
			.map((svc) => {
				let label = '';
				let serverId = '';
				if (svc.ownerKind === NetworkOwnerKind.PANEL) {
					label = 'DiscoPanel';
				} else if (svc.ownerKind === NetworkOwnerKind.MODULE) {
					const module = modules.find((m) => m.id === svc.ownerId);
					label = module?.name ?? svc.ownerId.slice(0, 8);
					serverId = module?.serverId ?? '';
				} else {
					const server = servers.find((s) => s.id === svc.ownerId);
					label = server?.name ?? svc.ownerId.slice(0, 8);
					serverId = server?.id ?? '';
				}
				return {
					key: svc.key,
					name: label,
					serverId,
					lane: svc.protocols.map(laneLabel).join(' + '),
					hostnames: [...svc.hostnames, ...svc.staleHostnames],
					catchAll: svc.catchAll,
					relay: svc.relay,
					live: svc.live
				};
			})
			.sort((a, b) => a.name.localeCompare(b.name));
	});

	function nextFreePort(): number {
		const used = new Set(usedPorts);
		let candidate = 25565;
		while (used.has(candidate)) candidate++;
		return candidate;
	}

	function validatePort(p: number): boolean {
		portError = '';
		if (!p || p < 1 || p > 65535) {
			portError = 'Port must be between 1 and 65535';
			return false;
		}
		const clash = listeners.find(
			(lwc) => lwc.listener?.port === p && lwc.listener?.id !== editing?.id
		);
		if (clash) {
			portError = `Port ${p} is already used by "${clash.listener?.name}"`;
			return false;
		}
		if (!editing && usedPorts.includes(p)) {
			portError = `Port ${p} is already reserved`;
			return false;
		}
		return true;
	}

	function validCIDR(str: string): boolean {
		const trimmed = str.trim();
		if (!trimmed) return false;
		const parts = trimmed.split('/');
		if (parts.length > 2) return false;
		const ip = parts[0];
		const mask = parts[1];

		if (mask !== undefined) {
			if (!/^\d+$/.test(mask)) return false;
			const num = parseInt(mask, 10);
			if (ip.includes(':')) {
				if (num < 0 || num > 128) return false;
			} else {
				if (num < 0 || num > 32) return false;
			}
		}

		if (ip.includes(':')) {
			return /^[0-9a-fA-F:]+$/.test(ip) && (ip.includes('::') || ip.split(':').length === 8);
		} else {
			const octets = ip.split('.');
			if (octets.length !== 4) return false;
			return octets.every((o) => /^\d+$/.test(o) && parseInt(o, 10) >= 0 && parseInt(o, 10) <= 255);
		}
	}

	async function doSubmit() {
		saving = true;
		try {
			if (editing) {
				await rpcClient.proxy.updateProxyListener({
					id: editing.id,
					name,
					description,
					enabled,
					isDefault,
					ingressProxyProtocol,
					trustedProxies
				});
				notify.success(`Listener "${name}" updated`);
			} else {
				await rpcClient.proxy.createProxyListener({
					port,
					name,
					description,
					enabled,
					isDefault,
					ingressProxyProtocol,
					trustedProxies
				});
				notify.success(`Listener "${name}" created`);
			}
			await onDone();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to save listener');
		} finally {
			proxyWarningOpen = false;
			saving = false;
		}
	}

	function submit() {
		if (!name.trim()) {
			notify.error('Listener name is required');
			return;
		}
		if (!editing && !validatePort(port)) return;

		const dismissed =
			typeof localStorage !== 'undefined' &&
			localStorage.getItem(DISMISS_CIDR_WARNING_KEY) === 'true';
		if (ingressProxyProtocol && trustedProxies.length === 0 && !dismissed) {
			proxyWarningOpen = true;
			return;
		}
		doSubmit();
	}

	async function handleWarningConfirm() {
		if (dontWarnAgain && typeof localStorage !== 'undefined') {
			localStorage.setItem(DISMISS_CIDR_WARNING_KEY, 'true');
		}
		console.log("Submit")
		await doSubmit();
	}

	async function confirmDelete() {
		if (!editing) return;
		const label = editing.name;
		try {
			await rpcClient.proxy.deleteProxyListener({ id: editing.id });
			notify.success(`Listener "${label}" deleted`);
			await onDone();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to delete listener');
		}
	}
</script>

<div class="flex h-full min-h-0 flex-col">
	<InspectorHeader
		title={editing ? editing.name : 'New listener'}
		subtitle={editing
			? `Port ${editing.port} · ${workloadCount} ${workloadCount === 1 ? 'workload' : 'workloads'}`
			: 'Open a new port for player connections'}
		{onBack}
	>
		{#snippet icon()}
			<Network class="size-4 shrink-0 text-primary" />
		{/snippet}
		{#snippet tag()}
			{#if isPanel}
				<span class="shrink-0 text-[11px] text-muted-foreground">panel</span>
			{:else if editing?.autoCreated}
				<span class="inline-flex shrink-0 items-center gap-1 text-[11px] text-muted-foreground">
					<Zap class="size-2.5" />
					auto
				</span>
			{/if}
		{/snippet}
	</InspectorHeader>

	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
		{#if isPanel}
			<p class="text-xs text-muted-foreground">
				Carries the DiscoPanel web UI, always on and undeletable
			</p>
		{:else if editing?.autoCreated}
			<p class="text-xs text-muted-foreground">
				Opened automatically for routed ports, retires when unused
			</p>
		{/if}

		{#if editing && serviceRows.length > 0}
			<div>
				<span class="stat-label">Routing table</span>
				<div class="mt-1.5 divide-y rounded-lg border">
					{#each serviceRows as row (row.key)}
						<div class="space-y-1.5 px-3 py-2.5">
							<div class="flex items-center justify-between gap-2">
								<div class="flex min-w-0 items-center gap-2">
									{#if row.serverId}
										<a
											href={resolve(`/servers/${row.serverId}`)}
											class="inline-flex min-w-0 items-center gap-1 text-sm font-medium hover:underline"
										>
											<span class="truncate">{row.name}</span>
											<ArrowUpRight class="size-3 shrink-0 text-muted-foreground" />
										</a>
									{:else}
										<span class="truncate text-sm font-medium">{row.name}</span>
									{/if}
									<span class="shrink-0 font-mono text-[11px] text-muted-foreground">
										{row.lane}
									</span>
								</div>
								<span
									class="size-2 shrink-0 rounded-full {row.live
										? 'bg-status-ok'
										: 'bg-status-idle'}"
									title={row.live ? 'Serving' : 'Not serving'}
								></span>
							</div>
							{#if row.relay}
								<p class="text-xs text-muted-foreground">Relays all unmatched traffic</p>
							{:else if row.hostnames.length > 0}
								<p
									class="truncate font-mono text-xs text-muted-foreground"
									title={row.hostnames.join(', ')}
								>
									{row.hostnames.join(', ')}
								</p>
							{/if}
						</div>
					{/each}
				</div>
			</div>
		{/if}

		{#if !isPanel}
			<div class="grid gap-4 sm:grid-cols-[minmax(0,1fr)_7rem]">
				<div class="space-y-2">
					<Label for="listener-name">Name</Label>
					<Input id="listener-name" bind:value={name} placeholder="e.g. Main, Events" />
				</div>
				<div class="space-y-2">
					<Label for="listener-port">Port</Label>
					<Input
						id="listener-port"
						type="number"
						bind:value={port}
						disabled={!!editing}
						oninput={(e) => validatePort(Number(e.currentTarget.value))}
						class={portError ? 'border-destructive' : editing ? 'bg-muted' : ''}
					/>
				</div>
			</div>
			{#if portError}
				<p class="text-xs text-destructive">{portError}</p>
			{:else if editing}
				<p class="text-xs text-muted-foreground">Listener ports cannot change after creation</p>
			{/if}

			<div class="space-y-2">
				<Label for="listener-description">Description</Label>
				<Input id="listener-description" bind:value={description} placeholder="Optional" />
			</div>

			<div class="space-y-3 rounded-lg border px-3.5 py-3">
				<label class="flex cursor-pointer items-center justify-between gap-3 text-sm">
					<span>
						Enabled
						<span class="block text-xs font-normal text-muted-foreground">
							Accept connections on this port
						</span>
					</span>
					<Switch checked={enabled} onCheckedChange={(v) => (enabled = v)} />
				</label>
				<label class="flex cursor-pointer items-center justify-between gap-3 border-t pt-3 text-sm">
					<span>
						Default listener
						<span class="block text-xs font-normal text-muted-foreground">
							{defaultLocked
								? 'Make another listener the default first'
								: 'New proxied servers use this port'}
						</span>
					</span>
					<Switch
						checked={isDefault}
						disabled={defaultLocked}
						onCheckedChange={(v) => (isDefault = v)}
					/>
				</label>
				<label class="flex cursor-pointer items-center justify-between gap-3 border-t pt-3 text-sm" id="use-ingress-proxy">
					<span>
						PROXY protocol (v1/v2) ingress
						<span class="block text-xs font-normal text-muted-foreground">
							Expect and parse PROXY headers from upstream edge proxies
						</span>
					</span>
					<Switch
						checked={ingressProxyProtocol}
						onCheckedChange={(v) => (ingressProxyProtocol = v)}
					/>
				</label>
			</div>

			{#if ingressProxyProtocol}
				<div class="space-y-2" id="trusted-proxies">
					<Label for="trusted-proxies">Trusted upstream proxies (CIDRs)</Label>
					<ListInput
						inputId="trusted-proxies"
						bind:items={trustedProxies}
						placeholder="e.g. 172.19.0.0/16, 10.0.0.0/8"
						disabled={saving}
						emptyText="No trusted proxies configured (all IPs allowed)"
						removeTitle="Remove proxy CIDR"
						validate={(cidr) =>
							validCIDR(cidr) ? undefined : `${cidr} is not a valid IP or CIDR network`}
					/>
					<p class="text-xs text-muted-foreground">
						IP ranges allowed to send PROXY headers. PROXY headers sent by untrusted IPs are ignored to prevent IP spoofing.
					</p>
				</div>
			{/if}

			{#if editing}
				<div class="rounded-lg border border-status-danger/20 p-3">
					<Button
						variant="outline"
						size="sm"
						class="text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
						onclick={() => (deleteOpen = true)}
					>
						<Trash2 class="size-4" />
						Delete listener
					</Button>
					{#if workloadCount > 0}
						<p class="mt-2 text-xs text-muted-foreground">
							{workloadCount}
							{workloadCount === 1 ? 'workload rides' : 'workloads ride'} this listener
						</p>
					{/if}
				</div>
			{/if}
		{/if}
	</div>

	{#if !isPanel}
		<div class="flex items-center justify-end gap-2 border-t bg-muted/20 px-4 py-3">
			<Button size="sm" onclick={submit} disabled={saving || !name.trim() || !!portError}>
				{#if saving}
					<Loader2 class="size-4 animate-spin" />
				{:else if editing}
					<Save class="size-4" />
				{:else}
					<Plus class="size-4" />
				{/if}
				{editing ? 'Save changes' : 'Add listener'}
			</Button>
		</div>
	{/if}
</div>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete listener {editing?.name ?? ''}?"
	description="The proxy will stop accepting connections on port {editing?.port ?? ''}."
	confirmLabel="Delete listener"
	destructive
	onConfirm={confirmDelete}
/>

<AlertDialog.Root
	open={proxyWarningOpen}
	onOpenChange={(o) => {
		if (!o) proxyWarningOpen = false;
	}}
>
	<AlertDialog.Content>
		<AlertDialog.Header>
			<AlertDialog.Title class="flex items-center gap-2">
				<AlertTriangle class="size-5 text-status-warn" />
				Review warning
			</AlertDialog.Title>
			<AlertDialog.Description>
				The following issue was detected. You can still proceed, but you may want to review it
				first.
			</AlertDialog.Description>
		</AlertDialog.Header>
		<div class="space-y-2 py-2">
			<div
				class="flex items-start gap-2 rounded-md border border-status-warn/30 bg-status-warn/10 p-3 text-sm text-status-warn"
			>
				<AlertTriangle class="mt-0.5 size-4 shrink-0" />
				<span>
					PROXY protocol (v1/v2) ingress is enabled, but no trusted proxy CIDRs are set. Any IP
					address will be allowed to send PROXY headers and potentially spoof client IP
					addresses.
				</span>
			</div>
			<label class="flex cursor-pointer items-center gap-2 pt-2 text-xs text-muted-foreground">
				<Checkbox bind:checked={dontWarnAgain} />
				<span>Don't warn me again on this device</span>
			</label>
		</div>
		<AlertDialog.Footer>
			<AlertDialog.Cancel onclick={() => (proxyWarningOpen = false)}>Go back</AlertDialog.Cancel>
			<AlertDialog.Action onclick={handleWarningConfirm}>
				{editing ? 'Save anyway' : 'Add anyway'}
			</AlertDialog.Action>
		</AlertDialog.Footer>
	</AlertDialog.Content>
</AlertDialog.Root>
