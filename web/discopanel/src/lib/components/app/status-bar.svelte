<script lang="ts">
	import { activity, KIND_TONE, type ActivityEvent } from '$lib/stores/activity.svelte';
	import { loadingStore } from '$lib/stores/loading.svelte';
	import { TONE_BG } from '$lib/server-status';
	import { Popover, PopoverTrigger, PopoverContent } from '$lib/components/ui/popover';
	import { Button } from '$lib/components/ui/button';
	import CardStack from './card-stack.svelte';
	import Equalizer from './equalizer.svelte';
	import { ChevronUp } from '@lucide/svelte';
	import { fade } from 'svelte/transition';
	import { cn } from '$lib/utils';

	let current = $derived(activity.current);
	let fresh = $derived(activity.fresh);
	let unseen = $derived(activity.unseenErrors);
	let busy = $derived($loadingStore.global);
	let open = $state(false);
	let now = $state(Date.now());

	// Relative times tick only while events exist
	$effect(() => {
		if (!current) return;
		const timer = setInterval(() => (now = Date.now()), 5000);
		return () => clearInterval(timer);
	});

	// Opening history counts as seeing every error
	$effect(() => {
		if (open) activity.markSeen();
	});

	function ago(at: number): string {
		const s = Math.max(0, Math.round((now - at) / 1000));
		if (s < 10) return 'now';
		if (s < 60) return `${s}s`;
		const m = Math.round(s / 60);
		if (m < 60) return `${m}m`;
		return `${Math.round(m / 60)}h`;
	}

	function toneBg(event: ActivityEvent): string {
		return TONE_BG[KIND_TONE[event.kind]];
	}
</script>

<div class="fixed inset-x-0 bottom-0 z-[60] h-7 border-t bg-background/85 backdrop-blur-sm">
	<Popover bind:open>
		<PopoverTrigger
			class="flex h-full w-full cursor-pointer items-center gap-3 px-3 text-left transition-colors hover:bg-accent/40"
			aria-label="Recent activity"
		>
			<Equalizer
				size="sm"
				bars={4}
				animated={busy}
				tone={fresh && current ? KIND_TONE[current.kind] : 'idle'}
				class="shrink-0"
			/>
			<div class="relative h-full min-w-0 flex-1" role="status" aria-live="polite">
				{#if current}
					{#key current.id}
						<div
							class="absolute inset-0 flex items-center gap-2"
							transition:fade={{ duration: 150 }}
						>
							<span
								class={cn('size-2 shrink-0 rounded-full', toneBg(current), !fresh && 'opacity-40')}
							></span>
							<span
								class={cn('truncate text-xs', fresh ? 'text-foreground' : 'text-muted-foreground')}
							>
								{current.message}
							</span>
							{#if current.detail}
								<span class="hidden min-w-0 truncate text-xs text-muted-foreground sm:inline">
									{current.detail}
								</span>
							{/if}
						</div>
					{/key}
				{/if}
			</div>
			{#if current}
				<span class="tabular shrink-0 text-[10px] text-muted-foreground">{ago(current.at)}</span>
			{/if}
			<span class="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
				{#if unseen > 0}
					<span class="size-2 animate-pulse rounded-full bg-status-danger"></span>
				{/if}
				<span class="hidden sm:inline">Recent</span>
				<ChevronUp class="size-3" />
			</span>
		</PopoverTrigger>
		<PopoverContent
			side="top"
			align="end"
			sideOffset={6}
			class="z-[60] w-96 max-w-[calc(100vw-1rem)] p-2"
		>
			<div class="mb-1 flex items-center justify-between pl-1">
				<span class="text-xs font-medium">Recent activity</span>
				<Button
					variant="ghost"
					size="sm"
					class="h-6 px-2 text-xs text-muted-foreground"
					onclick={() => activity.clear()}
				>
					Clear
				</Button>
			</div>
			<CardStack
				items={activity.events}
				visible={6}
				slotHeight="2.5rem"
				gap="0.25rem"
				itemKey={(event) => event.id}
			>
				{#snippet card(event: ActivityEvent)}
					<div class="flex h-full items-center gap-2 rounded-md border bg-card px-2">
						<span class={cn('size-2 shrink-0 rounded-full', toneBg(event))}></span>
						<div class="min-w-0 flex-1">
							<p class="truncate text-xs">{event.message}</p>
							{#if event.detail}
								<p class="truncate text-[10px] text-muted-foreground">{event.detail}</p>
							{/if}
						</div>
						<span class="tabular shrink-0 text-[10px] text-muted-foreground">{ago(event.at)}</span>
					</div>
				{/snippet}
				{#snippet empty()}
					<div class="flex h-full items-center justify-center text-xs text-muted-foreground">
						Nothing yet
					</div>
				{/snippet}
			</CardStack>
		</PopoverContent>
	</Popover>
</div>
