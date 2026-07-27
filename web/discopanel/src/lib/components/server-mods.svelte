<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import { Input } from '$lib/components/ui/input';
	import { Switch } from '$lib/components/ui/switch';
	import { Progress } from '$lib/components/ui/progress';
	import { EmptyState, ConfirmDialog } from '$lib/components/app';
	import { Loader2, Upload, Download, Trash2, Package, Search, X } from '@lucide/svelte';
	import { rpcClient } from '$lib/api/rpc-client';
	import { modsDirectoryFor } from '$lib/stores/loaders';
	import { toast } from 'svelte-sonner';
	import type { Server, Mod } from '$lib/proto/discopanel/v1/storage_pb';
	import { formatBytes } from '$lib/utils';
	import { formatDate } from '$lib/utils/time';
	import { uploadFile, cancelUpload, type UploadProgress } from '$lib/utils/chunked-upload';

	interface Props {
		server: Server;
		active?: boolean;
	}

	let { server, active = false }: Props = $props();

	let mods = $state<Mod[]>([]);
	let loading = $state(true);
	let uploading = $state(false);
	let uploadProgress = $state<UploadProgress | null>(null);
	let currentUploadFilename = $state('');
	let uploadAbortController = $state<AbortController | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);
	let deleteTarget = $state<Mod | null>(null);
	let deleteOpen = $state(false);
	let filterText = $state('');
	let dragActive = $state(false);

	let hasLoaded = false;
	let previousServerId = $state(server.id);

	// Registry decides mods vs plugins, never a loader list
	let modsDirectory = $state('mods');
	$effect(() => {
		modsDirectoryFor(server.modLoader).then((dir) => (modsDirectory = dir));
	});

	// Reset state when server changes
	$effect(() => {
		if (server.id !== previousServerId) {
			previousServerId = server.id;
			mods = [];
			loading = true;
			uploading = false;
			filterText = '';
			hasLoaded = false;
		}
	});

	$effect(() => {
		if (active && !hasLoaded) {
			hasLoaded = true;
			loadMods();
		}
	});

	async function loadMods() {
		try {
			loading = true;
			const response = await rpcClient.mod.listMods({ serverId: server.id });
			mods = response.mods;
		} catch {
			if (canHaveMods()) {
				toast.error('Failed to load mods');
			}
		} finally {
			loading = false;
		}
	}

	async function uploadFiles(fileList: FileList | File[]) {
		const files = Array.from(fileList).filter(
			(f) => f.name.endsWith('.jar') || f.name.endsWith('.zip')
		);
		if (files.length === 0) {
			toast.error('Only .jar and .zip files are supported');
			return;
		}

		uploading = true;
		uploadAbortController = new AbortController();

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

				await rpcClient.mod.importUploadedMod({
					serverId: server.id,
					uploadSessionId: result.sessionId
				});
			}
			toast.success(`Uploaded ${files.length} ${files.length === 1 ? 'file' : 'files'}`);
			await loadMods();
		} catch (error: unknown) {
			if (error instanceof Error && error.message === 'Upload cancelled') {
				toast.info('Upload cancelled');
			} else {
				toast.error('Failed to upload mod');
			}
		} finally {
			uploading = false;
			uploadProgress = null;
			currentUploadFilename = '';
			uploadAbortController = null;
		}
	}

	async function handleFileSelect(event: Event) {
		const input = event.target as HTMLInputElement;
		if (!input.files || input.files.length === 0) return;
		await uploadFiles(input.files);
		input.value = '';
	}

	function handleDrop(e: DragEvent) {
		e.preventDefault();
		dragActive = false;
		if (!canHaveMods() || uploading) return;
		if (e.dataTransfer?.files?.length) {
			uploadFiles(e.dataTransfer.files);
		}
	}

	function cancelCurrentUpload() {
		if (uploadAbortController) {
			uploadAbortController.abort();
		}
		if (uploadProgress?.sessionId) {
			cancelUpload(uploadProgress.sessionId).catch(() => {});
		}
	}

	async function toggleMod(mod: Mod) {
		try {
			await rpcClient.mod.updateMod({
				serverId: server.id,
				modId: mod.id,
				enabled: !mod.enabled
			});
			toast.success(`Mod ${!mod.enabled ? 'enabled' : 'disabled'}`);
			await loadMods();
		} catch {
			toast.error('Failed to toggle mod');
		}
	}

	function requestDelete(mod: Mod) {
		deleteTarget = mod;
		deleteOpen = true;
	}

	async function confirmDelete() {
		if (!deleteTarget) return;
		try {
			await rpcClient.mod.deleteMod({
				serverId: server.id,
				modId: deleteTarget.id
			});
			toast.success('Mod deleted');
			await loadMods();
		} catch {
			toast.error('Failed to delete mod');
		}
	}

	async function downloadMod(mod: Mod) {
		try {
			const response = await rpcClient.file.getFile({
				serverId: server.id,
				path: `${getModsDirectory()}/${mod.fileName}`
			});
			const blob = new Blob([new Uint8Array(response.content)]);
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = mod.fileName;
			a.click();
			URL.revokeObjectURL(url);
		} catch {
			toast.error('Failed to download mod');
		}
	}

	function getModsDirectory(): string {
		return modsDirectory;
	}

	function canHaveMods(): boolean {
		return modsDirectory !== '';
	}

	let dirLabel = $derived(modsDirectory === 'plugins' ? 'Plugins' : 'Mods');
	let enabledCount = $derived(mods.filter((m) => m.enabled).length);

	let visibleMods = $derived.by(() => {
		if (!filterText.trim()) return mods;
		const q = filterText.trim().toLowerCase();
		return mods.filter(
			(m) =>
				m.displayName.toLowerCase().includes(q) ||
				m.fileName.toLowerCase().includes(q) ||
				m.modId.toLowerCase().includes(q)
		);
	});
