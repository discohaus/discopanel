<script lang="ts">
	import { Dialog as DialogPrimitive } from 'bits-ui';
	import {
		DialogContent,
		DialogDescription,
		DialogFooter,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { SvelteSet } from 'svelte/reactivity';
	import { Folder, FolderOpen, ChevronRight, ChevronDown } from '@lucide/svelte';
	import type { FileInfo } from '$lib/proto/discopanel/v1/file_pb';
	import { flattenTree, fileDepth } from './tree.svelte';

	interface Props {
		open?: boolean;
		title: string;
		confirmLabel: string;
		files: FileInfo[];
		onConfirm: (destinationPath: string) => void;
	}

	let { open = $bindable(false), title, confirmLabel, files, onConfirm }: Props = $props();

	let selectedPath = $state('');
	let expanded = new SvelteSet<string>();

	// Resets picker each time dialog opens
	$effect(() => {
		if (open) {
			selectedPath = '';
			expanded.clear();
		}
	});

	function toggleExpand(path: string) {
		if (expanded.has(path)) {
			expanded.delete(path);
		} else {
			expanded.add(path);
		}
	}
</script>

<DialogPrimitive.Root bind:open>
	<DialogContent class="!max-w-md">
		<DialogHeader>
			<DialogTitle>{title}</DialogTitle>
			<DialogDescription>Select a destination folder</DialogDescription>
		</DialogHeader>
		<div class="max-h-60 overflow-auto rounded-md border bg-muted/20 py-1">
			<!-- Root destination -->
			<button
				class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm transition-colors hover:bg-accent/40
					{selectedPath === '' ? 'bg-primary/10 font-medium' : ''}"
				onclick={() => (selectedPath = '')}
			>
				<FolderOpen class="size-4 text-status-sleep" />
				<span class="font-mono">/ (root)</span>
			</button>
			{#each flattenTree(files, { expanded, dirsOnly: true }) as dir (dir.path)}
				<button
					class="flex w-full items-center gap-1 py-1.5 text-left text-sm transition-colors hover:bg-accent/40
						{selectedPath === dir.path ? 'bg-primary/10 font-medium' : ''}"
					style="padding-left: {(fileDepth(dir) + 1) * 16 + 12}px"
					onclick={() => (selectedPath = dir.path)}
				>
					{#if dir.children?.some((c) => c.isDir)}
						<span
							role="button"
							tabindex="-1"
							class="shrink-0 cursor-pointer p-0 text-muted-foreground transition-colors hover:text-foreground"
							onclick={(e) => {
								e.stopPropagation();
								toggleExpand(dir.path);
							}}
						>
							{#if expanded.has(dir.path)}
								<ChevronDown class="size-3" />
							{:else}
								<ChevronRight class="size-3" />
							{/if}
						</span>
					{:else}
						<span class="w-3"></span>
					{/if}
					<Folder class="size-4 shrink-0 text-status-sleep" />
					<span class="truncate font-mono">{dir.name}</span>
				</button>
			{/each}
		</div>
		<DialogFooter>
			<Button variant="outline" onclick={() => (open = false)}>Cancel</Button>
			<Button onclick={() => onConfirm(selectedPath)}>{confirmLabel}</Button>
		</DialogFooter>
	</DialogContent>
</DialogPrimitive.Root>
