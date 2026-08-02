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
	import { ConfirmDialog } from '$lib/components/app';
	import { toast } from 'svelte-sonner';
	import { groupServices, laneLabel } from '../topology-data';
	import { ArrowUpRight, Loader2, Plus, Save, Trash2, X, Zap } from '@lucide/svelte';

	let {
		target,
		listeners,
		usedPorts,
		topology,
		servers,
		modules,
		onDone,
		onClose
	}: {
		target: ProxyListenerWithCount | null;
		listeners: ProxyListenerWithCount[];
		usedPorts: number[];
		topology: GetNetworkTopologyResponse | null;
		servers: Server[];
		modules: Module[];
		onDone: () => Promise<void>;
		onClose: () => void;
	} = $props();

	let editing = $derived(target?.listener ?? null);
	let workloadCount = $derived(target?.workloadCount ?? 0);

	let name = $state('');
	let description = $state('');
	let port = $state(25565);
	let enabled = $state(true);
	let isDefault = $state(false);
	let portError = $state('');
	let saving = $state(false);
	let deleteOpen = $state(false);

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
		} else {
			name = '';
			description = '';
			port = nextFreePort();
			enabled = true;
			isDefault = listeners.length === 0;
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
					lane: laneLabel(svc.protocol),
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

	async function submit() {
		if (!name.trim()) {
			toast.error('Listener name is required');
			return;
		}
		if (!editing && !validatePort(port)) return;
		saving = true;
		try {
			if (editing) {
				await rpcClient.proxy.updateProxyListener({
					id: editing.id,
					name,
					description,
					enabled,
					isDefault
				});
				toast.success(`Listener "${name}" updated`);
			} else {
				await rpcClient.proxy.createProxyListener({
					port,
					name,
					description,
					enabled,
					isDefault
				});
				toast.success(`Listener "${name}" created`);
			}
			await onDone();
		} catch (error: unknown) {
			toast.error(error instanceof Error ? error.message : 'Failed to save listener');
		} finally {
			saving = false;
		}
	}

	async function confirmDelete() {
		if (!editing) return;
		const label = editing.name;
		try {
			await rpcClient.proxy.deleteProxyListener({ id: editing.id });
			toast.success(`Listener "${label}" deleted`);
			await onDone();
		} catch (error: unknown) {
			toast.error(error instanceof Error ? error.message : 'Failed to delete listener');
		}
	}
</script>

<div class="flex h-full min-h-0 flex-col">
	<div class="flex items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="min-w-0">
			<div class="flex items-center gap-2">
				<h3 class="truncate text-sm font-semibold">
					{editing ? editing.name : 'New listener'}
				</h3>
				{#if editing?.autoCreated}
					<span class="inline-flex shrink-0 items-center gap-1 text-[11px] text-muted-foreground">
						<Zap class="size-2.5" />
						auto
					</span>
				{/if}
			</div>
			<p class="text-xs text-muted-foreground">
				{editing
					? `Port ${editing.port} · ${workloadCount} ${workloadCount === 1 ? 'workload' : 'workloads'}`
					: 'Open a new port for player connections'}
			</p>
		</div>
		<Button variant="ghost" size="icon" class="size-8" onclick={onClose} title="Back to overview">
			<X class="size-4" />
		</Button>
	</div>

	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
		{#if editing?.autoCreated}
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
								<p class="truncate font-mono text-xs text-muted-foreground" title={row.hostnames.join(', ')}>
									{row.hostnames.join(', ')}
								</p>
							{/if}
						</div>
					{/each}
				</div>
			</div>
		{/if}

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
		</div>

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
	</div>

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
</div>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete listener {editing?.name ?? ''}?"
	description="The proxy will stop accepting connections on port {editing?.port ?? ''}."
	confirmLabel="Delete listener"
	destructive
	onConfirm={confirmDelete}
/>
