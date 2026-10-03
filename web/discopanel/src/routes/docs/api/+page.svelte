<script lang="ts">
	import { onMount } from 'svelte';
	import { mode } from 'mode-watcher';

	import { asset } from '$app/paths';
	import { Progress } from '$lib/components/ui/progress';
	import type { Asset } from '$app/types';
	import { PageHeader } from '$lib/components/app';

	let isLoading = $state(true);
	let loadingProgress = $state(10);
	let iframeElement: HTMLIFrameElement | null = $state(null);

	const isDark = $derived(mode.current !== 'light');

	function getScalarFrame(dark: boolean) {
		const modeState = dark ? 'dark' : 'light';
		return (
			`
			<!DOCTYPE html>
			<html>
			<head>
				<meta charset="utf-8">
				<meta name="viewport" content="width=device-width, initial-scale=1">
				<script src="` +
			asset('/scalar.js' as Asset) +
			`">${'<'}/script>
        <style>
          /* Hides the powered by scalar link */
          a[href="https://www.scalar.com"] {
            display: none !important;
          }
          /* Style scrollbar to match app */
          ::-webkit-scrollbar {
            width: 8px;
          }
          ::-webkit-scrollbar-track {
            background: transparent;
          }
          ::-webkit-scrollbar-thumb {
            background: hsl(0 0% 50% / 0.3);
            border-radius: 4px;
          }
          ::-webkit-scrollbar-thumb:hover {
            background: hsl(0 0% 50% / 0.5);
          }
        </style>
			</head>
			<body class="${dark ? 'dark-mode' : 'light-mode'}" style="margin: 0; padding: 0;">
				<div id="api-reference"></div>
				<script>
					window.addEventListener('load', () => {
						window.parent.postMessage({ type: 'scalar-progress', value: 50 }, '*');
						window.Scalar.createApiReference('#api-reference', {
							url: '/api/v1/openapi.yaml',
							darkMode: ${dark},
							forceDarkModeState: '${modeState}',
							hideClientButton: true,
              showDeveloperTools: 'never',
              showToolbar: 'never'
						});
						window.parent.postMessage({ type: 'scalar-loaded' }, '*');
					});

					window.addEventListener('message', (e) => {
						if (e.data?.type === 'scalar-set-mode') {
							if (e.data.isDark) {
								document.body.classList.add('dark-mode');
								document.body.classList.remove('light-mode');
							} else {
								document.body.classList.add('light-mode');
								document.body.classList.remove('dark-mode');
							}
							window.dispatchEvent(new CustomEvent('scalar-update-dark-mode', { detail: e.data.isDark }));
						}
					});
				${'<'}/script>
			</body>
			</html>
		`
		);
	}

	onMount(() => {
		// Simulate progress, but gets overridden by actual load state.
		const progressInterval = setInterval(() => {
			if (loadingProgress < 90) {
				loadingProgress += 10;
			}
		}, 200);

		// Listen for load confirmation
		const handleMessage = (e: MessageEvent) => {
			if (e.data?.type === 'scalar-progress') {
				loadingProgress = e.data.value;
			} else if (e.data?.type === 'scalar-loaded') {
				clearInterval(progressInterval);
				loadingProgress = 100;

				// Small delay for progress, makes transition smoother
				setTimeout(() => {
					isLoading = false;
				}, 300);
				window.removeEventListener('message', handleMessage);
			}
		};
		window.addEventListener('message', handleMessage);

		// Cleanup on unmount
		return () => {
			clearInterval(progressInterval);
			window.removeEventListener('message', handleMessage);
		};
	});

	$effect(() => {
		const dark = isDark;
		if (iframeElement?.contentWindow) {
			iframeElement.contentWindow.postMessage({ type: 'scalar-set-mode', isDark: dark }, '*');
		}
	});
</script>

<svelte:head>
	<title>API reference · DiscoPanel</title>
</svelte:head>

<div class="flex min-h-0 w-full flex-1 flex-col">
	<div class="shrink-0 border-b bg-card/40">
		<div class="mx-auto w-full px-4 pt-5 sm:px-6">
			<PageHeader
				title="API reference"
				description="Every panel feature is available over the API"
				class="pb-4"
			/>
		</div>
	</div>

	<div class="relative min-h-0 flex-1">
		{#if isLoading}
			<div class="absolute inset-0 z-10 flex items-center justify-center bg-background/80">
				<div class="w-full max-w-md px-8">
					<div class="mb-4 text-center">
						<p class="text-sm text-muted-foreground">Loading API reference...</p>
					</div>
					<Progress value={loadingProgress} max={100} class="h-2" />
				</div>
			</div>
		{/if}
		<iframe
			bind:this={iframeElement}
			id="openapispecs"
			title="API Documentation"
			class="h-full w-full border-0 {isLoading ? 'hidden' : ''}"
			referrerpolicy="same-origin"
			sandbox="allow-scripts allow-same-origin allow-downloads"
			srcdoc={getScalarFrame(isDark)}
		></iframe>
	</div>
</div>
