<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { X } from '@lucide/svelte';

	let {
		publicIp,
		onClose
	}: {
		publicIp: string;
		onClose: () => void;
	} = $props();
</script>

<div class="flex h-full min-h-0 flex-col">
	<div class="flex items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="min-w-0">
			<h3 class="truncate text-sm font-semibold">Internet</h3>
			<p class="text-xs text-muted-foreground">Info</p>
		</div>
		<Button variant="ghost" size="icon" class="size-8" onclick={onClose} title="Back to overview">
			<X class="size-4" />
		</Button>
	</div>

	<div class="min-h-0 flex-1 space-y-2 overflow-y-auto p-4 text-sm">
		<div class="flex items-center justify-between gap-3">
			<span class="text-muted-foreground">Public address</span>
			<span class="font-mono text-xs">{publicIp || 'Not detected'}</span>
		</div>
		<p class="pt-2 text-xs text-muted-foreground">
			Players outside your network reach you through this address
		</p>
	</div>
</div>
