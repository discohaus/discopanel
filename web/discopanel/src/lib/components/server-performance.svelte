<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import { Button } from '$lib/components/ui/button';
	import {
		Dialog,
		DialogContent,
		DialogDescription,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Badge } from '$lib/components/ui/badge';
	import { EmptyState } from '$lib/components/app';
	import {
		Loader2,
		Gauge,
		Info,
		AlertTriangle,
		AlertCircle,
		CheckCircle2,
		ChevronRight,
		CircleDashed,
		MoonStar,
		Bot,
		EyeOff,
		RotateCcw,
		ScrollText
	} from '@lucide/svelte';
	import { ServerStatus, type Server } from '$lib/proto/discopanel/v1/storage_pb';
	import {
		FindingSource,
		FindingSourceSchema,
		PerformanceSeverity,
		type PerformanceFinding
	} from '$lib/proto/discopanel/v1/server_pb';
	import { enumLabel } from '$lib/proto-meta';
	import { statusMeta } from '$lib/server-status';

	let { server }: { server: Server } = $props();

	let loading = $state(true);
	let detailsOpen = $state(false);
	let dismissing = $state('');
	let findings = $state<PerformanceFinding[]>([]);
	let agentConnected = $state(false);
	let initialized = false;

	const serverUp = $derived(
		server.status === ServerStatus.RUNNING || server.status === ServerStatus.UNHEALTHY
	);
	const sorted = $derived(
		[...findings].filter((f) => !f.dismissed).sort((a, b) => b.severity - a.severity)
	);
	const dismissedList = $derived(findings.filter((f) => f.dismissed));
	const problems = $derived(sorted.filter((f) => f.severity >= PerformanceSeverity.WARNING));
	const criticals = $derived(
		problems.filter((f) => f.severity === PerformanceSeverity.CRITICAL).length
	);

	// Live status always co-decides, findings alone can go stale
	const health = $derived.by(() => {
		if (criticals > 0) {
			return {
				label: criticals === 1 ? '1 critical issue' : `${criticals} critical issues`,
				text: 'text-status-danger',
				icon: AlertCircle
			};
		}
		if (server.status === ServerStatus.ERROR) {
			return { label: 'Server hit an error', text: 'text-status-danger', icon: AlertCircle };
		}
		if (problems.length > 0) {
			return {
				label: problems.length === 1 ? '1 issue found' : `${problems.length} issues found`,
				text: 'text-status-warn',
				icon: AlertTriangle
			};
		}
		if (server.status === ServerStatus.UNHEALTHY) {
			return { label: 'Not responding normally', text: 'text-status-warn', icon: AlertTriangle };
		}
		if (statusMeta(server.status).transitional) {
			return {
				label: statusMeta(server.status).label + '...',
				text: 'text-status-busy',
				icon: CircleDashed
			};
		}
		if (serverUp) {
			return { label: 'No issues found', text: 'text-status-ok', icon: CheckCircle2 };
		}
		if (server.status === ServerStatus.PAUSED) {
			return { label: 'Sleeping, checks paused', text: 'text-status-sleep', icon: MoonStar };
		}
		return {
			label: 'Offline, config checks only',
			text: 'text-muted-foreground',
			icon: CircleDashed
		};
	});

	function chipClass(severity: PerformanceSeverity) {
		return severity === PerformanceSeverity.CRITICAL
			? 'border-status-danger/30 bg-status-danger/10 text-status-danger'
			: 'border-status-warn/30 bg-status-warn/10 text-status-warn';
	}

	async function loadReport(silent = false) {
		try {
			const res = await rpcClient.server.getServerPerformanceReport(
				{ id: server.id },
				silent ? silentCallOptions : undefined
			);
			findings = res.findings;
			agentConnected = res.agentConnected;
		} catch {
			if (!silent) notify.error('Failed to load performance report');
		} finally {
			loading = false;
		}
	}

	async function dismiss(finding: PerformanceFinding, restore = false) {
		dismissing = finding.id;
		try {
			await rpcClient.server.dismissPerformanceFinding({
				id: server.id,
				findingId: finding.id,
				restore
			});
			await loadReport(true);
		} catch {
			notify.error('Failed to update finding');
		} finally {
			dismissing = '';
		}
	}

	// Jumps to the console actions channel at the incident start
	function viewLogs(finding: PerformanceFinding) {
		detailsOpen = false;
		const url =
			resolve(`/servers/${server.id}`) +
			`?tab=console&channel=actions&hl=${finding.actionLogStartMs}`;
		goto(url, { noScroll: true });
	}

	// Refetch only when server or status actually changes
	let loadedServerId = '';
	let loadedStatus: ServerStatus | null = null;
	$effect(() => {
		if (server.id === loadedServerId && server.status === loadedStatus) return;
		const serverChanged = server.id !== loadedServerId;
		const isFirst = !initialized;
		if (isFirst || serverChanged) loading = true;
		loadedServerId = server.id;
		loadedStatus = server.status;
		initialized = true;
		loadReport(!isFirst && !serverChanged);
	});

	$effect(() => {
		if (!detailsOpen || !serverUp) return;
		untrack(() => loadReport(true));
		const timer = setInterval(() => loadReport(true), 10000);
		return () => clearInterval(timer);
	});
