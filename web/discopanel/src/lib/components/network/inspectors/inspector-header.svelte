<script lang="ts">
	import type { Snippet } from 'svelte';
	import { ChevronLeft } from '@lucide/svelte';

	let {
		title,
		subtitle = '',
		mono = false,
		onBack,
		icon,
		tag
	}: {
		title: string;
		subtitle?: string;
		// Ports and addresses read better in mono
		mono?: boolean;
		// Missing on the root settings panel
		onBack?: () => void;
		icon?: Snippet;
		tag?: Snippet;
	} = $props();
</script>

<div class="shrink-0 border-b bg-muted/30">
	{#if onBack}
		<div class="px-2 pt-2">
			<button
				type="button"
				class="inline-flex items-center gap-0.5 rounded-md py-1 pr-2 pl-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
				onclick={onBack}
			>
				<ChevronLeft class="size-3.5" />
				Settings
			</button>
		</div>
	{/if}
	<div class="flex items-center gap-2.5 px-4 {onBack ? 'pt-1 pb-3' : 'py-3'}">
		{#if icon}
			{@render icon()}
		{/if}
		<div class="min-w-0 flex-1">
			<div class="flex items-center gap-2">
				<h3 class="truncate text-sm font-semibold {mono ? 'font-mono' : ''}">{title}</h3>
				{#if tag}
					{@render tag()}
				{/if}
			</div>
			{#if subtitle}
				<p class="truncate text-xs text-muted-foreground">{subtitle}</p>
			{/if}
		</div>
	</div>
</div>
