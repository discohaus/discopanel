<script lang="ts">
	import { untrack } from 'svelte';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { Loader2, RotateCw } from '@lucide/svelte';
	import { rpcClient, rpcErrorMessage, silentCallOptions } from '$lib/api/rpc-client';
	import type { LoaderBuild } from '$lib/proto/discopanel/v1/minecraft_pb';
	import type { ModLoader } from '$lib/proto/discopanel/v1/storage_pb';

	interface Props {
		id: string;
		modLoader: ModLoader;
		mcVersion: string;
		// Empty means the recommended build
		value: string;
		disabled?: boolean;
	}

	let { id, modLoader, mcVersion, value = $bindable(''), disabled = false }: Props = $props();

	let builds = $state<LoaderBuild[]>([]);
	let loading = $state(false);
	let error = $state('');
	let request = 0;

	let recommended = $derived(builds.find((b) => b.recommended)?.version ?? '');
	// A saved pin the listing no longer offers stays visible
	let unlisted = $derived(value !== '' && !loading && !builds.some((b) => b.version === value));

	$effect(() => {
		const loader = modLoader;
		const mc = mcVersion;
		untrack(() => load(loader, mc));
	});

	async function load(loader: ModLoader, mc: string) {
		const seq = ++request;
		builds = [];
		error = '';
		if (!mc) return;
		loading = true;
		try {
			const res = await rpcClient.minecraft.getLoaderVersions(
				{ modLoader: loader, mcVersion: mc },
				silentCallOptions
			);
			if (seq !== request) return;
			builds = res.builds;
		} catch (err) {
			if (seq !== request) return;
			error = rpcErrorMessage(err, 'Could not load loader builds');
		} finally {
			if (seq === request) loading = false;
		}
	}
</script>

<Select
	type="single"
	{value}
	onValueChange={(v: string | undefined) => (value = v ?? '')}
	disabled={disabled || loading}
>
	<SelectTrigger {id} class="h-9 w-full">
		<span class="flex min-w-0 items-center gap-2 truncate">
			{#if loading}
				<Loader2 class="size-3.5 shrink-0 animate-spin text-muted-foreground" />
				Loading builds...
			{:else if value}
				{value}{value === recommended ? ' (recommended)' : ''}
			{:else}
				Latest{recommended ? ` (${recommended})` : ''}
			{/if}
		</span>
	</SelectTrigger>
	<SelectContent class="max-h-72">
		<SelectItem value="">
			Latest{recommended ? ` (${recommended})` : ''}
		</SelectItem>
		{#if unlisted}
			<SelectItem {value}>{value} · not listed for MC {mcVersion}</SelectItem>
		{/if}
		{#each builds as build (build.version)}
			<SelectItem value={build.version}>
				{build.version}
				{#if build.recommended}
					<span class="text-xs text-status-ok">recommended</span>
				{:else if !build.stable}
					<span class="text-xs text-muted-foreground">beta</span>
				{/if}
			</SelectItem>
		{/each}
	</SelectContent>
</Select>
{#if error}
	<p class="mt-1.5 flex items-center gap-1.5 text-xs text-destructive">
		<span class="min-w-0 truncate" title={error}>{error}</span>
		<button
			type="button"
			class="inline-flex shrink-0 items-center gap-1 text-muted-foreground hover:text-foreground"
			onclick={() => load(modLoader, mcVersion)}
		>
			<RotateCw class="size-3" />
			Retry
		</button>
	</p>
{:else if !loading && mcVersion && builds.length === 0}
	<p class="mt-1.5 text-xs text-muted-foreground">
		No builds are published for MC {mcVersion} yet
	</p>
{:else if unlisted}
	<p class="mt-1.5 text-xs text-status-busy">
		{value} is not available for MC {mcVersion}, pick another build
	</p>
{/if}
