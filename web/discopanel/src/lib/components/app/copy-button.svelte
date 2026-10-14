<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Check, Copy } from '@lucide/svelte';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { notify } from '$lib/stores/activity.svelte';
	import { cn } from '$lib/utils';

	let {
		text,
		label = 'Copy',
		size = 'icon',
		variant = 'ghost',
		class: className = ''
	}: {
		text: string;
		label?: string;
		size?: 'icon' | 'sm';
		variant?: 'ghost' | 'outline';
		class?: string;
	} = $props();

	let copied = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;

	async function copy(e: MouseEvent) {
		e.stopPropagation();
		const ok = await copyToClipboard(text);
		if (!ok) {
			notify.error('Failed to copy to clipboard');
			return;
		}
		copied = true;
		clearTimeout(timer);
		timer = setTimeout(() => (copied = false), 1500);
	}
</script>

<Button
	{variant}
	size={size === 'icon' ? 'icon' : 'sm'}
	class={cn(size === 'icon' && 'size-7', className)}
	onclick={copy}
	aria-label={label}
	title={label}
>
	{#if copied}
		<Check class="size-3.5 text-status-ok" />
	{:else}
		<Copy class="size-3.5" />
	{/if}
	{#if size === 'sm'}
		<span>{copied ? 'Copied' : label}</span>
	{/if}
</Button>
