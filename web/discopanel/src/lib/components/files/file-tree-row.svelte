<script lang="ts">
	import { Checkbox } from '$lib/components/ui/checkbox';
	import {
		Folder,
		FolderOpen,
		File,
		FileText,
		FileCode,
		Image,
		Archive,
		ChevronRight,
		ChevronDown
	} from '@lucide/svelte';
	import type { FileInfo } from '$lib/proto/discopanel/v1/file_pb';
	import { formatBytes } from '$lib/utils';
	import { formatRelative } from '$lib/utils/time';
	import { archiveExts } from './tree.svelte';

	interface Props {
		file: FileInfo;
		depth: number;
		isExpanded: boolean;
		isSelected: boolean;
		isFocused: boolean;
		isDragOver: boolean;
		hasSelection: boolean;
		onToggleExpand: (path: string) => void;
		onSelect: (file: FileInfo, event: MouseEvent) => void;
		onCheckboxToggle: (file: FileInfo) => void;
		onContextMenu: (file: FileInfo, event: MouseEvent) => void;
		onDragStart: (file: FileInfo, event: DragEvent) => void;
		onDragOver: (file: FileInfo, event: DragEvent) => void;
		onDragLeave: () => void;
		onDrop: (file: FileInfo, event: DragEvent) => void;
	}

	let {
		file,
		depth,
		isExpanded,
		isSelected,
		isFocused,
		isDragOver,
		hasSelection,
		onToggleExpand,
		onSelect,
		onCheckboxToggle,
		onContextMenu,
		onDragStart,
		onDragOver,
		onDragLeave,
		onDrop
	}: Props = $props();

	const textExts = ['txt', 'md', 'json', 'yml', 'yaml', 'toml', 'properties', 'conf', 'cfg', 'log'];
	const codeExts = [
		'js',
		'ts',
		'jsx',
		'tsx',
		'py',
		'java',
		'cpp',
		'c',
		'h',
		'cs',
		'go',
		'rs',
		'php',
		'rb',
		'lua'
	];
	const imageExts = ['png', 'jpg', 'jpeg', 'gif', 'bmp', 'svg', 'webp'];

	function getFileIcon(f: FileInfo) {
		if (f.isDir) return isExpanded ? FolderOpen : Folder;
		const ext = f.name.toLowerCase().split('.').pop() || '';
		if (textExts.includes(ext)) return FileText;
		if (codeExts.includes(ext)) return FileCode;
		if (imageExts.includes(ext)) return Image;
		if (archiveExts.includes(ext)) return Archive;
		return File;
	}

	function formatModified(timestamp: number | bigint): string {
		const ts = Number(timestamp);
		if (!ts) return '';
		return formatRelative(new Date(ts * 1000));
	}

	let Icon = $derived(getFileIcon(file));
	let showCheckbox = $derived(hasSelection || isSelected);
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
	class="group flex h-7 cursor-pointer items-center pr-3 text-xs select-none
		{isSelected ? 'bg-primary/10' : ''}
		{isFocused && !isSelected ? 'bg-accent/60' : ''}
		{isDragOver && file.isDir ? 'bg-primary/15 ring-1 ring-primary/40 ring-inset' : ''}
		hover:bg-accent/40"
	draggable="true"
	data-file-path={file.path}
	onclick={(e) => onSelect(file, e)}
	oncontextmenu={(e) => {
		e.preventDefault();
		onContextMenu(file, e);
	}}
	ondragstart={(e) => onDragStart(file, e)}
	ondragover={(e) => onDragOver(file, e)}
	ondragleave={() => onDragLeave()}
	ondrop={(e) => onDrop(file, e)}
	role="treeitem"
	aria-selected={isSelected}
	aria-expanded={file.isDir ? isExpanded : undefined}
>
	<!-- Checkbox shows on hover or while selecting -->
	<div
		class="flex w-6 shrink-0 items-center justify-center {showCheckbox
			? 'visible'
			: 'invisible group-hover:visible'}"
		onclick={(e) => e.stopPropagation()}
	>
		<Checkbox
			checked={isSelected}
			onCheckedChange={() => onCheckboxToggle(file)}
			class="size-3.5"
		/>
	</div>

	<!-- Indent plus chevron -->
	<div class="flex shrink-0 items-center" style="width: {depth * 16}px"></div>
	<div class="flex w-4 shrink-0 items-center justify-center">
		{#if file.isDir}
			<button
				class="p-0 text-muted-foreground transition-colors hover:text-foreground"
				onclick={(e) => {
					e.stopPropagation();
					onToggleExpand(file.path);
				}}
			>
				{#if isExpanded}
					<ChevronDown class="size-3.5" />
				{:else}
					<ChevronRight class="size-3.5" />
				{/if}
			</button>
		{/if}
	</div>

	<!-- Icon plus name -->
	<div class="flex min-w-0 flex-1 items-center gap-1.5 pl-1">
		<Icon class="size-4 shrink-0 {file.isDir ? 'text-status-sleep' : 'text-muted-foreground'}" />
		<span class="truncate font-mono"
			>{file.name}{#if file.isDir}/{/if}</span
		>
	</div>

	<!-- Size column -->
	<span class="tabular w-20 shrink-0 text-right font-mono text-muted-foreground">
		{#if !file.isDir}
			{formatBytes(Number(file.size))}
		{/if}
	</span>

	<!-- Modified column -->
	<span class="tabular hidden w-20 shrink-0 text-right text-muted-foreground sm:inline-block">
		{formatModified(file.modified)}
	</span>
</div>
