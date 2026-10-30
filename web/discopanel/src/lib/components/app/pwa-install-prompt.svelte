<script lang="ts">
	import { onMount } from 'svelte';
	import { browser } from '$app/environment';
	import { Button } from '$lib/components/ui/button';
	import DiscoLogo from './disco-logo.svelte';
	import { isStandalonePwa } from '$lib/pwa';
	import { Download, X, Share, PlusSquare } from '@lucide/svelte';
	import { fly } from 'svelte/transition';

	interface BeforeInstallPromptEvent extends Event {
		readonly userChoice: Promise<{ outcome: 'accepted' | 'dismissed'; platform: string }>;
		prompt(): Promise<void>;
	}

	const STORAGE_KEY = 'discopanel_pwa_dismissed';

	let show = $state(false);
	let deferredPrompt = $state<BeforeInstallPromptEvent | null>(null);
	let isIos = $state(false);
	let showIosInstructions = $state(false);

	function dismiss() {
		show = false;
		if (browser) {
			try {
				localStorage.setItem(STORAGE_KEY, 'true');
			} catch (e) {
				console.debug('Failed to write PWA dismiss state:', e);
			}
		}
	}

	async function install() {
		if (deferredPrompt) {
			await deferredPrompt.prompt();
			const { outcome } = await deferredPrompt.userChoice;
			deferredPrompt = null;
			if (outcome === 'accepted') {
				dismiss();
			} else {
				show = false;
			}
		} else if (isIos) {
			showIosInstructions = true;
		}
	}

	onMount(() => {
		if (!browser) return;

		// Don't show if already installed (standalone mode)
		if (isStandalonePwa()) return;

		// Don't show if user dismissed it previously
		try {
			if (localStorage.getItem(STORAGE_KEY) === 'true') {
				return;
			}
		} catch (e) {
			console.debug('Failed to read PWA dismiss state:', e);
		}

		// Detect iOS Safari
		const ua = window.navigator.userAgent;
		const isIosDevice = /iPhone|iPad|iPod/.test(ua) && !('MSStream' in window);
		const isSafari = /Safari/.test(ua) && !/CriOS|FxiOS|EdgiOS/.test(ua);
		if (isIosDevice && isSafari) {
			isIos = true;
			// Small delay for clean page entry
			setTimeout(() => {
				show = true;
			}, 1500);
		}

		// Listen for Chromium install prompt
		const handleBeforeInstall = (e: Event) => {
			e.preventDefault();
			deferredPrompt = e as BeforeInstallPromptEvent;
			setTimeout(() => {
				show = true;
			}, 1000);
		};

		window.addEventListener('beforeinstallprompt', handleBeforeInstall);

		return () => {
			window.removeEventListener('beforeinstallprompt', handleBeforeInstall);
		};
	});
</script>

{#if show}
	<div
		transition:fly={{ y: 20, duration: 300 }}
		class="fixed inset-x-3 bottom-[calc(2.25rem+env(safe-area-inset-bottom,0px))] z-50 mx-auto max-w-md rounded-xl border border-border/80 bg-card/95 p-3.5 shadow-2xl backdrop-blur-md select-none"
		role="dialog"
		aria-label="Install DiscoPanel"
	>
		{#if showIosInstructions}
			<div class="space-y-2.5">
				<div class="flex items-start justify-between">
					<div class="flex items-center gap-2">
						<DiscoLogo class="size-5" />
						<span class="text-sm font-semibold">Add DiscoPanel to Home Screen</span>
					</div>
					<button
						onclick={dismiss}
						class="text-muted-foreground hover:text-foreground"
						aria-label="Close"
					>
						<X class="size-4" />
					</button>
				</div>
				<p class="text-xs leading-relaxed text-muted-foreground">
					Tap the share button in Safari <Share class="mx-0.5 inline size-3.5 text-primary" /> and select
					<span class="font-medium text-foreground"
						><PlusSquare class="mx-0.5 inline size-3.5" /> Add to Home Screen</span
					>.
				</p>
				<div class="flex justify-end pt-1">
					<Button size="sm" variant="outline" class="h-7 text-xs" onclick={dismiss}>Got it</Button>
				</div>
			</div>
		{:else}
			<div class="flex items-center gap-3">
				<div
					class="flex size-10 shrink-0 items-center justify-center rounded-lg border border-primary/20 bg-primary/10"
				>
					<DiscoLogo class="size-6" />
				</div>
				<div class="min-w-0 flex-1">
					<h3 class="truncate text-sm font-semibold tracking-tight text-foreground">
						Install DiscoPanel
					</h3>
					<p class="truncate text-xs text-muted-foreground">
						Fast launch & fullscreen app experience
					</p>
				</div>
				<button
					onclick={dismiss}
					class="p-1 text-muted-foreground/60 transition-colors hover:text-foreground"
					aria-label="Dismiss"
					title="Dismiss"
				>
					<X class="size-4" />
				</button>
			</div>
			<div class="mt-2.5 flex items-center justify-end gap-2 pt-1">
				<Button
					variant="ghost"
					size="sm"
					class="h-7 px-2.5 text-xs text-muted-foreground hover:text-foreground"
					onclick={dismiss}
				>
					Not now
				</Button>
				<Button
					variant="default"
					size="sm"
					class="h-7 gap-1.5 px-3 text-xs shadow-xs"
					onclick={install}
				>
					<Download class="size-3.5" />
					<span>Install</span>
				</Button>
			</div>
		{/if}
	</div>
{/if}
