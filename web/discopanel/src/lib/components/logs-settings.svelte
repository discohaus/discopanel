<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { ScrollText, Download, Loader2, AlertCircle, ArrowDown } from '@lucide/svelte';
	import * as Tooltip from '$lib/components/ui/tooltip/index.js';
	import { toast } from 'svelte-sonner';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { registerRefresh } from '$lib/stores/refresh';
	import { formatBytes } from '$lib/utils';

	let loading = $state(true);
	let refreshing = $state(false);
	let logs = $state('');
	let filename = $state('');
	let fileSize = $state(0);
	let pinned = $state(true);
	let logContainer: HTMLPreElement | null = $state(null);
	let refreshInterval: ReturnType<typeof setInterval> | null = null;
	let unregisterRefresh: (() => void) | null = null;

	async function loadLogs(showToast = false) {
		if (refreshing) return;

		refreshing = true;
		try {
			const response = await rpcClient.support.getApplicationLogs(
				{
					tail: 500 // Fetches last 500 lines
				},
				silentCallOptions
			);
			logs = response.content;
			filename = response.filename;
			fileSize = Number(response.size);

			if (showToast) {
				toast.success('Logs refreshed');
			}

			// Keeps view pinned to newest line
			if (pinned && logContainer) {
				setTimeout(() => {
					if (logContainer) {
						logContainer.scrollTop = logContainer.scrollHeight;
					}
				}, 50);
			}
		} catch (error) {
			const message = error instanceof Error ? error.message : 'Unknown error occurred';
			if (showToast) {
				toast.error('Failed to load logs', { description: message });
			}
			console.error('Failed to load logs:', error);
		} finally {
			loading = false;
			refreshing = false;
		}
	}

	// Scrolling away unpins, returning to the bottom repins
	function handleScroll() {
		if (!logContainer) return;
		const { scrollTop, scrollHeight, clientHeight } = logContainer;
		pinned = scrollHeight - scrollTop - clientHeight < 5;
	}

	function scrollToBottom() {
		if (!logContainer) return;
		logContainer.scrollTop = logContainer.scrollHeight;
		pinned = true;
	}

	function downloadLogs() {
		if (!logs) return;

		const blob = new Blob([logs], { type: 'text/plain' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = filename || 'discopanel.log';
		a.click();
		URL.revokeObjectURL(url);
		toast.success('Logs downloaded');
	}


	onMount(() => {
		loadLogs();
		unregisterRefresh = registerRefresh(() => loadLogs(true));
		// Refreshes logs every 5 seconds
		refreshInterval = setInterval(() => loadLogs(), 5000);
	});

	onDestroy(() => {
		if (refreshInterval) {
			clearInterval(refreshInterval);
		}
		unregisterRefresh?.();
	});
</script>

<div class="flex min-h-0 flex-1 flex-col gap-3">
	<div
		class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border bg-terminal shadow-sm transition-colors duration-300"
	>
		<div
			class="flex shrink-0 items-center gap-3 border-b border-terminal-foreground/8 bg-terminal-foreground/4 px-3 py-2 transition-colors duration-300"
		>
			<div class="flex min-w-0 items-center gap-2">
				<span class="relative flex size-2 shrink-0">
					<span
						class="absolute inline-flex h-full w-full animate-ping rounded-full bg-status-ok opacity-40"
					></span>
					<span class="relative inline-flex size-2 rounded-full bg-status-ok"></span>
				</span>
				<span
					class="truncate font-mono text-xs font-medium tracking-wide text-terminal-foreground/85"
				>
					{filename || 'discopanel.log'}
				</span>
				{#if fileSize > 0}
					<span class="tabular shrink-0 font-mono text-[11px] text-terminal-foreground/40">
						{formatBytes(fileSize)}
					</span>
				{/if}
			</div>

			<div class="ml-auto flex shrink-0 items-center gap-0.5">
				<Tooltip.Root>
					<Tooltip.Trigger>
						<Button
							size="icon"
							variant="ghost"
							onclick={downloadLogs}
							disabled={!logs}
							class="size-6.5 text-terminal-foreground/45 hover:bg-terminal-foreground/10 hover:text-terminal-foreground"
						>
							<Download class="size-3.5" />
						</Button>
					</Tooltip.Trigger>
					<Tooltip.Content>Download logs</Tooltip.Content>
				</Tooltip.Root>
			</div>
		</div>

		<div class="relative min-h-0 flex-1">
			{#if loading}
				<div
					class="absolute inset-0 flex flex-col items-center justify-center gap-1 text-terminal-foreground/35"
				>
					<Loader2 class="mb-2 size-6 animate-spin" />
					<p class="font-mono text-sm">Loading logs...</p>
				</div>
			{:else if !logs}
				<div
					class="absolute inset-0 flex flex-col items-center justify-center gap-1 text-terminal-foreground/35"
				>
					<AlertCircle class="mb-2 size-6" />
					<p class="font-mono text-sm">No logs available</p>
					<p class="font-mono text-xs">File logging may not be enabled in your configuration.</p>
				</div>
			{:else}
				<pre
					bind:this={logContainer}
					onscroll={handleScroll}
					class="absolute inset-0 overflow-auto p-4 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap text-terminal-foreground">{logs}</pre>

				{#if !pinned}
					<button
						class="absolute bottom-3 left-1/2 flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-terminal-foreground/15 bg-terminal/95 px-3 py-1 font-mono text-xs text-terminal-foreground/80 shadow-lg backdrop-blur-sm transition-colors hover:bg-terminal-foreground/10"
						onclick={scrollToBottom}
					>
						<ArrowDown class="size-3" />
						Follow output
					</button>
				{/if}
			{/if}
		</div>
	</div>

	<div class="flex shrink-0 items-start gap-3 rounded-lg border bg-muted/30 px-4 py-3">
		<ScrollText class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
		<p class="text-xs leading-relaxed text-muted-foreground">
			Showing the last 500 lines, refreshed every 5 seconds. For complete logs, download the file or
			generate a support bundle from the Support tab.
		</p>
	</div>
</div>
