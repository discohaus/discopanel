<script lang="ts">
	import { onDestroy } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import { Progress } from '$lib/components/ui/progress';
	import MotdText from '$lib/components/app/motd-text.svelte';
	import { rpcClient, rpcErrorMessage, silentCallOptions } from '$lib/api/rpc-client';
	import { uploadFile, cancelUpload, type UploadProgress } from '$lib/utils/chunked-upload';
	import { formatBytes } from '$lib/utils';
	import { enumLabel } from '$lib/proto-meta';
	import { ModLoaderSchema } from '$lib/proto/discopanel/v1/storage_pb';
	import type { ServerImportAnalysis } from '$lib/proto/discopanel/v1/server_pb';
	import {
		AlertTriangle,
		Archive,
		ChevronDown,
		ChevronUp,
		FileArchive,
		Loader2,
		Play,
		Upload,
		X
	} from '@lucide/svelte';

	let {
		sessionId = $bindable(''),
		analysis = $bindable<ServerImportAnalysis | null>(null),
		disabled = false,
		onanalyzed,
		oncleared
	}: {
		sessionId?: string;
		analysis?: ServerImportAnalysis | null;
		disabled?: boolean;
		onanalyzed?: (analysis: ServerImportAnalysis) => void;
		oncleared?: () => void;
	} = $props();

	const ACCEPTED = ['.zip', '.tar.gz', '.tgz', '.tar'];

	let fileInput = $state<HTMLInputElement | null>(null);
	let phase = $state<'idle' | 'uploading' | 'analyzing'>('idle');
	let progress = $state<UploadProgress | null>(null);
	let error = $state('');
	let fileName = $state('');
	let showProperties = $state(false);
	let abort: AbortController | null = null;
	// Session whose extracted tree waits on the server
	let staged = '';

	let busy = $derived(phase !== 'idle');
	let propertyEntries = $derived(
		Object.entries(analysis?.properties ?? {}).sort(([a], [b]) => a.localeCompare(b))
	);

	function accepted(name: string) {
		const lower = name.toLowerCase();
		return ACCEPTED.some((ext) => lower.endsWith(ext));
	}

	async function handleSelect(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		input.value = '';
		if (!file) return;
		if (!accepted(file.name)) {
			error = 'Choose a .zip, .tar.gz, .tgz or .tar archive of the whole server folder';
			return;
		}

		clear();
		error = '';
		fileName = file.name;
		phase = 'uploading';
		abort = new AbortController();
		try {
			const uploaded = await uploadFile(file, {
				onProgress: (p) => (progress = p),
				signal: abort.signal
			});
			phase = 'analyzing';
			const res = await rpcClient.server.analyzeServerImport({
				uploadSessionId: uploaded.sessionId
			});
			if (!res.analysis) throw new Error('Analysis returned nothing');
			staged = uploaded.sessionId;
			sessionId = uploaded.sessionId;
			analysis = res.analysis;
			onanalyzed?.(res.analysis);
		} catch (err) {
			if (err instanceof Error && err.message === 'Upload cancelled') {
				error = '';
			} else {
				error = rpcErrorMessage(err, 'Import failed');
			}
			fileName = '';
		} finally {
			phase = 'idle';
			progress = null;
			abort = null;
		}
	}

	function cancel() {
		abort?.abort();
		if (progress?.sessionId) cancelUpload(progress.sessionId).catch(() => {});
	}

	// Frees the staged tree, a create that consumed it makes this a no-op
	function discard() {
		if (!staged) return;
		const id = staged;
		staged = '';
		rpcClient.server
			.discardServerImport({ uploadSessionId: id }, silentCallOptions)
			.catch((err) => console.error('Failed to discard server import:', err));
	}

	// Drops an import the server already cleaned up
	export function expire(message: string) {
		staged = '';
		clear();
		error = message;
	}

	function clear() {
		discard();
		const had = !!analysis;
		sessionId = '';
		analysis = null;
		fileName = '';
		showProperties = false;
		if (had) oncleared?.();
	}

	onDestroy(() => {
		cancel();
		discard();
	});
</script>

<input
	bind:this={fileInput}
	type="file"
	accept=".zip,.tar,.tgz,.gz,application/zip,application/gzip,application/x-tar"
	class="hidden"
	onchange={handleSelect}
/>

