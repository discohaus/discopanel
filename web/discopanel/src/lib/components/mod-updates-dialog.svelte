<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import { Switch } from '$lib/components/ui/switch';
	import { Label } from '$lib/components/ui/label';
	import { Progress } from '$lib/components/ui/progress';
	import { EmptyState } from '$lib/components/app';
	import {
		Loader2,
		ArrowUpCircle,
		CheckCircle2,
		ExternalLink,
		RotateCcw,
		RefreshCw,
		HelpCircle,
		AlertTriangle
	} from '@lucide/svelte';
	import { rpcClient, rpcErrorMessage } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import {
		ModUpdateStatus,
		ModUpdateSource,
		type ModUpdate
	} from '$lib/proto/discopanel/v1/mod_pb';
	import { ReleaseType } from '$lib/proto/discopanel/v1/storage_pb';

	interface Props {
		open?: boolean;
		serverId: string;
		label: string;
		updates: ModUpdate[];
		checking: boolean;
		checkError: string;
		modpack?: boolean;
		includeBeta?: boolean;
		target: string;
		onRecheck: (includeBeta?: boolean) => void | Promise<void>;
		onChanged: () => void | Promise<void>;
	}

	let {
		open = $bindable(false),
		serverId,
		label,
		updates,
		checking,
		checkError,
		modpack = false,
		includeBeta = $bindable(false),
		target,
		onRecheck,
		onChanged
	}: Props = $props();

	// Per entry busy flag and last failure
	let busy = $state<Record<string, boolean>>({});
	let failures = $state<Record<string, string>>({});
	let bulk = $state<{ done: number; total: number } | null>(null);

	let available = $derived(
		updates.filter((u) => u.status === ModUpdateStatus.UPDATE_AVAILABLE && !u.managedBy)
	);
	// Reinstalled by the pack or a setting, updating them would not stick
	let managed = $derived(
		updates.filter((u) => u.managedBy && u.status === ModUpdateStatus.UPDATE_AVAILABLE)
	);
	let upToDate = $derived(updates.filter((u) => u.status === ModUpdateStatus.UP_TO_DATE));
	let incompatible = $derived(
		updates.filter((u) => u.status === ModUpdateStatus.NO_COMPATIBLE_VERSION)
	);
	let unknown = $derived(updates.filter((u) => u.status === ModUpdateStatus.UNKNOWN_SOURCE));
	let errored = $derived(updates.filter((u) => u.status === ModUpdateStatus.ERROR));
	let rollbacks = $derived(
		updates.filter((u) => u.rollbackFileName && u.status !== ModUpdateStatus.UPDATE_AVAILABLE)
	);
	let working = $derived(bulk !== null || Object.values(busy).some(Boolean));

	$effect(() => {
		if (!open) {
			failures = {};
		}
	});

	function sourceLabel(s: ModUpdateSource): string {
		switch (s) {
			case ModUpdateSource.MODRINTH:
				return 'Modrinth';
			case ModUpdateSource.CURSEFORGE:
				return 'CurseForge';
		}
		return 'Unknown';
	}

	function channelLabel(t: ReleaseType | undefined): string {
		if (t === ReleaseType.BETA) return 'beta';
		if (t === ReleaseType.ALPHA) return 'alpha';
		return '';
	}

	function nameOf(u: ModUpdate): string {
		return u.projectName || u.fileName;
	}

	// Applies one entry, reports whether it changed anything
	async function applyOne(u: ModUpdate): Promise<{ ok: boolean; restart: boolean }> {
		busy[u.modId] = true;
		delete failures[u.modId];
		try {
			const res = await rpcClient.mod.applyModUpdates({
				serverId,
				modIds: [u.modId],
				includeBeta
			});
			const out = res.outcomes[0];
			if (out?.error) failures[u.modId] = out.error;
			return { ok: !!out?.updated, restart: res.restartRequired };
		} catch (error) {
			failures[u.modId] = rpcErrorMessage(error, 'Update failed');
			return { ok: false, restart: false };
		} finally {
			busy[u.modId] = false;
		}
	}

	function restartNote(restart: boolean): string {
		return restart
			? 'Restart the server to load the new files.'
			: 'The new files load on the next start.';
	}

	async function updateOne(u: ModUpdate) {
		const { ok, restart } = await applyOne(u);
		if (ok) {
			notify.success(`Updated ${nameOf(u)}`, { description: restartNote(restart) });
			await onChanged();
		} else {
			notify.error(`Failed to update ${nameOf(u)}`, { description: failures[u.modId] });
		}
	}

	async function updateAll() {
		const queue = [...available];
		bulk = { done: 0, total: queue.length };
		let updated = 0;
		let restart = false;
		for (const u of queue) {
			const res = await applyOne(u);
			if (res.ok) updated++;
			restart ||= res.restart;
			bulk = { done: bulk.done + 1, total: queue.length };
		}
		bulk = null;
		const failed = queue.length - updated;
		if (updated > 0) {
			notify.success(`Updated ${updated} of ${queue.length}`, {
				description:
					restartNote(restart) + (failed > 0 ? ` ${failed} failed, see the list for details.` : '')
			});
			await onChanged();
		} else {
			notify.error('No updates could be applied');
		}
	}

	async function rollback(u: ModUpdate) {
		busy[u.modId] = true;
		delete failures[u.modId];
		try {
			const res = await rpcClient.mod.rollbackModUpdate({ serverId, modId: u.modId });
			notify.success(`Restored ${res.fileName}`, { description: restartNote(res.restartRequired) });
			await onChanged();
		} catch (error) {
			failures[u.modId] = rpcErrorMessage(error, 'Rollback failed');
			notify.error(`Failed to roll back ${nameOf(u)}`, { description: failures[u.modId] });
		} finally {
			busy[u.modId] = false;
		}
	}
