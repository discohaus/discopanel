<script lang="ts">
	import type { FileInfo } from '$lib/proto/discopanel/v1/file_pb';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { EmptyState } from '$lib/components/app';
	import { SearchX } from '@lucide/svelte';
	import FileTreeRow from './file-tree-row.svelte';

	interface Props {
		flatFiles: FileInfo[];
		expandedDirs: Set<string>;
		selectedPaths: Set<string>;
		focusedPath: string;
		dragOverPath: string;
		filterText?: string;
		onToggleExpand: (path: string) => void;
		onSelect: (file: FileInfo, event: MouseEvent) => void;
		onCheckboxToggle: (file: FileInfo) => void;
		onSelectAll: () => void;
		onContextMenu: (file: FileInfo, event: MouseEvent) => void;
		onDragStart: (file: FileInfo, event: DragEvent) => void;
		onDragOver: (file: FileInfo, event: DragEvent) => void;
		onDragLeave: () => void;
		onDrop: (file: FileInfo, event: DragEvent) => void;
		onKeydown: (event: KeyboardEvent) => void;
		getDepth: (file: FileInfo) => number;
	}

	let {
		flatFiles,
		expandedDirs,
		selectedPaths,
		focusedPath,
		dragOverPath,
		filterText = '',
		onToggleExpand,
		onSelect,
		onCheckboxToggle,
		onSelectAll,
		onContextMenu,
		onDragStart,
		onDragOver,
		onDragLeave,
		onDrop,
		onKeydown,
		getDepth
	}: Props = $props();

	let hasSelection = $derived(selectedPaths.size > 0);
	let allSelected = $derived(flatFiles.length > 0 && selectedPaths.size === flatFiles.length);
</script>

<div
	class="min-h-0 flex-1 overflow-auto focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none focus-visible:ring-inset"
	tabindex="0"
	role="tree"
	onkeydown={onKeydown}
>
	<!-- Column header mirrors row layout exactly -->
	<div class="sticky top-0 z-10 flex h-[26px] items-center border-b bg-card pr-3">
		<!-- Select all column -->
		<div
			class="flex w-6 shrink-0 items-center justify-center {hasSelection ? 'visible' : 'invisible'}"
		>
			<Checkbox checked={allSelected} onCheckedChange={onSelectAll} class="size-3.5" />
		</div>
		<!-- Chevron placeholder -->
		<div class="w-4 shrink-0"></div>
		<div class="stat-label flex-1 pl-1">Name</div>
		<span class="stat-label w-16 shrink-0 text-right">Size</span>
		<span class="stat-label hidden w-20 shrink-0 text-right sm:inline-block">Modified</span>
	</div>

	<!-- File rows -->
	{#each flatFiles as file (file.path)}
		<FileTreeRow
			{file}
			depth={getDepth(file)}
			isExpanded={expandedDirs.has(file.path)}
			isSelected={selectedPaths.has(file.path)}
			isFocused={focusedPath === file.path}
			isDragOver={dragOverPath === file.path}
			{hasSelection}
			{onToggleExpand}
			{onSelect}
			{onCheckboxToggle}
			{onContextMenu}
			{onDragStart}
			{onDragOver}
			{onDragLeave}
			{onDrop}
		/>
	{/each}

	{#if flatFiles.length === 0}
		<EmptyState
			icon={SearchX}
			title={filterText ? `No matches for "${filterText}"` : 'No files found'}
			description={filterText ? 'Try a different filter.' : ''}
			class="py-10"
		/>
	{/if}
</div>
