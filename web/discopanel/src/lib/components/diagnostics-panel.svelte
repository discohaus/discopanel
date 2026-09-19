<script lang="ts">
	import { onMount } from 'svelte';
	import { slide } from 'svelte/transition';
	import { Button } from '$lib/components/ui/button';
	import { rpcClient, rpcErrorMessage, silentCallOptions } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import { registerRefresh } from '$lib/stores/refresh';
	import { enumLabelOr } from '$lib/proto-meta';
	import { TONE_BADGE, TONE_BG, TONE_TEXT, type StatusTone } from '$lib/server-status';
	import { formatRelative } from '$lib/utils/time';
	import { cn } from '$lib/utils';
	import {
		DiagnosticSeverity,
		DiagnosticSeveritySchema,
		DiagnosticCategory,
		DiagnosticCategorySchema,
		type DiagnosticCheck,
		type DiagnosticReport
	} from '$lib/proto/discopanel/v1/support_pb';
	import {
		Stethoscope,
		RefreshCw,
		Loader2,
		ChevronDown,
		ChevronRight,
		ExternalLink,
		Wrench,
		CircleCheck,
		CircleAlert,
		CircleX,
		Info,
		CircleMinus
	} from '@lucide/svelte';
	import { SvelteMap, SvelteSet } from 'svelte/reactivity';

	let { report = $bindable(null) }: { report?: DiagnosticReport | null } = $props();

	let loading = $state(true);
	let running = $state(false);
	let expanded = new SvelteSet<string>();
	let problemsOnly = $state(false);

	const SEVERITY_UI: Record<DiagnosticSeverity, { tone: StatusTone; icon: typeof Info }> = {
		[DiagnosticSeverity.UNSPECIFIED]: { tone: 'idle', icon: CircleMinus },
		[DiagnosticSeverity.PASS]: { tone: 'ok', icon: CircleCheck },
		[DiagnosticSeverity.INFO]: { tone: 'sleep', icon: Info },
		[DiagnosticSeverity.WARN]: { tone: 'warn', icon: CircleAlert },
		[DiagnosticSeverity.FAIL]: { tone: 'danger', icon: CircleX },
		[DiagnosticSeverity.SKIP]: { tone: 'idle', icon: CircleMinus }
	};

	const SEVERITY_ORDER: Record<DiagnosticSeverity, number> = {
		[DiagnosticSeverity.FAIL]: 0,
		[DiagnosticSeverity.WARN]: 1,
		[DiagnosticSeverity.INFO]: 2,
		[DiagnosticSeverity.PASS]: 3,
		[DiagnosticSeverity.SKIP]: 4,
		[DiagnosticSeverity.UNSPECIFIED]: 5
	};

	function isProblem(check: DiagnosticCheck): boolean {
		return check.severity === DiagnosticSeverity.FAIL || check.severity === DiagnosticSeverity.WARN;
	}

	let groups = $derived.by(() => {
		if (!report) return [];
		const byCategory = new SvelteMap<DiagnosticCategory, DiagnosticCheck[]>();
		for (const check of report.checks) {
			if (problemsOnly && !isProblem(check)) continue;
			const list = byCategory.get(check.category) ?? [];
			list.push(check);
			byCategory.set(check.category, list);
		}
		return [...byCategory.entries()]
			.sort((a, b) => a[0] - b[0])
			.map(([category, checks]) => ({
				category,
				label: enumLabelOr(DiagnosticCategorySchema, category),
				checks: checks.sort(
					(a, b) =>
						SEVERITY_ORDER[a.severity] - SEVERITY_ORDER[b.severity] || a.id.localeCompare(b.id)
				),
				problems: checks.filter(isProblem).length
			}));
	});

	let problemCount = $derived(report ? report.warnCount + report.failCount : 0);

	let headline = $derived.by(() => {
		if (!report) return 'No diagnostics yet';
		if (report.failCount > 0)
			return `${report.failCount} failed, ${report.warnCount} ${report.warnCount === 1 ? 'warning' : 'warnings'}`;
		if (report.warnCount > 0)
			return `${report.warnCount} ${report.warnCount === 1 ? 'warning' : 'warnings'}, nothing failed`;
		return 'Everything checks out';
	});

	let headlineTone = $derived<StatusTone>(
		!report ? 'idle' : report.failCount > 0 ? 'danger' : report.warnCount > 0 ? 'warn' : 'ok'
	);

	// Failures and warnings start open so the fix is visible
	function openProblems(next: DiagnosticReport | null) {
		expanded.clear();
		if (!next) return;
		for (const check of next.checks) {
			if (isProblem(check)) expanded.add(check.id);
		}
	}

	async function load(silent = true) {
		try {
			const res = await rpcClient.support.getDiagnostics(
				{},
				silent ? silentCallOptions : undefined
			);
			running = res.running;
			if (res.report) {
				report = res.report;
				if (expanded.size === 0) openProblems(res.report);
			}
		} catch (error) {
			console.error('Failed to load diagnostics:', error);
		} finally {
			loading = false;
		}
	}

	async function run() {
		if (running) return;
		const previousRun = report?.startedAt;
		running = true;
		try {
			const res = await rpcClient.support.runDiagnostics({}, silentCallOptions);
			running = false;
			if (res.report) {
				report = res.report;
				openProblems(res.report);
				notifyReport(res.report);
			}
		} catch (error) {
			running = false;
			// The runner survives a lost connection. Resume polling if it is still active.
			await load();
			if (running) {
				notify.warning('Diagnostics are still running', {
					description: 'Results will appear here when the checks finish.'
				});
			} else if (
				report?.startedAt &&
				(report.startedAt.seconds !== previousRun?.seconds ||
					report.startedAt.nanos !== previousRun?.nanos)
			) {
				notifyReport(report);
			} else {
				const message = rpcErrorMessage(error, 'Unknown error occurred');
				notify.error('Diagnostics request failed', { description: message });
			}
		}
	}

	function notifyReport(result: DiagnosticReport) {
		const problems = result.failCount + result.warnCount;
		if (problems === 0) notify.success('Diagnostics passed');
		else
			notify.warning(`Diagnostics found ${problems} ${problems === 1 ? 'issue' : 'issues'}`, {
				description: 'Expand a check to see the fix'
			});
	}

	function toggle(id: string) {
		if (expanded.has(id)) expanded.delete(id);
		else expanded.add(id);
	}

	function factRows(check: DiagnosticCheck): [string, string][] {
		return Object.entries(check.facts).sort(([a], [b]) => a.localeCompare(b));
	}

	// Startup runs finish shortly after page load, poll until settled
	let pollTimer: ReturnType<typeof setInterval> | null = null;
	$effect(() => {
		if (!running || pollTimer) return;
		pollTimer = setInterval(async () => {
			await load();
			if (!running && pollTimer) {
				clearInterval(pollTimer);
				pollTimer = null;
			}
		}, 3000);
		return () => {
			if (pollTimer) clearInterval(pollTimer);
			pollTimer = null;
		};
	});

	// Reports handed in by the bundle flow open their problems
	let lastSeen: DiagnosticReport | null = null;
	$effect(() => {
		if (report && report !== lastSeen) {
			lastSeen = report;
			openProblems(report);
		}
	});

	onMount(() => {
		load();
		return registerRefresh(() => load());
	});
