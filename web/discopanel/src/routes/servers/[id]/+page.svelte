<script lang="ts">
	import { page } from '$app/state';
	import { onMount, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { serversStore } from '$lib/stores/servers';
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { registerRefresh } from '$lib/stores/refresh';
	import {
		DropdownMenu,
		DropdownMenuContent,
		DropdownMenuItem,
		DropdownMenuSeparator,
		DropdownMenuTrigger
	} from '$lib/components/ui/dropdown-menu';
	import {
		StatusBadge,
		ServerAvatar,
		ConfirmDialog,
		TabRail,
		EmptyState
	} from '$lib/components/app';
	import { notify } from '$lib/stores/activity.svelte';
	import {
		Play,
		Square,
		RotateCw,
		RefreshCcw,
		MoreVertical,
		Loader2,
		Trash2,
		Copy,
		ServerOff,
		CircleArrowUp
	} from '@lucide/svelte';
	import { create } from '@bufbuild/protobuf';
	import { ConnectError, Code } from '@connectrpc/connect';
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import { ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import type { PendingModulePrompt } from '$lib/proto/discopanel/v1/module_pb';
	import {
		GetServerRequestSchema,
		DeleteServerRequestSchema
	} from '$lib/proto/discopanel/v1/server_pb';
	import { canStop, canRestart } from '$lib/server-status';
	import { runServerAction, type ServerOp } from '$lib/server-actions';
	import { loaderDisplayName } from '$lib/stores/loaders';
	import { formatDate } from '$lib/utils/time';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { modpackUpdatePending, modpackSettingsHref } from '$lib/stores/modpack-updates.svelte';
	import StatusPanel from '$lib/components/server/overview/status-panel.svelte';
	import ConnectionPanel from '$lib/components/server/overview/connection-panel.svelte';
	import ResourcesPanel from '$lib/components/server/overview/resources-panel.svelte';
	import ServerMetricsCharts from '$lib/components/server-metrics-charts.svelte';
	import ServerConsole from '$lib/components/server-console.svelte';
	import ServerSettings from '$lib/components/server-settings.svelte';
	import ServerProperties from '$lib/components/server-properties.svelte';
	import ServerMods from '$lib/components/server-mods.svelte';
	import ServerFiles from '$lib/components/files/server-files.svelte';
	import ServerBackups from '$lib/components/server-backups.svelte';
	import ServerRouting from '$lib/components/server-routing.svelte';
	import ServerTasks from '$lib/components/server-tasks.svelte';
	import ServerModules from '$lib/components/server/ServerModules.svelte';

	const DELETE_WARNING =
		'This permanently stops and removes the container, deletes all server files and the world, and removes every mod and property.\nThis cannot be undone.';

	const TABS = [
		{ key: 'overview', label: 'Overview' },
		{ key: 'settings', label: 'Settings' },
		{ key: 'console', label: 'Console' },
		{ key: 'files', label: 'Files' },
		{ key: 'mods', label: 'Mods' },
		{ key: 'modules', label: 'Modules' },
		{ key: 'tasks', label: 'Tasks' },
		{ key: 'network', label: 'Network' },
		{ key: 'properties', label: 'Properties' }
	] as const;
	type TabKey = (typeof TABS)[number]['key'];

	// Tabs that manage their own height and inner scrolling
	const FILL_TABS: TabKey[] = ['console', 'files', 'mods', 'properties', 'settings'];

	let server = $state<Server | null>(null);
	let loading = $state(true);
	let actionLoading = $state(false);
	let deleteOpen = $state(false);
	let modulePrompts = $state<PendingModulePrompt[]>([]);
	// Null means module presence is still unknown
	let hasModules = $state<boolean | null>(null);
	let serverId = $derived(page.params.id);
	let prevServerId = $state<string | undefined>(undefined);
	let now = $state(new Date());
	let tabPane = $state<HTMLDivElement | null>(null);

	let activeTab = $derived.by<TabKey>(() => {
		let requested = page.url.searchParams.get('tab');
		// Old configuration links stay valid
		if (requested === 'config') requested = 'properties';
		return (TABS.find((t) => t.key === requested)?.key ?? 'overview') as TabKey;
	});

	// Fresh tab always opens scrolled to the top
	$effect(() => {
		void activeTab;
		tabPane?.scrollTo({ top: 0 });
	});

	function setTab(tab: string | undefined) {
		if (!tab || tab === activeTab || !serverId) return;
		const suffix = tab === 'overview' ? '' : `?tab=${tab}`;
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- base is resolved, only query varies
		goto(resolve(`/servers/${serverId}`) + suffix, { noScroll: true, keepFocus: true });
	}

	onMount(() => {
		const clock = setInterval(() => (now = new Date()), 30000);
		return () => clearInterval(clock);
	});

	$effect(() => {
		if (!serverId) return;
		return registerRefresh(() => {
			hasModules = null;
			return loadServer(true);
		});
	});

	// Status poll: faster while a transition is in flight, paused while the
	// tab is hidden, and always torn down with the effect (the old interval
	// lived outside the effect and relied on onMount for cleanup).
	const POLL_MS = 5000;
	const POLL_BUSY_MS = 2000;
	let transitioning = $derived(
		server !== null &&
			[
				ServerStatus.PROVISIONING,
				ServerStatus.CREATING,
				ServerStatus.STARTING,
				ServerStatus.STOPPING
			].includes(server.status)
	);

	$effect(() => {
		if (!serverId) return;
		const prev = untrack(() => prevServerId);
		if (prev !== serverId) {
			untrack(() => {
				loading = true;
				server = null;
				loadError = null;
				prevServerId = serverId;
				modulePrompts = [];
				hasModules = null;
			});
		}
		untrack(() => loadServer(true));
		const timer = setInterval(pollServer, transitioning ? POLL_BUSY_MS : POLL_MS);
		const onVisible = () => pollServer();
		document.addEventListener('visibilitychange', onVisible);
		return () => {
			clearInterval(timer);
			document.removeEventListener('visibilitychange', onVisible);
		};
	});

	let loadError = $state<string | null>(null);
	let inflight: Promise<void> | null = null;
	let rerun = false;

	// Explicit refreshes (after a save or an action) must not be swallowed by
	// a poll already in flight, since that poll may return pre-save data:
	// they queue one more fetch. Timer polls just skip.
	function loadServer(skipLoading = false): Promise<void> {
		if (!serverId) return Promise.resolve();
		if (inflight) {
			rerun = true;
			return inflight;
		}
		inflight = fetchServer(skipLoading).finally(() => {
			inflight = null;
			if (rerun) {
				rerun = false;
				void loadServer(true);
			}
		});
		return inflight;
	}

	function pollServer() {
		if (!inflight && document.visibilityState === 'visible') void loadServer(true);
	}

	async function fetchServer(skipLoading: boolean) {
		if (!serverId) return;
		const requestedId = serverId;
		try {
			const request = create(GetServerRequestSchema, { id: requestedId });
			const callOptions = skipLoading ? silentCallOptions : undefined;
			const response = await rpcClient.server.getServer(request, callOptions);
			if (response.server && serverId === requestedId) {
				server = response.server;
				loadError = null;
				serversStore.updateServer(server);
				loading = false;
				void loadModulePrompts();
			}
		} catch (error) {
			if (serverId !== requestedId) return;
			// Deleted elsewhere: drop the stale page instead of showing it forever
			if (error instanceof ConnectError && error.code === Code.NotFound) {
				if (server) notify.info(`${server.name} no longer exists`);
				server = null;
				loading = false;
				return;
			}
			if (!server) {
				notify.error('Failed to load server');
				loading = false;
			} else {
				loadError = 'Lost contact with the panel, retrying...';
			}
		}
	}

	// Keeps the modules tab badge live from any tab
	async function loadModulePrompts() {
		if (!serverId) return;
		const requestedId = serverId;
		try {
			// Servers without modules never get polled for prompts
			if (hasModules === null) {
				const res = await rpcClient.module.listModules(
					{ serverId: requestedId },
					silentCallOptions
				);
				if (serverId !== requestedId) return;
				hasModules = res.modules.length > 0;
			}
			if (!hasModules) {
				modulePrompts = [];
				return;
			}
			const response = await rpcClient.module.listModulePrompts(
				{ serverId: requestedId },
				silentCallOptions
			);
			if (serverId === requestedId) modulePrompts = response.prompts;
		} catch {
			/* Keep last known prompts on transient errors */
		}
	}

	async function handleServerAction(action: ServerOp) {
		if (!server) return;
		actionLoading = true;
		try {
			await runServerAction(action, server, () => loadServer());
		} finally {
			actionLoading = false;
		}
	}

	// Disruptive actions ask first: a misclick on Stop used to kick every
	// player with no warning.
	let pendingAction = $state<ServerOp | null>(null);
	let confirmActionOpen = $state(false);

	function requestAction(action: ServerOp) {
		if (!server) return;
		const quietlyIdle =
			(server.playersOnline ?? 0) === 0 &&
			(action === 'stop' || action === 'restart') &&
			server.status !== ServerStatus.PROVISIONING;
		// Starting, or stopping an empty server, needs no ceremony
		if (action === 'start' || quietlyIdle) {
			void handleServerAction(action);
			return;
		}
		pendingAction = action;
		confirmActionOpen = true;
	}

	let confirmCopy = $derived.by(() => {
		const name = server?.name ?? 'this server';
		const players = server?.playersOnline ?? 0;
		const kicked =
			players > 0
				? `${players} player${players === 1 ? ' is' : 's are'} online and will be disconnected.`
				: '';
		switch (pendingAction) {
			case 'stop':
				return server?.status === ServerStatus.PROVISIONING
					? {
							title: `Cancel starting ${name}?`,
							description:
								'Installation stops where it is. Files already downloaded are kept, so the next start resumes quickly.',
							label: 'Cancel start'
						}
					: {
							title: `Stop ${name}?`,
							description: `${kicked}\nThe world is saved before the server shuts down.`.trim(),
							label: 'Stop server'
						};
			case 'restart':
				return {
					title: `Restart ${name}?`,
					description:
						`${kicked}\nThe server stops, re-applies its configuration and starts again.`.trim(),
					label: 'Restart'
				};
			case 'recreate':
				return {
					title: `Recreate the container for ${name}?`,
					description:
						`The container is destroyed and rebuilt from the current settings. World and files are kept. ${kicked}`.trim(),
					label: 'Recreate container'
				};
			default:
				return { title: '', description: '', label: 'Confirm' };
		}
	});

	async function confirmDelete() {
		if (!server) return;
		try {
			await rpcClient.server.deleteServer(
				create(DeleteServerRequestSchema, { id: server.id }),
				silentCallOptions
			);
			serversStore.removeServer(server.id);
			notify.success('Server deleted');
			goto(resolve('/servers'));
		} catch (error) {
			notify.error(error instanceof Error ? error.message : 'Failed to delete server');
		}
	}

	function openModpackSettings() {
		if (!serverId) {
			return;
		}
		goto(modpackSettingsHref(serverId), { noScroll: true, keepFocus: true });
	}

	async function copyServerId() {
		if (!server) {
			return;
		}
		const ok = await copyToClipboard(server.id);
		if (ok) notify.success('Server ID copied');
		else notify.error('Failed to copy server ID');
	}

	// A brand-new server has no container while it provisions; that is not
	// "stopped" (it used to show Start and hide the way to cancel)
	let showStart = $derived(
		server
			? server.status === ServerStatus.STOPPED ||
					(!server.containerId && !transitioning && server.status !== ServerStatus.ERROR)
			: false
	);
	let transitionalLabel = $derived.by(() => {
		if (!server) return null;
		if (server.status === ServerStatus.STOPPING) return 'Stopping...';
		if (server.status === ServerStatus.CREATING) return 'Creating...';
		return null;
	});
</script>

<svelte:head>
	<title>{server ? `${server.name} · DiscoPanel` : 'DiscoPanel'}</title>
</svelte:head>

{#if loading && !server}
	<div class="flex h-96 items-center justify-center">
		<Loader2 class="size-8 animate-spin text-muted-foreground" />
	</div>
{:else if server}
	{@const srv = server}
	{@const fillTab = FILL_TABS.includes(activeTab)}
	<div class="flex min-h-0 flex-1 flex-col">
		<TabRail tabs={TABS} value={activeTab} onValueChange={setTab}>
			{#snippet tab(t)}
				{t.label}
				{#if t.key === 'modules' && modulePrompts.length > 0}
					<span class="ml-1 font-semibold text-amber-500">({modulePrompts.length})</span>
				{/if}
			{/snippet}
			{#snippet header()}
				<div class="flex items-start justify-between gap-4 pt-5 pb-4">
					<div class="flex min-w-0 items-center gap-3.5">
						<ServerAvatar name={srv.name} favicon={srv.favicon} size="lg" />
						<div class="min-w-0">
							<div class="flex flex-wrap items-center gap-2.5">
								<h1 class="truncate text-xl font-semibold tracking-tight">{srv.name}</h1>
								<StatusBadge status={srv.status} />
							</div>
							<div
								class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground"
							>
								<span>{srv.mcVersion}</span>
								{#if srv.slpAvailable && srv.serverVersion && srv.serverVersion !== srv.mcVersion}
									<span>·</span>
									<span title="protocol {srv.protocolVersion}">
										running {srv.serverVersion}
									</span>
								{/if}
								{#if $loaderDisplayName(srv.modLoader)}
									<span>·</span>
									<span>{$loaderDisplayName(srv.modLoader)}</span>
								{/if}
								{#if modpackUpdatePending(srv)}
									<button
										type="button"
										class="inline-flex items-center text-status-busy/80 transition-colors hover:text-status-busy"
										title="Modpack update available: {srv.modpackUpdateVersionName}"
										aria-label="Modpack update available"
										onclick={openModpackSettings}
									>
										<CircleArrowUp class="size-3.5" />
									</button>
								{/if}
								{#if srv.javaVersion}
									<span>·</span>
									{@const pinnedJava = Number(/^java(\d+)/.exec(srv.dockerImage ?? '')?.[1] ?? 0)}
									{#if pinnedJava && pinnedJava !== srv.javaVersion}
										<span
											class="text-status-busy"
											title="The runtime image is pinned to Java {pinnedJava}, but this Minecraft version wants Java {srv.javaVersion}. Clear the Docker image in Settings to use the recommended runtime."
										>
											Java {pinnedJava} (wants {srv.javaVersion})
										</span>
									{:else}
										<span>Java {pinnedJava || srv.javaVersion}</span>
									{/if}
								{/if}
								<span>·</span>
								<span>created {formatDate(srv.createdAt)}</span>
							</div>
							{#if srv.description}
								<p class="mt-1 line-clamp-1 text-xs text-muted-foreground/80">
									{srv.description}
								</p>
							{/if}
						</div>
					</div>

					<div class="flex shrink-0 items-center gap-1.5 rounded-xl border bg-card p-1.5 shadow-sm">
						{#if transitionalLabel}
							<Button variant="secondary" disabled size="sm">
								<Loader2 class="size-4 animate-spin" />
								{transitionalLabel}
							</Button>
						{:else if showStart || srv.status === ServerStatus.ERROR}
							<Button
								size="sm"
								class="bg-status-ok text-white hover:bg-status-ok/90"
								disabled={actionLoading}
								onclick={() => requestAction('start')}
							>
								{#if actionLoading}
									<Loader2 class="size-4 animate-spin" />
								{:else}
									<Play class="size-4" />
								{/if}
								Start
							</Button>
						{/if}
						{#if !transitionalLabel && canStop(srv.status) && !showStart}
							<Button
								variant="destructive"
								size="sm"
								disabled={actionLoading}
								onclick={() => requestAction('stop')}
							>
								{#if actionLoading}
									<Loader2 class="size-4 animate-spin" />
								{:else}
									<Square class="size-4" />
								{/if}
								{srv.status === ServerStatus.PROVISIONING ? 'Cancel' : 'Stop'}
							</Button>
						{/if}
						{#if !transitionalLabel && canRestart(srv.status)}
							<Button
								variant="outline"
								size="sm"
								class="hidden sm:inline-flex"
								disabled={actionLoading}
								onclick={() => requestAction('restart')}
							>
								<RotateCw class="size-4" />
								Restart
							</Button>
						{/if}
						<DropdownMenu>
							<DropdownMenuTrigger>
								{#snippet child({ props })}
									<Button
										{...props}
										variant="ghost"
										size="icon"
										class="size-8"
										disabled={actionLoading}
									>
										<MoreVertical class="size-4" />
										<span class="sr-only">More actions</span>
									</Button>
								{/snippet}
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								{#if !transitionalLabel && canRestart(srv.status)}
									<!-- Restart lives in the header on wider screens -->
									<DropdownMenuItem class="sm:hidden" onclick={() => requestAction('restart')}>
										<RotateCw class="mr-2 size-4" />
										Restart
									</DropdownMenuItem>
								{/if}
								<DropdownMenuItem onclick={() => requestAction('recreate')}>
									<RefreshCcw class="mr-2 size-4" />
									Recreate container
								</DropdownMenuItem>
								<DropdownMenuItem onclick={copyServerId}>
									<Copy class="mr-2 size-4" />
									Copy server ID
								</DropdownMenuItem>
								<DropdownMenuSeparator />
								<DropdownMenuItem variant="destructive" onclick={() => (deleteOpen = true)}>
									<Trash2 class="mr-2 size-4" />
									Delete server
								</DropdownMenuItem>
							</DropdownMenuContent>
						</DropdownMenu>
					</div>
				</div>
			{/snippet}
		</TabRail>

		{#if loadError}
			<div
				role="status"
				class="mx-auto mt-3 w-full max-w-6xl rounded-lg border border-status-busy/30 bg-status-busy/10 px-3 py-2 text-xs text-status-busy 2xl:max-w-7xl"
			>
				{loadError}
			</div>
		{/if}
		{#if fillTab}
			<div class="mx-auto flex min-h-0 w-full max-w-6xl flex-1 flex-col p-4 sm:p-6 2xl:max-w-7xl">
				{#if activeTab === 'console'}
					<ServerConsole {server} active={true} />
				{:else if activeTab === 'files'}
					<div class="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto">
						<div class="flex min-h-[calc(100%-4rem)] flex-1 shrink-0 flex-col">
							<ServerFiles {server} active={true} />
						</div>
						<ServerBackups {server} active={true} />
					</div>
				{:else if activeTab === 'mods'}
					<ServerMods {server} active={true} />
				{:else if activeTab === 'properties'}
					<ServerProperties {server} onUpdate={loadServer} />
				{:else if activeTab === 'settings'}
					<ServerSettings {server} onUpdate={loadServer} />
				{/if}
			</div>
		{:else}
			<div bind:this={tabPane} class="min-h-0 flex-1 overflow-y-auto">
				<div class="mx-auto w-full max-w-6xl p-4 sm:p-6 2xl:max-w-7xl">
					{#if activeTab === 'overview'}
						<div class="space-y-4">
							<div class="grid gap-4 lg:grid-cols-3">
								<StatusPanel {server} {now} />
								<ConnectionPanel {server} />
								<ResourcesPanel {server} />
							</div>
							<ServerMetricsCharts {server} />
						</div>
					{:else if activeTab === 'modules'}
						<ServerModules
							{server}
							active={true}
							prompts={modulePrompts}
							onPromptAnswered={loadModulePrompts}
							onModuleCount={(count) => (hasModules = count > 0)}
						/>
					{:else if activeTab === 'tasks'}
						<ServerTasks {server} active={true} />
					{:else if activeTab === 'network'}
						<ServerRouting {server} active={true} />
					{/if}
				</div>
			</div>
		{/if}
	</div>
{:else}
	<div class="flex h-96 items-center justify-center">
		<EmptyState
			icon={ServerOff}
			title="Server not found"
			description="This server may have been deleted or the link is wrong."
		>
			<Button onclick={() => goto(resolve('/servers'))}>Back to servers</Button>
		</EmptyState>
	</div>
{/if}

<ConfirmDialog
	bind:open={confirmActionOpen}
	title={confirmCopy.title}
	description={confirmCopy.description}
	confirmLabel={confirmCopy.label}
	destructive={pendingAction !== 'restart'}
	onConfirm={async () => {
		// Keep pendingAction until done so the dialog text stays put while
		// it shows its working state
		if (pendingAction) await handleServerAction(pendingAction);
		pendingAction = null;
	}}
/>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete {server?.name ?? 'server'}?"
	description={DELETE_WARNING}
	confirmLabel="Delete server"
	destructive
	requireText={server?.name ?? ''}
	onConfirm={confirmDelete}
/>
