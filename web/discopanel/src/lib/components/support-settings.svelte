<script lang="ts">
	import { onMount } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { Alert, AlertDescription } from '$lib/components/ui/alert';
	import { Input } from '$lib/components/ui/input';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Label } from '$lib/components/ui/label';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { Switch } from '$lib/components/ui/switch';
	import { Badge } from '$lib/components/ui/badge';
	import SettingRow from '$lib/components/app/setting-row.svelte';
	import { canUpdateSettings } from '$lib/stores/auth';
	import { formatRelative } from '$lib/utils/time';
	import {
		Download,
		CheckCircle2,
		Loader2,
		FileArchive,
		Database,
		ScrollText,
		Stethoscope,
		Send,
		Copy,
		ExternalLink,
		User,
		Mail,
		Github,
		ChevronDown,
		ChevronUp
	} from '@lucide/svelte';
	import { notify } from '$lib/stores/activity.svelte';
	import { SvelteSet } from 'svelte/reactivity';
	import { ConnectError, Code } from '@connectrpc/connect';
	import { rpcClient, rpcErrorMessage, silentCallOptions } from '$lib/api/rpc-client';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { serversStore } from '$lib/stores/servers';
	import DiagnosticsPanel from '$lib/components/diagnostics-panel.svelte';
	import type { Server as ServerType } from '$lib/proto/discopanel/v1/storage_pb';
	import {
		SupportBundleState,
		type DiagnosticReport,
		type GetSupportBundleResponse,
		type GetTelemetrySettingsResponse
	} from '$lib/proto/discopanel/v1/support_pb';

	let generating = $state(false);
	let uploading = $state(false);
	let canEdit = $derived($canUpdateSettings);
	let checkin = $state<GetTelemetrySettingsResponse | null>(null);
	let checkinEnabled = $state(false);
	let loadingCheckin = $state(true);
	let checkinError = $state('');
	let savingCheckin = $state(false);
	let checkinHint = $derived.by(() => {
		if (!checkin?.enabled) return '';
		if (checkin.lastError) return checkin.lastError;
		return checkin.lastHeartbeatAt
			? `Last check-in ${formatRelative(checkin.lastHeartbeatAt)}`
			: 'First check-in pending';
	});

	// Bundle runs hand their fresh report to the panel below
	let diagReport = $state<DiagnosticReport | null>(null);
	let bundlePath = $state<string | null>(null);
	let referenceId = $state<string | null>(null);
	let discordUsername = $state('');
	let email = $state('');
	let githubUsername = $state('');
	let issueDescription = $state('');
	let stepsToReproduce = $state('');

	// Server selection
	let servers = $state<ServerType[]>([]);
	let selectedServerIds = new SvelteSet<string>();
	let serverSectionExpanded = $state(false);
	let loadingServers = $state(true);

	const BUNDLE_CONTENTS = [
		{ icon: ScrollText, label: 'Panel and server logs' },
		{ icon: FileArchive, label: 'Configs and system info' },
		{ icon: Database, label: 'Database snapshot' },
		{ icon: Stethoscope, label: 'Diagnostics run' }
	];

	onMount(async () => {
		loadCheckin();
		try {
			servers = await serversStore.fetchServers(true);
		} catch (error) {
			console.error('Failed to load servers:', error);
		} finally {
			loadingServers = false;
		}
	});

	async function loadCheckin() {
		loadingCheckin = true;
		checkinError = '';
		try {
			checkin = await rpcClient.support.getTelemetrySettings({}, silentCallOptions);
			checkinEnabled = checkin.enabled;
		} catch (error) {
			checkinError = rpcErrorMessage(error, 'Failed to load the discohaus relay');
		} finally {
			loadingCheckin = false;
		}
	}

	async function setCheckin(enabled: boolean) {
		if (!checkin || savingCheckin) return;
		const previous = checkin.enabled;
		savingCheckin = true;
		try {
			const res = await rpcClient.support.updateTelemetrySettings({ enabled }, silentCallOptions);
			if (!res.settings) throw new Error('The panel did not return the relay');
			checkin = res.settings;
			checkinEnabled = res.settings.enabled;
		} catch (error) {
			checkinEnabled = previous;
			const message = rpcErrorMessage(error, 'Unknown error occurred');
			notify.error('Failed to update the discohaus relay', { description: message });
		} finally {
			savingCheckin = false;
		}
	}

	function toggleServer(serverId: string) {
		if (selectedServerIds.has(serverId)) {
			selectedServerIds.delete(serverId);
		} else {
			selectedServerIds.add(serverId);
		}
	}

	function selectAllServers() {
		selectedServerIds.clear();
		for (const s of servers) {
			selectedServerIds.add(s.id);
		}
	}

	function clearServerSelection() {
		selectedServerIds.clear();
	}

	// Polls a bundle job until it leaves the running state
	async function waitForBundle(bundleId: string): Promise<GetSupportBundleResponse> {
		let failures = 0;
		for (;;) {
			try {
				const res = await rpcClient.support.getSupportBundle({ bundleId }, silentCallOptions);
				failures = 0;
				if (res.state !== SupportBundleState.RUNNING) return res;
			} catch (error) {
				if (error instanceof ConnectError && error.code === Code.NotFound) throw error;
				// Short polls ride out a dropped connection
				if (++failures >= 5) throw error;
			}
			await new Promise((resolve) => setTimeout(resolve, 2000));
		}
	}

	async function generateBundle(upload: boolean = false) {
		if (upload) {
			uploading = true;
		} else {
			generating = true;
		}

		bundlePath = null;
		referenceId = null;

		const serverIds = Array.from(selectedServerIds);

		try {
			const options = {
				includeLogs: true,
				includeConfigs: true,
				includeSystemInfo: true,
				serverIds
			};
			const { bundleId } = upload
				? await rpcClient.support.uploadSupportBundle(
						{
							...options,
							discordUsername: discordUsername.trim(),
							email: email.trim(),
							githubUsername: githubUsername.trim(),
							issueDescription: issueDescription.trim(),
							stepsToReproduce: stepsToReproduce.trim()
						},
						silentCallOptions
					)
				: await rpcClient.support.generateSupportBundle(options, silentCallOptions);

			const result = await waitForBundle(bundleId);
			if (result.diagnostics) diagReport = result.diagnostics;
			if (result.state === SupportBundleState.FAILED) {
				notify.error(
					upload ? 'Failed to upload support bundle' : 'Failed to generate support bundle',
					{ description: result.message || 'Unknown error occurred' }
				);
				return;
			}
			if (upload) {
				referenceId = result.referenceId;
				discordUsername = '';
				email = '';
				githubUsername = '';
				issueDescription = '';
				stepsToReproduce = '';
				notify.success('Support bundle uploaded', {
					description: 'Keep the reference ID for your support request.'
				});
			} else {
				bundlePath = bundleId;
				notify.success('Support bundle ready', {
					description: 'Click download to save the archive.'
				});
			}
		} catch (error) {
			const message = rpcErrorMessage(error, 'Unknown error occurred');
			const action = upload ? 'upload' : 'generate';
			notify.error(`Failed to ${action} support bundle`, {
				description: message
			});
		} finally {
			generating = false;
			uploading = false;
		}
	}

	async function downloadBundle() {
		if (!bundlePath) return;

		try {
			const response = await rpcClient.support.downloadSupportBundle(
				{ bundleId: bundlePath },
				silentCallOptions
			);
			// Streams the archive through the download session
			const a = document.createElement('a');
			a.href = `/api/v1/download/${response.sessionId}`;
			a.download = response.filename;
			a.click();
			// Clears the bundle path after download
			bundlePath = null;
			notify.success('Support bundle download started');
		} catch (error) {
			const message = rpcErrorMessage(error, 'Unknown error occurred');
			notify.error('Failed to download support bundle', {
				description: message
			});
		}
	}

	async function copyReferenceId() {
		if (!referenceId) return;
		const success = await copyToClipboard(referenceId);
		if (success) {
			notify.success('Reference ID copied to clipboard');
		} else {
			notify.error('Failed to copy to clipboard');
		}
	}
