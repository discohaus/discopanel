<script lang="ts">
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { Input } from '$lib/components/ui/input';
	import { Button } from '$lib/components/ui/button';
	import { HostnameScope, type HostnameSuggestion } from '$lib/proto/discopanel/v1/proxy_pb';
	import { validHostname } from '$lib/hostname';
	import { portal } from '$lib/portal';
	import { CopyButton } from '$lib/components/app';
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
	let open = $state(false);
	let suggestions = $state<HostnameSuggestion[]>([]);
	let draftError = $state('');
	let inputWrap = $state<HTMLDivElement | null>(null);
	let menuPos = $state({ top: 0, left: 0, width: 0 });

	// Fixed menu tracks the input through scrolling ancestors
	function placeMenu() {
		if (!inputWrap) return;
		const rect = inputWrap.getBoundingClientRect();
		menuPos = { top: rect.bottom + 4, left: rect.left, width: rect.width };
	}

	$effect(() => {
		if (!open) return;
		// Row count changes move the input, track both lists
		void hostnames.length;
		void visible.length;
		placeMenu();
		window.addEventListener('scroll', placeMenu, true);
		window.addEventListener('resize', placeMenu);
		return () => {
			window.removeEventListener('scroll', placeMenu, true);
			window.removeEventListener('resize', placeMenu);
		};
	});

	// Typed labels without a dot suggest full names live
	let fetchLabel = $derived.by(() => {
		const typed = draft.trim().toLowerCase();
		if (typed && !typed.includes('.')) return typed;
		return label.trim().toLowerCase();
	});

	// Display filter waits for the same debounce as fetches
	let debouncedTyped = $state('');

	// One debounce gates fetching and refiltering alike
	$effect(() => {
		if (!open) return;
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
		}, 400);
		return () => clearTimeout(timer);
	});

	let visible = $derived.by(() => {
		const typed = debouncedTyped;
		return suggestions.filter(
			(s) => !hostnames.includes(s.hostname) && (!typed || s.hostname.includes(typed))
		);
	});

	function add(name: string) {
		// Pasted addresses drop ports and trailing dots
		name = name.trim().toLowerCase().split(':')[0].replace(/\.$/, '');
		if (!name || hostnames.includes(name)) return;
		if (!validHostname(name)) {
			draftError = 'Invalid hostname format';
			return;
		}
		hostnames = [...hostnames, name];
		draft = '';
		draftError = '';
		onchange?.(hostnames);
	}

	function remove(name: string) {
		hostnames = hostnames.filter((h) => h !== name);
		onchange?.(hostnames);
	}

	// Bare labels take a suggestion actually carrying them
	function commit() {
		const typed = draft.trim().toLowerCase();
		if (typed && !typed.includes('.')) {
			const match = suggestions.find(
				(s) => !hostnames.includes(s.hostname) && s.hostname.startsWith(typed + '.')
			);
			if (match) {
				add(match.hostname);
				return;
			}
		}
		add(draft);
	}

	function onKeydown(event: KeyboardEvent) {
		if (event.key === 'Enter') {
			event.preventDefault();
			commit();
		} else if (event.key === 'Escape') {
			open = false;
		}
	}
</script>

<div class="space-y-1.5">
	<div class="divide-y rounded-lg border {draftError || error ? 'border-destructive' : ''}">
		{#each hostnames as name (name)}
			{@const shown = addressFor ? addressFor(name) : name}
			<div class="flex items-center gap-2 bg-muted/30 py-1.5 pr-1.5 pl-3">
				<span class="min-w-0 flex-1 truncate font-mono text-sm" title={shown}>{shown}</span>
				{#if copyable}
					<CopyButton text={shown} label="Copy address" />
				{/if}
				<Button
					variant="ghost"
					size="icon"
					class="size-7 text-muted-foreground hover:text-destructive"
					onclick={() => remove(name)}
					{disabled}
					title="Remove hostname"
				>
					<X class="size-3.5" />
				</Button>
			</div>
		{/each}

		<div bind:this={inputWrap}>
			<div class="flex items-center">
				<Input
					id={inputId}
					{placeholder}
					bind:value={draft}
					{disabled}
					autocomplete="off"
					onfocus={() => (open = true)}
					oninput={() => {
						open = true;
						draftError = '';
					}}
					onblur={() => setTimeout(() => (open = false), 150)}
					onkeydown={onKeydown}
					class="min-w-0 flex-1 border-0 font-mono shadow-none focus-visible:ring-0"
				/>
				<Button
					type="button"
					variant="ghost"
					size="sm"
					class="mr-1 shrink-0 text-muted-foreground"
					onclick={commit}
					disabled={disabled || !draft.trim()}
				>
					<Plus class="size-3.5" />
					Add
				</Button>
			</div>

			{#if open && visible.length > 0}
				<div
					use:portal
					style="top: {menuPos.top}px; left: {menuPos.left}px; width: {menuPos.width}px"
					class="fixed z-[60] max-h-56 divide-y overflow-y-auto rounded-lg border bg-popover shadow-md"
				>
					{#each visible as suggestion (suggestion.hostname)}
						<button
							type="button"
							onmousedown={(e) => {
								e.preventDefault();
								add(suggestion.hostname);
							}}
							class="flex w-full items-center justify-between gap-2 px-3 py-1.5 text-left transition-colors hover:bg-accent/60"
						>
							<span class="truncate font-mono text-xs">
								{addressFor ? addressFor(suggestion.hostname) : suggestion.hostname}
							</span>
							{#if suggestion.scope === HostnameScope.LAN}
								<span class="flex shrink-0 items-center gap-1 text-[10px] text-muted-foreground">
									<Wifi class="size-2.5" />
									LAN
								</span>
							{:else if suggestion.scope === HostnameScope.PUBLIC}
								<span class="flex shrink-0 items-center gap-1 text-[10px] text-muted-foreground">
									<Globe class="size-2.5" />
									Public
								</span>
							{/if}
						</button>
					{/each}
				</div>
			{/if}
		</div>
	</div>

	{#if draftError || error}
		<p class="text-xs text-destructive">{draftError || error}</p>
	{/if}
</div>
