<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { FilePlus, FolderPlus, Upload, Search, X } from '@lucide/svelte';

	interface Props {
		filterText: string;
		onNewFile: () => void;
		onNewFolder: () => void;
		onUpload: () => void;
		onFilterChange: (value: string) => void;
	}

	let { filterText, onNewFile, onNewFolder, onUpload, onFilterChange }: Props = $props();

	let showSearch = $state(false);
</script>

<div class="flex items-center justify-between border-b bg-muted/40 px-2 py-1.5">
	<div class="flex items-center gap-0.5">
		<Button size="icon" variant="ghost" class="size-7" onclick={onNewFile} title="New file">
			<FilePlus class="size-3.5" />
		</Button>
		<Button size="icon" variant="ghost" class="size-7" onclick={onNewFolder} title="New folder">
			<FolderPlus class="size-3.5" />
		</Button>
		<Button size="icon" variant="ghost" class="size-7" onclick={onUpload} title="Upload files">
			<Upload class="size-3.5" />
		</Button>
	</div>
	<div class="flex items-center gap-0.5">
		{#if showSearch}
			<div class="flex items-center gap-1">
				<Input
					class="h-7 w-40 text-xs"
					placeholder="Filter files..."
					value={filterText}
					oninput={(e) => onFilterChange((e.target as HTMLInputElement).value)}
					autofocus
				/>
				<Button
					size="icon"
					variant="ghost"
					class="size-7"
					title="Clear filter"
					onclick={() => {
						showSearch = false;
						onFilterChange('');
					}}
				>
					<X class="size-3.5" />
				</Button>
			</div>
		{:else}
			<Button
				size="icon"
				variant="ghost"
				class="size-7"
				onclick={() => (showSearch = true)}
				title="Filter"
			>
				<Search class="size-3.5" />
			</Button>
		{/if}
	</div>
</div>
