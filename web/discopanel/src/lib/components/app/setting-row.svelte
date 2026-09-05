<script lang="ts">
	import { Label } from '$lib/components/ui/label';
	import type { Snippet } from 'svelte';
	import { cn } from '$lib/utils';

	let {
		id,
		label,
		description = '',
		envVar = '',
		hint = '',
		required = false,
		system = false,
		modified = false,
		unset = false,
		dimmed = false,
		highlighted = false,
		children,
		actions
	}: {
		id: string;
		label: string;
		description?: string;
		envVar?: string;
		hint?: string;
		required?: boolean;
		system?: boolean;
		modified?: boolean;
		unset?: boolean;
		dimmed?: boolean;
		highlighted?: boolean;
		children: Snippet;
		actions?: Snippet;
	} = $props();
</script>

<div
	{id}
	data-field="true"
	class={cn(
		'group relative grid gap-x-8 gap-y-2.5 px-4 py-4 transition-colors sm:grid-cols-[minmax(0,1fr)_minmax(0,19rem)] sm:items-start',
		highlighted && 'bg-primary/5 ring-2 ring-primary ring-inset',
		unset && 'bg-muted/20'
	)}
>
	{#if modified}
		<span class="absolute inset-y-1 left-0 w-0.5 rounded-full bg-status-busy"></span>
	{/if}

	<div class="min-w-0">
		<div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
			<Label for={id} class="text-sm font-medium {dimmed ? 'text-muted-foreground' : ''}">
				{label}
			</Label>
			{#if required}
				<span class="text-[11px] font-medium text-status-danger">required</span>
			{/if}
			{#if system}
				<span class="text-[11px] font-medium text-status-sleep">system</span>
			{/if}
			{#if modified}
				<span class="text-[11px] font-medium text-status-busy">modified</span>
			{/if}
			{#if unset}
				<span class="text-[11px] text-muted-foreground">using default</span>
			{/if}
		</div>
		{#if description}
			<p class="mt-1 text-xs leading-relaxed text-pretty text-muted-foreground">{description}</p>
		{/if}
		{#if envVar}
			<code class="mt-1 block truncate font-mono text-[11px] text-muted-foreground/60">
				{envVar}
			</code>
		{/if}
	</div>

	<div class="min-w-0">
		<div class="flex items-center gap-1">
			<div class="min-w-0 flex-1">
				{@render children()}
			</div>
			{#if actions}
				<div class="flex shrink-0 items-center gap-0.5">
					{@render actions()}
				</div>
			{/if}
		</div>
		{#if hint}
			<p class="mt-1.5 text-[11px] text-muted-foreground">{hint}</p>
		{/if}
	</div>
</div>
