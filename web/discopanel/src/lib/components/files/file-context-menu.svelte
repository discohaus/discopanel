<script lang="ts">
	import type { FileInfo } from '$lib/proto/discopanel/v1/file_pb';
	import { isArchiveFile } from './tree.svelte';
	import {
		FileEdit,
		Pencil,
		Copy,
		FolderInput,
		Download,
		FilePlus,
		FolderPlus,
		Upload,
		Archive,
		Package,
		Trash2
	} from '@lucide/svelte';

	interface Props {
		visible: boolean;
		x: number;
		y: number;
		file: FileInfo | null;
		hasSelection: boolean;
		selectedCount: number;
		onClose: () => void;
		onEdit: () => void;
		onRename: () => void;
		onCopy: () => void;
		onMove: () => void;
		onDownload: () => void;
		onNewFile: () => void;
		onNewFolder: () => void;
		onUpload: () => void;
		onCompress: () => void;
		onExtract: () => void;
		onDelete: () => void;
	}

	let {
		visible,
		x,
		y,
		file,
		hasSelection,
		selectedCount,
		onClose,
		onEdit,
		onRename,
		onCopy,
		onMove,
		onDownload,
		onNewFile,
		onNewFolder,
		onUpload,
		onCompress,
		onExtract,
		onDelete
	}: Props = $props();

	function handleAction(action: () => void) {
		action();
		onClose();
	}

	// Clamps position so menu stays on screen
	let menuStyle = $derived.by(() => {
		const maxX = typeof window !== 'undefined' ? window.innerWidth - 200 : x;
		const maxY = typeof window !== 'undefined' ? window.innerHeight - 300 : y;
		return `left: ${Math.min(x, maxX)}px; top: ${Math.min(y, maxY)}px;`;
	});

	const itemClass =
		'flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-left text-xs transition-colors hover:bg-accent hover:text-accent-foreground';
	const dangerClass =
		'flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-left text-xs text-destructive transition-colors hover:bg-destructive/10 hover:text-destructive';
</script>

<svelte:window
	onkeydown={(e) => {
		if (visible && e.key === 'Escape') onClose();
	}}
/>

{#if visible}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		class="fixed inset-0 z-50"
		onclick={onClose}
		oncontextmenu={(e) => {
			e.preventDefault();
			onClose();
		}}
	>
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="fixed z-50 min-w-[180px] rounded-md border bg-popover p-1 text-popover-foreground shadow-md"
			style={menuStyle}
			onclick={(e) => e.stopPropagation()}
		>
			{#if file?.isEditable}
				<button class={itemClass} onclick={() => handleAction(onEdit)}>
					<FileEdit class="size-3.5" />
					Edit
				</button>
			{/if}

			{#if file}
				<button class={itemClass} onclick={() => handleAction(onRename)}>
					<Pencil class="size-3.5" />
					Rename
				</button>
			{/if}

			<button class={itemClass} onclick={() => handleAction(onCopy)}>
				<Copy class="size-3.5" />
				Copy{hasSelection && selectedCount > 1 ? ` (${selectedCount})` : ''}
			</button>

			<button class={itemClass} onclick={() => handleAction(onMove)}>
				<FolderInput class="size-3.5" />
				Move to...{hasSelection && selectedCount > 1 ? ` (${selectedCount})` : ''}
			</button>

			<button class={itemClass} onclick={() => handleAction(onDownload)}>
				<Download class="size-3.5" />
				Download{hasSelection && selectedCount > 1 ? ` (${selectedCount})` : ''}
			</button>

			<div class="my-1 h-px bg-border"></div>

			{#if file?.isDir}
				<button class={itemClass} onclick={() => handleAction(onNewFile)}>
					<FilePlus class="size-3.5" />
					New file
				</button>
				<button class={itemClass} onclick={() => handleAction(onNewFolder)}>
					<FolderPlus class="size-3.5" />
					New folder
				</button>
				<button class={itemClass} onclick={() => handleAction(onUpload)}>
					<Upload class="size-3.5" />
					Upload here
				</button>
				<div class="my-1 h-px bg-border"></div>
			{/if}

			<button class={itemClass} onclick={() => handleAction(onCompress)}>
				<Archive class="size-3.5" />
				Compress{hasSelection && selectedCount > 1 ? ` (${selectedCount})` : ''}
			</button>

			{#if isArchiveFile(file)}
				<button class={itemClass} onclick={() => handleAction(onExtract)}>
					<Package class="size-3.5" />
					Extract
				</button>
			{/if}

			<div class="my-1 h-px bg-border"></div>

			<button class={dangerClass} onclick={() => handleAction(onDelete)}>
				<Trash2 class="size-3.5" />
				Delete{hasSelection && selectedCount > 1 ? ` (${selectedCount})` : ''}
			</button>
		</div>
	</div>
{/if}
