<script lang="ts">
	import { onDestroy } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { Dialog, DialogContent, DialogHeader, DialogTitle } from '$lib/components/ui/dialog';
	import * as Tooltip from '$lib/components/ui/tooltip/index.js';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import type { Module } from '$lib/proto/discopanel/v1/storage_pb';
	import { ModuleStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import type { LogEntry } from '$lib/proto/discopanel/v1/server_pb';
	import { TONE_BG } from '$lib/server-status';
	import { moduleStatusMeta } from '$lib/module-status';
	import { Download, Trash2, RefreshCw, Loader2, X, ArrowDown } from '@lucide/svelte';
	import { mode } from 'mode-watcher';
	import { themedAnsiConverter } from '$lib/ansi-console';
	import { toast } from 'svelte-sonner';

	// Renders ansi escape codes as colored html
	let ansiConverter = $derived(themedAnsiConverter(mode.current));

	const TAIL_OPTIONS = [100, 500, 1000, 2000];

	interface Props {
		open: boolean;
		module: Module;
	}

	let { open = $bindable(), module }: Props = $props();

	let logEntries = $state<LogEntry[]>([]);
	let autoScroll = $state(true);
	let scrollAreaRef = $state<HTMLDivElement | null>(null);
	let pollingInterval: ReturnType<typeof setInterval> | null = null;
	let tailLines = $state(500);
	let fetching = $state(false);

	let statusMeta = $derived(moduleStatusMeta(module.status));

	// Fetches on open and polls while dialog visible
	$effect(() => {
		if (open) {
			fetchLogs();
			startPolling();
		} else {
			stopPolling();
			logEntries = [];
			autoScroll = true;
		}
	});

	onDestroy(() => {
		stopPolling();
	});

	function startPolling() {
		if (!pollingInterval) {
			pollingInterval = setInterval(fetchLogs, 3000);
		}
	}

	function stopPolling() {
		if (pollingInterval) {
			clearInterval(pollingInterval);
			pollingInterval = null;
		}
	}

	// Keeps view pinned to bottom while auto scroll on
	$effect(() => {
		if (logEntries.length > 0 && autoScroll && scrollAreaRef) {
			queueMicrotask(() => {
				if (scrollAreaRef) {
					scrollAreaRef.scrollTop = scrollAreaRef.scrollHeight;
				}
			});
		}
	});

	// Detaches auto scroll on scroll up, resumes at bottom
	function handleScroll() {
		if (!scrollAreaRef) return;

		const { scrollTop, scrollHeight, clientHeight } = scrollAreaRef;
		const isAtBottom = scrollHeight - scrollTop - clientHeight < 5;

		if (isAtBottom && !autoScroll) {
			autoScroll = true;
		} else if (!isAtBottom && autoScroll) {
			autoScroll = false;
		}
	}

	function jumpToBottom() {
		if (!scrollAreaRef) return;
		scrollAreaRef.scrollTop = scrollAreaRef.scrollHeight;
		autoScroll = true;
	}

	async function fetchLogs() {
		if (fetching) return;

		// Creating modules have no container to read yet
		if (module.status === ModuleStatus.CREATING) {
			return;
		}

		fetching = true;
		try {
			const response = await rpcClient.module.getModuleLogs(
				{
					id: module.id,
					tail: tailLines
				},
				silentCallOptions
			);
			logEntries = response.logs || [];
		} catch (error) {
			console.error('Failed to fetch module logs:', error);
		} finally {
			fetching = false;
		}
	}

	function clearLogs() {
		logEntries = [];
		toast.success('Logs cleared (local only)');
	}

	function downloadLogs() {
		const logText = logEntries.map((entry) => entry.message).join('\n');
		const blob = new Blob([logText], { type: 'text/plain' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = `${module.name}-logs-${new Date().toISOString()}.txt`;
		document.body.appendChild(a);
		a.click();
		document.body.removeChild(a);
		URL.revokeObjectURL(url);
		toast.success('Logs downloaded');
	}
</script>

<Dialog bind:open>
	<DialogContent
		class="flex h-[80vh]! w-[90vw]! max-w-4xl! flex-col gap-0! overflow-hidden border-terminal-foreground/10 bg-terminal p-0!"
		showCloseButton={false}
	>
		<DialogHeader
			class="shrink-0 border-b border-terminal-foreground/8 bg-terminal-foreground/4 px-3 py-2"
		>
			<div class="flex items-center gap-3">
				<div class="flex min-w-0 items-center gap-2">
					<span class="relative flex size-2 shrink-0">
						{#if statusMeta.transitional}
							<span
								class="absolute inline-flex h-full w-full animate-ping rounded-full opacity-60 {TONE_BG[
									statusMeta.tone
								]}"
							></span>
						{/if}
						<span class="relative inline-flex size-2 rounded-full {TONE_BG[statusMeta.tone]}"
						></span>
					</span>
					<DialogTitle
						class="truncate font-mono text-xs font-medium tracking-wide text-terminal-foreground/85"
					>
						{module.name}
					</DialogTitle>
					<span class="shrink-0 font-mono text-xs text-terminal-foreground/40">
						{statusMeta.label.toLowerCase()}
					</span>
				</div>

				<div class="ml-auto flex shrink-0 items-center gap-2">
					<span class="tabular hidden font-mono text-[11px] text-terminal-foreground/40 sm:inline">
						{logEntries.length} lines
					</span>
					<select
						bind:value={tailLines}
						onchange={fetchLogs}
						title="Lines of history to keep"
						class="h-6 rounded border border-terminal-foreground/10 bg-transparent px-1.5 font-mono text-[11px] text-terminal-foreground/60 focus:outline-none"
					>
						{#each TAIL_OPTIONS as option (option)}
							<option value={option} class="bg-terminal">tail {option}</option>
						{/each}
					</select>
					<div class="flex items-center gap-0.5 border-l border-terminal-foreground/10 pl-2">
						<Tooltip.Root>
							<Tooltip.Trigger>
								<Button
									size="icon"
									variant="ghost"
									onclick={fetchLogs}
									disabled={fetching}
									class="size-6.5 text-terminal-foreground/45 hover:bg-terminal-foreground/10 hover:text-terminal-foreground"
								>
									{#if fetching}
										<Loader2 class="size-3.5 animate-spin" />
									{:else}
										<RefreshCw class="size-3.5" />
									{/if}
								</Button>
							</Tooltip.Trigger>
							<Tooltip.Content>Refresh logs</Tooltip.Content>
						</Tooltip.Root>
						<Tooltip.Root>
							<Tooltip.Trigger>
								<Button
									size="icon"
									variant="ghost"
									onclick={downloadLogs}
									disabled={logEntries.length === 0}
									class="size-6.5 text-terminal-foreground/45 hover:bg-terminal-foreground/10 hover:text-terminal-foreground"
								>
									<Download class="size-3.5" />
								</Button>
							</Tooltip.Trigger>
							<Tooltip.Content>Download logs</Tooltip.Content>
						</Tooltip.Root>
						<Tooltip.Root>
							<Tooltip.Trigger>
								<Button
									size="icon"
									variant="ghost"
									onclick={clearLogs}
									disabled={logEntries.length === 0}
									class="size-6.5 text-terminal-foreground/45 hover:bg-terminal-foreground/10 hover:text-terminal-foreground"
								>
									<Trash2 class="size-3.5" />
								</Button>
							</Tooltip.Trigger>
							<Tooltip.Content>Clear view (local only)</Tooltip.Content>
						</Tooltip.Root>
					</div>
					<div class="h-4 w-px bg-terminal-foreground/10"></div>
					<Button
						size="icon"
						variant="ghost"
						onclick={() => (open = false)}
						class="size-6.5 text-terminal-foreground/45 hover:bg-terminal-foreground/10 hover:text-terminal-foreground"
					>
						<X class="size-3.5" />
					</Button>
				</div>
			</div>
		</DialogHeader>

		<div class="relative min-h-0 flex-1">
			<div
				class="absolute inset-0 overflow-x-auto overflow-y-auto px-4 py-3"
				bind:this={scrollAreaRef}
				onscroll={handleScroll}
			>
				<div class="font-mono text-xs leading-relaxed text-terminal-foreground">
					{#if logEntries.length === 0}
						<div class="py-8 text-center font-mono text-terminal-foreground/35">
							{#if module.status === ModuleStatus.STOPPED}
								No logs available. Start the module to see output.
							{:else if module.status === ModuleStatus.STARTING || module.status === ModuleStatus.CREATING}
								Waiting for module to start...
							{:else}
								No logs available. Try refreshing.
							{/if}
						</div>
					{:else}
						{#each logEntries as entry, i (i)}
							<div class="log-line break-all whitespace-pre-wrap">
								<!-- eslint-disable-next-line svelte/no-at-html-tags -->
								{@html ansiConverter.toHtml(entry.message)}
							</div>
						{/each}
					{/if}
				</div>
			</div>

			{#if !autoScroll}
				<button
					class="absolute bottom-3 left-1/2 flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-terminal-foreground/15 bg-terminal/95 px-3 py-1 font-mono text-xs text-terminal-foreground/80 shadow-lg backdrop-blur-sm transition-colors hover:bg-terminal-foreground/10"
					onclick={jumpToBottom}
				>
					<ArrowDown class="size-3" />
					Follow output
				</button>
			{/if}
		</div>
	</DialogContent>
</Dialog>

<style>
	.log-line {
		padding: 0.125rem 0;
		line-height: 1.45;
	}

	.log-line:hover {
		background-color: color-mix(in oklab, var(--terminal-foreground) 6%, transparent);
	}
</style>
