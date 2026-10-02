<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Trash2, X } from '@lucide/svelte';

	interface KVRow {
		key: string;
		value: string;
	}

	let {
		rows = $bindable([]),
		variant = 'card',
		separator = '=',
		keyClass = 'w-56',
		keyPlaceholder = 'key',
		valuePlaceholder = 'value',
		entryLabel = 'entry',
		disabled = false,
		onrowchange
	}: {
		rows?: KVRow[];
		variant?: 'card' | 'compact';
		separator?: string;
		keyClass?: string;
		keyPlaceholder?: string;
		valuePlaceholder?: string;
		entryLabel?: string;
		disabled?: boolean;
		// Fires with the key on key edits only
		onrowchange?: (changedKey?: string) => void;
	} = $props();

	function removeRow(row: KVRow) {
		rows = rows.filter((r) => r !== row);
		onrowchange?.();
	}
</script>

<div class="space-y-2">
	{#each rows as row (row)}
		{#if variant === 'compact'}
			<div class="flex items-center gap-2">
				<Input
					bind:value={row.key}
					onchange={() => onrowchange?.(row.key)}
					placeholder={keyPlaceholder}
					aria-label="{entryLabel} name"
					{disabled}
					class="h-8 flex-1 font-mono text-xs"
				/>
				<span class="text-xs text-muted-foreground">{separator}</span>
				<Input
					bind:value={row.value}
					onchange={() => onrowchange?.()}
					placeholder={valuePlaceholder}
					aria-label="{entryLabel} value"
					{disabled}
					class="h-8 flex-1 font-mono text-xs"
				/>
				<Button
					type="button"
					variant="ghost"
					size="icon"
					onclick={() => removeRow(row)}
					{disabled}
					class="size-8 hover:bg-destructive/10 hover:text-destructive"
				>
					<X class="size-3" />
					<span class="sr-only">Remove {entryLabel}</span>
				</Button>
			</div>
		{:else}
			<div class="flex items-center gap-2 rounded-lg border bg-card p-3">
				<Input
					bind:value={row.key}
					onchange={() => onrowchange?.(row.key)}
					placeholder={keyPlaceholder}
					aria-label="{entryLabel} name"
					{disabled}
					class="{keyClass} font-mono"
				/>
				<span class="text-muted-foreground">{separator}</span>
				<Input
					bind:value={row.value}
					onchange={() => onrowchange?.()}
					placeholder={valuePlaceholder}
					aria-label="{entryLabel} value"
					{disabled}
					class="flex-1 font-mono"
				/>
				{#if !disabled}
					<Button
						variant="ghost"
						size="icon"
						class="size-8 shrink-0 text-muted-foreground hover:text-destructive"
						onclick={() => removeRow(row)}
					>
						<Trash2 class="size-4" />
						<span class="sr-only">Remove {entryLabel}</span>
					</Button>
				{/if}
			</div>
		{/if}
	{/each}
</div>
