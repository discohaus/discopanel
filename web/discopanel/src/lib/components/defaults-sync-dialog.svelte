<script lang="ts">
	import { untrack } from 'svelte';
	import {
		Dialog,
		DialogContent,
		DialogDescription,
		DialogFooter,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { EmptyState } from '$lib/components/app';
	import { rpcClient, rpcErrorMessage } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import { Code, ConnectError } from '@connectrpc/connect';
	import {
		DefaultsSyncOutcome,
		DefaultsSyncRestart,
		ListSyncMode,
		type DefaultsSyncChange,
		type DefaultsSyncExclusion,
		type DefaultsSyncField,
		type DefaultsSyncResult,
		type DefaultsSyncServer
	} from '$lib/proto/discopanel/v1/properties_pb';
	import { ArrowLeft, ArrowRight, Loader2, RefreshCw, RotateCw, Server } from '@lucide/svelte';

	let {
		open = $bindable(false),
		preselect = []
	}: {
		open?: boolean;
		// Field keys checked when the dialog opens
		preselect?: string[];
	} = $props();

	type Step = 'fields' | 'servers' | 'result';

	let step = $state<Step>('fields');
	let loading = $state(false);
	let loadError = $state('');
	let applying = $state(false);
	let fields = $state<DefaultsSyncField[]>([]);
	let excluded = $state<DefaultsSyncExclusion[]>([]);
	let servers = $state<DefaultsSyncServer[]>([]);
	let version = $state('');
	let results = $state<DefaultsSyncResult[]>([]);
	let selectedKeys = $state<Set<string>>(new Set());
	let selectedServers = $state<Set<string>>(new Set());
	let listMode = $state<ListSyncMode>(ListSyncMode.ADD);
	let restartRunning = $state(false);
	let confirmSensitive = $state(false);
	let showAllFields = $state(false);

	let fieldByKey = $derived(new Map(fields.map((f) => [f.key, f])));
	let selectable = $derived(servers.filter((s) => s.selectable));

	// Per field, servers holding another value versus no value at all
	let counts = $derived.by(() => {
		const differ: Record<string, number> = {};
		const unset: Record<string, number> = {};
		for (const s of selectable) {
			for (const c of s.changes) {
				const bucket = c.oldSet ? differ : unset;
				bucket[c.key] = (bucket[c.key] ?? 0) + 1;
			}
		}
		return { differ, unset };
	});
	// Only real disagreements count, filling unset fields stays explicit
	let differing = $derived(fields.filter((f) => (counts.differ[f.key] ?? 0) > 0));

	// Preselected fields stay listed even when nothing differs
	let visibleFields = $derived(
		showAllFields
			? fields
			: fields.filter(
					(f) =>
						(counts.differ[f.key] ?? 0) + (counts.unset[f.key] ?? 0) > 0 ||
						preselect.includes(f.key)
				)
	);
	let listSelected = $derived([...selectedKeys].some((k) => fieldByKey.get(k)?.list));
	let sensitiveSelected = $derived(
		fields.filter((f) => f.sensitive && selectedKeys.has(f.key)).map((f) => f.label)
	);
	// Turning the whitelist on without its players can lock people out
	let whitelistWarning = $derived(
		selectedKeys.has('enableWhitelist') &&
			fieldByKey.get('enableWhitelist')?.defaultValue === 'true' &&
			!selectedKeys.has('whitelist')
	);

	function changesFor(server: DefaultsSyncServer): DefaultsSyncChange[] {
		return server.changes.filter((c) => selectedKeys.has(c.key));
	}

	let targets = $derived(
		selectable.filter((s) => selectedServers.has(s.serverId) && changesFor(s).length > 0)
	);
	let runningCount = $derived(targets.filter((s) => s.running && !s.busy).length);
	let busyCount = $derived(targets.filter((s) => s.busy).length);

	// Fresh preview every time the dialog opens
	$effect(() => {
		if (!open) return;
		untrack(() => {
			step = 'fields';
			results = [];
			listMode = ListSyncMode.ADD;
			restartRunning = false;
			confirmSensitive = false;
			showAllFields = false;
			void load(true);
		});
	});

	async function load(initial: boolean) {
		loading = true;
		loadError = '';
		try {
			const resp = await rpcClient.properties.previewDefaultsSync({ keys: [], listMode });
			fields = resp.fields;
			excluded = resp.excluded;
			servers = resp.servers;
			version = resp.defaultsVersion;
			const syncable = new Set(resp.fields.map((f) => f.key));
			if (initial) {
				selectedKeys = withPartners(preselect.filter((k) => syncable.has(k)));
				selectedServers = new Set(resp.servers.filter((s) => s.selectable).map((s) => s.serverId));
			} else {
				// Fields can vanish when defaults were edited meanwhile
				selectedKeys = new Set([...selectedKeys].filter((k) => syncable.has(k)));
			}
		} catch (error: unknown) {
			loadError = rpcErrorMessage(error, 'Failed to compare defaults with servers');
		} finally {
			loading = false;
		}
	}

	// Linked fields such as a resource pack and its hash move together
	function withPartners(keys: string[]): Set<string> {
		return new Set(
			keys.flatMap((key) => [
				key,
				...(fieldByKey.get(key)?.linked ?? []).filter((p) => fieldByKey.has(p))
			])
		);
	}

	function toggleKey(key: string, on: boolean) {
		const group = [key, ...(fieldByKey.get(key)?.linked ?? [])];
		const rest = [...selectedKeys].filter((k) => !group.includes(k));
		selectedKeys = on ? withPartners([...rest, key]) : new Set(rest);
	}

	function toggleServer(id: string, on: boolean) {
		const rest = [...selectedServers].filter((k) => k !== id);
		selectedServers = new Set(on ? [...rest, id] : rest);
	}

	function selectDiffering() {
		selectedKeys = withPartners(differing.filter((f) => !f.sensitive).map((f) => f.key));
	}

	async function setListMode(mode: ListSyncMode) {
		if (mode === listMode) return;
		listMode = mode;
		await load(false);
	}

	async function apply() {
		applying = true;
		try {
			const resp = await rpcClient.properties.applyDefaultsSync({
				keys: [...selectedKeys],
				serverIds: targets.map((s) => s.serverId),
				listMode,
				defaultsVersion: version,
				restartRunning,
				confirmSensitive: sensitiveSelected.length > 0 && confirmSensitive
			});
			results = resp.results;
			step = 'result';
			const failed = resp.results.filter((r) => r.outcome === DefaultsSyncOutcome.FAILED).length;
			if (failed > 0) notify.warning(`Defaults applied with ${failed} failed`);
			else notify.success('Defaults applied to servers');
		} catch (error: unknown) {
			if (error instanceof ConnectError && error.code === Code.FailedPrecondition) {
				notify.warning('Defaults changed since this preview', {
					description: 'The preview was refreshed, review the changes again.'
				});
				step = 'fields';
				await load(false);
			} else {
				notify.error(rpcErrorMessage(error, 'Failed to apply defaults'));
			}
		} finally {
			applying = false;
		}
	}

	function display(field: DefaultsSyncField | undefined, value: string, set = true): string {
		if (!set) return 'not set';
		if (value === '') return 'cleared';
		if (field?.type === 'checkbox') return value === 'true' ? 'on' : 'off';
		return value;
	}

	const outcomeLabel: Record<number, string> = {
		[DefaultsSyncOutcome.APPLIED]: 'Applied',
		[DefaultsSyncOutcome.UNCHANGED]: 'Already up to date',
		[DefaultsSyncOutcome.SKIPPED]: 'Skipped',
		[DefaultsSyncOutcome.FAILED]: 'Failed'
	};
	const outcomeClass: Record<number, string> = {
		[DefaultsSyncOutcome.APPLIED]: 'text-status-ok',
		[DefaultsSyncOutcome.UNCHANGED]: 'text-muted-foreground',
		[DefaultsSyncOutcome.SKIPPED]: 'text-status-warn',
		[DefaultsSyncOutcome.FAILED]: 'text-destructive'
	};
	// Restart states the user has to act on
	const restartAttention = new Set([
		DefaultsSyncRestart.NEEDED,
		DefaultsSyncRestart.BUSY,
		DefaultsSyncRestart.STALE_START
	]);

	let tally = $derived.by(() => {
		const t = { applied: 0, unchanged: 0, skipped: 0, failed: 0, queued: 0, attention: 0 };
		for (const r of results) {
			if (r.outcome === DefaultsSyncOutcome.APPLIED) t.applied++;
			else if (r.outcome === DefaultsSyncOutcome.UNCHANGED) t.unchanged++;
			else if (r.outcome === DefaultsSyncOutcome.SKIPPED) t.skipped++;
			else if (r.outcome === DefaultsSyncOutcome.FAILED) t.failed++;
			if (r.restart === DefaultsSyncRestart.QUEUED) t.queued++;
			if (restartAttention.has(r.restart)) t.attention++;
		}
		return t;
	});
</script>

<!-- Closing mid-apply would hide the results of a running batch -->
<Dialog
	bind:open={
		() => open,
		(value) => {
			if (!applying) open = value;
		}
	}
>
	<DialogContent class="sm:max-w-2xl">
		<DialogHeader>
			<DialogTitle>Apply defaults to existing servers</DialogTitle>
			<DialogDescription>
				{#if step === 'fields'}
					Choose which default properties to copy onto servers that already exist.
				{:else if step === 'servers'}
					Review what changes on each server and pick where to apply it.
				{:else}
					Results for each server.
				{/if}
			</DialogDescription>
		</DialogHeader>

		{#if loading && fields.length === 0}
			<div class="space-y-2">
				<Skeleton class="h-10 rounded-lg" />
				<Skeleton class="h-10 rounded-lg" />
				<Skeleton class="h-10 rounded-lg" />
			</div>
		{:else if loadError}
			<div class="space-y-3 rounded-lg border border-dashed p-4 text-center">
				<p class="text-sm text-destructive">{loadError}</p>
				<Button variant="outline" size="sm" onclick={() => load(fields.length === 0)}>
					<RefreshCw class="size-4" />
					Retry
				</Button>
			</div>
		{:else if servers.length === 0}
			<EmptyState
				icon={Server}
				title="No servers to update"
				description="There are no existing servers you can see. New servers pick up the defaults automatically."
			/>
		{:else if step === 'fields'}
			<div class="space-y-3">
				<div class="flex flex-wrap items-center justify-between gap-2 text-xs">
					<span class="text-muted-foreground">
						{selectedKeys.size} selected · {differing.length} of {fields.length} fields differ somewhere
					</span>
					<div class="flex items-center gap-2">
						<Button
							variant="ghost"
							size="sm"
							class="h-7"
							onclick={selectDiffering}
							disabled={differing.length === 0}
							title="Fields not set on a server and sensitive fields are left for you to pick"
						>
							Select all that differ
						</Button>
						<Button
							variant="ghost"
							size="sm"
							class="h-7"
							onclick={() => (selectedKeys = new Set())}
							disabled={selectedKeys.size === 0}
						>
							Clear
						</Button>
					</div>
				</div>

				<div class="max-h-80 divide-y overflow-y-auto rounded-lg border">
					{#each visibleFields as field (field.key)}
						{@const differ = counts.differ[field.key] ?? 0}
						{@const unset = counts.unset[field.key] ?? 0}
						<label class="flex cursor-pointer items-start gap-3 px-3 py-2 hover:bg-accent/40">
							<Checkbox
								class="mt-0.5"
								checked={selectedKeys.has(field.key)}
								onCheckedChange={(v) => toggleKey(field.key, v === true)}
							/>
							<span class="min-w-0 flex-1">
								<span class="flex items-center gap-1.5 text-sm font-medium">
									<span class="truncate">{field.label}</span>
									{#if field.sensitive}
										<span
											class="shrink-0 rounded bg-destructive/10 px-1 text-[10px] font-semibold text-destructive uppercase"
										>
											Sensitive
										</span>
									{/if}
								</span>
								<span class="block truncate text-xs text-muted-foreground">
									{field.category} · default
									<span class="font-mono">
										{field.defaultSet ? display(field, field.defaultValue) : 'not set, clears'}
									</span>
									{#if field.linked.length > 0}
										· synced with {field.linked
											.map((k) => fieldByKey.get(k)?.label ?? k)
											.join(', ')}
									{/if}
								</span>
							</span>
							<span class="shrink-0 text-right text-xs">
								{#if differ > 0}
									<span class="block text-status-busy">differs on {differ}</span>
								{/if}
								{#if unset > 0}
									<span class="block text-muted-foreground">not set on {unset}</span>
								{/if}
								{#if differ + unset === 0}
									<span class="text-muted-foreground">matches</span>
								{/if}
							</span>
						</label>
					{:else}
						<p class="p-4 text-center text-sm text-muted-foreground">
							Every server already matches the defaults.
						</p>
					{/each}
				</div>

				<div class="flex flex-wrap items-center justify-between gap-2">
					<label class="flex items-center gap-2 text-xs text-muted-foreground">
						<Checkbox
							checked={showAllFields}
							onCheckedChange={(v) => (showAllFields = v === true)}
						/>
						Show fields that already match
					</label>
					{#if excluded.length > 0}
						<details class="text-xs text-muted-foreground">
							<summary class="cursor-pointer">{excluded.length} fields never sync</summary>
							<ul class="mt-2 max-h-40 space-y-1 overflow-y-auto">
								{#each excluded as ex (ex.key)}
									<li><span class="text-foreground">{ex.label}</span> · {ex.reason}</li>
								{/each}
							</ul>
						</details>
					{/if}
				</div>

				{#if whitelistWarning}
					<p class="text-xs text-status-warn">
						This turns the whitelist on while each server keeps its own player list. Select
						Whitelist Players too if they should get the default players.
					</p>
				{/if}

				{#if listSelected}
					<div class="space-y-2 rounded-lg border p-3">
						<p class="text-sm font-medium">Whitelist and operators</p>
						<div class="flex flex-col gap-1.5 text-sm">
							<label class="flex items-center gap-2">
								<input
									type="radio"
									name="list-mode"
									checked={listMode !== ListSyncMode.MIRROR}
									disabled={loading}
									onchange={() => setListMode(ListSyncMode.ADD)}
								/>
								Add missing players, keep everyone else
							</label>
							<label class="flex items-center gap-2">
								<input
									type="radio"
									name="list-mode"
									checked={listMode === ListSyncMode.MIRROR}
									disabled={loading}
									onchange={() => setListMode(ListSyncMode.MIRROR)}
								/>
								Match the defaults exactly, removing other players
							</label>
						</div>
						<p class="text-xs text-muted-foreground">
							Lists are written to whitelist.json and ops.json when the server next starts.
						</p>
					</div>
				{/if}
			</div>
		{:else if step === 'servers'}
			<div class="space-y-3">
				<div class="flex items-center justify-between gap-2 text-xs text-muted-foreground">
					<span>{targets.length} of {selectable.length} servers will change</span>
					{#if loading}<Loader2 class="size-3.5 animate-spin" />{/if}
				</div>
				<div class="max-h-72 divide-y overflow-y-auto rounded-lg border">
					{#each servers as server (server.serverId)}
						{@const changes = changesFor(server)}
						<div class="px-3 py-2.5 {server.selectable ? '' : 'opacity-60'}">
							<label class="flex items-center gap-3 {server.selectable ? 'cursor-pointer' : ''}">
								<Checkbox
									checked={server.selectable && selectedServers.has(server.serverId)}
									disabled={!server.selectable || changes.length === 0}
									onCheckedChange={(v) => toggleServer(server.serverId, v === true)}
								/>
								<span class="min-w-0 flex-1 truncate text-sm font-medium">{server.serverName}</span>
								{#if !server.selectable}
									<span class="shrink-0 text-xs text-muted-foreground">{server.skipReason}</span>
								{:else if changes.length === 0}
									<span class="shrink-0 text-xs text-muted-foreground">Already up to date</span>
								{:else}
									<span class="shrink-0 text-xs text-muted-foreground">
										{changes.length}
										{changes.length === 1 ? 'change' : 'changes'}
										{#if server.busy}
											· <span class="text-status-warn">busy</span>
										{:else if server.running}
											· <span class="text-status-busy">running</span>
										{/if}
									</span>
								{/if}
							</label>
							{#if server.selectable && changes.length > 0}
								<ul class="mt-1.5 ml-7 space-y-0.5 text-xs">
									{#each changes as change (change.key)}
										{@const field = fieldByKey.get(change.key)}
										<li class="flex min-w-0 flex-wrap gap-x-1.5">
											<span class="text-muted-foreground">{field?.label ?? change.key}:</span>
											{#if !change.oldSet}
												<span class="text-status-warn">not set on server</span>
												<span class="text-muted-foreground">→</span>
											{/if}
											{#if field?.list}
												{#if change.added.length > 0}
													<span class="font-mono text-status-ok">+{change.added.join(', ')}</span>
												{/if}
												{#if change.removed.length > 0}
													<span class="font-mono text-destructive"
														>−{change.removed.join(', ')}</span
													>
												{/if}
											{:else}
												{#if change.oldSet}
													<span class="truncate font-mono text-muted-foreground line-through">
														{display(field, change.oldValue)}
													</span>
													<span class="text-muted-foreground">→</span>
												{/if}
												<span class="truncate font-mono">{display(field, change.newValue)}</span>
											{/if}
										</li>
									{/each}
									{#each server.notes as note (note)}
										<li class="text-status-warn">{note}</li>
									{/each}
								</ul>
							{/if}
						</div>
					{/each}
				</div>

				<div class="space-y-1.5 rounded-lg border p-3 text-sm">
					<p class="font-medium">Running servers</p>
					<label class="flex items-center gap-2">
						<input
							type="radio"
							name="restart-mode"
							checked={!restartRunning}
							onchange={() => (restartRunning = false)}
						/>
						Apply on next start, nothing restarts now
					</label>
					<label class="flex items-center gap-2">
						<input
							type="radio"
							name="restart-mode"
							checked={restartRunning}
							onchange={() => (restartRunning = true)}
						/>
						Restart running servers now, two at a time
					</label>
					<p class="text-xs text-muted-foreground">
						{#if runningCount === 0}
							None of the chosen servers are running.
						{:else if restartRunning}
							{runningCount} running {runningCount === 1 ? 'server restarts' : 'servers restart'} in
							the background.
						{:else}
							{runningCount} running {runningCount === 1 ? 'server keeps' : 'servers keep'} the old values
							until restarted.
						{/if}
						{#if busyCount > 0}
							{busyCount} busy {busyCount === 1 ? 'server needs' : 'servers need'} a manual restart once
							their current action finishes.
						{/if}
					</p>
				</div>

				{#if sensitiveSelected.length > 0}
					<label
						class="flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/5 p-3 text-sm"
					>
						<Checkbox
							class="mt-0.5"
							checked={confirmSensitive}
							onCheckedChange={(v) => (confirmSensitive = v === true)}
						/>
						<span>
							I understand this changes {sensitiveSelected.join(', ')} on every chosen server, which
							can grant players operator rights.
						</span>
					</label>
				{/if}
			</div>
		{:else}
			<div class="space-y-3">
				<p class="text-sm">
					{tally.applied} applied · {tally.unchanged} already up to date · {tally.skipped} skipped
					{#if tally.failed > 0}· <span class="text-destructive">{tally.failed} failed</span>{/if}
				</p>
				<div class="max-h-96 divide-y overflow-y-auto rounded-lg border">
					{#each results as result (result.serverId)}
						<div class="flex items-center gap-3 px-3 py-2 text-sm">
							<span class="min-w-0 flex-1 truncate">
								{result.serverName || result.serverId.slice(0, 8)}
								{#if result.reason}
									<span class="block truncate text-xs text-muted-foreground">{result.reason}</span>
								{:else if result.restartNote}
									<span
										class="block truncate text-xs {restartAttention.has(result.restart)
											? 'text-status-warn'
											: 'text-muted-foreground'}"
									>
										{result.restartNote}
									</span>
								{/if}
							</span>
							{#if result.restart === DefaultsSyncRestart.QUEUED}
								<span class="flex shrink-0 items-center gap-1 text-xs text-status-busy">
									<RotateCw class="size-3" />
									Restart queued
								</span>
							{/if}
							<span class="shrink-0 text-xs {outcomeClass[result.outcome] ?? ''}">
								{outcomeLabel[result.outcome] ?? 'Unknown'}
							</span>
						</div>
					{/each}
				</div>
				{#if tally.attention > 0}
					<p class="text-xs text-status-warn">
						{tally.attention}
						{tally.attention === 1 ? 'server needs' : 'servers need'} a restart before the new values
						take effect.
					</p>
				{/if}
			</div>
		{/if}

		<DialogFooter>
			{#if step === 'fields'}
				<Button variant="outline" onclick={() => (open = false)}>Cancel</Button>
				<Button
					onclick={() => (step = 'servers')}
					disabled={loading || !!loadError || servers.length === 0 || selectedKeys.size === 0}
				>
					Review servers
					<ArrowRight class="size-4" />
				</Button>
			{:else if step === 'servers'}
				<Button variant="outline" onclick={() => (step = 'fields')} disabled={applying}>
					<ArrowLeft class="size-4" />
					Back
				</Button>
				<Button
					onclick={apply}
					disabled={applying ||
						loading ||
						targets.length === 0 ||
						(sensitiveSelected.length > 0 && !confirmSensitive)}
				>
					{#if applying}
						<Loader2 class="size-4 animate-spin" />
					{/if}
					Apply to {targets.length}
					{targets.length === 1 ? 'server' : 'servers'}
				</Button>
			{:else}
				<Button onclick={() => (open = false)}>Done</Button>
			{/if}
		</DialogFooter>
	</DialogContent>
</Dialog>
