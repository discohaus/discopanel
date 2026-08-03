<script lang="ts">
	import { Portal } from 'bits-ui';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { Input } from '$lib/components/ui/input';
	import { Button } from '$lib/components/ui/button';
	import { HostnameScope, type HostnameSuggestion } from '$lib/proto/discopanel/v1/proxy_pb';
	import { validHostname } from '$lib/hostname';
	import { CopyButton } from '$lib/components/app';
	import { certificatesStore } from '$lib/stores/certificates.svelte';
	import { ChevronDown, Globe, Lock, Plus, Wifi, X } from '@lucide/svelte';

	// Https marks need the coverage list loaded once
	certificatesStore.ensure();

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
	let focused = $state(false);
	let suggestions = $state<HostnameSuggestion[]>([]);
	let draftError = $state('');
	let draftInput = $state<HTMLInputElement | null>(null);
	let shell = $state<HTMLDivElement | null>(null);
	let menu = $state<HTMLDivElement | null>(null);

	// Menu leaves the card so nothing can clip it
	let pos = $state({ left: 0, top: 0, width: 0, flip: false, max: 320 });

	// Overlays host their own menu, everything else uses body
	function container(): HTMLElement {
		const overlay = shell?.closest('[data-slot="dialog-content"], [data-slot="sheet-content"]');
		return (overlay as HTMLElement | null) ?? document.body;
	}

	let target = $derived<HTMLElement | string>(open ? container() : 'body');

	function measure() {
		if (!shell) return;
		const host = container();
		const rect = shell.getBoundingClientRect();
		const body = host === document.body;
		const hostRect = host.getBoundingClientRect();
		// Absolute coordinates measured against the hosting box
		const originLeft = body ? -window.scrollX : hostRect.left;
		const originTop = body ? -window.scrollY : hostRect.top;
		const boundsTop = body ? 0 : hostRect.top;
		const boundsBottom = body ? window.innerHeight : hostRect.bottom;
		const below = boundsBottom - rect.bottom - 12;
		const above = rect.top - boundsTop - 12;
		const flip = below < 180 && above > below;
		pos = {
			left: rect.left - originLeft,
			top: flip ? rect.top - 4 - originTop : rect.bottom + 4 - originTop,
			width: rect.width,
			flip,
			max: Math.max(120, Math.min(320, flip ? above : below))
		};
	}

	function openMenu() {
		measure();
		open = true;
	}

	// Scrolling anywhere keeps the menu glued to the field
	$effect(() => {
		if (!open) return;
		measure();
		const track = () => measure();
		window.addEventListener('scroll', track, true);
		window.addEventListener('resize', track);
		return () => {
			window.removeEventListener('scroll', track, true);
			window.removeEventListener('resize', track);
		};
	});

	const shown = (name: string) => (addressFor ? addressFor(name) : name);

	// Field mirrors the newest name plus the rest
	let newest = $derived(hostnames.length > 0 ? hostnames[hostnames.length - 1] : '');
	let preview = $derived.by(() => {
		if (!newest) return 'No hostnames configured';
		if (hostnames.length === 1) return shown(newest);
		return `${shown(newest)} +${hostnames.length - 1}`;
	});

	// Typing hint replaces the preview once focused
	let previewing = $derived(!focused && !draft && hostnames.length > 0);
	let fieldPlaceholder = $derived(focused || draft ? placeholder : preview);

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
		draftInput?.focus();
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

	// Leaving the field and its menu closes the drop down
	function onFocusOut(event: FocusEvent) {
		const next = event.relatedTarget as globalThis.Node | null;
		if (next && (shell?.contains(next) || menu?.contains(next))) return;
		focused = false;
		open = false;
	}
</script>