</script>

<button
	type="button"
	class="w-full border-t px-5 py-3 text-left transition-colors hover:bg-accent/40"
	onclick={() => (detailsOpen = true)}
>
	<div class="flex items-center justify-between gap-2">
		<span class="stat-label">Health</span>
		{#if loading}
			<Loader2 class="size-3.5 animate-spin text-muted-foreground/50" />
		{:else}
			{@const HealthIcon = health.icon}
			<HealthIcon class="size-3.5 {health.text}" />
		{/if}
	</div>
	{#if loading}
		<span class="font-mono text-sm text-muted-foreground/60">--</span>
	{:else}
		<div class="mt-1 flex items-center justify-between gap-2">
			<span class="text-sm font-semibold {health.text}">{health.label}</span>
			<span class="flex shrink-0 items-center text-[11px] text-muted-foreground">
				Details <ChevronRight class="size-3" />
			</span>
		</div>
		{#if problems.length > 0}
			<div class="mt-1.5 flex flex-wrap gap-1.5">
				{#each problems.slice(0, 2) as finding (finding.id)}
					<span
						class="max-w-full truncate rounded-md border px-1.5 py-0.5 text-[11px] font-medium {chipClass(
							finding.severity
						)}"
					>
						{finding.title}
					</span>
				{/each}
				{#if problems.length > 2}
					<span
						class="rounded-md border bg-muted/40 px-1.5 py-0.5 text-[11px] text-muted-foreground"
					>
						+{problems.length - 2} more
					</span>
				{/if}
			</div>
		{:else}
			<p class="mt-0.5 text-[11px] text-muted-foreground">
				{#if !serverUp}
					Start the server for the full checkup
				{:else if agentConnected}
					Live checks from the DiscoPanel agent
				{:else}
					Agent offline, basic checks only
				{/if}
			</p>
		{/if}
	{/if}
</button>

<Dialog bind:open={detailsOpen}>
	<DialogContent class="max-h-[85vh] overflow-y-auto sm:max-w-xl">
		<DialogHeader>
			<DialogTitle class="flex items-center gap-2">
				<Gauge class="size-5" />
				Health check
			</DialogTitle>
			<DialogDescription>
				{#if agentConnected}
					Live checks from the DiscoPanel agent
				{:else if serverUp}
					Agent offline, basic checks only
				{:else}
					Config checks (start the server for live telemetry)
				{/if}
			</DialogDescription>
		</DialogHeader>
		<div class="space-y-3">
			{#if sorted.length === 0}
				<EmptyState
					icon={CheckCircle2}
					title="Nothing to report"
					description={serverUp
						? 'All checks passed for this server.'
						: 'Start the server to run the full checkup.'}
					class="py-8"
				/>
			{/if}
			{#each sorted as finding (finding.id)}
				<div
					class="overflow-hidden rounded-lg border {finding.severity ===
					PerformanceSeverity.CRITICAL
						? 'border-status-danger/30'
						: finding.severity === PerformanceSeverity.WARNING
							? 'border-status-warn/30'
							: ''}"
				>
					<div class="flex items-start gap-3 p-3">
						{#if finding.severity === PerformanceSeverity.CRITICAL}
							<AlertCircle class="mt-0.5 size-5 shrink-0 text-status-danger" />
						{:else if finding.severity === PerformanceSeverity.WARNING}
							<AlertTriangle class="mt-0.5 size-5 shrink-0 text-status-warn" />
						{:else if finding.severity === PerformanceSeverity.INFO}
							<Info class="mt-0.5 size-5 shrink-0 text-status-sleep" />
						{:else}
							<CheckCircle2 class="mt-0.5 size-5 shrink-0 text-status-ok" />
						{/if}
						<div class="min-w-0 flex-1">
							<div class="flex flex-wrap items-center gap-2">
								<span class="font-medium">{finding.title}</span>
								{#if finding.source !== FindingSource.UNSPECIFIED}
									<Badge
										variant="outline"
										class="px-1.5 py-0 text-[10px] tracking-wide text-muted-foreground uppercase"
									>
										{enumLabel(FindingSourceSchema, finding.source)}
									</Badge>
								{/if}
								{#if finding.severity === PerformanceSeverity.CRITICAL}
									<Badge variant="destructive" class="text-xs">critical</Badge>
								{/if}
							</div>
							<p class="mt-1 text-sm text-muted-foreground">{finding.detail}</p>
							{#if finding.evidence.length > 0}
								<ul class="mt-2 space-y-1 border-l-2 border-muted pl-3">
									{#each finding.evidence as line (line)}
										<li class="text-xs break-words text-muted-foreground/80">{line}</li>
									{/each}
								</ul>
							{/if}
						</div>
					</div>
					{#if finding.action}
						<div class="flex items-center gap-2 border-t bg-primary/5 px-3 py-2">
							<Bot class="size-3.5 shrink-0 text-primary" />
							<p class="line-clamp-1 min-w-0 flex-1 text-xs text-foreground/80">{finding.action}</p>
							{#if finding.actionLogStartMs > 0n}
								<Button
									size="sm"
									variant="ghost"
									class="h-6 shrink-0 gap-1 px-2 text-xs"
									onclick={() => viewLogs(finding)}
								>
									<ScrollText class="size-3" />
									View logs
								</Button>
							{/if}
						</div>
					{/if}
					{#if finding.severity >= PerformanceSeverity.INFO}
						<div class="flex items-center gap-2 border-t bg-muted/20 px-3 py-2">
							<Button
								size="sm"
								variant="ghost"
								class="ml-auto shrink-0 gap-1 text-xs text-muted-foreground"
								disabled={dismissing !== ''}
								onclick={() => dismiss(finding)}
							>
								{#if dismissing === finding.id}
									<Loader2 class="size-3 animate-spin" />
								{:else}
									<EyeOff class="size-3" />
								{/if}
								Dismiss
							</Button>
						</div>
					{/if}
				</div>
			{/each}
			{#if dismissedList.length > 0}
				<div class="rounded-lg border border-dashed px-3 py-2">
					<p class="text-[11px] tracking-wide text-muted-foreground uppercase">
						{dismissedList.length} dismissed
					</p>
					{#each dismissedList as finding (finding.id)}
						<div class="mt-1 flex items-center justify-between gap-2">
							<span class="truncate text-xs text-muted-foreground">{finding.title}</span>
							<Button
								size="sm"
								variant="ghost"
								class="h-6 shrink-0 gap-1 px-2 text-xs text-muted-foreground"
								disabled={dismissing !== ''}
								onclick={() => dismiss(finding, true)}
							>
								<RotateCcw class="size-3" />
								Restore
							</Button>
						</div>
					{/each}
				</div>
			{/if}
		</div>
	</DialogContent>
</Dialog>
