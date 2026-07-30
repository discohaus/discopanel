<script lang="ts">
	import { rpcClient } from '$lib/api/rpc-client';
	import type { ProxyListenerWithCount } from '$lib/proto/discopanel/v1/proxy_pb';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import { ConfirmDialog } from '$lib/components/app';
	import { toast } from 'svelte-sonner';
	import { Loader2, Plus, Save, Trash2, X, Zap } from '@lucide/svelte';

	let {
		target,
		listeners,
		usedPorts,
		onDone,
		onClose
	}: {
		target: ProxyListenerWithCount | null;
		listeners: ProxyListenerWithCount[];
		usedPorts: number[];
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
					<span
						class="inline-flex shrink-0 items-center gap-1 rounded-full border px-1.5 py-px text-[10px] text-muted-foreground"
					>
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