</script>

<section class="overflow-hidden rounded-xl border bg-card">
	<header class="flex flex-wrap items-center justify-between gap-3 border-b bg-muted/30 px-4 py-3">
		<div class="flex min-w-0 items-center gap-3">
			<div
				class={cn(
					'flex size-9 shrink-0 items-center justify-center rounded-md border',
					TONE_BADGE[headlineTone]
				)}
			>
				{#if running}
					<Loader2 class="size-4 animate-spin" />
				{:else}
					<Stethoscope class="size-4" />
				{/if}
			</div>
			<div class="min-w-0">
				<h3 class="text-sm font-semibold">Diagnostics</h3>
				<p class="mt-0.5 truncate text-xs text-muted-foreground">
					{#if running}
						Running checks, this takes up to a minute
					{:else if report}
						{headline}
						<span class="text-muted-foreground/70">
							· {report.trigger} run {formatRelative(report.finishedAt)}
						</span>
					{:else if loading}
						Loading
					{:else}
						Checks run at startup and whenever a bundle is generated
					{/if}
				</p>
			</div>
		</div>
		<div class="flex shrink-0 items-center gap-2">
			{#if report}
				<button
					type="button"
					class={cn(
						'rounded-md border px-2 py-1 text-xs transition-colors',
						problemsOnly
							? 'border-primary/50 bg-primary/5 text-foreground'
							: 'text-muted-foreground hover:bg-accent/40'
					)}
					onclick={() => (problemsOnly = !problemsOnly)}
					disabled={problemCount === 0}
				>
					Problems only
				</button>
			{/if}
			<Button size="sm" variant="outline" onclick={run} disabled={running}>
				{#if running}
					<Loader2 class="size-4 animate-spin" />
					Running
				{:else}
					<RefreshCw class="size-4" />
					Run diagnostics
				{/if}
			</Button>
		</div>
	</header>

	{#if report}
		<div class="grid grid-cols-2 gap-px border-b bg-border sm:grid-cols-5">
			{#each [['Passed', report.passCount, 'ok'], ['Info', report.infoCount, 'sleep'], ['Warnings', report.warnCount, 'warn'], ['Failed', report.failCount, 'danger'], ['Skipped', report.skipCount, 'idle']] as [label, count, tone] (label)}
				<div class="flex items-center gap-2 bg-card px-4 py-2.5">
					<span class={cn('size-2 rounded-full', TONE_BG[tone as StatusTone])}></span>
					<span class="tabular text-sm font-semibold">{count}</span>
					<span class="text-xs text-muted-foreground">{label}</span>
				</div>
			{/each}
		</div>

		{#if groups.length === 0}
			<p class="px-4 py-6 text-center text-sm text-muted-foreground">No warnings or failures</p>
		{/if}

		{#each groups as group (group.category)}
			<div class="border-b last:border-b-0">
				<div class="flex items-center justify-between bg-muted/15 px-4 py-2">
					<span class="stat-label">{group.label}</span>
					{#if group.problems > 0}
						<span class={cn('text-xs', TONE_TEXT[headlineTone])}>
							{group.problems} to review
						</span>
					{/if}
				</div>
				{#each group.checks as check (check.id)}
					{@const ui = SEVERITY_UI[check.severity] ?? SEVERITY_UI[DiagnosticSeverity.UNSPECIFIED]}
					{@const Icon = ui.icon}
					{@const open = expanded.has(check.id)}
					<div class="border-t border-border/60">
						<button
							type="button"
							class="flex w-full cursor-pointer items-start gap-3 px-4 py-2.5 text-left transition-colors hover:bg-accent/30"
							onclick={() => toggle(check.id)}
							aria-expanded={open}
						>
							<Icon class={cn('mt-0.5 size-4 shrink-0', TONE_TEXT[ui.tone])} />
							<div class="min-w-0 flex-1">
								<div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
									<span class="text-sm font-medium">{check.title}</span>
									<span
										class={cn(
											'rounded-full border px-1.5 py-px text-[10px] font-medium uppercase',
											TONE_BADGE[ui.tone]
										)}
									>
										{enumLabelOr(DiagnosticSeveritySchema, check.severity)}
									</span>
									<span class="font-mono text-[10px] text-muted-foreground/60">{check.id}</span>
								</div>
								<p class="mt-0.5 text-xs text-muted-foreground">{check.summary}</p>
							</div>
							{#if open}
								<ChevronDown class="mt-1 size-4 shrink-0 text-muted-foreground" />
							{:else}
								<ChevronRight class="mt-1 size-4 shrink-0 text-muted-foreground" />
							{/if}
						</button>
						{#if open}
							<div
								transition:slide={{ duration: 150 }}
								class="space-y-3 bg-muted/10 px-4 pt-1 pb-4 pl-11"
							>
								{#if check.remedy}
									<div
										class={cn(
											'flex items-start gap-2 rounded-md border px-3 py-2 text-xs leading-relaxed',
											isProblem(check)
												? TONE_BADGE[ui.tone]
												: 'bg-background/60 text-muted-foreground'
										)}
									>
										<Wrench class="mt-0.5 size-3.5 shrink-0" />
										<div class="min-w-0 flex-1">
											<span class="text-foreground">{check.remedy}</span>
											{#if check.docsUrl}
												<!-- eslint-disable svelte/no-navigation-without-resolve -- external docs URL -->
												<a
													href={check.docsUrl}
													target="_blank"
													rel="noopener noreferrer"
													class="ml-1 inline-flex items-center gap-1 text-primary hover:underline"
												>
													Docs
													<ExternalLink class="size-3" />
												</a>
											{/if}
										</div>
									</div>
								{/if}
								{#if factRows(check).length > 0}
									<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
										{#each factRows(check) as [key, value] (key)}
											<dt class="font-mono text-muted-foreground">{key}</dt>
											<dd class="min-w-0 font-mono break-all">{value}</dd>
										{/each}
									</dl>
								{/if}
								{#if check.detail}
									<pre
										class="max-h-64 overflow-auto rounded-md border bg-background/60 p-3 font-mono text-[11px] leading-relaxed break-all whitespace-pre-wrap text-muted-foreground">{check.detail}</pre>
								{/if}
								<p class="text-[10px] text-muted-foreground/60">
									took {Number(check.durationMs)}ms
								</p>
							</div>
						{/if}
					</div>
				{/each}
			</div>
		{/each}
	{:else if loading}
		<div class="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
			<Loader2 class="size-4 animate-spin" />
			Loading diagnostics
		</div>
	{:else}
		<div class="px-4 py-8 text-center text-sm text-muted-foreground">
			No report yet. Run diagnostics to check DNS, ports, permissions, Docker, and upstream
			services.
		</div>
	{/if}
</section>
