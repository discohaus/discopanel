<script lang="ts">
	import { onMount } from 'svelte';
	import { registerRefresh } from '$lib/stores/refresh';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { Alert, AlertDescription } from '$lib/components/ui/alert';
	import SettingRow from '$lib/components/app/setting-row.svelte';
	import { rpcClient, rpcErrorMessage, silentCallOptions } from '$lib/api/rpc-client';
	import { canUpdateSettings } from '$lib/stores/auth';
	import { notify } from '$lib/stores/activity.svelte';
	import type { GetMemoryQuotaResponse } from '$lib/proto/discopanel/v1/server_pb';
	import { Loader2, Save, RotateCcw, FileCode } from '@lucide/svelte';

	let loading = $state(true);
	let saving = $state(false);
	let loadError = $state('');
	let quota = $state<GetMemoryQuotaResponse | null>(null);

	// Edited in exact MB so a saved value round-trips, empty or 0 is unlimited
	let limitMb = $state<number | null>(null);

	let canEdit = $derived($canUpdateSettings);
	let currentMb = $derived(quota ? Number(quota.maxTotalMb) : 0);
	let editedMb = $derived(limitMb == null || Number.isNaN(limitMb) ? 0 : limitMb);
	let invalid = $derived(
		limitMb != null && (Number.isNaN(limitMb) || limitMb < 0 || !Number.isInteger(limitMb))
	);
	let hasChanges = $derived(quota != null && editedMb !== currentMb);
	let modulesMb = $derived(quota ? Number(quota.modulesMb) : 0);
	let editHint = $derived.by(() => {
		const parts: string[] = [];
		if (editedMb > 0 && !invalid) parts.push(`${fmtGb(editedMb)} across servers and modules`);
		if (hostTotalMb > 0) parts.push(`the host has ${fmtGb(hostTotalMb)}`);
		const hint = parts.join(', ');
		return hint ? hint.charAt(0).toUpperCase() + hint.slice(1) : '';
	});
	let allocatedMb = $derived(quota ? Number(quota.allocatedMb) : 0);
	let hostTotalMb = $derived(quota ? Number(quota.hostTotalMb) : 0);
	let configMb = $derived(quota ? Number(quota.configMaxTotalMb) : 0);

	function fmtGb(mb: number): string {
		return `${Math.round((mb / 1024) * 100) / 100} GB`;
	}

	function applyQuota(next: GetMemoryQuotaResponse) {
		quota = next;
		limitMb = Number(next.maxTotalMb);
	}

	async function load() {
		loadError = '';
		try {
			applyQuota(await rpcClient.server.getMemoryQuota({}, silentCallOptions));
		} catch (error) {
			loadError = rpcErrorMessage(error, 'Failed to load the memory limit');
		} finally {
			loading = false;
		}
	}

	async function save(useConfig = false) {
		if (saving || (!useConfig && invalid)) return;
		saving = true;
		try {
			const res = await rpcClient.server.updateMemoryQuota(
				{ maxTotalMb: BigInt(useConfig ? 0 : editedMb), useConfig },
				silentCallOptions
			);
			if (res.quota) applyQuota(res.quota);
			notify.success(useConfig ? 'Memory limit reset to the config value' : 'Memory limit saved');
		} catch (error) {
			notify.error('Failed to save the memory limit', {
				description: rpcErrorMessage(error, 'Unknown error occurred')
			});
		} finally {
			saving = false;
		}
	}

	function discard() {
		if (quota) applyQuota(quota);
	}

	onMount(() => {
		load();
		return registerRefresh(load);
	});
</script>

{#if loading}
	<Skeleton class="h-48 rounded-xl" />
{:else if loadError && !quota}
	<Alert variant="destructive">
		<AlertDescription class="flex flex-wrap items-center justify-between gap-2">
			<span>{loadError}</span>
			<Button variant="outline" size="sm" onclick={load}>Retry</Button>
		</AlertDescription>
	</Alert>
{:else if quota}
	<section class="overflow-hidden rounded-xl border bg-card">
		<header
			class="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3"
		>
			<div class="min-w-0">
				<h3 class="text-sm font-semibold">Server memory</h3>
				<p class="mt-0.5 text-xs text-muted-foreground">
					Keep servers from overcommitting the host's memory
				</p>
			</div>
			{#if quota.overridden}
				<Badge variant="outline">Panel setting</Badge>
			{:else if configMb > 0}
				<Badge variant="outline">
					<FileCode class="size-3" />
					From config
				</Badge>
			{/if}
		</header>

		<div class="divide-y">
			<SettingRow
				id="memory-limit"
				controlId="memory-limit-input"
				label="Total memory limit"
				description="Caps the memory allocated across all servers and modules, running or stopped, in MB. A docker memory override larger than a server's memory counts instead. Creating a server or module, or raising its memory, past the limit is refused for users who can manage servers; administrators may exceed it and only get a warning. Lowering memory is always allowed. 0 means no limit."
				hint={editHint}
				modified={hasChanges}
			>
				{#if canEdit}
					<div class="flex items-center gap-2 sm:justify-end">
						<Input
							id="memory-limit-input"
							type="number"
							min="0"
							step="1024"
							class="h-9 w-32"
							aria-invalid={invalid}
							bind:value={limitMb}
							disabled={saving}
						/>
						<span class="text-sm text-muted-foreground">MB</span>
					</div>
				{:else}
					<div class="flex h-9 items-center sm:justify-end">
						<Badge variant="outline">{currentMb > 0 ? fmtGb(currentMb) : 'No limit'}</Badge>
					</div>
				{/if}
			</SettingRow>

			{#if quota.authDisabled}
				<p class="px-4 py-3 text-xs text-status-busy">
					Authentication is off, so everyone counts as an administrator. The limit then applies to
					everyone with no exemption; to go past it, raise or clear it here.
				</p>
			{/if}

			<div
				class="flex flex-wrap items-center gap-x-6 gap-y-1 px-4 py-3 text-xs text-muted-foreground"
			>
				<span>
					Allocated <span class="tabular font-medium text-foreground">{fmtGb(allocatedMb)}</span>
					{#if currentMb > 0}
						of {fmtGb(currentMb)}
					{/if}
					{#if modulesMb > 0}
						({fmtGb(modulesMb)} by modules)
					{/if}
				</span>
				{#if currentMb > 0 && allocatedMb > currentMb}
					<span class="text-status-busy">
						{fmtGb(allocatedMb - currentMb)} over the limit, servers can only shrink until it fits
					</span>
				{/if}
				<span>
					Config value
					<code class="font-mono">minecraft.max_total_memory_mb</code>:
					{configMb > 0 ? fmtGb(configMb) : 'no limit'}
				</span>
			</div>
		</div>

		{#if canEdit && (hasChanges || quota.overridden)}
			<div class="flex flex-wrap items-center justify-end gap-2 border-t bg-muted/20 px-4 py-3">
				{#if quota.overridden && !hasChanges}
					<Button variant="outline" size="sm" onclick={() => save(true)} disabled={saving}>
						<RotateCcw class="size-4" />
						Use config value
					</Button>
				{/if}
				{#if hasChanges}
					<Button variant="outline" size="sm" onclick={discard} disabled={saving}>
						<RotateCcw class="size-4" />
						Discard
					</Button>
					<Button size="sm" onclick={() => save()} disabled={saving || invalid}>
						{#if saving}
							<Loader2 class="size-4 animate-spin" />
						{:else}
							<Save class="size-4" />
						{/if}
						Save changes
					</Button>
				{/if}
			</div>
		{/if}
	</section>
{/if}
