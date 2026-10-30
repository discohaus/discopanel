<script lang="ts">
	import type { ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import { statusMeta, TONE_BG } from '$lib/server-status';
	import { cn } from '$lib/utils';

	let {
		status,
		class: className = ''
	}: {
		status: ServerStatus;
		class?: string;
	} = $props();

	let meta = $derived(statusMeta(status));
	let pulse = $derived(meta.transitional || meta.tone === 'danger' || meta.tone === 'warn');
</script>

<span
	class={cn('inline-block size-2 shrink-0 rounded-full', TONE_BG[meta.tone], className)}
	class:animate-pulse={pulse}
	title={meta.label}
></span>