</script>

<div class="space-y-4">
	<section class="overflow-hidden rounded-xl border bg-card">
		<header class="border-b bg-muted/30 px-4 py-3">
			<h3 class="text-sm font-semibold">Support bundle</h3>
			<p class="mt-0.5 text-xs text-muted-foreground">
				Package diagnostic data to troubleshoot issues, locally or with the DiscoPanel team
			</p>
		</header>

		<div
			class="flex flex-wrap items-center gap-x-5 gap-y-1.5 border-b px-4 py-2.5 text-xs text-muted-foreground"
		>
			{#each BUNDLE_CONTENTS as item (item.label)}
				{@const Icon = item.icon}
				<span class="inline-flex items-center gap-1.5">
					<Icon class="size-3.5" />
					{item.label}
				</span>
			{/each}
		</div>

		<div class="border-b px-4 py-4">
			<p class="text-sm font-medium">Contact and issue details</p>
			<p class="mt-0.5 mb-4 text-xs text-muted-foreground">
				Optional, but helps the team follow up on an uploaded bundle
			</p>

			<div class="mb-4 grid grid-cols-1 gap-4 md:grid-cols-3">
				<div class="space-y-2">
					<Label for="discord" class="flex items-center gap-2 text-sm font-medium">
						<User class="size-3.5" />
						Discord username
					</Label>
					<Input
						id="discord"
						type="text"
						placeholder="username"
						bind:value={discordUsername}
						class="h-9"
					/>
				</div>

				<div class="space-y-2">
					<Label for="email" class="flex items-center gap-2 text-sm font-medium">
						<Mail class="size-3.5" />
						Email
					</Label>
					<Input
						id="email"
						type="email"
						placeholder="you@example.com"
						bind:value={email}
						class="h-9"
					/>
				</div>

				<div class="space-y-2">
					<Label for="github" class="flex items-center gap-2 text-sm font-medium">
						<Github class="size-3.5" />
						GitHub username
					</Label>
					<Input
						id="github"
						type="text"
						placeholder="username"
						bind:value={githubUsername}
						class="h-9"
					/>
				</div>
			</div>

			<div class="grid grid-cols-1 gap-4 md:grid-cols-2">
				<div class="space-y-2">
					<Label for="description" class="text-sm font-medium">Issue description</Label>
					<Textarea
						id="description"
						placeholder="What went wrong, and what you expected instead"
						bind:value={issueDescription}
						rows={3}
						class="resize-none"
					/>
				</div>

				<div class="space-y-2">
					<Label for="steps" class="text-sm font-medium">Steps to reproduce</Label>
					<Textarea
						id="steps"
						placeholder="1. Go to...&#10;2. Click on...&#10;3. See error..."
						bind:value={stepsToReproduce}
						rows={3}
						class="resize-none"
					/>
				</div>
			</div>
		</div>

		<div class="border-b">
			<button
				type="button"
				class="flex w-full cursor-pointer items-center justify-between gap-3 px-4 py-3 text-left transition-colors hover:bg-accent/30"
				onclick={() => (serverSectionExpanded = !serverSectionExpanded)}
			>
				<div class="min-w-0">
					<p class="text-sm font-medium">Servers</p>
					<p class="mt-0.5 text-xs text-muted-foreground">
						{#if selectedServerIds.size === 0}
							All servers included
						{:else}
							{selectedServerIds.size} of {servers.length} selected
						{/if}
					</p>
				</div>
				{#if serverSectionExpanded}
					<ChevronUp class="size-4 shrink-0 text-muted-foreground" />
				{:else}
					<ChevronDown class="size-4 shrink-0 text-muted-foreground" />
				{/if}
			</button>

			{#if serverSectionExpanded}
				<div class="border-t bg-muted/10 px-4 py-4">
					{#if loadingServers}
						<div class="flex items-center justify-center py-4">
							<Loader2 class="size-5 animate-spin text-muted-foreground" />
							<span class="ml-2 text-sm text-muted-foreground">Loading servers...</span>
						</div>
					{:else if servers.length === 0}
						<p class="py-4 text-center text-sm text-muted-foreground">
							No servers found. Panel logs and configs are still included.
						</p>
					{:else}
						<div class="space-y-3">
							<div class="flex items-center justify-between gap-3">
								<p class="text-xs text-muted-foreground">
									Pick servers to limit the bundle to their logs and configs
								</p>
								<div class="flex gap-2">
									<Button variant="ghost" size="sm" class="h-7 text-xs" onclick={selectAllServers}>
										Select all
									</Button>
									<Button
										variant="ghost"
										size="sm"
										class="h-7 text-xs"
										onclick={clearServerSelection}
										disabled={selectedServerIds.size === 0}
									>
										Clear
									</Button>
								</div>
							</div>
							<div class="grid grid-cols-1 gap-2 sm:grid-cols-2 md:grid-cols-3">
								{#each servers as server (server.id)}
									<button
										type="button"
										class="flex cursor-pointer items-center gap-3 rounded-lg border p-3 text-left transition-colors hover:bg-muted/50 {selectedServerIds.has(
											server.id
										)
											? 'border-primary/50 bg-primary/5'
											: ''}"
										onclick={() => toggleServer(server.id)}
									>
										<Checkbox
											checked={selectedServerIds.has(server.id)}
											class="pointer-events-none"
										/>
										<div class="min-w-0 flex-1">
											<p class="truncate text-sm font-medium">{server.name}</p>
											<p class="truncate text-xs text-muted-foreground">
												{server.mcVersion || 'Unknown version'}
											</p>
										</div>
									</button>
								{/each}
							</div>
						</div>
					{/if}
				</div>
			{/if}
		</div>

		<div class="flex flex-wrap items-center justify-between gap-3 bg-muted/20 px-4 py-3">
			<p class="text-xs text-muted-foreground">
				Download first if you want to read through the configs and logs before uploading
			</p>
			<div class="flex flex-wrap gap-2">
				<Button
					onclick={() => generateBundle(false)}
					disabled={generating || uploading}
					variant="outline"
					size="sm"
				>
					{#if generating && !uploading}
						<Loader2 class="size-4 animate-spin" />
						Generating...
					{:else}
						<Download class="size-4" />
						Generate & download
					{/if}
				</Button>
				<Button onclick={() => generateBundle(true)} disabled={generating || uploading} size="sm">
					{#if uploading}
						<Loader2 class="size-4 animate-spin" />
						Uploading...
					{:else}
						<Send class="size-4" />
						Upload to support
					{/if}
				</Button>
			</div>
		</div>
	</section>

	{#if bundlePath}
		<Alert class="border-status-ok/30 bg-status-ok/5">
			<CheckCircle2 class="size-4 text-status-ok" />
			<div class="flex items-center justify-between gap-3">
				<div>
					<AlertDescription class="font-medium text-foreground">
						Support bundle ready for download
					</AlertDescription>
					<AlertDescription class="mt-1 text-xs text-muted-foreground">
						The archive is deleted from the panel once downloaded
					</AlertDescription>
				</div>
				<Button onclick={downloadBundle} size="sm" variant="outline" class="shrink-0">
					<Download class="size-4" />
					Download now
				</Button>
			</div>
		</Alert>
	{/if}

	{#if referenceId}
		<Alert class="border-status-ok/30 bg-status-ok/5">
			<CheckCircle2 class="size-4 text-status-ok" />
			<div class="min-w-0 space-y-3">
				<div>
					<p class="font-medium text-foreground">Support bundle uploaded</p>
					<p class="mt-1 text-xs leading-relaxed text-muted-foreground">
						Quote the reference ID when you ask for help on Discord, open a GitHub issue, or contact
						support directly.
						<a
							href="https://discopanel.app"
							target="_blank"
							rel="noopener noreferrer"
							class="inline-flex items-center gap-1 text-primary hover:underline"
						>
							For more info and links to Discord/Github, please visit discopanel.app
							<ExternalLink class="size-3" />
						</a>
					</p>
				</div>
				<div
					class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border bg-background/60 py-2 pr-2 pl-3"
				>
					<span class="stat-label shrink-0">Reference ID</span>
					<code class="min-w-0 flex-1 font-mono text-sm font-semibold text-foreground select-all">
						{referenceId}
					</code>
					<Button onclick={copyReferenceId} size="sm" variant="outline" class="shrink-0">
						<Copy class="size-3.5" />
						Copy
					</Button>
				</div>
			</div>
		</Alert>
	{/if}

	<DiagnosticsPanel bind:report={diagReport} />

	<section class="rounded-xl border bg-card">
		<SettingRow
			id="hub-relay"
			label="DiscoHaus Support Sync"
			description="Sync with DiscoHaus for release updates, notices, and other support features"
		>
			<div class="flex h-9 items-center gap-2 sm:justify-end">
				{#if loadingCheckin}
					<Loader2 class="size-4 animate-spin text-muted-foreground" />
				{:else if checkinError}
					<Button variant="outline" size="sm" onclick={loadCheckin}>Retry</Button>
				{:else if checkin?.managed}
					<Badge variant="secondary">Managed by discohaus</Badge>
				{:else if checkin?.configDisabled}
					<Badge variant="outline">Disabled in config</Badge>
				{:else if canEdit}
					<Switch
						id="hub-checkin"
						bind:checked={checkinEnabled}
						onCheckedChange={setCheckin}
						disabled={savingCheckin}
					/>
				{:else}
					<Badge variant="outline">{checkin?.enabled ? 'On' : 'Off'}</Badge>
				{/if}
			</div>
			{#if checkinError}
				<p class="mt-1.5 text-[11px] text-status-danger sm:text-right" role="alert">
					{checkinError}
				</p>
			{:else if checkinHint}
				<p
					class="mt-1.5 text-[11px] sm:text-right {checkin?.lastError
						? 'text-status-warn'
						: 'text-muted-foreground'}"
				>
					{checkinHint}
				</p>
			{/if}
		</SettingRow>
	</section>
</div>
