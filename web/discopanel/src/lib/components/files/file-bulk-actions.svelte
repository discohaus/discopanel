<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Download, Trash2, FolderInput, Archive, Package, X } from '@lucide/svelte';

	interface Props {
		selectedCount: number;
		canExtract: boolean;
		onClear: () => void;
		onDelete: () => void;
		onDownload: () => void;
		onMove: () => void;
		onCompress: () => void;
		onExtract: () => void;
	}

	let {
		selectedCount,
		canExtract,
		onClear,
		onDelete,
		onDownload,
		onMove,
		onCompress,
		onExtract
	}: Props = $props();

	let active = $derived(selectedCount > 0);
</script>

<!-- Fixed height bar prevents layout shift -->
<div
	class="flex h-8 items-center justify-between border-b px-3 transition-colors {active
		? 'bg-primary/5'
		: 'bg-transparent'}"
>
	{#if active}
		<div class="flex items-center gap-2">
			<span class="tabular text-xs font-medium">{selectedCount} selected</span>
			<Button size="sm" variant="ghost" class="h-6 px-2 text-xs" onclick={onClear}>
				<X class="size-3" />
				Clear
			</Button>
		</div>
		<div class="flex items-center gap-0.5">
			<Button
				size="icon"
				variant="ghost"
				class="size-7"
				onclick={onDownload}
				title="Download selected"
			>
				<Download class="size-3.5" />
			</Button>
			<Button size="icon" variant="ghost" class="size-7" onclick={onMove} title="Move selected">
				<FolderInput class="size-3.5" />
			</Button>
			<Button
				size="icon"
				variant="ghost"
				class="size-7"
				onclick={onCompress}
				title="Compress selected"
			>
				<Archive class="size-3.5" />
			</Button>
			{#if canExtract}
				<Button
					size="icon"
					variant="ghost"
					class="size-7"
					onclick={onExtract}
					title="Extract archive"
				>
					<Package class="size-3.5" />
				</Button>
			{/if}
			<Button
				size="icon"
				variant="ghost"
				class="size-7 text-destructive hover:text-destructive"
				onclick={onDelete}
				title="Delete selected"
			>
				<Trash2 class="size-3.5" />
			</Button>
		</div>
	{:else}
		<span class="text-[11px] text-muted-foreground"
			>Ctrl+Click to select &middot; Right-click for options</span
		>
	{/if}
</div>
