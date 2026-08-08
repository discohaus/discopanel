<script lang="ts">
	import { slide } from 'svelte/transition';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { Input } from '$lib/components/ui/input';
	import { Button } from '$lib/components/ui/button';
	import { HostnameScope, type HostnameSuggestion } from '$lib/proto/discopanel/v1/proxy_pb';
	import { validHostname } from '$lib/hostname';
	import { CardStack, CopyButton } from '$lib/components/app';
	import { Globe, Plus, Wifi, X } from '@lucide/svelte';

	let {
		hostnames = $bindable([]),
		label = '',
		disabled = false,
		placeholder = 'mc.example.com',
		inputId = undefined,
		error = '',
		addressFor = undefined,
		copyable = false,
		requireLabel = false,
		onchange
	}: {
		hostnames?: string[];
		// Subdomain label suggestions derive under
		label?: string;
		disabled?: boolean;
		placeholder?: string;
		inputId?: string;
		error?: string;
		// Turns a name into the full joinable address
		addressFor?: (name: string) => string;
		copyable?: boolean;
		// Never suggests bare base names without a label
		requireLabel?: boolean;
		onchange?: (hostnames: string[]) => void;
	} = $props();

	let draft = $state('');
	let draftError = $state('');
	let draftInput = $state<HTMLInputElement | null>(null);
	let suggestions = $state<HostnameSuggestion[]>([]);

	// Display filter waits for the same debounce as fetches
	let debouncedTyped = $state('');

	const shown = (name: string) => (addressFor ? addressFor(name) : name);

	// Typed labels without a dot suggest full names live
	let fetchLabel = $derived.by(() => {
		const typed = draft.trim().toLowerCase();
		if (typed && !typed.includes('.')) return typed;
		return label.trim().toLowerCase();
	});

	// One debounce gates fetching and refiltering alike
	$effect(() => {
		const current = fetchLabel;
		const typed = draft.trim().toLowerCase();
		// Server inputs never offer unlabeled base names
		if (requireLabel && !current) {
			suggestions = [];
			debouncedTyped = '';
			return;
		}
		const timer = setTimeout(async () => {
			debouncedTyped = typed;
			try {
				const res = await rpcClient.proxy.getHostnameSuggestions(
					{ label: current },
					silentCallOptions
				);
				suggestions = res.suggestions;
			} catch {
				suggestions = [];
			}
		}, 300);
		return () => clearTimeout(timer);
	});

	let matches = $derived.by(() => {
		const typed = debouncedTyped;
		return suggestions.filter(
			(s) => !hostnames.includes(s.hostname) && (!typed || s.hostname.includes(typed))
		);
	});

	// Pasted addresses drop ports and trailing dots
	function normalize(raw: string): string {
		return raw.trim().toLowerCase().split(':')[0].replace(/\.$/, '');
	}

	function addName(raw: string): boolean {
		const name = normalize(raw);
		if (!name || hostnames.includes(name)) return true;
		if (!validHostname(name)) {
			draftError = `${name} is not a valid hostname`;
			return false;
		}
		hostnames = [...hostnames, name];
		onchange?.(hostnames);
		return true;
	}

	// Bare labels take a suggestion actually carrying them
	function commit() {
		const typed = draft.trim().toLowerCase();
		if (!typed) return;
		draftError = '';
		const tokens = typed.split(/[\s,]+/).filter(Boolean);
		if (tokens.length === 1 && !typed.includes('.')) {
			const match = suggestions.find(
				(s) => !hostnames.includes(s.hostname) && s.hostname.startsWith(typed + '.')
			);
			if (match) {
				addName(match.hostname);
				draft = '';
				draftInput?.focus();
				return;
			}
		}
		// Rejected pieces stay in the field for fixing
		const kept = tokens.filter((t) => !addName(t));
		draft = kept.join(' ');
		draftInput?.focus();
	}

	function addSuggestion(name: string) {
		draftError = '';
		if (addName(name)) draft = '';
	}

	function remove(name: string) {
		hostnames = hostnames.filter((h) => h !== name);
		onchange?.(hostnames);
	}

	function onKeydown(event: KeyboardEvent) {
		if (event.key === 'Enter') {
			event.preventDefault();
			commit();
		}
	}
</script>

<div class="space-y-1.5">
	<CardStack
		items={hostnames}
		visible={2}
		slotHeight="2rem"
		gap="0.25rem"
		itemKey={(n: string) => n}
	>
		{#snippet card(name: string)}
			<div
				class="flex h-full items-center gap-2 rounded-md pr-0.5 pl-2.5 transition-colors hover:bg-accent/40"
			>
				<span class="min-w-0 flex-1 truncate font-mono text-xs">{shown(name)}</span>
				{#if copyable}
					<CopyButton text={shown(name)} label="Copy address" />
				{/if}
				<Button
					type="button"
					variant="ghost"
					size="icon"
					class="size-7 shrink-0 text-muted-foreground hover:text-destructive"
					onclick={() => remove(name)}
					{disabled}
					title="Remove hostname"
				>
					<X class="size-3.5" />
				</Button>
			</div>
		{/snippet}
		{#snippet empty()}
			<div class="flex h-full items-center justify-center text-xs text-muted-foreground">
				No hostnames yet
			</div>
		{/snippet}
	</CardStack>

	<div class="flex items-center gap-2">
		<Input
			id={inputId}
			bind:ref={draftInput}
			bind:value={draft}
			{disabled}
			{placeholder}
			autocomplete="off"
			oninput={() => (draftError = '')}
			onkeydown={onKeydown}
			class="h-9 min-w-0 flex-1 font-mono text-xs {draftError || error ? 'border-destructive' : ''}"
		/>
		<Button
			type="button"
			variant="outline"
			class="h-9 shrink-0"
			onclick={commit}
			disabled={disabled || !draft.trim()}
		>
			<Plus class="size-3.5" />
			Add
		</Button>
	</div>

	{#if draftError || error || matches.length > 0}
		<div
			transition:slide={{ duration: 150 }}
			class="flex h-6 items-center gap-1 overflow-x-auto whitespace-nowrap"
		>
			{#if draftError || error}
				<p class="truncate text-xs text-destructive">{draftError || error}</p>
			{:else}
				<span class="shrink-0 pr-1 text-[10px] text-muted-foreground">Suggested</span>
				{#each matches as suggestion (suggestion.hostname)}
					<button
						type="button"
						onclick={() => addSuggestion(suggestion.hostname)}
						{disabled}
						class="flex shrink-0 items-center gap-1 rounded-md border bg-background px-1.5 py-px font-mono text-[11px] transition-colors hover:border-primary/50 hover:bg-accent/60"
					>
						{#if suggestion.scope === HostnameScope.LAN}
							<Wifi class="size-2.5 shrink-0 text-muted-foreground" />
						{:else if suggestion.scope === HostnameScope.PUBLIC}
							<Globe class="size-2.5 shrink-0 text-muted-foreground" />
						{/if}
						{suggestion.hostname}
					</button>
				{/each}
			{/if}
		</div>
	{/if}
</div>
