<script lang="ts">
	import { rpcClient, rpcErrorMessage } from '$lib/api/rpc-client';
	import {
		Dialog,
		DialogContent,
		DialogDescription,
		DialogFooter,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Textarea } from '$lib/components/ui/textarea';
	import { certificatesStore } from '$lib/stores/certificates.svelte';
	import { certNameMatches, parseUpload, type ParsedUpload } from '$lib/certs';
	import { toast } from 'svelte-sonner';
	import {
		Check,
		FileUp,
		KeyRound,
		Loader2,
		Lock,
		RotateCcw,
		ShieldCheck,
		TriangleAlert
	} from '@lucide/svelte';

	let {
		open = $bindable(false),
		hostnames = [],
		onAdded
	}: {
		open?: boolean;
		// Addresses in use, coverage is reported against these
		hostnames?: string[];
		onAdded?: () => void;
	} = $props();

	let raw = $state('');
	let parsed = $state<ParsedUpload | null>(null);
	let parsing = $state(false);
	let saving = $state(false);
	let dragging = $state(false);
	let fileInput = $state<HTMLInputElement | null>(null);

	// Fresh sheet every time the dialog opens
	$effect(() => {
		if (open) reset();
	});

	// Reparse follows every paste and dropped file
	$effect(() => {
		const text = raw;
		if (!text.trim()) {
			parsed = null;
			parsing = false;
			return;
		}
		parsing = true;
		parseUpload(text)
			.then((result) => {
				if (raw === text) parsed = result;
			})
			.finally(() => {
				if (raw === text) parsing = false;
			});
	});

	let hasCert = $derived(!!parsed?.certPem);
	let hasKey = $derived(!!parsed?.keyPem);
	// Summary replaces the input once something readable lands
	let reading = $derived(hasCert || hasKey);
	let ready = $derived(hasCert && hasKey && parsed?.keyStatus !== 'mismatch' && !parsed?.error);

	let covered = $derived.by(() => {
		if (!parsed?.names.length) return [];
		return hostnames.filter((host) => parsed!.names.some((name) => certNameMatches(name, host)));
	});

	function reset() {
		raw = '';
		parsed = null;
		parsing = false;
		dragging = false;
	}

	async function addFiles(list: FileList | null) {
		if (!list?.length) return;
		const texts = await Promise.all([...list].map((file) => file.text()));
		raw = [raw.trim(), ...texts].filter(Boolean).join('\n');
	}

	function handleDrop(event: DragEvent) {
		event.preventDefault();
		dragging = false;
		addFiles(event.dataTransfer?.files ?? null);
	}

	function handleDragOver(event: DragEvent) {
		event.preventDefault();
		dragging = true;
	}

	function formatDate(date: Date): string {
		return date.toLocaleDateString(undefined, { dateStyle: 'medium' });
	}

	async function save() {
		if (!parsed || !ready) return;
		saving = true;
		try {
			await rpcClient.proxy.uploadCertificate({ certPem: parsed.certPem, keyPem: parsed.keyPem });
			toast.success('Certificate added');
			await certificatesStore.refresh();
			open = false;
			onAdded?.();
		} catch (error: unknown) {
			toast.error(rpcErrorMessage(error, 'Failed to add the certificate'));
		} finally {
			saving = false;
		}
	}
</script>

