<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { EmptyState, KeyValueRowsEditor, PathInput } from '$lib/components/app';
	import { serverDataRoot } from '$lib/components/files/picker-roots';
	import { enumLabel } from '$lib/proto-meta';
	import { ConfigFormat, ConfigFormatSchema } from '$lib/proto/discopanel/v1/properties_pb';
	import { FileCog, Loader2, Plus, RotateCw, Trash2 } from '@lucide/svelte';
	import type { ConfigOverridesForm } from './config-overrides-form.svelte';

	interface Props {
		form: ConfigOverridesForm;
		serverId: string;
		disabled?: boolean;
		onRetry?: () => void;
	}

	let { form, serverId, disabled = false, onRetry }: Props = $props();

	let roots = $derived([serverDataRoot(serverId)]);
</script>

<div class="space-y-3">
	<div class="flex flex-wrap items-start justify-between gap-3">
		<div class="max-w-2xl space-y-1.5">
			<p class="text-xs text-muted-foreground">
				Pin individual keys inside mod config files. Overrides are re-applied after every modpack
				update and on every start, so you keep pack updates without losing your values. Use dot
				paths like <code class="font-mono">general.maxClaims</code> or JSON pointers like
				<code class="font-mono">/general/maxClaims</code>; .properties files take the literal key.
				Values follow the existing key's type, wrap them in quotes to force text. Missing keys are
				added.
			</p>
			<p class="text-xs text-muted-foreground">
				The format follows the file extension: .properties, .toml, .json, .json5, .jsonc, .mcmeta,
				.yml, .yaml, Forge .cfg and .snbt. Comments and layout are kept, except YAML files, which
				are re-formatted when a value changes (comments still survive). Scripts and launch argument
				files cannot be overridden.
			</p>
		</div>
		{#if !form.loading && !form.loadError}
			<Button variant="outline" size="sm" onclick={() => form.addFile()} {disabled}>
				<Plus class="size-4" />
				Add file
			</Button>
		{/if}
	</div>

	{#if form.loading}
		<div class="flex items-center justify-center py-8">
			<Loader2 class="size-5 animate-spin text-muted-foreground" />
		</div>
	{:else if form.loadError}
		<div
			class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm"
		>
			<span class="text-destructive">{form.loadError}</span>
			{#if onRetry}
				<Button variant="outline" size="sm" onclick={onRetry}>
					<RotateCw class="size-4" />
					Retry
				</Button>
			{/if}
		</div>
	{:else if form.files.length === 0}
		<EmptyState
			icon={FileCog}
			title="No config overrides"
			description="Add a config file, then the keys you want to keep across modpack updates."
			class="rounded-lg border border-dashed py-8"
		/>
	{:else}
		{#each form.files as file, i (file.uid)}
			{@const status = form.statusFor(file.path)}
			<div class="space-y-3 rounded-lg border p-3">
				<div class="flex flex-col gap-2 sm:flex-row sm:items-start">
					<div class="min-w-0 flex-1">
						<PathInput
							id="override-path-{file.uid}"
							bind:value={file.path}
							placeholder="config/create-server.toml"
							aria-label="Config file path"
							class="h-8 text-xs"
							mode="file"
							pickerTitle="Select config file"
							pickerDescription="Pick a file in the server directory, or type its path"
							{roots}
							{disabled}
						/>
						{#if status}
							<p class="mt-1 text-[11px] text-muted-foreground">
								{#if !status.exists}
									<span class="text-status-busy">File not found yet</span>, applies once the mod
									creates it
								{:else if status.format !== ConfigFormat.UNSPECIFIED}
									Edited as {enumLabel(ConfigFormatSchema, status.format)}
								{/if}
							</p>
						{/if}
					</div>
					<div class="flex items-center gap-2">
						<Button
							variant="ghost"
							size="icon"
							class="size-8 shrink-0 text-muted-foreground hover:text-destructive"
							onclick={() => form.removeFile(file.uid)}
							{disabled}
							title="Remove file"
						>
							<Trash2 class="size-4" />
							<span class="sr-only">Remove file {i + 1}</span>
						</Button>
					</div>
				</div>
				<KeyValueRowsEditor
					bind:rows={file.entries}
					variant="compact"
					keyPlaceholder="general.maxClaims"
					valuePlaceholder="value"
					entryLabel="key"
					{disabled}
				/>
				<Button
					variant="ghost"
					size="sm"
					class="h-7 text-xs"
					onclick={() => file.entries.push({ key: '', value: '' })}
					{disabled}
				>
					<Plus class="size-3.5" />
					Add key
				</Button>
			</div>
		{/each}
	{/if}
</div>
