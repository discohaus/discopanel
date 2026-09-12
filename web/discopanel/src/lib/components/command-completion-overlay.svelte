<script lang="ts">
	import type { CommandToken } from '$lib/proto/discopanel/v1/server_pb';

	let {
		tokens = [],
		selectedIndex = 0,
		onSelect
	}: {
		tokens: CommandToken[];
		selectedIndex: number;
		onSelect: (token: CommandToken) => void;
	} = $props();

	let overlayRef = $state<HTMLDivElement | null>(null);

	$effect(() => {
		if (overlayRef && selectedIndex >= 0) {
			const item = overlayRef.children[selectedIndex] as HTMLElement | undefined;
			if (item) {
				item.scrollIntoView({ block: 'nearest', inline: 'nearest' });
			}
		}
	});

	function handleMouseDown(e: MouseEvent, token: CommandToken) {
		// Prevent input blur before click registers
		e.preventDefault();
		onSelect(token);
	}
	function isPlayerToken(token: CommandToken): boolean {
		return !!token.isPlayer;
	}
</script>

{#if tokens.length > 0}
	<div
		bind:this={overlayRef}
		class="absolute bottom-full left-0 z-50 mb-1 max-h-56 w-72 overflow-y-auto rounded-lg border border-terminal-foreground/20 bg-terminal/98 py-1 font-mono text-xs shadow-2xl backdrop-blur-md transition-all duration-150"
	>
		{#each tokens as token, index (index)}
			<button
				type="button"
				class="flex w-full items-center justify-between gap-2 px-3 py-1.5 text-left transition-colors hover:bg-terminal-foreground/10 {index ===
				selectedIndex
					? 'bg-terminal-foreground/20 font-bold text-terminal-foreground'
					: 'text-terminal-foreground/90'}"
				onmousedown={(e) => handleMouseDown(e, token)}
			>
				<div class="flex min-w-0 items-center gap-1.5 font-mono">
					<span class="truncate font-mono">{token.text}</span>
					{#if isPlayerToken(token)}
						<img
							src="https://mc-heads.net/avatar/{token.text}"
							alt={token.text}
							class="size-4 shrink-0 rounded-sm border border-terminal-foreground/10"
							loading="lazy"
						/>
					{/if}
				</div>

				<div class="flex shrink-0 items-center gap-1">
					{#if token.isStatic}
						<span
							class="rounded border border-emerald-500/60 bg-emerald-500/30 px-1.5 py-0.5 text-[10px] font-bold text-emerald-300 shadow-sm"
						>
							value
						</span>
					{:else if token.isArgument}
						<span
							class="rounded border border-amber-500/60 bg-amber-500/30 px-1.5 py-0.5 text-[10px] font-bold text-amber-300 shadow-sm"
						>
							arg
						</span>
					{:else}
						<span
							class="rounded border border-sky-500/60 bg-sky-500/30 px-1.5 py-0.5 text-[10px] font-bold text-sky-300 shadow-sm"
						>
							cmd
						</span>
					{/if}

					{#if token.isOptional}
						<span
							class="rounded border border-purple-500/50 bg-purple-500/25 px-1 py-0.5 text-[10px] font-medium text-purple-300"
						>
							opt
						</span>
					{/if}
				</div>
			</button>
		{/each}
	</div>
{/if}
