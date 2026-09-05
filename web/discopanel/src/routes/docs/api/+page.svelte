<script lang="ts">
	import { onMount } from 'svelte';

	import { asset } from '$app/paths';
	import { Progress } from '$lib/components/ui/progress';
	import type { Asset } from '$app/types';
	import { FileText } from '@lucide/svelte';

	let isLoading = $state(true);
	let loadingProgress = $state(10);
	let iframeElement: HTMLIFrameElement | null = $state(null);
	const scalarFrame =
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
			<body style="margin: 0; padding: 0;">
				<div id="api-reference"></div>
				<script>
					window.addEventListener('load', () => {
						window.parent.postMessage({ type: 'scalar-progress', value: 50 }, '*');
						window.Scalar.createApiReference('#api-reference', {
							url: '/api/v1/openapi.yaml',
							hideClientButton: true,
              showDeveloperTools: 'never',
              showToolbar: 'never'
						});
						window.parent.postMessage({ type: 'scalar-loaded' }, '*');
					});
				${'<'}/script>
			</body>
			</html>
		`;

	onMount(() => {
		// Write to iframe on ready
		if (iframeElement?.contentWindow) {
			const doc = iframeElement.contentDocument;
			if (doc) {
				doc.open();
				doc.write(scalarFrame);
				doc.close();
			}
		}
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
</script>

<svelte:head>
	<title>API reference · DiscoPanel</title>
</svelte:head>

<div class="flex min-h-0 w-full flex-1 flex-col overflow-hidden">
	<div class="flex shrink-0 items-center gap-2.5 border-b px-4 py-3 sm:px-6">
		<FileText class="size-4 text-muted-foreground" />
		<h1 class="text-sm font-semibold">API reference</h1>
		<span class="text-xs text-muted-foreground">Every panel feature is available over the API</span>
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
			sandbox="allow-scripts allow-same-origin"
		></iframe>
	</div>
</div>
