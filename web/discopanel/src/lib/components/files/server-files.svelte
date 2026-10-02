<script lang="ts">
	import { onDestroy } from 'svelte';
	import { Progress } from '$lib/components/ui/progress';
	import { Button } from '$lib/components/ui/button';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { ConfirmDialog, EmptyState } from '$lib/components/app';
	import { Loader2, Folder, Upload, X } from '@lucide/svelte';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { registerRefresh } from '$lib/stores/refresh';
	import { notify } from '$lib/stores/activity.svelte';
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import type { FileInfo } from '$lib/proto/discopanel/v1/file_pb';
	import { ExtractionState } from '$lib/proto/discopanel/v1/file_pb';
	import { formatBytes } from '$lib/utils';
	import { uploadFile, cancelUpload, type UploadProgress } from '$lib/utils/chunked-upload';
	import FileEditorDialog from './file-editor-dialog.svelte';
	import FileToolbar from './file-toolbar.svelte';
	import FileBulkActions from './file-bulk-actions.svelte';
	import FileTree from './file-tree.svelte';
	import FileContextMenu from './file-context-menu.svelte';
	import FileMoveDialog from './file-move-dialog.svelte';
	import FileNameDialog from './file-name-dialog.svelte';
	import {
		FileTreeState,
		flattenTree,
		collectFiles,
		findFile,
		fileDepth,
		isArchiveFile
	} from './tree.svelte';
	import { SvelteSet } from 'svelte/reactivity';

	interface Props {
		server: Server;
		active?: boolean;
	}

	let { server, active = false }: Props = $props();

	// --- File state ---
	const tree = new FileTreeState(server.id);

	// --- Upload state ---
	let uploading = $state(false);
	let uploadProgress = $state<UploadProgress | null>(null);
	let currentUploadFilename = $state('');
	let uploadAbortController = $state<AbortController | null>(null);

	// --- Extraction state ---
	let extracting = $state(false);
	let extractionFilesExtracted = $state(0);
	let extractionFilename = $state('');
	let extractionPollId: ReturnType<typeof setInterval> | null = null;

	// --- Tree state ---
	let filterText = $state('');

	// --- Selection state ---
	let selectedPaths = $state<Set<string>>(new Set());
	let focusedPath = $state('');
	let lastClickedPath = $state('');

	// --- Context menu state ---
	let contextMenuVisible = $state(false);
	let contextMenuX = $state(0);
	let contextMenuY = $state(0);
	let contextMenuFile = $state<FileInfo | null>(null);

	// --- Drag and drop state ---
	let dragOverPath = $state('');

	// --- Dialog state ---
	let editingFile = $state<FileInfo | null>(null);
	let showEditor = $state(false);
	let showNewFileDialog = $state(false);
	let showNewFolderDialog = $state(false);
	let showRenameDialog = $state(false);
	let showMoveDialog = $state(false);
	let showCopyDialog = $state(false);
	let dialogTargetPath = $state('');
	let newItemName = $state('');
	let renamingItem = $state<FileInfo | null>(null);

	// --- Delete confirmation state ---
	let confirmDeleteOpen = $state(false);
	let deleteTarget = $state<FileInfo | null>(null);
	let confirmBulkDeleteOpen = $state(false);
	let bulkDeletePaths = $state<string[]>([]);

	let hasLoaded = false;
	let previousServerId: string;

	// --- Derived ---
	let flatFiles = $derived(
		flattenTree(tree.files, { expanded: tree.expanded, filter: filterText })
	);
	let selectedFiles = $derived(collectFiles(tree.files, selectedPaths));
	let canExtractSelection = $derived(selectedFiles.length === 1 && isArchiveFile(selectedFiles[0]));

	// --- Lifecycle ---
	$effect(() => {
		if (server.id !== previousServerId) {
			previousServerId = server.id;
			// Stops in flight uploads targeting the old server
			uploadAbortController?.abort();
			// Stops extraction polling aimed at the old server
			stopExtractionPoll();
			extracting = false;
			uploading = false;
			tree.reset(server.id);
			selectedPaths = new SvelteSet();
			focusedPath = '';
			hasLoaded = false;
			if (active) {
				tree.load();
				hasLoaded = true;
			}
		}
	});

	$effect(() => {
		if (active && !hasLoaded) {
			tree.load();
			hasLoaded = true;
		}
	});

	$effect(() => {
		if (!active) return;
		return registerRefresh(tree.load);
	});

	onDestroy(() => {
		stopExtractionPoll();
	});

	function itemsLabel(count: number): string {
		return count === 1 ? '1 item' : `${count} items`;
	}

	// Blocks moving a folder into itself
	function violatesNesting(paths: string[], destinationPath: string): boolean {
		return paths.some((p) => destinationPath === p || destinationPath.startsWith(p + '/'));
	}

	// --- Selection logic ---
	function handleSelect(file: FileInfo, event: MouseEvent) {
		if (event.ctrlKey || event.metaKey) {
			// Toggles membership
			const next = new SvelteSet(selectedPaths);
			if (next.has(file.path)) {
				next.delete(file.path);
			} else {
				next.add(file.path);
			}
			selectedPaths = next;
			lastClickedPath = file.path;
		} else if (event.shiftKey && lastClickedPath) {
			// Selects range over visible rows
			const startIdx = flatFiles.findIndex((f) => f.path === lastClickedPath);
			const endIdx = flatFiles.findIndex((f) => f.path === file.path);
			if (startIdx !== -1 && endIdx !== -1) {
				const [lo, hi] = startIdx < endIdx ? [startIdx, endIdx] : [endIdx, startIdx];
				const next = new SvelteSet(selectedPaths);
				for (let i = lo; i <= hi; i++) {
					next.add(flatFiles[i].path);
				}
				selectedPaths = next;
			}
		} else {
			// Plain click navigates dirs or opens editor
			if (file.isDir) {
				tree.toggle(file.path);
			} else if (file.isEditable) {
				editFile(file);
			}
			selectedPaths = new SvelteSet();
			lastClickedPath = file.path;
		}
		focusedPath = file.path;
	}

	function handleCheckboxToggle(file: FileInfo) {
		const next = new SvelteSet(selectedPaths);
		if (next.has(file.path)) {
			next.delete(file.path);
		} else {
			next.add(file.path);
		}
		selectedPaths = next;
		lastClickedPath = file.path;
	}

	function handleSelectAll() {
		if (selectedPaths.size === flatFiles.length) {
			selectedPaths = new SvelteSet();
		} else {
			selectedPaths = new SvelteSet(flatFiles.map((f) => f.path));
		}
	}

	function clearSelection() {
		selectedPaths = new SvelteSet();
	}

	// --- Keyboard navigation ---
	function scrollFocusedIntoView() {
		queueMicrotask(() => {
			if (!focusedPath) return;
			const row = document.querySelector(`[data-file-path="${CSS.escape(focusedPath)}"]`);
			row?.scrollIntoView({ block: 'nearest' });
		});
	}

	function handleKeydown(event: KeyboardEvent) {
		const idx = flatFiles.findIndex((f) => f.path === focusedPath);

		if (event.key === 'ArrowDown') {
			event.preventDefault();
			if (idx < flatFiles.length - 1) {
				focusedPath = flatFiles[idx + 1].path;
				scrollFocusedIntoView();
			}
		} else if (event.key === 'ArrowUp') {
			event.preventDefault();
			if (idx > 0) {
				focusedPath = flatFiles[idx - 1].path;
				scrollFocusedIntoView();
			}
		} else if (event.key === 'ArrowRight') {
			event.preventDefault();
			const file = flatFiles[idx];
			if (file?.isDir && !tree.expanded.has(file.path)) {
				tree.toggle(file.path);
			}
		} else if (event.key === 'ArrowLeft') {
			event.preventDefault();
			const file = flatFiles[idx];
			if (file?.isDir && tree.expanded.has(file.path)) {
				tree.toggle(file.path);
			}
		} else if (event.key === 'Enter') {
			event.preventDefault();
			const file = flatFiles[idx];
			if (file?.isDir) tree.toggle(file.path);
			else if (file?.isEditable) editFile(file);
		} else if (event.key === ' ') {
			event.preventDefault();
			const file = flatFiles[idx];
			if (file) handleCheckboxToggle(file);
		} else if (event.key === 'a' && (event.ctrlKey || event.metaKey)) {
			event.preventDefault();
			handleSelectAll();
		} else if (event.key === 'Delete' || event.key === 'Backspace') {
			if (selectedPaths.size > 0) {
				event.preventDefault();
				bulkDelete();
			}
		} else if (event.key === 'Escape') {
			if (selectedPaths.size > 0) {
				event.preventDefault();
				clearSelection();
			}
		}
	}

	// --- Context menu ---
	function handleContextMenu(file: FileInfo, event: MouseEvent) {
		contextMenuFile = file;
		contextMenuX = event.clientX;
		contextMenuY = event.clientY;
		contextMenuVisible = true;
	}

	// --- Drag and drop ---
	function handleDragStart(file: FileInfo, event: DragEvent) {
		if (!event.dataTransfer) return;
		// Drags whole selection when row belongs to it
		let paths: string[];
		if (selectedPaths.has(file.path)) {
			paths = Array.from(selectedPaths);
		} else {
			paths = [file.path];
		}
		event.dataTransfer.setData('text/plain', JSON.stringify(paths));
		event.dataTransfer.effectAllowed = 'copyMove';
	}

	function handleDragOver(file: FileInfo, event: DragEvent) {
		if (!event.dataTransfer || !file.isDir) return;
		event.preventDefault();
		event.dataTransfer.dropEffect = event.ctrlKey ? 'copy' : 'move';
		dragOverPath = file.path;
	}

	function handleDragLeave() {
		dragOverPath = '';
	}

	async function handleDrop(file: FileInfo, event: DragEvent) {
		event.preventDefault();
		dragOverPath = '';
		if (!event.dataTransfer || !file.isDir) return;

		const data = event.dataTransfer.getData('text/plain');
		let paths: string[];
		try {
			paths = JSON.parse(data);
		} catch {
			return;
		}

		// Guards against dropping into self or descendants
		for (const p of paths) {
			if (file.path === p || file.path.startsWith(p + '/')) {
				notify.error('Cannot move a folder into itself');
				return;
			}
		}

		const isCopy = event.ctrlKey;

		try {
			for (const sourcePath of paths) {
				const fileName = sourcePath.split('/').pop() || '';
				const destPath = file.path + '/' + fileName;
				if (isCopy) {
					await rpcClient.file.copyFile({
						serverId: server.id,
						sourcePath,
						destinationPath: destPath
					});
				} else {
					await rpcClient.file.moveFile({
						serverId: server.id,
						sourcePath,
						destinationPath: destPath
					});
				}
			}
			notify.success(`${isCopy ? 'Copied' : 'Moved'} ${itemsLabel(paths.length)}`);
			await tree.load();
		} catch {
			notify.error(`Failed to ${isCopy ? 'copy' : 'move'} files`);
		}
	}

	// --- File operations ---
	function editFile(file: FileInfo) {
		if (file.isDir || !file.isEditable) return;
		editingFile = file;
		showEditor = true;
	}

	async function downloadFile(file: FileInfo) {
		try {
			if (file.isDir) {
				await downloadArchive([file.path]);
			} else {
				const response = await rpcClient.file.initFileDownload({
					serverId: server.id,
					path: file.path
				});
				triggerStreamDownload(response.sessionId, response.filename);
			}
		} catch {
			notify.error('Failed to download');
		}
	}

	async function downloadArchive(paths: string[]) {
		const response = await rpcClient.file.downloadArchive({
			serverId: server.id,
			paths
		});
		triggerStreamDownload(response.sessionId, response.filename);
	}

	function triggerStreamDownload(sessionId: string, filename: string) {
		const a = document.createElement('a');
		a.href = `/api/v1/download/${sessionId}`;
		a.download = filename;
		a.click();
	}

	function deleteFile(file: FileInfo) {
		deleteTarget = file;
		confirmDeleteOpen = true;
	}

	async function performDelete() {
		if (!deleteTarget) return;
		try {
			await rpcClient.file.deleteFile({
				serverId: server.id,
				path: deleteTarget.path
			});
			notify.success('Deleted');
			await tree.load();
		} catch {
			notify.error('Failed to delete');
		}
	}

	function renameFile(file: FileInfo) {
		renamingItem = file;
		newItemName = file.name;
		showRenameDialog = true;
	}

	async function confirmRename() {
		if (!renamingItem || !newItemName.trim() || newItemName === renamingItem.name) {
			showRenameDialog = false;
			return;
		}
		try {
			const oldPath = renamingItem.path;
			await rpcClient.file.renameFile({
				serverId: server.id,
				path: oldPath,
				newName: newItemName
			});
			// Remaps selection and focus onto the new path
			const parent = oldPath.includes('/') ? oldPath.slice(0, oldPath.lastIndexOf('/')) : '';
			const newPath = parent ? `${parent}/${newItemName}` : newItemName;
			if (selectedPaths.has(oldPath)) {
				const next = new SvelteSet(selectedPaths);
				next.delete(oldPath);
				next.add(newPath);
				selectedPaths = next;
			}
			if (focusedPath === oldPath) focusedPath = newPath;
			if (lastClickedPath === oldPath) lastClickedPath = newPath;
			notify.success(`Renamed to ${newItemName}`);
			showRenameDialog = false;
			await tree.load();
		} catch (error) {
			const msg = error instanceof Error ? error.message : 'Failed to rename';
			notify.error(msg);
		}
	}

	async function createNewFile() {
		if (!newItemName.trim()) return;
		const fullPath = dialogTargetPath ? `${dialogTargetPath}/${newItemName}` : newItemName;
		// Refuses names that would truncate existing files
		if (findFile(tree.files, fullPath)) {
			notify.error(`"${newItemName}" already exists here`);
			return;
		}
		try {
			await rpcClient.file.updateFile({
				serverId: server.id,
				path: fullPath,
				content: new Uint8Array()
			});
			notify.success(`Created file: ${newItemName}`);
			showNewFileDialog = false;
			await tree.load();
		} catch {
			notify.error('Failed to create file');
		}
	}

	async function createNewFolder() {
		if (!newItemName.trim()) return;
		const fullPath = dialogTargetPath ? `${dialogTargetPath}/${newItemName}` : newItemName;
		if (findFile(tree.files, fullPath)) {
			notify.error(`"${newItemName}" already exists here`);
			return;
		}
		try {
			await rpcClient.file.createFolder({
				serverId: server.id,
				path: fullPath
			});
			notify.success(`Created folder: ${newItemName}`);
			showNewFolderDialog = false;
			await tree.load();
		} catch {
			notify.error('Failed to create folder');
		}
	}

	// --- Bulk operations ---
	function bulkDelete() {
		if (selectedPaths.size === 0) return;
		bulkDeletePaths = Array.from(selectedPaths);
		confirmBulkDeleteOpen = true;
	}

	async function performBulkDelete() {
		const paths = bulkDeletePaths;
		if (paths.length === 0) return;
		try {
			await rpcClient.file.deleteFile({
				serverId: server.id,
				path: '',
				paths
			});
			notify.success(`Deleted ${itemsLabel(paths.length)}`);
			selectedPaths = new SvelteSet();
			await tree.load();
		} catch {
			notify.error('Failed to delete items');
		}
	}

	async function bulkDownload() {
		const paths = Array.from(selectedPaths);
		if (paths.length === 0) return;
		// Single plain file downloads directly
		if (paths.length === 1) {
			const file = selectedFiles[0];
			if (file && !file.isDir) {
				await downloadFile(file);
				return;
			}
		}
		try {
			await downloadArchive(paths);
		} catch {
			notify.error('Failed to download');
		}
	}

	function bulkMove() {
		if (selectedPaths.size === 0) return;
		showMoveDialog = true;
	}

	async function bulkCompress() {
		const paths = Array.from(selectedPaths);
		if (paths.length === 0) return;
		try {
			const result = await rpcClient.file.createArchive({
				serverId: server.id,
				paths,
				destinationPath: '',
				archiveName: ''
			});
			notify.success(`Archive created: ${result.filesArchived} files`);
			await tree.load();
		} catch {
			notify.error('Failed to create archive');
		}
	}

	async function confirmMove(destinationPath: string) {
		const paths = Array.from(selectedPaths);
		if (violatesNesting(paths, destinationPath)) {
			notify.error('Cannot move a folder into itself');
			return;
		}
		showMoveDialog = false;
		try {
			for (const sourcePath of paths) {
				const fileName = sourcePath.split('/').pop() || '';
				const dest = destinationPath ? `${destinationPath}/${fileName}` : fileName;
				await rpcClient.file.moveFile({
					serverId: server.id,
					sourcePath,
					destinationPath: dest
				});
			}
			notify.success(`Moved ${itemsLabel(paths.length)}`);
			selectedPaths = new SvelteSet();
			await tree.load();
		} catch {
			notify.error('Failed to move items');
		}
	}

	async function confirmCopy(destinationPath: string) {
		const paths = Array.from(selectedPaths);
		if (violatesNesting(paths, destinationPath)) {
			notify.error('Cannot copy a folder into itself');
			return;
		}
		showCopyDialog = false;
		try {
			for (const sourcePath of paths) {
				const fileName = sourcePath.split('/').pop() || '';
				const dest = destinationPath ? `${destinationPath}/${fileName}` : fileName;
				await rpcClient.file.copyFile({
					serverId: server.id,
					sourcePath,
					destinationPath: dest
				});
			}
			notify.success(`Copied ${itemsLabel(paths.length)}`);
			selectedPaths = new SvelteSet();
			await tree.load();
		} catch {
			notify.error('Failed to copy items');
		}
	}

	function stopExtractionPoll() {
		if (extractionPollId !== null) {
			clearInterval(extractionPollId);
			extractionPollId = null;
		}
	}

	async function extractArchive(file?: FileInfo) {
		const target =
			file || contextMenuFile || (selectedFiles.length === 1 ? selectedFiles[0] : null);
		if (!target || extracting) return;
		try {
			extracting = true;
			extractionFilename = target.name;
			extractionFilesExtracted = 0;

			const { operationId } = await rpcClient.file.extractArchive({
				serverId: server.id,
				path: target.path
			});

			// Polls progress until done
			extractionPollId = setInterval(async () => {
				try {
					const status = await rpcClient.file.getExtractionStatus(
						{ operationId, serverId: server.id },
						silentCallOptions
					);
					extractionFilesExtracted = status.filesExtracted;

					if (status.state === ExtractionState.COMPLETED) {
						stopExtractionPoll();
						extracting = false;
						notify.success(`Extracted ${status.filesExtracted} files`);
						await tree.load();
					} else if (status.state === ExtractionState.FAILED) {
						stopExtractionPoll();
						extracting = false;
						notify.error(status.error || 'Extraction failed');
					}
				} catch {
					stopExtractionPoll();
					extracting = false;
					notify.error('Lost connection to extraction');
				}
			}, 2000);
		} catch (error) {
			extracting = false;
			const msg = error instanceof Error ? error.message : 'Failed to start extraction';
			notify.error(msg);
		}
	}

	// Context actions, picks selection or the right-clicked file
	function ctxPaths(): string[] {
		if (selectedPaths.size > 0) return Array.from(selectedPaths);
		if (contextMenuFile) return [contextMenuFile.path];
		return [];
	}

	function ctxEdit() {
		if (contextMenuFile) editFile(contextMenuFile);
	}
	function ctxRename() {
		if (contextMenuFile) renameFile(contextMenuFile);
	}
	function ctxCopy() {
		const paths = ctxPaths();
		if (paths.length === 0) return;
		// Seeds selection so copy dialog works
		selectedPaths = new SvelteSet(paths);
		showCopyDialog = true;
	}
	function ctxMove() {
		const paths = ctxPaths();
		if (paths.length === 0) return;
		selectedPaths = new SvelteSet(paths);
		showMoveDialog = true;
	}
	function ctxDownload() {
		if (selectedPaths.size > 0) {
			bulkDownload();
		} else if (contextMenuFile) {
			downloadFile(contextMenuFile);
		}
	}
	function ctxNewFile() {
		dialogTargetPath = contextMenuFile?.isDir ? contextMenuFile.path : '';
		newItemName = '';
		showNewFileDialog = true;
	}
	function ctxNewFolder() {
		dialogTargetPath = contextMenuFile?.isDir ? contextMenuFile.path : '';
		newItemName = '';
		showNewFolderDialog = true;
	}
	function ctxUpload() {
		triggerUpload(contextMenuFile?.isDir ? contextMenuFile.path : '');
	}
	function ctxCompress() {
		const paths = ctxPaths();
		if (paths.length === 0) return;
		selectedPaths = new SvelteSet(paths);
		bulkCompress();
	}
	function ctxExtract() {
		extractArchive();
	}
	function ctxDelete() {
		if (selectedPaths.size > 0) {
			bulkDelete();
		} else if (contextMenuFile) {
			deleteFile(contextMenuFile);
		}
	}

	// --- Upload ---
	function triggerUpload(path: string = '') {
		const input = document.createElement('input');
		input.type = 'file';
		input.multiple = true;
		input.onchange = (e) => handleFileSelect(e, path);
		input.click();
	}

	async function handleFileSelect(event: Event, path: string) {
		const input = event.target as HTMLInputElement;
		const fileList = input.files;
		if (!fileList || fileList.length === 0) return;

		uploading = true;
		uploadAbortController = new AbortController();

		try {
			for (const file of Array.from(fileList)) {
				currentUploadFilename = file.name;
				uploadProgress = null;

				const result = await uploadFile(file, {
					onProgress: (progress) => {
						uploadProgress = progress;
					},
					signal: uploadAbortController.signal
				});

				await rpcClient.file.saveUploadedFile({
					serverId: server.id,
					uploadSessionId: result.sessionId,
					destinationPath: path,
					filename: file.name
				});
			}
			notify.success(`Uploaded ${fileList.length} file(s)`);
			await tree.load();
		} catch (error) {
			if (error instanceof Error && error.message === 'Upload cancelled') {
				notify.info('Upload cancelled');
			} else {
				notify.error('Failed to upload files');
			}
		} finally {
			uploading = false;
			uploadProgress = null;
			currentUploadFilename = '';
			uploadAbortController = null;
			input.value = '';
		}
	}

	function cancelCurrentUpload() {
		if (uploadAbortController) uploadAbortController.abort();
		if (uploadProgress?.sessionId) cancelUpload(uploadProgress.sessionId).catch(() => {});
	}