</script>

{#snippet versionLine(u: ModUpdate)}
	<div class="mt-0.5 flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
		<span class="font-mono">{u.current?.version || u.fileName}</span>
		{#if u.latest}
			<span aria-hidden="true">&rarr;</span>
			<span class="font-mono text-foreground">{u.latest.version}</span>
			{#if channelLabel(u.latest.releaseType)}
				<Badge variant="outline" class="text-[10px]">{channelLabel(u.latest.releaseType)}</Badge>
			{/if}
		{/if}
		<span>{sourceLabel(u.source)}</span>
	</div>
{/snippet}

{#snippet links(u: ModUpdate)}
	<!-- eslint-disable svelte/no-navigation-without-resolve -- external URL -->
	{#if u.latest?.changelogUrl}
		<a
			href={u.latest.changelogUrl}
			target="_blank"
			rel="noopener noreferrer"
			class="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
		>
			Changelog <ExternalLink class="size-3" />
		</a>
	{:else if u.projectUrl}
		<a
			href={u.projectUrl}
			target="_blank"
			rel="noopener noreferrer"
			class="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
		>
			Project <ExternalLink class="size-3" />
		</a>
	{/if}
	<!-- eslint-enable svelte/no-navigation-without-resolve -->
{/snippet}

<Dialog.Root bind:open>
	<Dialog.Content class="flex max-h-[85vh] flex-col gap-0 p-0 sm:max-w-2xl">
		<Dialog.Header class="border-b px-5 py-4">
			<Dialog.Title>{label} updates</Dialog.Title>
			<Dialog.Description>
				Jars are identified by file hash on Modrinth and CurseForge, never by name.
				{#if target}Checked against {target}.{/if}
			</Dialog.Description>
		</Dialog.Header>

		<div class="flex shrink-0 flex-wrap items-center justify-between gap-3 border-b px-5 py-2.5">
			<div class="flex items-center gap-2">
				<Switch
					id="mod-updates-beta"
					bind:checked={includeBeta}
					onCheckedChange={(v) => onRecheck(v)}
					disabled={checking || working}
				/>
				<Label for="mod-updates-beta" class="text-xs font-normal">Include beta versions</Label>
			</div>
			<div class="flex items-center gap-2">
				<Button
					size="sm"
					variant="outline"
					onclick={() => onRecheck()}
					disabled={checking || working}
				>
					<RefreshCw class="size-4 {checking ? 'animate-spin' : ''}" />
					Recheck
				</Button>
				{#if available.length > 1}
					<Button size="sm" onclick={updateAll} disabled={checking || working}>
						{#if modpack}
							<div
								class="flex shrink-0 items-start gap-2 border-b bg-status-warn/10 px-5 py-2.5 text-xs text-muted-foreground"
							>
								<AlertTriangle class="mt-0.5 size-3.5 shrink-0 text-status-warn" />
								<span>
									This server runs a modpack. Jars the pack ships are left alone, and a later pack
									install may replace other updated jars too.
								</span>
							</div>
						{/if}

						{#if bulk}
							<Loader2 class="size-4 animate-spin" />
						{:else}
							<ArrowUpCircle class="size-4" />
						{/if}
						Update all ({available.length})
					</Button>
				{/if}
			</div>
		</div>

		{#if bulk}
			<div class="shrink-0 border-b px-5 py-2.5">
				<div class="mb-1.5 text-xs text-muted-foreground">
					Updating {Math.min(bulk.done + 1, bulk.total)} of {bulk.total}
				</div>
				<Progress value={(bulk.done / bulk.total) * 100} class="h-1.5" />
			</div>
		{/if}

		<div class="min-h-0 flex-1 overflow-y-auto">
			{#if checking && updates.length === 0}
				<div class="flex items-center justify-center gap-2 py-14 text-sm text-muted-foreground">
					<Loader2 class="size-5 animate-spin" />
					Checking {label.toLowerCase()}...
				</div>
			{:else if checkError}
				<EmptyState icon={AlertTriangle} title="Update check failed" description={checkError}>
					<Button size="sm" variant="outline" onclick={() => onRecheck()}>Try again</Button>
				</EmptyState>
			{:else if updates.length === 0}
				<EmptyState
					icon={CheckCircle2}
					title="Nothing to check"
					description="No {label.toLowerCase()} are installed."
				/>
			{:else}
				{#if available.length === 0}
					<EmptyState
						icon={CheckCircle2}
						title="All up to date"
						description={unknown.length > 0
							? `${unknown.length} ${unknown.length === 1 ? 'file has' : 'files have'} an unknown source and could not be checked.`
							: 'Every identified file is on its newest compatible version.'}
						class="py-8"
					/>
				{:else}
					<div class="divide-y">
						{#each available as u (u.modId)}
							<div class="flex items-center gap-3 px-5 py-3">
								<div class="min-w-0 flex-1">
									<div class="flex flex-wrap items-center gap-2">
										<span class="truncate text-sm font-medium">{nameOf(u)}</span>
										{#if !u.enabled}
											<Badge variant="outline" class="text-xs">Disabled</Badge>
										{/if}
									</div>
									{@render versionLine(u)}
									<div class="mt-1 flex items-center gap-3">
										<span class="truncate font-mono text-[11px] text-muted-foreground">
											{u.latest?.fileName}
										</span>
										{@render links(u)}
									</div>
									{#if failures[u.modId]}
										<p class="mt-1 text-xs text-status-danger">{failures[u.modId]}</p>
									{/if}
								</div>
								<Button
									size="sm"
									onclick={() => updateOne(u)}
									disabled={checking || working}
									class="shrink-0"
								>
									{#if busy[u.modId]}
										<Loader2 class="size-4 animate-spin" />
									{:else}
										<ArrowUpCircle class="size-4" />
									{/if}
									Update
								</Button>
							</div>
						{/each}
					</div>
				{/if}

				{#if rollbacks.length > 0}
					<div class="border-t px-5 py-3">
						<h4 class="mb-2 text-xs font-semibold text-muted-foreground uppercase">
							Recently updated
						</h4>
						<div class="space-y-2">
							{#each rollbacks as u (u.modId)}
								<div class="flex items-center gap-3">
									<div class="min-w-0 flex-1">
										<span class="truncate text-sm">{nameOf(u)}</span>
										<p class="truncate text-xs text-muted-foreground">
											Previous file {u.rollbackFileName}{u.rollbackVersion
												? ` (${u.rollbackVersion})`
												: ''}
										</p>
										{#if failures[u.modId]}
											<p class="mt-1 text-xs text-status-danger">{failures[u.modId]}</p>
										{/if}
									</div>
									<Button
										size="sm"
										variant="outline"
										onclick={() => rollback(u)}
										disabled={checking || working}
									>
										{#if busy[u.modId]}
											<Loader2 class="size-4 animate-spin" />
										{:else}
											<RotateCcw class="size-4" />
										{/if}
										Roll back
									</Button>
								</div>
							{/each}
						</div>
					</div>
				{/if}

				{#if managed.length > 0}
					<div class="border-t px-5 py-3 text-xs">
						<h4 class="mb-1 font-semibold text-muted-foreground">
							Newer build, not updated here ({managed.length})
						</h4>
						<ul class="space-y-0.5 text-muted-foreground">
							{#each managed as u (u.modId)}
								<li class="flex items-center gap-2">
									<span class="truncate">
										{nameOf(u)}
										{u.latest?.version}: {u.managedBy}
									</span>
									{@render links(u)}
								</li>
							{/each}
						</ul>
					</div>
				{/if}

				{#if errored.length > 0 || incompatible.length > 0 || unknown.length > 0}
					<div class="space-y-3 border-t px-5 py-3 text-xs">
						{#if errored.length > 0}
							<div>
								<h4 class="mb-1 flex items-center gap-1.5 font-semibold text-status-danger">
									<AlertTriangle class="size-3.5" />
									Could not check {errored.length}
								</h4>
								<ul class="space-y-0.5 text-muted-foreground">
									{#each errored as u (u.modId)}
										<li class="truncate"><span class="font-mono">{u.fileName}</span>: {u.error}</li>
									{/each}
								</ul>
							</div>
						{/if}
						{#if incompatible.length > 0}
							<div>
								<h4 class="mb-1 font-semibold text-muted-foreground">
									No compatible newer build ({incompatible.length})
								</h4>
								<ul class="space-y-0.5 text-muted-foreground">
									{#each incompatible as u (u.modId)}
										<li class="flex items-center gap-2">
											<span class="truncate">{nameOf(u)}{u.error ? `: ${u.error}` : ''}</span>
											{@render links(u)}
										</li>
									{/each}
								</ul>
							</div>
						{/if}
						{#if unknown.length > 0}
							<div>
								<h4 class="mb-1 flex items-center gap-1.5 font-semibold text-muted-foreground">
									<HelpCircle class="size-3.5" />
									Unknown source ({unknown.length})
								</h4>
								<p class="mb-1 text-muted-foreground">
									Not published on Modrinth or CurseForge under this exact file. Update these by
									hand.
								</p>
								<ul class="space-y-0.5 font-mono text-muted-foreground">
									{#each unknown as u (u.modId)}
										<li class="truncate">{u.fileName}</li>
									{/each}
								</ul>
							</div>
						{/if}
					</div>
				{/if}

				{#if upToDate.length > 0}
					<p class="border-t px-5 py-3 text-xs text-muted-foreground">
						{upToDate.length} up to date
					</p>
				{/if}
			{/if}
		</div>
	</Dialog.Content>
</Dialog.Root>
