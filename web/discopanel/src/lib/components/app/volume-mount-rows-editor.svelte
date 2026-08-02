<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import type { VolumeMount } from '$lib/proto/discopanel/v1/storage_pb';
	import { Trash2 } from '@lucide/svelte';

	let {
		volumes = $bindable([]),
		disabled = false,
		sourcePlaceholder = '/host/path'
	}: {
		volumes?: VolumeMount[];
		disabled?: boolean;
		sourcePlaceholder?: string;
	} = $props();

	const uid = $props.id();

	function removeVolume(vol: VolumeMount) {
		volumes = volumes.filter((v) => v !== vol);
	}
</script>

<div class="space-y-3">
	{#each volumes as vol, i (vol)}
		<div class="space-y-3 rounded-lg border bg-card p-4">
			<div class="flex items-center justify-between">
				<span class="stat-label">Volume {i + 1}</span>
				{#if !disabled}
					<Button
						variant="ghost"
						size="icon"
						class="size-8 text-muted-foreground hover:text-destructive"
						onclick={() => removeVolume(vol)}
					>
						<Trash2 class="size-4" />
						<span class="sr-only">Remove volume</span>
					</Button>
				{/if}
			</div>

			<div class="grid gap-3 sm:grid-cols-2">
				<div class="space-y-1.5">
					<Label for="{uid}-{i}-source">Host path</Label>
					<Input
						id="{uid}-{i}-source"
						bind:value={vol.source}
						placeholder={sourcePlaceholder}
						class="font-mono"
						{disabled}
					/>
				</div>
				<div class="space-y-1.5">
					<Label for="{uid}-{i}-target">Container path</Label>
					<Input
						id="{uid}-{i}-target"
						bind:value={vol.target}
						placeholder="/container/path"
						class="font-mono"
						{disabled}
					/>
				</div>
			</div>

			<div class="flex flex-wrap items-center gap-x-6 gap-y-2">
				<label class="flex cursor-pointer items-center gap-2">
					<Checkbox
						checked={vol.readOnly}
						{disabled}
						onCheckedChange={(checked) => {
							vol.readOnly = !!checked;
							if (vol.readOnly) vol.createDir = false;
						}}
					/>
					<span class="text-sm">Read-only mount</span>
				</label>
				<label class="flex cursor-pointer items-center gap-2">
					<Checkbox
						checked={vol.createDir}
						{disabled}
						onCheckedChange={(checked) => {
							vol.createDir = !!checked;
							if (vol.createDir) vol.readOnly = false;
						}}
					/>
					<span class="text-sm">Pre-create directory</span>
				</label>
			</div>
		</div>
	{/each}
</div>
