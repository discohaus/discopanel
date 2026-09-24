<script lang="ts">
	import { Loader2 } from '@lucide/svelte';
	import type { ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import { statusMeta, TONE_BADGE, TONE_BG } from '$lib/server-status';
	import { cn } from '$lib/utils';

	let {
		status,
		class: className = ''
	}: {
		status: ServerStatus;
		class?: string;
	} = $props();

	let meta = $derived(statusMeta(status));
</script>

<span
	class={cn(
		'inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium',
		TONE_BADGE[meta.tone],
		className
	)}
>
	{#if meta.transitional}
		<Loader2 class="size-3 animate-spin" />
	{:else}
		<span class={cn('size-1.5 rounded-full', TONE_BG[meta.tone])}></span>
	{/if}
	{meta.label}
</span>
