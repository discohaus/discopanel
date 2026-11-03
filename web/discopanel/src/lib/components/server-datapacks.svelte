<script lang="ts">
	import { untrack } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import { Input } from '$lib/components/ui/input';
	import { Switch } from '$lib/components/ui/switch';
	import { Progress } from '$lib/components/ui/progress';
	import { EmptyState, ConfirmDialog } from '$lib/components/app';
	import {
		Loader2,
		Upload,
		Download,
		Trash2,
		Boxes,
		Search,
		X,
		RotateCw,
		TriangleAlert,
		Folder,
		Info,
		Sprout
	} from '@lucide/svelte';
	import { rpcClient, rpcErrorMessage, silentCallOptions } from '$lib/api/rpc-client';
	import { authStore } from '$lib/stores/auth';
	import { notify } from '$lib/stores/activity.svelte';
	import { isUp } from '$lib/server-status';
	import { ResourceType, ActionType } from '$lib/proto/discopanel/options/v1/options_pb';
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import type { Datapack } from '$lib/proto/discopanel/v1/mod_pb';
	import { formatBytes } from '$lib/utils';
	import { formatDate } from '$lib/utils/time';
	import { uploadFile, cancelUpload, type UploadProgress } from '$lib/utils/chunked-upload';

	interface Props {
		server: Server;
		active?: boolean;
	}

	let { server, active = false }: Props = $props();

	let datapacks = $state<Datapack[]>([]);
	let directory = $state('world/datapacks');
	// Null until the first list answers
	let worldExists = $state<boolean | null>(null);
	let loading = $state(true);
	let loadError = $state('');
	let uploading = $state(false);
	let uploadProgress = $state<UploadProgress | null>(null);
	let currentUploadFilename = $state('');
	let uploadAbortController = $state<AbortController | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);
	let deleteTarget = $state<Datapack | null>(null);
	let deleteOpen = $state(false);
	let filterText = $state('');
	let dragActive = $state(false);
	// Names with a toggle in flight
	let toggling = $state<Record<string, boolean>>({});
	// Set once anything changed since the last reload
	let needsReload = $state(false);
	let reloading = $state(false);
	// Uploads waiting on the replace confirmation
	let pendingFiles = $state<File[]>([]);
	let replaceOpen = $state(false);

	// Server the list was loaded for, null before the first load
	let loadedFor: string | null = null;

	let running = $derived(isUp(server.status));
	let canCommand = $derived(
		!!$authStore && authStore.hasPermission(ResourceType.SERVERS, ActionType.COMMAND, server.id)
	);
	// Downloads go through the file service
	let canDownload = $derived(
		!!$authStore && authStore.hasPermission(ResourceType.FILES, ActionType.READ, server.id)
	);

	// Loads per server id, a server switch resets and reloads
	$effect(() => {
		const id = server.id;
		if (!active || loadedFor === id) return;
		untrack(() => {
			if (loadedFor !== null) {
				datapacks = [];
				worldExists = null;
				loadError = '';
				filterText = '';
				needsReload = false;
				pendingFiles = [];
				replaceOpen = false;
			}
			loadedFor = id;
			loadDatapacks();
		});
	});

	async function loadDatapacks() {
		const id = server.id;
		try {
			loading = true;
			const response = await rpcClient.mod.listDatapacks({ serverId: id }, silentCallOptions);
			// A reply for a server we left is dropped
			if (id !== server.id) return;
			datapacks = response.datapacks;
			directory = response.directory || directory;
			worldExists = response.worldExists;
			loadError = '';
		} catch (error) {
			if (id !== server.id) return;
			loadError = rpcErrorMessage(error, 'Failed to load datapacks');
		} finally {
			if (id === server.id) loading = false;
		}
	}

	// Existing packs an upload of these files would replace
	function conflictsFor(files: File[]): Datapack[] {
		const names = new Set(files.map((f) => f.name));
		return datapacks.filter((p) => !p.isDirectory && names.has(p.name));
	}

	function requestUpload(fileList: FileList | File[]) {
		const files = Array.from(fileList).filter((f) => f.name.toLowerCase().endsWith('.zip'));
		if (files.length === 0) {
			notify.error('Datapacks must be .zip files');
			return;
		}
		if (conflictsFor(files).length > 0) {
			pendingFiles = files;
			replaceOpen = true;
			return;
		}
		uploadFiles(files);
	}

	// Not awaited so the dialog closes and the progress bar takes over;
	// pendingFiles stays so the closing dialog keeps its text
	function confirmReplace() {
		uploadFiles(pendingFiles);
	}

	let replaceConflicts = $derived(conflictsFor(pendingFiles));
	let replaceTitle = $derived(
		replaceConflicts.length === 1
			? `Replace ${replaceConflicts[0].name}?`
			: `Replace ${replaceConflicts.length} datapacks?`
	);
	let replaceDescription = $derived.by(() => {
		const off = replaceConflicts.filter((p) => !p.enabled);
		const lines = [
			replaceConflicts.length === 1
				? 'A datapack with this name is already installed. The upload overwrites it.'
				: `Already installed: ${replaceConflicts.map((p) => p.name).join(', ')}. The upload overwrites them.`
		];
		if (off.length === replaceConflicts.length) {
			lines.push(
				replaceConflicts.length === 1
					? 'It is currently disabled and will end up enabled.'
					: 'They are currently disabled and will all end up enabled.'
			);
		} else if (off.length > 0) {
			lines.push(
				`${off.map((p) => p.name).join(', ')} ${off.length === 1 ? 'is' : 'are'} currently disabled and will end up enabled.`
			);
		} else {
			lines.push(replaceConflicts.length === 1 ? 'It stays enabled.' : 'They stay enabled.');
		}
		return lines.join('\n');
	});

	async function uploadFiles(files: File[]) {
		uploading = true;
		uploadAbortController = new AbortController();
		let imported = 0;

		try {
			for (const file of files) {
				currentUploadFilename = file.name;
				uploadProgress = null;

				const result = await uploadFile(file, {
					onProgress: (progress) => {
						uploadProgress = progress;
					},
					signal: uploadAbortController.signal
				});

				try {
					await rpcClient.mod.importUploadedDatapack(
						{ serverId: server.id, uploadSessionId: result.sessionId },
						silentCallOptions
					);
					imported++;
				} catch (error) {
					// One bad zip should not stop the rest of the batch
					notify.error(`${file.name} was not added`, {
						description: rpcErrorMessage(error, 'Failed to import datapack')
					});
				}
			}
			if (imported > 0) {
				needsReload = true;
				notify.success(`Added ${imported} ${imported === 1 ? 'datapack' : 'datapacks'}`, {
					description: 'Run /reload or restart the server to apply'
				});
			}
		} catch (error: unknown) {
			if (error instanceof Error && error.message === 'Upload cancelled') {
				notify.info('Upload cancelled');
			} else {
				notify.error('Failed to upload datapack', {
					description: rpcErrorMessage(error, 'Upload failed')
				});
			}
		} finally {
			uploading = false;
			uploadProgress = null;
			currentUploadFilename = '';
			uploadAbortController = null;
			if (imported > 0) await loadDatapacks();
		}
	}

	async function handleFileSelect(event: Event) {
		const input = event.target as HTMLInputElement;
		if (!input.files || input.files.length === 0) return;
		requestUpload(input.files);
		input.value = '';
	}

	function handleDrop(e: DragEvent) {
		e.preventDefault();
		dragActive = false;
		if (uploading) return;
		if (e.dataTransfer?.files?.length) {
			requestUpload(e.dataTransfer.files);
		}
	}

	function cancelCurrentUpload() {
		uploadAbortController?.abort();
		if (uploadProgress?.sessionId) {
			cancelUpload(uploadProgress.sessionId).catch(() => {});
		}
	}

	async function toggleDatapack(pack: Datapack) {
		const enabled = !pack.enabled;
		toggling[pack.name] = true;
		try {
			const res = await rpcClient.mod.toggleDatapack(
				{ serverId: server.id, name: pack.name, enabled },
				silentCallOptions
			);
			if (res.datapack) {
				datapacks = datapacks.map((p) => (p.name === pack.name ? res.datapack! : p));
			}
			needsReload = true;
		} catch (error) {
			notify.error(`Failed to ${enabled ? 'enable' : 'disable'} ${pack.name}`, {
				description: rpcErrorMessage(error, 'Toggle failed')
			});
			await loadDatapacks();
		} finally {
			delete toggling[pack.name];
		}
	}

	function requestDelete(pack: Datapack) {
		deleteTarget = pack;
		deleteOpen = true;
	}

	async function confirmDelete() {
		if (!deleteTarget) return;
		const name = deleteTarget.name;
		try {
			await rpcClient.mod.deleteDatapack({ serverId: server.id, name }, silentCallOptions);
			datapacks = datapacks.filter((p) => p.name !== name);
			needsReload = true;
			notify.success(`Deleted ${name}`);
		} catch (error) {
			notify.error(`Failed to delete ${name}`, {
				description: rpcErrorMessage(error, 'Delete failed')
			});
		}
	}

	// Streams through the download endpoint, folders arrive zipped
	async function downloadDatapack(pack: Datapack) {
		try {
			const dir = pack.enabled ? directory : `${directory}_disabled`;
			const path = `${dir}/${pack.name}`;
			const response = pack.isDirectory
				? await rpcClient.file.downloadArchive(
						{ serverId: server.id, paths: [path] },
						silentCallOptions
					)
				: await rpcClient.file.initFileDownload({ serverId: server.id, path }, silentCallOptions);
			const a = document.createElement('a');
			a.href = `/api/v1/download/${response.sessionId}`;
			a.download = response.filename || pack.name;
			a.click();
		} catch (error) {
			notify.error('Failed to download datapack', {
				description: rpcErrorMessage(error, 'Download failed')
			});
		}
	}

	async function reload() {
		reloading = true;
		try {
			const res = await rpcClient.server.sendCommand(
				{ id: server.id, command: 'reload' },
				silentCallOptions
			);
			if (!res.success) {
				notify.error('Reload failed', { description: res.error || undefined });
				return;
			}
			needsReload = false;
			notify.success('Datapacks reloaded', {
				description: 'Check the console for any datapack errors'
			});
		} catch (error) {
			notify.error('Reload failed', { description: rpcErrorMessage(error, 'Command failed') });
		} finally {
			reloading = false;
		}
	}

	let enabledCount = $derived(datapacks.filter((p) => p.enabled).length);

	let visibleDatapacks = $derived.by(() => {
		if (!filterText.trim()) return datapacks;
		const q = filterText.trim().toLowerCase();
		return datapacks.filter(
			(p) => p.name.toLowerCase().includes(q) || p.description.toLowerCase().includes(q)
		);
	});