<div class="space-y-1.5">
	<div bind:this={shell} onfocusout={onFocusOut}>
		<div
			class="flex h-9 w-full items-center rounded-md border bg-background pr-1 dark:bg-input/30 {draftError ||
			error
				? 'border-destructive'
				: 'border-input'} {focused ? 'border-ring ring-[3px] ring-ring/50' : ''}"
		>
			<Input
				id={inputId}
				bind:ref={draftInput}
				bind:value={draft}
				{disabled}
				placeholder={fieldPlaceholder}
				autocomplete="off"
				onfocus={() => {
					focused = true;
					openMenu();
				}}
				oninput={() => {
					openMenu();
					draftError = '';
				}}
				onkeydown={onKeydown}
				class="h-9 min-w-0 flex-1 rounded-none border-0 bg-transparent font-mono text-xs shadow-none focus-visible:ring-0 dark:bg-transparent {previewing
					? 'placeholder:text-foreground'
					: ''}"
			/>
			{#if previewing && certificatesStore.isSecured(newest)}
				<Lock class="mr-1 size-3.5 shrink-0 text-emerald-500" aria-label="Secured" />
			{/if}
			{#if draft.trim()}
				<Button
					type="button"
					variant="ghost"
					size="sm"
					class="shrink-0 text-muted-foreground"
					onclick={commit}
					{disabled}
				>
					<Plus class="size-3.5" />
					Add
				</Button>
			{:else}
				<Button
					type="button"
					variant="ghost"
					size="icon"
					class="size-7 shrink-0 text-muted-foreground"
					onclick={() => {
						if (open) {
							open = false;
							focused = false;
						} else {
							openMenu();
							draftInput?.focus();
						}
					}}
					{disabled}
					title={open ? 'Hide hostnames' : 'Show hostnames'}
				>
					<ChevronDown class="size-3.5 transition-transform {open ? 'rotate-180' : ''}" />
				</Button>
			{/if}
		</div>
	</div>

	{#if open}
		<Portal to={target}>
			<div
				bind:this={menu}
				onfocusout={onFocusOut}
				class="absolute z-50 flex flex-col overflow-hidden rounded-md border bg-popover shadow-md {pos.flip
					? '-translate-y-full'
					: ''}"
				style="left: {pos.left}px; top: {pos.top}px; width: {pos.width}px; max-height: {pos.max}px"
			>
				{#if hostnames.length > 0}
					<div class="min-h-0 flex-1 divide-y overflow-y-auto">
						{#each hostnames as name (name)}
							<div class="flex items-center gap-2 py-1.5 pr-1.5 pl-3">
								<span class="min-w-0 flex-1 truncate font-mono text-xs">{shown(name)}</span>
								{#if certificatesStore.isSecured(name)}
									<Lock class="size-3.5 shrink-0 text-emerald-500" aria-label="Secured" />
								{/if}
								{#if copyable}
									<CopyButton text={shown(name)} label="Copy address" />
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
					</div>
				{:else}
					<p class="shrink-0 px-3 py-2 text-xs text-muted-foreground">Nothing added yet</p>
				{/if}

				{#if visible.length > 0}
					<div class="max-h-32 shrink-0 overflow-y-auto border-t bg-muted/20 p-2">
						<span class="block pb-1 text-[10px] text-muted-foreground">Suggested</span>
						<div class="flex flex-wrap gap-1">
							{#each visible as suggestion (suggestion.hostname)}
								<button
									type="button"
									onclick={() => add(suggestion.hostname)}
									class="flex items-center gap-1 rounded-md border bg-background px-1.5 py-0.5 font-mono text-[11px] transition-colors hover:border-primary/50 hover:bg-accent/60"
								>
									{#if suggestion.scope === HostnameScope.LAN}
										<Wifi class="size-2.5 shrink-0 text-muted-foreground" />
									{:else if suggestion.scope === HostnameScope.PUBLIC}
										<Globe class="size-2.5 shrink-0 text-muted-foreground" />
									{/if}
									{suggestion.hostname}
								</button>
							{/each}
						</div>
					</div>
				{/if}
			</div>
		</Portal>
	{/if}

	{#if draftError || error}
		<p class="text-xs text-destructive">{draftError || error}</p>
	{/if}
</div>
