<script lang="ts">
	import { portal } from '$lib/portal';
	import { addressScope } from '$lib/hostname';
	import CopyButton from './copy-button.svelte';
	import { Lock } from '@lucide/svelte';

	let {
		addresses = [],
		label = 'address',
		link = false
	}: {
		addresses?: string[];
		label?: string;
		// Renders values as openers for full urls
		link?: boolean;
	} = $props();

	let open = $state(false);
	let wrap = $state<HTMLDivElement | null>(null);
	let menu = $state<HTMLDivElement | null>(null);
	let pos = $state({ top: 0, left: 0, width: 0 });

	// Any address works alike, sets arrive in canonical order
	let first = $derived(addresses[0] ?? '');

	// Floating list anchors under the row, never reflows
	function place() {
		if (!wrap) return;
		const rect = wrap.getBoundingClientRect();
		pos = { top: rect.bottom + 4, left: rect.left, width: rect.width };
	}

	function onDocDown(event: MouseEvent) {
		const target = event.target as Node;
		if (wrap?.contains(target) || menu?.contains(target)) return;
		open = false;
	}

	function onDocKey(event: KeyboardEvent) {
		if (event.key === 'Escape') open = false;
	}

	$effect(() => {
		if (!open) return;
		place();
		window.addEventListener('scroll', place, true);
		window.addEventListener('resize', place);
		document.addEventListener('mousedown', onDocDown);
		document.addEventListener('keydown', onDocKey);
		return () => {
			window.removeEventListener('scroll', place, true);
			window.removeEventListener('resize', place);
			document.removeEventListener('mousedown', onDocDown);
			document.removeEventListener('keydown', onDocKey);
		};
	});
</script>

{#snippet scopeBadge(address: string)}
	{#if address.startsWith('https://')}
		<Lock class="size-3 shrink-0 text-emerald-500" aria-label="Secure" />
	{/if}
	{@const scope = addressScope(address)}
	{#if scope}
		<span
			class="shrink-0 rounded border px-1 py-px text-[10px] tracking-wide text-muted-foreground uppercase"
		>
			{scope}
		</span>
	{/if}
{/snippet}

{#snippet addressText(address: string, cls: string)}
	{#if link}
		<!-- eslint-disable svelte/no-navigation-without-resolve -- external URL -->
		<a
			href={address}
			target="_blank"
			rel="noopener noreferrer"
			class="min-w-0 flex-1 truncate font-mono {cls} text-primary hover:underline"
			title={address}
		>
			{address}
		</a>
		<!-- eslint-enable svelte/no-navigation-without-resolve -->
	{:else}
		<p class="min-w-0 flex-1 truncate font-mono {cls}" title={address}>{address}</p>
	{/if}
{/snippet}

<div
	bind:this={wrap}
	class="flex items-center justify-between gap-2 rounded-lg border bg-muted/40 py-1.5 pr-1.5 pl-3"
>
	{@render addressText(first, 'text-sm')}
	{@render scopeBadge(first)}
	{#if addresses.length > 1}
		<button
			type="button"
			class="shrink-0 text-xs whitespace-nowrap text-muted-foreground transition-colors hover:text-foreground"
			onclick={() => (open = !open)}
		>
			+{addresses.length - 1} more
		</button>
	{/if}
	<CopyButton text={first} label="Copy {label}" />
</div>

{#if open}
	<div
		use:portal
		bind:this={menu}
		style="top: {pos.top}px; left: {pos.left}px; width: {pos.width}px"
		class="fixed z-[60] max-h-56 divide-y overflow-y-auto rounded-lg border bg-popover shadow-md"
	>
		{#each addresses as address (address)}
			<div class="flex items-center justify-between gap-2 py-1.5 pr-1.5 pl-3">
				{@render addressText(address, 'text-xs')}
				{@render scopeBadge(address)}
				<CopyButton text={address} label="Copy {label}" />
			</div>
		{/each}
	</div>
{/if}