{#if phase === 'uploading'}
	<div class="rounded-lg border bg-card p-4">
		<div class="mb-2 flex items-center justify-between gap-2">
			<span class="truncate text-sm font-medium">Uploading {fileName}...</span>
			<div class="flex shrink-0 items-center gap-2">
				<span class="tabular text-sm text-muted-foreground">
					{(progress?.percentComplete ?? 0).toFixed(0)}%
				</span>
				<Button
					type="button"
					size="icon"
					variant="ghost"
					class="size-6"
					onclick={cancel}
					title="Cancel upload"
				>
					<X class="size-4" />
				</Button>
			</div>
		</div>
		<Progress value={progress?.percentComplete ?? 0} class="h-2" />
		{#if progress}
			<p class="mt-1 text-xs text-muted-foreground">
				{formatBytes(progress.bytesUploaded)} / {formatBytes(progress.totalBytes)}
			</p>
		{/if}
	</div>
{:else if phase === 'analyzing'}
	<div class="flex items-center gap-3 rounded-lg border bg-card p-4 text-sm">
		<Loader2 class="size-4 animate-spin text-primary" />
		<div>
			<p class="font-medium">Extracting and analyzing {fileName}</p>
			<p class="text-xs text-muted-foreground">Large servers can take a few minutes</p>
		</div>
	</div>
{:else if analysis}
	<div class="space-y-3 rounded-lg border border-primary/30 bg-primary/5 p-4">
		<div class="flex items-start gap-3">
			<div class="flex size-10 shrink-0 items-center justify-center rounded-md bg-primary/10">
				<FileArchive class="size-5 text-primary" />
			</div>
			<div class="min-w-0 flex-1">
				<h4 class="truncate font-semibold">{analysis.archiveName || 'Server archive'}</h4>
				<div class="mt-1.5 flex flex-wrap gap-1.5">
					<Badge variant="secondary" class="text-xs">
						{enumLabel(ModLoaderSchema, analysis.modLoader)}
						{analysis.loaderVersion}
					</Badge>
					<Badge variant={analysis.mcVersion ? 'secondary' : 'outline'} class="text-xs">
						{analysis.mcVersion ? `MC ${analysis.mcVersion}` : 'MC version unknown'}
					</Badge>
					<Badge variant="outline" class="text-xs">{analysis.fileCount} files</Badge>
					{#if analysis.modCount > 0}
						<Badge variant="outline" class="text-xs">{analysis.modCount} mods</Badge>
					{/if}
					{#if analysis.pluginCount > 0}
						<Badge variant="outline" class="text-xs">{analysis.pluginCount} plugins</Badge>
					{/if}
				</div>
			</div>
			<Button
				type="button"
				variant="ghost"
				size="icon"
				class="size-7 shrink-0"
				onclick={clear}
				{disabled}
				title="Remove import"
			>
				<X class="size-4" />
			</Button>
		</div>

		<dl class="grid gap-x-4 gap-y-1.5 text-xs sm:grid-cols-[8rem_minmax(0,1fr)]">
			<dt class="flex items-center gap-1.5 text-muted-foreground">
				<Play class="size-3" /> Starts from
			</dt>
			<dd class="truncate font-mono" title={analysis.launchTarget}>
				{analysis.launchTarget || 'Nothing launchable found'}
			</dd>
			<dt class="flex items-center gap-1.5 text-muted-foreground">
				<Archive class="size-3" /> World
			</dt>
			<dd class="truncate">
				{analysis.levelName}{analysis.hasWorld ? '' : ' (not in archive, generates on start)'}
			</dd>
			{#if analysis.motd}
				<dt class="text-muted-foreground">MOTD</dt>
				<dd class="truncate"><MotdText motd={analysis.motd.replace(/\\u00a7/gi, '§')} /></dd>
			{/if}
		</dl>

		{#if propertyEntries.length > 0}
			<div>
				<button
					type="button"
					class="flex items-center gap-1 text-xs text-primary hover:underline"
					onclick={() => (showProperties = !showProperties)}
				>
					{propertyEntries.length} server.properties settings carry over
					{#if showProperties}
						<ChevronUp class="size-3" />
					{:else}
						<ChevronDown class="size-3" />
					{/if}
				</button>
				{#if showProperties}
					<div class="mt-2 max-h-48 overflow-y-auto rounded-md border bg-background/60 p-2">
						<dl
							class="grid grid-cols-[minmax(0,12rem)_minmax(0,1fr)] gap-x-3 gap-y-1 font-mono text-[11px]"
						>
							{#each propertyEntries as [key, value] (key)}
								<dt class="truncate text-muted-foreground" title={key}>{key}</dt>
								<dd class="truncate" title={value}>{value || '(empty)'}</dd>
							{/each}
						</dl>
					</div>
				{/if}
			</div>
		{/if}

		{#if analysis.warnings.length > 0}
			<ul class="space-y-1 rounded-md bg-status-warn/10 p-2.5 text-xs text-status-warn">
				{#each analysis.warnings as warning (warning)}
					<li class="flex gap-1.5">
						<AlertTriangle class="mt-0.5 size-3 shrink-0" />
						<span>{warning}</span>
					</li>
				{/each}
			</ul>
		{/if}

		<p class="text-xs text-muted-foreground">
			Files are kept as they are. DiscoPanel only manages server.properties, eula.txt, ops and
			whitelist, and never reinstalls the loader unless you change the version or force a
			reprovision.
		</p>
	</div>
{:else}
	<button
		type="button"
		class="flex w-full flex-col items-center gap-2 rounded-lg border border-dashed p-6 text-center transition-colors hover:bg-accent/30"
		onclick={() => fileInput?.click()}
		{disabled}
	>
		<Upload class="size-5 text-muted-foreground" />
		<span class="text-sm font-medium">Choose a server archive</span>
		<span class="text-xs text-muted-foreground">
			A .zip or .tar.gz of the whole server folder, as exported from Crafty, Pterodactyl or a plain
			backup. Loader, version and settings are detected automatically.
		</span>
	</button>
{/if}

{#if error && !busy}
	<p class="rounded-md bg-status-danger/10 px-2.5 py-1.5 text-xs text-status-danger">{error}</p>
{/if}
