<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import { Switch } from '$lib/components/ui/switch';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { EmptyState, ConfirmDialog, AddressSelect } from '$lib/components/app';
	import { rpcClient, rpcErrorMessage, silentCallOptions } from '$lib/api/rpc-client';
	import { toast } from 'svelte-sonner';
	import type { Module } from '$lib/proto/discopanel/v1/storage_pb';
	import { ModuleStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import { TONE_BADGE, TONE_BG } from '$lib/server-status';
	import { moduleStatusMeta } from '$lib/module-status';
	import { certificatesStore } from '$lib/stores/certificates.svelte';
	import { upgradeUrl } from '$lib/certs';
	import { cn } from '$lib/utils';
	import {
		Loader2,
		Play,
		Square,
		RotateCw,
		Settings,
		Trash2,
		Terminal,
		Cpu,
		Server,
		Package,
		ExternalLink,
		ShieldCheck
	} from '@lucide/svelte';
	import ModuleDialog from '$lib/components/server/ModuleDialog.svelte';
	import ModuleLogsDialog from '$lib/components/server/ModuleLogsDialog.svelte';
	import { onMount } from 'svelte';
	import { registerRefresh } from '$lib/stores/refresh';

	let modules = $state<Module[]>([]);
	let loading = $state(true);
	let actionLoading = $state<string | null>(null);
	let aliasValues = $state<Record<string, Record<string, string>>>({});
	let aliasKey = '';

	let editDialogOpen = $state(false);
	let logsDialogOpen = $state(false);
	let selectedModule = $state<Module | null>(null);
	let deleteTarget = $state<Module | null>(null);
	let deleteOpen = $state(false);

	// Feeds the logs dialog fresh status from polling
	let liveSelectedModule = $derived(
		modules.find((m) => m.id === selectedModule?.id) ?? selectedModule
	);

	// Panel-owned globals lead the grid
	let sortedModules = $derived(
		[...modules].sort((a, b) => Number(Boolean(a.serverId)) - Number(Boolean(b.serverId)))
	);

	// Auto start is the persisted enable bit for system modules
	async function handleToggleSystem(module: Module, enabled: boolean) {
		actionLoading = module.id;
		const running =
			module.status === ModuleStatus.RUNNING || moduleStatusMeta(module.status).transitional;
		try {
			await rpcClient.module.updateModule({ id: module.id, autoStart: enabled });
			if (enabled) {
				if (!running) await rpcClient.module.startModule({ id: module.id });
				toast.success(`Enabling ${module.name}...`);
			} else {
				if (running) await rpcClient.module.stopModule({ id: module.id });
				toast.success(`${module.name} disabled`);
			}
		} catch (error) {
			toast.error(rpcErrorMessage(error, `Failed to ${enabled ? 'enable' : 'disable'} module`));
		} finally {
			await loadModules(true);
			actionLoading = null;
		}
	}

	let pageVisible = $state(true);

	onMount(() => {
		pageVisible = document.visibilityState === 'visible';
		loadModules();
		return registerRefresh(() => loadModules(true));
	});

	// Polls while the page stays visible
	$effect(() => {
		if (!pageVisible) return;
		const interval = setInterval(() => loadModules(true), 5000);
		return () => clearInterval(interval);
	});

	// Refreshes once the page turns visible again
	function handleVisibilityChange() {
		pageVisible = document.visibilityState === 'visible';
		if (pageVisible) loadModules(true);
	}

	async function loadModules(silent = false) {
		try {
			if (!silent) loading = true;
			// Empty request fetches modules across all servers
			const response = await rpcClient.module.listModules(
				{ fullStats: true },
				silent ? silentCallOptions : undefined
			);
			modules = response.modules;
			// Refetches aliases when ids or statuses change
			const key = modules.map((m) => `${m.id}:${m.status}`).join(',');
			if (key !== aliasKey) {
				aliasKey = key;
				modules.forEach((m) => loadAliases(m));
			}
		} catch {
			if (!silent) toast.error('Failed to load modules');
		} finally {
			if (!silent) loading = false;
		}
	}

	async function loadAliases(module: Module) {
		try {
			const response = await rpcClient.module.getResolvedAliases(
				{ serverId: module.serverId || undefined, moduleId: module.id },
				silentCallOptions
			);
			aliasValues = { ...aliasValues, [module.id]: response.aliases };
		} catch {
			/* Ignore alias lookup errors */
		}
	}

	function resolve(input: string, moduleId: string): string {
		const vals = aliasValues[moduleId] ?? {};
		return input.replace(/\{\{[^}]+\}\}/g, (match) => vals[match] ?? match);
	}

	// Lock aware urls need coverage loaded
	certificatesStore.ensure();

	const HOST_ALIASES = ['{{host.hostname}}', '{{server.proxy_hostnames}}'];

	// Host templates expand into one url per hostname
	function resolveUrls(input: string, moduleId: string, hostnames: string[]): string[] {
		const hostAlias = HOST_ALIASES.find((alias) => input.includes(alias));
		const secured = (name: string) => certificatesStore.isSecured(name);
		if (hostAlias && hostnames.length > 0) {
			return hostnames.map((name) =>
				upgradeUrl(resolve(input.replaceAll(hostAlias, name), moduleId), secured)
			);
		}
		return [upgradeUrl(resolve(input, moduleId), secured)];
	}

	async function handleStartModule(module: Module) {
		actionLoading = module.id;
		try {
			await rpcClient.module.startModule({ id: module.id });
			toast.success(`Starting ${module.name}...`);
			await loadModules(true);
		} catch (error) {
			toast.error(
				`Failed to start module: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			actionLoading = null;
		}
	}

	async function handleStopModule(module: Module) {
		actionLoading = module.id;
		try {
			await rpcClient.module.stopModule({ id: module.id });
			toast.success(`Stopping ${module.name}...`);
			await loadModules(true);
		} catch (error) {
			toast.error(
				`Failed to stop module: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			actionLoading = null;
		}
	}

	async function handleRestartModule(module: Module) {
		actionLoading = module.id;
		try {
			await rpcClient.module.restartModule({ id: module.id });
			toast.success(`Restarting ${module.name}...`);
			await loadModules(true);
		} catch (error) {
			toast.error(
				`Failed to restart module: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			actionLoading = null;
		}
	}

	function requestDelete(module: Module) {
		deleteTarget = module;
		deleteOpen = true;
	}

	async function confirmDelete() {
		if (!deleteTarget) return;
		const module = deleteTarget;
		actionLoading = module.id;
		try {
			await rpcClient.module.deleteModule({ id: module.id });
			toast.success(`Module "${module.name}" deleted`);
			await loadModules(true);
		} catch (error) {
			toast.error(rpcErrorMessage(error, 'Failed to delete module'));
		} finally {
			actionLoading = null;
		}
	}

	function openEditDialog(module: Module) {
		selectedModule = module;
		editDialogOpen = true;
	}

	function openLogsDialog(module: Module) {
		selectedModule = module;
		logsDialogOpen = true;
	}
</script>

<svelte:document onvisibilitychange={handleVisibilityChange} />

<div class="space-y-4">
	{#if loading && modules.length === 0}
		<div class="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
			{#each Array(3) as _, i (i)}
				<Skeleton class="h-32 rounded-lg" />
			{/each}
		</div>
	{:else if modules.length === 0}
		<div class="rounded-lg border bg-card">
			<EmptyState
				icon={Package}
				title="No active modules"
				description="You don't have any modules running on any of your servers right now."
			/>
		</div>
	{:else}
		<div class="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
			{#each sortedModules as module (module.id)}
				{@const busy = actionLoading === module.id}
				{@const meta = moduleStatusMeta(module.status)}
				{@const isSystem = !module.serverId}
				{@const disabled = isSystem && !module.autoStart && !meta.transitional}
				{@const badge = disabled
					? { label: 'Disabled', tone: 'sleep' as const }
					: { label: meta.label, tone: meta.tone }}
				<div
					class={cn(
						'group flex flex-col rounded-lg border bg-card p-4 transition-colors hover:border-primary/20',
						isSystem && 'border-primary/25 bg-primary/[0.03]'
					)}
				>
					<div class="flex items-start justify-between gap-2">
						<div class="min-w-0 flex-1">
							<div class="flex items-center gap-2">
								<h3 class="truncate text-sm font-medium">{module.name}</h3>
								{#if isSystem}
									<span
										class="inline-flex shrink-0 items-center gap-1 rounded-full border border-primary/25 bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary"
										title="Managed by DiscoPanel, runs for the whole panel"
									>
										<ShieldCheck class="size-3" />
										System
									</span>
								{/if}
								<span
									class={cn(
										'inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium',
										TONE_BADGE[badge.tone]
									)}
								>
									<span
										class={cn(
											'size-1.5 rounded-full',
											TONE_BG[badge.tone],
											meta.transitional && 'animate-pulse'
										)}
									></span>
									{badge.label}
								</span>
							</div>
							<div class="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
								<span class="flex min-w-0 items-center gap-1">
									{#if isSystem}
										<ShieldCheck class="size-3 shrink-0" />
										<span class="truncate">All servers</span>
									{:else}
										<Server class="size-3 shrink-0" />
										<span class="truncate">{module.serverName || module.serverId}</span>
									{/if}
								</span>
								<span>·</span>
								<span class="truncate">{module.templateName}</span>
							</div>
						</div>
						<div class="flex shrink-0 items-center gap-1">
							{#if isSystem}
								{#if module.status === ModuleStatus.RUNNING}
									<Button
										size="icon"
										variant="ghost"
										class="size-8"
										onclick={() => handleRestartModule(module)}
										disabled={busy}
										title="Restart module"
									>
										<RotateCw class="size-4" />
									</Button>
								{/if}
								<label class="flex cursor-pointer items-center gap-2 pl-1">
									<span class="text-xs text-muted-foreground">Enabled</span>
									{#if busy}
										<Loader2 class="size-4 animate-spin text-muted-foreground" />
									{:else}
										<Switch
											checked={module.autoStart}
											onCheckedChange={(v) => handleToggleSystem(module, v)}
											aria-label="Enable module"
										/>
									{/if}
								</label>
							{:else if module.status === ModuleStatus.STOPPED || module.status === ModuleStatus.ERROR}
								<Button
									size="icon"
									variant="ghost"
									class="size-8 text-status-ok hover:bg-status-ok/10 hover:text-status-ok"
									onclick={() => handleStartModule(module)}
									disabled={busy}
									title="Start module"
								>
									{#if busy}
										<Loader2 class="size-4 animate-spin" />
									{:else}
										<Play class="size-4" />
									{/if}
								</Button>
							{:else if module.status === ModuleStatus.RUNNING}
								<Button
									size="icon"
									variant="ghost"
									class="size-8 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
									onclick={() => handleStopModule(module)}
									disabled={busy}
									title="Stop module"
								>
									{#if busy}
										<Loader2 class="size-4 animate-spin" />
									{:else}
										<Square class="size-4" />
									{/if}
								</Button>
								<Button
									size="icon"
									variant="ghost"
									class="size-8"
									onclick={() => handleRestartModule(module)}
									disabled={busy}
									title="Restart module"
								>
									<RotateCw class="size-4" />
								</Button>
							{:else if meta.transitional}
								<Button size="icon" variant="ghost" class="size-8" disabled>
									<Loader2 class="size-4 animate-spin" />
								</Button>
							{/if}
						</div>
					</div>

					<div class="mt-3 flex h-4 items-center gap-3 text-xs text-muted-foreground">
						{#if module.status === ModuleStatus.RUNNING && module.memoryUsage > 0}
							<span class="flex items-center gap-1">
								<Cpu class="size-3" />
								<span class="tabular">{module.memoryUsage.toFixed(0)} MB</span>
							</span>
							<span class="tabular">CPU: {module.cpuPercent.toFixed(1)}%</span>
						{/if}
					</div>

					{#if module.accessUrls?.length}
						<div class="mt-3 space-y-1">
							{#each module.accessUrls as url, i (i)}
								{@const resolved = resolveUrls(url, module.id, module.serverProxyHostnames ?? [])}
								{#if resolved[0].includes('{{')}
									<div class="flex items-center gap-2 rounded-md bg-muted/40 px-2 py-1.5">
										<ExternalLink class="size-3 shrink-0 text-muted-foreground" />
										<span class="truncate font-mono text-xs text-muted-foreground">
											{resolved[0]}
										</span>
									</div>
								{:else}
									<AddressSelect addresses={resolved} label="URL" link />
								{/if}
							{/each}
						</div>
					{/if}

					<div class="mt-3 flex items-center justify-between gap-2 border-t pt-2.5">
						<div class="flex min-w-0 items-center gap-1">
							{#if module.autoStart && !isSystem}
								<Badge variant="secondary">Auto-start</Badge>
							{/if}
						</div>
						<div
							class="flex shrink-0 items-center gap-1 opacity-60 transition-opacity group-hover:opacity-100"
						>
							<Button
								size="icon"
								variant="ghost"
								class="size-7"
								onclick={() => openLogsDialog(module)}
								title="View logs"
							>
								<Terminal class="size-3.5" />
							</Button>
							<Button
								size="icon"
								variant="ghost"
								class="size-7"
								onclick={() => openEditDialog(module)}
								title="Edit module"
							>
								<Settings class="size-3.5" />
							</Button>
							{#if !isSystem}
								<Button
									size="icon"
									variant="ghost"
									class="size-7 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
									onclick={() => requestDelete(module)}
									disabled={busy}
									title="Delete module"
								>
									<Trash2 class="size-3.5" />
								</Button>
							{/if}
						</div>
					</div>
				</div>
			{/each}
		</div>
		<p class="tabular text-xs text-muted-foreground">
			{modules.length}
			{modules.length === 1 ? 'instance' : 'instances'}
		</p>
	{/if}
</div>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete {deleteTarget?.name ?? 'module'}?"
	description="This will stop and remove the container and all module data."
	confirmLabel="Delete module"
	destructive
	onConfirm={confirmDelete}
/>

{#if selectedModule}
	<ModuleDialog
		bind:open={editDialogOpen}
		mode="edit"
		module={selectedModule}
		onSuccess={() => loadModules(true)}
	/>

	<ModuleLogsDialog bind:open={logsDialogOpen} module={liveSelectedModule ?? selectedModule} />
{/if}