</script>

<div class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border bg-card">
	<!-- Toolbar -->
	<FileToolbar
		{filterText}
		onNewFile={() => {
			dialogTargetPath = '';
			newItemName = '';
			showNewFileDialog = true;
		}}
		onNewFolder={() => {
			dialogTargetPath = '';
			newItemName = '';
			showNewFolderDialog = true;
		}}
		onUpload={() => triggerUpload('')}
		onFilterChange={(v) => (filterText = v)}
	/>

	<!-- Upload progress -->
	{#if uploading && uploadProgress}
		<div class="border-b bg-muted/20 px-3 py-2">
			<div class="mb-1 flex items-center justify-between gap-2">
				<span class="truncate font-mono text-xs text-muted-foreground">
					Uploading {currentUploadFilename}
				</span>
				<div class="flex shrink-0 items-center gap-2">
					<span class="tabular text-xs text-muted-foreground">
						{uploadProgress.percentComplete.toFixed(0)}%
					</span>
					<Button
						size="icon"
						variant="ghost"
						class="size-5"
						onclick={cancelCurrentUpload}
						title="Cancel"
					>
						<X class="size-3" />
					</Button>
				</div>
			</div>
			<Progress value={uploadProgress.percentComplete} class="h-1.5" />
			<p class="tabular mt-1 text-[11px] text-muted-foreground">
				{formatBytes(uploadProgress.bytesUploaded)} / {formatBytes(uploadProgress.totalBytes)}
			</p>
		</div>
	{/if}

	<!-- Extraction progress -->
	{#if extracting}
		<div class="border-b bg-muted/20 px-3 py-2">
			<div class="mb-1 flex items-center justify-between gap-2">
				<span class="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
					<Loader2 class="size-3 shrink-0 animate-spin" />
					<span class="truncate font-mono">Extracting {extractionFilename}</span>
				</span>
				<span class="tabular shrink-0 text-xs text-muted-foreground">
					{extractionFilesExtracted} files
				</span>
			</div>
			<Progress value={100} class="h-1.5 animate-pulse" />
		</div>
	{/if}

	<!-- Bulk actions bar -->
	<FileBulkActions
		selectedCount={selectedPaths.size}
		canExtract={canExtractSelection}
		onClear={clearSelection}
		onDelete={bulkDelete}
		onDownload={bulkDownload}
		onMove={bulkMove}
		onCompress={bulkCompress}
		onExtract={() => extractArchive()}
	/>

	{#if tree.loading}
		<div class="flex-1 space-y-1.5 p-3">
			{#each Array(8) as _, i (i)}
				<Skeleton class="h-6" />
			{/each}
		</div>
	{:else if tree.files.length === 0}
		<EmptyState
			icon={Folder}
			title="No files found"
			description="Upload files to get started."
			class="flex-1"
		>
			<Button variant="outline" size="sm" onclick={() => triggerUpload('')}>
				<Upload class="size-4" />
				Upload files
			</Button>
		</EmptyState>
	{:else}
		<!-- File tree -->
		<FileTree
			{flatFiles}
			expandedDirs={tree.expanded}
			{selectedPaths}
			{focusedPath}
			{filterText}
			{dragOverPath}
			onToggleExpand={tree.toggle}
			onSelect={handleSelect}
			onCheckboxToggle={handleCheckboxToggle}
			onSelectAll={handleSelectAll}
			onContextMenu={handleContextMenu}
			onDragStart={handleDragStart}
			onDragOver={handleDragOver}
			onDragLeave={handleDragLeave}
			onDrop={handleDrop}
			onKeydown={handleKeydown}
			getDepth={fileDepth}
		/>
	{/if}

	<!-- Status bar -->
	<div
		class="flex items-center justify-between border-t bg-muted/40 px-3 py-1 text-[11px] text-muted-foreground"
	>
		<span class="tabular">{flatFiles.length} items</span>
		{#if selectedPaths.size > 0}
			<span class="tabular">{selectedPaths.size} selected</span>
		{/if}
	</div>
</div>

<!-- Context menu -->
<FileContextMenu
	visible={contextMenuVisible}
	x={contextMenuX}
	y={contextMenuY}
	file={contextMenuFile}
	hasSelection={selectedPaths.size > 0}
	selectedCount={selectedPaths.size}
	onClose={() => (contextMenuVisible = false)}
	onEdit={ctxEdit}
	onRename={ctxRename}
	onCopy={ctxCopy}
	onMove={ctxMove}
	onDownload={ctxDownload}
	onNewFile={ctxNewFile}
	onNewFolder={ctxNewFolder}
	onUpload={ctxUpload}
	onCompress={ctxCompress}
	onExtract={ctxExtract}
	onDelete={ctxDelete}
/>

<!-- File editor -->
<FileEditorDialog
	serverId={server.id}
	file={editingFile}
	open={showEditor}
	onClose={() => {
		showEditor = false;
		editingFile = null;
	}}
	onSave={async () => {
		await tree.load();
	}}
/>

<!-- New file dialog -->
<FileNameDialog
	bind:open={showNewFileDialog}
	id="new-file-name"
	title="Create new file"
	description="Enter a name for the new file in"
	subject={dialogTargetPath || 'root'}
	label="File name"
	placeholder="example.txt"
	confirmLabel="Create"
	bind:value={newItemName}
	onConfirm={createNewFile}
/>

<!-- New folder dialog -->
<FileNameDialog
	bind:open={showNewFolderDialog}
	id="new-folder-name"
	title="Create new folder"
	description="Enter a name for the new folder in"
	subject={dialogTargetPath || 'root'}
	label="Folder name"
	placeholder="new-folder"
	confirmLabel="Create"
	bind:value={newItemName}
	onConfirm={createNewFolder}
/>

<!-- Rename dialog -->
<FileNameDialog
	bind:open={showRenameDialog}
	id="rename-item"
	title="Rename {renamingItem?.isDir ? 'folder' : 'file'}"
	description="Enter a new name for"
	subject={renamingItem?.name ?? ''}
	label="New name"
	placeholder={renamingItem?.name ?? ''}
	confirmLabel="Rename"
	bind:value={newItemName}
	onConfirm={confirmRename}
/>

<!-- Move dialog -->
<FileMoveDialog
	bind:open={showMoveDialog}
	title="Move {itemsLabel(selectedPaths.size)}"
	confirmLabel="Move here"
	files={tree.files}
	onConfirm={confirmMove}
/>

<!-- Copy dialog -->
<FileMoveDialog
	bind:open={showCopyDialog}
	title="Copy {itemsLabel(selectedPaths.size)}"
	confirmLabel="Copy here"
	files={tree.files}
	onConfirm={confirmCopy}
/>

<!-- Delete confirmation -->
<ConfirmDialog
	bind:open={confirmDeleteOpen}
	title={`Delete "${deleteTarget?.name ?? ''}"?`}
	description={deleteTarget?.isDir
		? 'The folder and all its contents will be permanently deleted.'
		: 'The file will be permanently deleted.'}
	confirmLabel="Delete"
	destructive
	onConfirm={performDelete}
/>

<!-- Bulk delete confirmation -->
<ConfirmDialog
	bind:open={confirmBulkDeleteOpen}
	title="Delete {itemsLabel(bulkDeletePaths.length)}?"
	description="The selected files and folders will be permanently deleted."
	confirmLabel="Delete"
	destructive
	onConfirm={performBulkDelete}
/>
