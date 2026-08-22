<script lang="ts">
	import type { HTMLInputAttributes } from 'svelte/elements';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { FolderSearch } from '@lucide/svelte';
	import PathPickerDialog from '$lib/components/files/path-picker-dialog.svelte';
	import type { RootsInput } from '$lib/components/files/picker-roots';
	import { cn } from '$lib/utils';

	type Props = Omit<HTMLInputAttributes, 'type' | 'files'> & {
		id: string;
		label?: string;
		labelClass?: string;
		hint?: string;
		pickerTitle?: string;
		pickerDescription?: string;
		mode?: 'dir' | 'file' | 'any';
		roots?: RootsInput;
		onSelect?: (picked: string) => void;
		onValueChange?: (v: string) => void;
	};

	let {
		id,
		label = '',
		labelClass = '',
		hint = '',
		pickerTitle = 'Select path',
		pickerDescription,
		mode = 'any',
		roots,
		onSelect,
		onValueChange,
		value = $bindable(''),
		class: className,
		disabled,
		...rest
	}: Props = $props();

	let pickerOpen = $state(false);
	let draft = $state('');

	function openPicker() {
		draft = onSelect ? '' : String(value ?? '');
		pickerOpen = true;
	}

	function handlePicked(picked: string) {
		if (onSelect) {
			onSelect(picked);
			return;
		}
		value = picked;
		onValueChange?.(picked);
	}
</script>

<div class={label || hint ? 'space-y-2' : ''}>
	{#if label}
		<Label for={id} class={labelClass}>{label}</Label>
	{/if}
	<div class="relative">
		<Input
			{id}
			bind:value
			class={cn('font-mono', roots && 'pr-8', className)}
			{disabled}
			onchange={() => onValueChange?.(String(value ?? ''))}
			{...rest}
		/>
		{#if roots}
			<Button
				type="button"
				variant="ghost"
				size="icon"
				class="absolute top-1/2 right-1 size-6 -translate-y-1/2 text-muted-foreground hover:text-foreground"
				onclick={openPicker}
				{disabled}
				title="Browse"
				tabindex={-1}
			>
				<FolderSearch class="size-3.5" />
				<span class="sr-only">Browse</span>
			</Button>
		{/if}
	</div>
	{#if hint}
		<p class="text-xs text-muted-foreground">{hint}</p>
	{/if}
</div>

{#if roots}
	<PathPickerDialog
		bind:open={pickerOpen}
		bind:value={draft}
		title={pickerTitle}
		description={pickerDescription}
		{mode}
		{roots}
		onConfirm={handlePicked}
	/>
{/if}
