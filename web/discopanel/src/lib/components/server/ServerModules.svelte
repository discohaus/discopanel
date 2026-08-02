<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { SectionCard, EmptyState, ConfirmDialog, AddressSelect } from '$lib/components/app';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { registerRefresh } from '$lib/stores/refresh';
	import { toast } from 'svelte-sonner';
	import type { Server, Module, ModuleTemplate } from '$lib/proto/discopanel/v1/storage_pb';
	import type { PendingModulePrompt } from '$lib/proto/discopanel/v1/module_pb';
	import {
		ModuleStatus,
		ModuleProtocol,
		ModuleProtocolSchema
	} from '$lib/proto/discopanel/v1/storage_pb';
	import { enumLabel } from '$lib/proto-meta';
	import { TONE_BADGE, TONE_BG } from '$lib/server-status';
	import { moduleStatusMeta } from '$lib/module-status';
	import { getEventTypeLabel } from '$lib/utils/events';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { cn } from '$lib/utils';
	import {
		Loader2,
		Plus,
		Play,
		Square,
		RotateCw,
		Settings,
		Trash2,
		Terminal,
		Cpu,
		ExternalLink,
		KeyRound,
		Package,
		Puzzle,
		Link,
		Zap,
		Copy
	} from '@lucide/svelte';
	import ModuleDialog from './ModuleDialog.svelte';
	import ModuleLogsDialog from './ModuleLogsDialog.svelte';
	import ModulePromptDialog from './ModulePromptDialog.svelte';
	import ModuleTemplateCreateDialog from './ModuleTemplateCreateDialog.svelte';

	interface Props {
		server: Server;
		active?: boolean;
		prompts?: PendingModulePrompt[];
		onPromptAnswered?: () => void;
		onModuleCount?: (count: number) => void;
	}

	let { server, active = false, prompts = [], onPromptAnswered, onModuleCount }: Props = $props();

	let modules = $state<Module[]>([]);
	let templates = $state<ModuleTemplate[]>([]);
	let loading = $state(true);
	let actionLoading = $state<string | null>(null);
	let aliasValues = $state<Record<string, Record<string, string>>>({});
	let aliasKey = '';
	let snapshots = $state<Record<string, Record<string, string>>>({});

	let createDialogOpen = $state(false);
	let editDialogOpen = $state(false);
	let logsDialogOpen = $state(false);
	let templateCreateDialogOpen = $state(false);
	let selectedModule = $state<Module | null>(null);
	let deleteTarget = $state<Module | null>(null);
	let deleteOpen = $state(false);
	let promptDialogOpen = $state(false);
	let seenPromptKey = $state('');

	// Auto opens the prompt dialog when new input is awaited
	$effect(() => {
		const key = prompts
			.map((p) => `${p.moduleId}:${p.prompt?.id}`)
			.sort()
			.join('|');
		if (!key) {
			seenPromptKey = '';
			return;
		}
		if (active && key !== seenPromptKey) {
			seenPromptKey = key;
			promptDialogOpen = true;
		}
	});

	function modulePrompt(moduleId: string): PendingModulePrompt | undefined {
		return prompts.find((p) => p.moduleId === moduleId);
	}

	// Feeds the logs dialog fresh status from polling
	let liveSelectedModule = $derived(
		modules.find((m) => m.id === selectedModule?.id) ?? selectedModule
	);

	let hasLoaded = $state(false);
	// svelte-ignore state_referenced_locally
	let previousServerId = $state(server.id);

	// Resets everything when server changes
	$effect(() => {
		if (server.id !== previousServerId) {
			previousServerId = server.id;
			modules = [];
			templates = [];
			aliasValues = {};
			aliasKey = '';
			snapshots = {};
			loading = true;
			hasLoaded = false;
		}
	});

	// Loads once when tab first activates
	$effect(() => {
		if (active && !hasLoaded) {
			hasLoaded = true;
			loadModules();
			loadTemplates();
		}
	});

	// Polls while tab stays active
	$effect(() => {
		if (!active || !hasLoaded) return;
		const interval = setInterval(() => loadModules(true), 5000);
		return () => clearInterval(interval);
	});

	$effect(() => {
		if (!active) return;
		return registerRefresh(() => Promise.all([loadModules(true), loadTemplates()]));
	});

	async function loadModules(silent = false) {
		try {
			if (!silent) loading = true;
			const response = await rpcClient.module.listModules(
				{ serverId: server.id, fullStats: true },
				silent ? silentCallOptions : undefined
			);
			modules = response.modules;
			onModuleCount?.(modules.length);
			// Refetches aliases when ids or statuses change
			const key = modules.map((m) => `${m.id}:${m.status}`).join(',');
			if (key !== aliasKey) {
				aliasKey = key;
				modules.forEach((m) => loadAliases(m.id));
			}
			// Snapshots refresh every poll, values change while running
			modules.filter((m) => m.status === ModuleStatus.RUNNING).forEach((m) => loadSnapshot(m.id));
		} catch {
			if (!silent) toast.error('Failed to load modules');
		} finally {
			if (!silent) loading = false;
		}
	}

	async function loadTemplates() {
		try {
			const response = await rpcClient.module.listModuleTemplates({});
			// Global templates never attach to a server
			templates = response.templates.filter((t) => !t.global);
		} catch {
			toast.error('Failed to load module templates');
		}
	}

	async function loadAliases(moduleId: string) {
		try {
			const response = await rpcClient.module.getResolvedAliases(
				{ serverId: server.id, moduleId },
				silentCallOptions
			);
			aliasValues = { ...aliasValues, [moduleId]: response.aliases };
		} catch {
			/* Ignore alias lookup errors */
		}
	}

	function resolve(input: string, moduleId: string): string {
		const vals = aliasValues[moduleId] ?? {};
		return input.replace(/\{\{[^}]+\}\}/g, (match) => vals[match] ?? match);
	}

	const HOST_ALIASES = ['{{host.hostname}}', '{{server.proxy_hostnames}}'];

	// Host templates expand into one url per hostname
	function resolveUrls(input: string, moduleId: string, hostnames: string[]): string[] {
		const hostAlias = HOST_ALIASES.find((alias) => input.includes(alias));
		if (hostAlias && hostnames.length > 0) {
			return hostnames.map((name) => resolve(input.replaceAll(hostAlias, name), moduleId));
		}
		return [resolve(input, moduleId)];
	}

	async function loadSnapshot(moduleId: string) {
		try {
			const response = await rpcClient.module.getModuleStatusSnapshot(
				{ id: moduleId },
				silentCallOptions
			);
			if (response.available) {
				snapshots = { ...snapshots, [moduleId]: response.fields };
			}
		} catch {
			/* Ignore snapshot fetch errors */
		}
	}

	function snapshotLabel(key: string): string {
		const label = key.replace(/_/g, ' ');
		return label.charAt(0).toUpperCase() + label.slice(1);
	}

	async function copySnapshotValue(value: string) {
		if (await copyToClipboard(value)) {
			toast.success('Copied to clipboard');
		}
	}

	async function handleStartModule(module: Module) {
		actionLoading = module.id;
		try {
			await rpcClient.module.startModule({ id: module.id });
			toast.success(`Starting ${module.name}...`);
			await loadModules();
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
			await loadModules();
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
			await loadModules();
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
			await loadModules();
		} catch (error) {
			toast.error(
				`Failed to delete module: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
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

	function getDependencyName(moduleId: string): string {
		const dep = modules.find((m) => m.id === moduleId);
		return dep?.name || moduleId.slice(0, 8);
	}

	function hasAdvancedConfig(module: Module): boolean {
		return (module.dependencies?.length ?? 0) > 0 || (module.eventHooks?.length ?? 0) > 0;
	}

	function handleModuleCreated() {
		loadModules();
		// Extra refreshes catch fast status transitions
		setTimeout(() => loadModules(true), 1000);
		setTimeout(() => loadModules(true), 3000);
	}

	function handleModuleUpdated() {
		loadModules();
	}

	function handleTemplateCreated() {
		loadTemplates();
	}
</script>

<SectionCard title="Server modules" description="Companion services attached to this server">
	{#snippet action()}
		<Button variant="outline" size="sm" onclick={() => (templateCreateDialogOpen = true)}>
			<Puzzle class="size-4" />
			Create template
		</Button>
		<Button size="sm" onclick={() => (createDialogOpen = true)} disabled={templates.length === 0}>
			<Plus class="size-4" />
			Add module
		</Button>
	{/snippet}

	{#if loading}
		<div class="grid gap-3 lg:grid-cols-2">
			{#each Array(2) as _, i (i)}
				<Skeleton class="h-44 rounded-lg" />
			{/each}
		</div>
	{:else if modules.length === 0}
		<EmptyState
			icon={Package}
			title="No modules attached"
			description="Add a module to extend this server with companion services."
		>
			{#if templates.length > 0}
				<Button size="sm" onclick={() => (createDialogOpen = true)}>
					<Plus class="size-4" />
					Add module
				</Button>
			{:else}
				<p class="text-xs text-muted-foreground">No module templates available</p>
			{/if}
		</EmptyState>
	{:else}
		<div class="grid gap-3 lg:grid-cols-2">
			{#each modules as module (module.id)}
				{@const busy = actionLoading === module.id}
				{@const meta = moduleStatusMeta(module.status)}
				<div
					class="group flex flex-col rounded-lg border bg-card p-4 transition-colors hover:border-primary/20"
				>
					<div class="flex-1">
						<div class="flex items-start justify-between gap-2">
							<div class="min-w-0 flex-1">
								<div class="flex items-center gap-2">
									<h3 class="truncate text-sm font-medium">{module.name}</h3>
									<span
										class={cn(
											'inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium',
											TONE_BADGE[meta.tone]
										)}
									>
										<span
											class={cn(
												'size-1.5 rounded-full',
												TONE_BG[meta.tone],
												meta.transitional && 'animate-pulse'
											)}
										></span>
										{meta.label}
									</span>
								</div>
								<p class="mt-0.5 truncate text-xs text-muted-foreground">
									{module.templateName}{#if module.createdByUsername}<span
											class="text-muted-foreground/70"
										>
											· by {module.createdByUsername}</span
										>{/if}
								</p>
							</div>
							<div class="flex shrink-0 items-center gap-1">
								{#if module.status === ModuleStatus.STOPPED || module.status === ModuleStatus.ERROR}
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

						{#if modulePrompt(module.id)}
							<button
								class="mt-3 flex w-full items-center gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-left text-xs font-medium text-amber-600 transition-colors hover:bg-amber-500/20 dark:text-amber-400"
								onclick={() => (promptDialogOpen = true)}
							>
								<KeyRound class="size-3.5 shrink-0" />
								<span class="truncate">
									{modulePrompt(module.id)?.prompt?.title || 'Input needed'}
								</span>
								<span class="ml-auto shrink-0 underline-offset-2 hover:underline">Respond</span>
							</button>
						{/if}

						<div class="mt-3 space-y-2 text-xs">
							{#if module.ports?.length}
								<div class="flex flex-wrap gap-1.5">
									{#each module.ports as port, i (i)}
										<Badge variant="outline" class="font-mono">
											{port.name || 'Port'}: {port.hostPort || '?'}:{port.containerPort}/{enumLabel(
												ModuleProtocolSchema,
												port.protocol || ModuleProtocol.TCP
											)}
										</Badge>
									{/each}
								</div>
							{:else}
								<span class="text-muted-foreground">No ports</span>
							{/if}
							{#if module.status === ModuleStatus.RUNNING && module.memoryUsage > 0}
								<div class="flex items-center gap-3 text-muted-foreground">
									<span class="flex items-center gap-1">
										<Cpu class="size-3" />
										<span class="tabular">{module.memoryUsage.toFixed(0)} MB</span>
									</span>
									<span class="tabular">CPU: {module.cpuPercent.toFixed(1)}%</span>
								</div>
							{/if}
						</div>

						{#if module.status === ModuleStatus.RUNNING && snapshots[module.id]}
							{@const snapshot = snapshots[module.id]}
							<div class="mt-3 space-y-0.5 rounded-md bg-muted/40 px-2.5 py-2 text-xs">
								{#each Object.keys(snapshot).sort() as key (key)}
									<div class="flex items-center gap-1.5">
										<span class="shrink-0 text-muted-foreground">{snapshotLabel(key)}:</span>
										<button
											class="group/copy flex min-w-0 items-center gap-1 font-mono text-foreground transition-colors hover:text-primary"
											onclick={() => copySnapshotValue(snapshot[key])}
											title="Copy value"
										>
											<span class="truncate">{snapshot[key]}</span>
											<Copy
												class="size-3 shrink-0 opacity-0 transition-opacity group-hover/copy:opacity-100"
											/>
										</button>
									</div>
								{/each}
							</div>
						{/if}

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

						{#if hasAdvancedConfig(module)}
							<div class="mt-3 space-y-1.5 text-xs">
								{#if module.dependencies && module.dependencies.length > 0}
									<div class="flex items-center gap-1.5 text-muted-foreground">
										<Link class="size-3 shrink-0" />
										<span>Depends on:</span>
										<span class="truncate text-foreground">
											{module.dependencies.map((d) => getDependencyName(d.moduleId)).join(', ')}
										</span>
									</div>
								{/if}
								{#if module.eventHooks && module.eventHooks.length > 0}
									<div class="flex items-center gap-1.5 text-muted-foreground">
										<Zap class="size-3 shrink-0" />
										<span
											>{module.eventHooks.length} hook{module.eventHooks.length > 1
												? 's'
												: ''}</span
										>
										<span class="truncate text-muted-foreground/70">
											({module.eventHooks.map((h) => getEventTypeLabel(h.event)).join(', ')})
										</span>
									</div>
								{/if}
							</div>
						{/if}
					</div>

					<div class="mt-3 flex items-center justify-between gap-2 border-t pt-2.5">
						<div class="flex min-w-0 flex-wrap items-center gap-1">
							{#if module.autoStart}
								<Badge variant="secondary">Auto-start</Badge>
							{/if}
							{#if module.followServerLifecycle}
								<Badge variant="secondary">Follows server</Badge>
							{/if}
							{#if module.detached}
								<Badge variant="secondary">Detached</Badge>
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
						</div>
					</div>
				</div>
			{/each}
		</div>
	{/if}
</SectionCard>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete {deleteTarget?.name ?? 'module'}?"
	description="This will stop and remove the container and all module data."
	confirmLabel="Delete module"
	destructive
	onConfirm={confirmDelete}
/>

<ModuleDialog
	bind:open={createDialogOpen}
	mode="create"
	{server}
	{templates}
	onSuccess={handleModuleCreated}
	onTemplateDeleted={loadTemplates}
/>

{#if selectedModule}
	<ModuleDialog
		bind:open={editDialogOpen}
		mode="edit"
		module={selectedModule}
		onSuccess={handleModuleUpdated}
	/>

	<ModuleLogsDialog bind:open={logsDialogOpen} module={liveSelectedModule ?? selectedModule} />
{/if}

<ModuleTemplateCreateDialog
	bind:open={templateCreateDialogOpen}
	onSuccess={handleTemplateCreated}
/>

<ModulePromptDialog bind:open={promptDialogOpen} {prompts} onAnswered={onPromptAnswered} />
