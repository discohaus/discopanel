<script lang="ts">
	import type { Component } from 'svelte';
	import type { Snippet } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import EmptyState from './empty-state.svelte';
	import { Plus } from '@lucide/svelte';

	interface Props {
		count: number;
		title?: string;
		headingIcon?: Component;
		countLabel?: string;
		countSuffix?: string;
		description?: string;
		addLabel: string;
		onAdd: () => void;
		addDisabled?: boolean;
		addOutline?: boolean;
		locked?: boolean;
		emptyIcon?: Component;
		emptyTitle?: string;
		emptyDescription?: string;
		emptyText?: string;
		children: Snippet;
	}

	let {
		count,
		title = '',
		headingIcon,
		countLabel = '',
		countSuffix = '',
		description = '',
		addLabel,
		onAdd,
		addDisabled = false,
		addOutline = false,
		locked = false,
		emptyIcon,
		emptyTitle = '',
		emptyDescription = '',
		emptyText = '',
		children
	}: Props = $props();

	const HeadingIcon = $derived(headingIcon);
	const EmptyIcon = $derived(emptyIcon);
</script>

<div class="space-y-4">
	<div class="flex items-start justify-between gap-4">
		<div>
			{#if title}
				<h3 class="flex items-center gap-1.5 text-sm font-semibold">
					{#if HeadingIcon}
						<HeadingIcon class="size-4" />
					{/if}
					{title}
				</h3>
			{:else}
				<p class="text-sm font-medium">
					{count}
					{countLabel}{count === 1 ? '' : 's'}
					{countSuffix}
				</p>
			{/if}
			{#if description}
				<p class="mt-0.5 text-xs text-muted-foreground">{description}</p>
			{/if}
		</div>
		{#if !locked}
			<Button
				size="sm"
				variant={addOutline ? 'outline' : 'default'}
				onclick={onAdd}
				disabled={addDisabled}
			>
				<Plus class="size-4" />
				{addLabel}
			</Button>
		{/if}
	</div>

	{#if count > 0}
		{@render children()}
	{:else if EmptyIcon}
		<div class="rounded-xl border border-dashed">
			<EmptyState icon={EmptyIcon} title={emptyTitle} description={emptyDescription}>
				{#if !locked}
					<Button variant="outline" size="sm" onclick={onAdd} disabled={addDisabled}>
						<Plus class="size-4" />
						{addLabel}
					</Button>
				{/if}
			</EmptyState>
		</div>
	{:else}
		<div
			class="rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground"
		>
			{emptyText}
		</div>
	{/if}
</div>
