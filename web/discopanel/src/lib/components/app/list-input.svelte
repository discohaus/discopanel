<script lang="ts">
	import { slide } from 'svelte/transition';
	import { Input } from '$lib/components/ui/input';
	import { Button } from '$lib/components/ui/button';
	import { CardStack, CopyButton } from '$lib/components/app';
	import { Plus, X } from '@lucide/svelte';
	import type { Snippet } from 'svelte';

	let {
		items = $bindable([]),
		placeholder = 'Add item...',
		disabled = false,
		inputId = undefined,
		error = '',
		emptyText = 'No items yet',
		copyable = false,
		removeTitle = 'Remove item',
		shown = (item: string) => item,
		normalize = (raw: string) => raw.trim(),
		validate = undefined,
		suggestions = [],
		onchange,
		renderSuggestion = undefined,
		renderCardExtra = undefined
	}: {
		items?: string[];
		placeholder?: string;
		disabled?: boolean;
		inputId?: string;
		error?: string;
		emptyText?: string;
		copyable?: boolean;
		removeTitle?: string;
		shown?: (item: string) => string;
		normalize?: (raw: string) => string;
		validate?: (item: string) => string | boolean | void | null;
		suggestions?: string[];
		onchange?: (items: string[]) => void;
		renderSuggestion?: Snippet<[string]>;
		renderCardExtra?: Snippet<[string]>;
	} = $props();

	let draft = $state('');
	let draftError = $state('');
	let draftInput = $state<HTMLInputElement | null>(null);

	function addItem(raw: string): boolean {
		const norm = normalize(raw);
		if (!norm || items.includes(norm)) return true;
		if (validate) {
			const res = validate(norm);
			if (typeof res === 'string') {
				draftError = res;
				return false;
			} else if (res === false) {
				draftError = `${norm} is invalid`;
				return false;
			}
		}
		items = [...items, norm];
		onchange?.(items);
		return true;
	}

	function commit() {
		const typed = draft.trim();
		if (!typed) return;
		draftError = '';
		const tokens = typed.split(/[\s,]+/).filter(Boolean);

		const kept = tokens.filter((t) => !addItem(t));
		draft = kept.join(' ');
		draftInput?.focus();
	}

	function addSuggestion(item: string) {
		draftError = '';
		if (addItem(item)) draft = '';
	}

	function remove(item: string) {
		items = items.filter((i) => i !== item);
		onchange?.(items);
	}

	function onKeydown(event: KeyboardEvent) {
		if (event.key === 'Enter') {
			event.preventDefault();
			commit();
		}
	}

	console.log(items)
</script>

<div class="space-y-1.5">
	<CardStack
		items={items}
		visible={2}
		slotHeight="2rem"
		gap="0.25rem"
		itemKey={(n: string) => n}
	>
		{#snippet card(item: string)}
			<div
				class="flex h-full items-center gap-2 rounded-md pr-0.5 pl-2.5 transition-colors hover:bg-accent/40"
			>
				<span class="min-w-0 flex-1 truncate font-mono text-xs">{shown(item)}</span>
				{#if renderCardExtra}
					{@render renderCardExtra(item)}
				{/if}
				{#if copyable}
					<CopyButton text={shown(item)} label="Copy address" />
				{/if}
				<Button
					type="button"
					variant="ghost"
					size="icon"
					class="size-7 shrink-0 text-muted-foreground hover:text-destructive"
					onclick={() => remove(item)}
					{disabled}
					title={removeTitle}
				>
					<X class="size-3.5" />
				</Button>
			</div>
		{/snippet}
		{#snippet empty()}
			<div class="flex h-full items-center justify-center text-xs text-muted-foreground">
				{emptyText}
			</div>
		{/snippet}
	</CardStack>

	<div class="flex items-center gap-2">
		<Input
			id={inputId}
			bind:ref={draftInput}
			bind:value={draft}
			{disabled}
			{placeholder}
			autocomplete="off"
			oninput={() => (draftError = '')}
			onkeydown={onKeydown}
			class="h-9 min-w-0 flex-1 font-mono text-xs {draftError || error ? 'border-destructive' : ''}"
		/>
		<Button
			type="button"
			variant="outline"
			class="h-9 shrink-0"
			onclick={commit}
			disabled={disabled || !draft.trim()}
		>
			<Plus class="size-3.5" />
			Add
		</Button>
	</div>

	{#if draftError || error || suggestions.length > 0}
		<div
			transition:slide={{ duration: 150 }}
			class="flex h-6 items-center gap-1 overflow-x-auto whitespace-nowrap"
		>
			{#if draftError || error}
				<p class="truncate text-xs text-destructive">{draftError || error}</p>
			{:else}
				<span class="shrink-0 pr-1 text-[10px] text-muted-foreground">Suggested</span>
				{#each suggestions as item (item)}
					<button
						type="button"
						onclick={() => addSuggestion(item)}
						{disabled}
						class="flex shrink-0 items-center gap-1 rounded-md border bg-background px-1.5 py-px font-mono text-[11px] transition-colors hover:border-primary/50 hover:bg-accent/60"
					>
						{#if renderSuggestion}
							{@render renderSuggestion(item)}
						{:else}
							{item}
						{/if}
					</button>
				{/each}
			{/if}
		</div>
	{/if}
</div>
