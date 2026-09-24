<script lang="ts">
	import { ListInput } from '$lib/components/app';
	import { addressScope, suggestionsFor, validHostname } from '$lib/hostname';
	import { Globe, Wifi } from '@lucide/svelte';

	let {
		hostnames = $bindable([]),
		label = '',
		suggestionBase = '',
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
		// Base domain suggested names derive from
		suggestionBase?: string;
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

	let suggestLabel = $derived.by(() => {
		const typed = (label || '').trim().toLowerCase();
		return typed;
	});

	let matches = $derived.by(() => {
		if (requireLabel && !suggestLabel) return [];
		return suggestionsFor(suggestLabel, suggestionBase).filter(
			(name) => !hostnames.includes(name)
		);
	});

	function normalize(raw: string): string {
		return raw.trim().toLowerCase().split(':')[0].replace(/\.$/, '');
	}

	function validate(name: string): string | undefined {
		if (!validHostname(name)) {
			return `${name} is not a valid hostname`;
		}
		return undefined;
	}
</script>

<ListInput
	bind:items={hostnames}
	{disabled}
	{placeholder}
	{inputId}
	{error}
	emptyText="No hostnames yet"
	removeTitle="Remove hostname"
	{copyable}
	shown={addressFor}
	{normalize}
	{validate}
	suggestions={matches}
	{onchange}
>
	{#snippet renderSuggestion(name: string)}
		{#if addressScope(name) === 'LAN'}
			<Wifi class="size-2.5 shrink-0 text-muted-foreground" />
		{:else if addressScope(name) === 'Public'}
			<Globe class="size-2.5 shrink-0 text-muted-foreground" />
		{/if}
		{name}
	{/snippet}
</ListInput>
