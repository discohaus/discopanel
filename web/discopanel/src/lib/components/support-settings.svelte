<script lang="ts">
	import { onMount } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { Alert, AlertDescription } from '$lib/components/ui/alert';
	import { Input } from '$lib/components/ui/input';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Label } from '$lib/components/ui/label';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import {
		Download,
		AlertCircle,
		CheckCircle2,
		Loader2,
		FileArchive,
		Database,
		ScrollText,
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
	import { rpcClient } from '$lib/api/rpc-client';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { serversStore } from '$lib/stores/servers';
	import type { Server as ServerType } from '$lib/proto/discopanel/v1/storage_pb';

	let generating = $state(false);
	let uploading = $state(false);
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
		{
			icon: ScrollText,
			title: 'Application logs',
			desc: 'Recent log entries and error messages'
		},
		{
			icon: Database,
			title: 'Database snapshot',
			desc: 'Current configuration and server data'
		},
		{
			icon: FileArchive,
			title: 'System information',
			desc: 'Version and environment details'
		}
	];

	onMount(async () => {
		try {
			servers = await serversStore.fetchServers(true);
		} catch (error) {
			console.error('Failed to load servers:', error);
		} finally {
			loadingServers = false;
		}
	});

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
			if (upload) {
				const response = await rpcClient.support.uploadSupportBundle({
					includeLogs: true,
					includeConfigs: true,
					includeSystemInfo: true,
					serverIds,
					discordUsername: discordUsername.trim(),
					email: email.trim(),
					githubUsername: githubUsername.trim(),
					issueDescription: issueDescription.trim(),
					stepsToReproduce: stepsToReproduce.trim()
				});

				if (response.success && response.referenceId) {
					referenceId = response.referenceId;
					discordUsername = '';
					email = '';
					githubUsername = '';
					issueDescription = '';
					stepsToReproduce = '';
					notify.success('Support bundle uploaded successfully!', {
						description: 'Save your reference ID for support requests.'
					});
				} else {
					notify.error('Failed to upload support bundle', {
						description: response.message || 'Unknown error occurred'
					});
				}
			} else {
				const response = await rpcClient.support.generateSupportBundle({
					includeLogs: true,
					includeConfigs: true,
					includeSystemInfo: true,
					serverIds
				});

				if (response.bundleId) {
					bundlePath = response.bundleId;
					notify.success('Support bundle generated!', {
						description: 'Click the download button to save the bundle.'
					});
				} else {
					notify.error('Failed to generate support bundle', {
						description: response.message
					});
				}
			}
		} catch (error) {
			const message = error instanceof Error ? error.message : 'Unknown error occurred';
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
			const response = await rpcClient.support.downloadSupportBundle({
				bundleId: bundlePath
			});
			// Builds a download link from the response
			const blob = new Blob([new Uint8Array(response.content)], { type: response.mimeType });
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = response.filename;
			a.click();
			URL.revokeObjectURL(url);
			// Clears the bundle path after download
			bundlePath = null;
			notify.success('Support bundle downloaded!');
		} catch (error) {
			const message = error instanceof Error ? error.message : 'Unknown error occurred';
			notify.error('Failed to download support bundle', {
				description: message
			});
		}
	}

	async function copyReferenceId() {
		if (!referenceId) return;
		const success = await copyToClipboard(referenceId);
		if (success) {
			notify.success('Reference ID copied to clipboard!');
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

		<div class="grid gap-4 border-b px-4 py-4 sm:grid-cols-3">
			{#each BUNDLE_CONTENTS as item (item.title)}
				{@const Icon = item.icon}
				<div class="flex items-start gap-2.5">
					<div
						class="flex size-8 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground"
					>
						<Icon class="size-4" />
					</div>
					<div class="min-w-0">
						<p class="text-sm font-medium">{item.title}</p>
						<p class="mt-0.5 text-xs leading-relaxed text-muted-foreground">{item.desc}</p>
					</div>
				</div>
			{/each}
		</div>

		<div class="border-b">
			<button
				type="button"
				class="flex w-full cursor-pointer items-center justify-between gap-3 px-4 py-3 text-left transition-colors hover:bg-accent/30"
				onclick={() => (serverSectionExpanded = !serverSectionExpanded)}
			>
				<div class="min-w-0">
					<p class="text-sm font-medium">Server selection</p>
					<p class="mt-0.5 text-xs text-muted-foreground">
						{#if selectedServerIds.size === 0}
							Include all servers (default)
						{:else}
							{selectedServerIds.size} server{selectedServerIds.size === 1 ? '' : 's'} selected
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
							No servers found. All available data will be included.
						</p>
					{:else}
						<div class="space-y-3">
							<div class="flex items-center justify-between gap-3">
								<p class="text-xs text-muted-foreground">
									Select specific servers to include their logs and configurations
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

		<div class="border-b px-4 py-4">
			<p class="text-sm font-medium">Contact and issue details</p>
			<p class="mt-0.5 mb-4 text-xs text-muted-foreground">
				Optional, but helps the team respond to an uploaded bundle
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
						placeholder="username#1234"
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
						placeholder="Describe the issue you're experiencing..."
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

		<div class="flex flex-wrap items-center justify-between gap-3 bg-muted/20 px-4 py-3">
			<p class="text-xs text-muted-foreground">Bundles include only the servers selected above</p>
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
						Bundle will be deleted after download
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
			<AlertDescription>
				<div class="space-y-3">
					<div class="font-medium text-foreground">Support bundle uploaded successfully!</div>

					<div class="space-y-2 rounded-lg border bg-background/50 p-3">
						<div class="stat-label">Reference ID</div>
						<div class="flex items-center gap-2">
							<code
								class="flex-1 rounded border border-border bg-background px-3 py-2 font-mono text-sm"
							>
								{referenceId}
							</code>
							<Button onclick={copyReferenceId} size="sm" variant="outline" class="shrink-0">
								<Copy class="size-3" />
							</Button>
						</div>
					</div>

					<div class="space-y-2 text-sm text-muted-foreground">
						<p class="font-medium">Please include this reference ID when:</p>
						<ul class="ml-2 list-inside list-disc space-y-1">
							<li>Requesting help in our Discord server</li>
							<li>Creating an issue on GitHub</li>
							<li>Contacting support directly</li>
						</ul>
					</div>

					<div class="border-t pt-2">
						<a
							href="https://discopanel.app"
							target="_blank"
							rel="noopener noreferrer"
							class="inline-flex items-center gap-1 text-xs text-primary hover:underline"
						>
							For more info and links to Discord/Github, please visit our site!
							<ExternalLink class="size-3" />
						</a>
					</div>
				</div>
			</AlertDescription>
		</Alert>
	{/if}

	<div class="flex items-start gap-3 rounded-lg border bg-muted/30 px-4 py-3">
		<AlertCircle class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
		<p class="text-xs leading-relaxed text-muted-foreground">
			<span class="font-medium text-foreground">Privacy notice.</span>
			Support bundles contain server configurations, logs, and database information. While we take privacy
			seriously and handle your data securely, please review the bundle contents before uploading if
			you have sensitive information.
		</p>
	</div>
</div>