</script>

<div
	role="region"
	aria-label="Datapacks upload area"
	ondragover={(e) => {
		e.preventDefault();
		if (!uploading) dragActive = true;
	}}
	ondragleave={(e) => {
		if (e.currentTarget === e.target) dragActive = false;
	}}
	ondrop={handleDrop}
	class="relative flex min-h-0 flex-1 flex-col"
>
	{#if dragActive}
		<div
			class="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-xl border-2 border-dashed border-primary bg-primary/5 backdrop-blur-[1px]"
		>
			<div class="flex items-center gap-2 text-sm font-medium text-primary">
				<Upload class="size-4" />
				Drop datapack .zip files to upload
			</div>
		</div>
	{/if}

	<section class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border bg-card">
		<header
			class="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3"
		>
			<div class="min-w-0">
				<h3 class="text-sm font-semibold">Datapacks</h3>
				<p class="mt-0.5 text-xs text-muted-foreground">
					Files in {directory}/. Changes apply after /reload or a restart.
				</p>
			</div>
			<div class="flex shrink-0 items-center gap-2">
				{#if datapacks.length > 0}
					<span class="tabular text-xs text-muted-foreground">
						{enabledCount}/{datapacks.length} enabled
					</span>
				{/if}
				{#if running && canCommand}
					<Button
						variant="outline"
						size="sm"
						onclick={reload}
						disabled={reloading}
						title="Run /reload on the server"
					>
						{#if reloading}
							<Loader2 class="size-4 animate-spin" />
						{:else}
							<RotateCw class="size-4" />
						{/if}
						Reload
					</Button>
				{/if}
				<Button onclick={() => fileInput?.click()} disabled={uploading} size="sm">
					{#if uploading}
						<Loader2 class="size-4 animate-spin" />
					{:else}
						<Upload class="size-4" />
					{/if}
					Upload
				</Button>
				<input
					bind:this={fileInput}
					type="file"
					multiple
					accept=".zip,application/zip"
					onchange={handleFileSelect}
					class="hidden"
				/>
			</div>
		</header>

		{#if needsReload}
			<div
				role="status"
				class="flex shrink-0 items-center gap-2 border-b bg-status-busy/10 px-4 py-2 text-xs text-status-busy"
			>
				<Info class="size-3.5 shrink-0" />
				<span class="min-w-0 flex-1">
					{#if running}
						Datapack changes are saved. Players see them after
						{canCommand ? 'you reload' : 'a /reload'} or a restart.
					{:else}
						Datapack changes are saved and apply the next time the server starts.
					{/if}
				</span>
				<Button
					size="icon"
					variant="ghost"
					class="size-6 shrink-0"
					onclick={() => (needsReload = false)}
					title="Dismiss"
				>
					<X class="size-3.5" />
				</Button>
			</div>
		{/if}

		{#if worldExists === false}
			<div
				role="status"
				class="flex shrink-0 items-center gap-2 border-b bg-muted/40 px-4 py-2 text-xs text-muted-foreground"
			>
				<Sprout class="size-3.5 shrink-0" />
				<span class="min-w-0 flex-1">
					The world has not been generated yet. Datapacks added now are applied when the server
					first creates {directory.split('/')[0]}, which is the right time for world generation
					packs.
				</span>
			</div>
		{/if}

		{#if uploading && uploadProgress}
			<div class="shrink-0 border-b px-4 py-3">
				<div class="mb-2 flex items-center justify-between">
					<span class="truncate text-sm text-muted-foreground">
						Uploading {currentUploadFilename}
					</span>
					<div class="flex shrink-0 items-center gap-2">
						<span class="tabular text-sm text-muted-foreground">
							{uploadProgress.percentComplete.toFixed(0)}%
						</span>
						<Button
							size="icon"
							variant="ghost"
							class="size-6"
							onclick={cancelCurrentUpload}
							title="Cancel upload"
						>
							<X class="size-4" />
						</Button>
					</div>
				</div>
				<Progress value={uploadProgress.percentComplete} class="h-1.5" />
				<p class="tabular mt-1 text-xs text-muted-foreground">
					{formatBytes(uploadProgress.bytesUploaded)} / {formatBytes(uploadProgress.totalBytes)}
				</p>
			</div>
		{/if}

		{#if loading && datapacks.length === 0}
			<div class="flex min-h-0 flex-1 items-center justify-center">
				<Loader2 class="size-8 animate-spin text-muted-foreground" />
			</div>
		{:else if loadError}
			<div class="flex min-h-0 flex-1 flex-col justify-center">
				<EmptyState icon={TriangleAlert} title="Could not load datapacks" description={loadError}>
					<Button variant="outline" size="sm" onclick={loadDatapacks}>
						<RotateCw class="size-4" />
						Retry
					</Button>
				</EmptyState>
			</div>
		{:else if datapacks.length === 0}
			<div class="flex min-h-0 flex-1 flex-col justify-center">
				<EmptyState
					icon={Boxes}
					title="No datapacks installed"
					description="Datapacks change recipes, loot, advancements, world generation and more without mods, and work on vanilla servers too. Upload a datapack .zip or drag one here."
				>
					<Button onclick={() => fileInput?.click()} disabled={uploading} size="sm">
						<Upload class="size-4" />
						Upload
					</Button>
				</EmptyState>
			</div>
		{:else}
			{#if datapacks.length > 6}
				<div class="shrink-0 border-b px-4 py-2.5">
					<div class="relative max-w-xs">
						<Search
							class="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground"
						/>
						<Input placeholder="Filter datapacks..." class="h-8 pl-8" bind:value={filterText} />
					</div>
				</div>
			{/if}

			{#if visibleDatapacks.length === 0}
				<p class="flex min-h-0 flex-1 items-center justify-center text-sm text-muted-foreground">
					No datapacks match "{filterText}"
				</p>
			{:else}
				<div class="min-h-0 flex-1 overflow-y-auto">
					<div class="divide-y">
						{#each visibleDatapacks as pack (pack.name)}
							<div
								class="group flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-accent/40 {pack.enabled
									? ''
									: 'opacity-60'}"
							>
								<Switch
									checked={pack.enabled}
									disabled={!!toggling[pack.name]}
									onCheckedChange={() => toggleDatapack(pack)}
									title={pack.enabled ? 'Disable' : 'Enable'}
									aria-label="{pack.enabled ? 'Disable' : 'Enable'} {pack.name}"
									class="shrink-0"
								/>

								<div class="min-w-0 flex-1">
									<div class="flex flex-wrap items-center gap-2">
										{#if pack.isDirectory}
											<Folder class="size-3.5 shrink-0 text-muted-foreground" />
										{/if}
										<span class="truncate font-mono text-sm font-medium">{pack.name}</span>
										{#if pack.packFormat > 0}
											<Badge variant="secondary" class="text-xs" title="pack_format in pack.mcmeta">
												format {pack.packFormat}
											</Badge>
										{/if}
										{#if pack.isDirectory}
											<Badge variant="outline" class="text-xs">Folder</Badge>
										{/if}
										{#if !pack.enabled}
											<Badge variant="outline" class="text-xs">Disabled</Badge>
										{/if}
									</div>
									{#if pack.description}
										<p class="mt-0.5 line-clamp-2 text-xs text-muted-foreground">
											{pack.description}
										</p>
									{/if}
									<div
										class="mt-0.5 flex flex-wrap items-center gap-x-3 text-xs text-muted-foreground"
									>
										<span class="tabular shrink-0">{formatBytes(Number(pack.size))}</span>
										{#if pack.modifiedAt}
											<span class="shrink-0">{formatDate(pack.modifiedAt)}</span>
										{/if}
									</div>
								</div>

								<div
									class="flex shrink-0 items-center gap-1 opacity-60 transition-opacity group-hover:opacity-100"
								>
									{#if canDownload}
										<Button
											size="icon"
											variant="ghost"
											class="size-8"
											onclick={() => downloadDatapack(pack)}
											title="Download"
										>
											<Download class="size-4" />
										</Button>
									{/if}
									<Button
										size="icon"
										variant="ghost"
										class="size-8 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
										onclick={() => requestDelete(pack)}
										title="Delete"
									>
										<Trash2 class="size-4" />
									</Button>
								</div>
							</div>
						{/each}
					</div>
				</div>
			{/if}
		{/if}
	</section>
</div>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete {deleteTarget?.name ?? 'datapack'}?"
	description={deleteTarget?.isDirectory
		? 'The datapack folder and everything in it is removed from the world. This cannot be undone.'
		: 'The datapack is removed from the world. This cannot be undone.'}
	confirmLabel="Delete"
	destructive
	onConfirm={confirmDelete}
/>

<ConfirmDialog
	bind:open={replaceOpen}
	title={replaceTitle}
	description={replaceDescription}
	confirmLabel="Replace"
	destructive
	onConfirm={confirmReplace}
/>