<Dialog bind:open>
	<DialogContent class="sm:max-w-lg">
		<DialogHeader>
			<DialogTitle>Add a certificate</DialogTitle>
			<DialogDescription>Serves your web addresses over https.</DialogDescription>
		</DialogHeader>

		<input
			bind:this={fileInput}
			type="file"
			multiple
			accept=".pem,.crt,.cer,.key,.chain,.ca-bundle,.txt"
			class="hidden"
			onchange={(e) => {
				addFiles(e.currentTarget.files);
				e.currentTarget.value = '';
			}}
		/>

		<div
			class="space-y-3"
			ondragover={handleDragOver}
			ondragleave={() => (dragging = false)}
			ondrop={handleDrop}
			role="group"
		>
			{#if parsing && !reading}
				<div
					class="flex h-40 flex-col items-center justify-center gap-2 rounded-xl border border-dashed text-sm text-muted-foreground"
				>
					<Loader2 class="size-5 animate-spin" />
					Reading what you gave me
				</div>
			{:else if !reading}
				<button
					type="button"
					onclick={() => fileInput?.click()}
					class="flex h-40 w-full flex-col items-center justify-center gap-1.5 rounded-xl border-2 border-dashed transition-colors hover:border-primary/60 hover:bg-accent/40 {dragging
						? 'border-primary bg-primary/5'
						: ''}"
				>
					<FileUp class="size-6 text-muted-foreground" />
					<span class="text-sm font-medium">Drop your certificate and key files here</span>
					<span class="text-xs text-muted-foreground">Or click to pick them</span>
				</button>

				<div class="flex items-center gap-3 text-[11px] text-muted-foreground">
					<span class="h-px flex-1 bg-border"></span>
					or paste the text
					<span class="h-px flex-1 bg-border"></span>
				</div>
				<Textarea
					bind:value={raw}
					spellcheck={false}
					placeholder="-----BEGIN CERTIFICATE-----"
					class="field-sizing-fixed h-20 min-h-0 resize-none overflow-y-auto font-mono text-[11px] md:text-[11px]"
				/>
			{:else}
				<div class="rounded-xl border p-3.5 {hasCert ? '' : 'border-dashed'}">
					<div class="flex items-center gap-2">
						{#if hasCert}
							<ShieldCheck class="size-4 shrink-0 text-status-ok" />
						{:else}
							<ShieldCheck class="size-4 shrink-0 text-muted-foreground" />
						{/if}
						<span class="text-sm font-medium">Certificate</span>
						{#if hasCert}
							<Check class="ml-auto size-4 text-status-ok" />
						{:else}
							<span class="ml-auto text-xs text-muted-foreground">still needed</span>
						{/if}
					</div>
					{#if hasCert && parsed}
						<p class="mt-2 font-mono text-xs break-all">
							{parsed.names.join(', ') || 'Unnamed certificate'}
						</p>
						<p class="mt-1 text-xs text-muted-foreground">
							{#if parsed.notAfter}Expires {formatDate(parsed.notAfter)}{/if}
							{#if parsed.notAfter && parsed.issuer}·{/if}
							{#if parsed.issuer}Issued by {parsed.issuer}{/if}
						</p>
					{/if}
				</div>

				<div class="rounded-xl border p-3.5 {hasKey ? '' : 'border-dashed'}">
					<div class="flex items-center gap-2">
						<KeyRound
							class="size-4 shrink-0 {hasKey ? 'text-status-ok' : 'text-muted-foreground'}"
						/>
						<span class="text-sm font-medium">Private key</span>
						{#if parsed?.keyStatus === 'mismatch'}
							<TriangleAlert class="ml-auto size-4 text-status-danger" />
						{:else if hasKey}
							<Check class="ml-auto size-4 text-status-ok" />
						{:else}
							<span class="ml-auto text-xs text-muted-foreground">still needed</span>
						{/if}
					</div>
					<p class="mt-2 text-xs text-muted-foreground">
						{#if parsed?.keyStatus === 'match'}
							Belongs to this certificate
						{:else if parsed?.keyStatus === 'mismatch'}
							Belongs to a different certificate
						{:else if hasKey}
							Added, pairing not confirmed
						{:else}
							Add the .key file that came with the certificate
						{/if}
					</p>
				</div>

				{#if !ready && !parsed?.error}
					<button
						type="button"
						onclick={() => fileInput?.click()}
						class="flex w-full items-center justify-center gap-2 rounded-xl border-2 border-dashed p-3 text-xs text-muted-foreground transition-colors hover:border-primary/60 hover:text-foreground {dragging
							? 'border-primary bg-primary/5 text-foreground'
							: ''}"
					>
						<FileUp class="size-4" />
						Drop or pick the missing file
					</button>
					<Textarea
						bind:value={raw}
						spellcheck={false}
						class="field-sizing-fixed h-16 min-h-0 resize-none overflow-y-auto font-mono text-[11px] md:text-[11px]"
					/>
				{/if}

				{#if hasCert && !parsed?.error}
					{#if covered.length > 0}
						<p class="flex items-start gap-2 text-xs text-muted-foreground">
							<Lock class="mt-0.5 size-3.5 shrink-0 text-status-ok" />
							<span>Secures <span class="font-mono">{covered.join(', ')}</span></span>
						</p>
					{:else if hostnames.length > 0}
						<p class="flex items-start gap-2 text-xs text-status-busy">
							<TriangleAlert class="mt-0.5 size-3.5 shrink-0" />
							<span>None of your addresses match this certificate yet</span>
						</p>
					{/if}
				{/if}
			{/if}

			{#if parsed?.error}
				<p class="flex items-start gap-2 text-xs text-status-danger">
					<TriangleAlert class="mt-0.5 size-3.5 shrink-0" />
					<span>{parsed.error}</span>
				</p>
			{/if}
		</div>

		<DialogFooter class="sm:justify-between">
			<Button variant="ghost" size="sm" onclick={reset} disabled={!raw.trim() || saving}>
				<RotateCcw class="size-4" />
				Start over
			</Button>
			<div class="flex items-center gap-2">
				<Button variant="outline" onclick={() => (open = false)} disabled={saving}>Cancel</Button>
				<Button onclick={save} disabled={!ready || saving}>
					{#if saving}
						<Loader2 class="size-4 animate-spin" />
					{:else}
						<Lock class="size-4" />
					{/if}
					Add certificate
				</Button>
			</div>
		</DialogFooter>
	</DialogContent>
</Dialog>