</script>

<div
	role="region"
	aria-label="{dirLabel} upload area"
	ondragover={(e) => {
		e.preventDefault();
		if (canHaveMods() && !uploading) dragActive = true;
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
				Drop .jar or .zip files to upload
			</div>
		</div>
	{/if}

	<section class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border bg-card">
		<header
			class="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3"
		>
			<div class="min-w-0">
				<h3 class="text-sm font-semibold">{dirLabel}</h3>
				<p class="mt-0.5 text-xs text-muted-foreground">
					{canHaveMods()
						? `Files in ${getModsDirectory()}/. Disabled entries are renamed, not removed.`
						: 'This server type does not support mods'}
				</p>
			</div>
			{#if canHaveMods()}
				<div class="flex shrink-0 items-center gap-2">
					{#if mods.length > 0}
						<span class="tabular text-xs text-muted-foreground">
							{enabledCount}/{mods.length} enabled
						</span>
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
						accept=".jar,.zip"
						onchange={handleFileSelect}
						class="hidden"
					/>
				</div>
			{/if}
		</header>

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

		{#if !canHaveMods()}
			<div class="flex min-h-0 flex-1 flex-col justify-center">
				<EmptyState
					icon={Package}
					title="No mod support"
					description="Vanilla servers run without mods. Switch the server to a modded loader to install some."
				/>
			</div>
		{:else if loading}
			<div class="flex min-h-0 flex-1 items-center justify-center">
				<Loader2 class="size-8 animate-spin text-muted-foreground" />
			</div>
		{:else if mods.length === 0}
			<div class="flex min-h-0 flex-1 flex-col justify-center">
				<EmptyState
					icon={Package}
					title="No {dirLabel.toLowerCase()} installed"
					description="Upload .jar or .zip files, or drag and drop them anywhere here."
				>
					<Button onclick={() => fileInput?.click()} disabled={uploading} size="sm">
						<Upload class="size-4" />
						Upload
					</Button>
				</EmptyState>
			</div>
		{:else}
			{#if mods.length > 6}
				<div class="shrink-0 border-b px-4 py-2.5">
					<div class="relative max-w-xs">
						<Search
							class="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground"
						/>
						<Input
							placeholder="Filter {dirLabel.toLowerCase()}..."
							class="h-8 pl-8"
							bind:value={filterText}
						/>
					</div>
				</div>
			{/if}

			{#if visibleMods.length === 0}
				<p class="flex min-h-0 flex-1 items-center justify-center text-sm text-muted-foreground">
					No {dirLabel.toLowerCase()} match "{filterText}"
				</p>
			{:else}
				<div class="min-h-0 flex-1 overflow-y-auto">
					<div class="divide-y">
						{#each visibleMods as mod (mod.id)}
							<div
								class="group flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-accent/40 {mod.enabled
									? ''
									: 'opacity-60'}"
							>
								<Switch
									checked={mod.enabled}
									onCheckedChange={() => toggleMod(mod)}
									title={mod.enabled ? 'Disable' : 'Enable'}
									class="shrink-0"
								/>

								<div class="min-w-0 flex-1">
									<div class="flex flex-wrap items-center gap-2">
										<span class="truncate text-sm font-medium">{mod.displayName}</span>
										{#if mod.version}
											<Badge variant="secondary" class="text-xs">{mod.version}</Badge>
										{/if}
										{#if !mod.enabled}
											<Badge variant="outline" class="text-xs">Disabled</Badge>
										{/if}
									</div>
									<div
										class="mt-0.5 flex flex-wrap items-center gap-x-3 text-xs text-muted-foreground"
									>
										<span class="truncate font-mono">{mod.fileName}</span>
										<span class="tabular shrink-0">{formatBytes(Number(mod.fileSize))}</span>
										{#if mod.uploadedAt}
											<span class="shrink-0">{formatDate(mod.uploadedAt)}</span>
										{/if}
									</div>
									{#if mod.modId}
										<p class="mt-1 line-clamp-1 text-xs text-muted-foreground">
											{mod.modId}{mod.version ? ` ${mod.version}` : ''}
										</p>
									{/if}
								</div>

								<div
									class="flex shrink-0 items-center gap-1 opacity-60 transition-opacity group-hover:opacity-100"
								>
									<Button
										size="icon"
										variant="ghost"
										class="size-8"
										onclick={() => downloadMod(mod)}
										title="Download"
									>
										<Download class="size-4" />
									</Button>
									<Button
										size="icon"
										variant="ghost"
										class="size-8 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
										onclick={() => requestDelete(mod)}
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
	title="Delete {deleteTarget?.displayName ?? 'mod'}?"
	description="The file is removed from the server. This cannot be undone."
	confirmLabel="Delete"
	destructive
	onConfirm={confirmDelete}
/>
