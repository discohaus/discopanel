<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import {
		Dialog,
		DialogContent,
		DialogDescription,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { rpcClient } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import type { PendingModulePrompt } from '$lib/proto/discopanel/v1/module_pb';
	import { ModuleConfigFieldType } from '$lib/proto/discopanel/v1/storage_pb';
	import { Check, KeyRound, Loader2 } from '@lucide/svelte';

	interface Props {
		open: boolean;
		prompts: PendingModulePrompt[];
		onAnswered?: () => void;
	}

	let { open = $bindable(), prompts, onAnswered }: Props = $props();

	let values = $state<Record<string, string>>({});
	let submittingId = $state<string | null>(null);

	// Drops stale values when the prompt set changes
	$effect(() => {
		const ids = new Set(prompts.map((p) => p.moduleId));
		for (const key of Object.keys(values)) {
			if (!ids.has(key)) delete values[key];
		}
	});

	// Closes itself once nothing is waiting anymore
	$effect(() => {
		if (open && prompts.length === 0) {
			open = false;
		}
	});

	async function submit(entry: PendingModulePrompt) {
		const prompt = entry.prompt;
		const value = values[entry.moduleId] ?? '';
		if (!prompt || !value) return;
		submittingId = entry.moduleId;
		try {
			await rpcClient.module.answerModulePrompt({
				id: entry.moduleId,
				promptId: prompt.id,
				value
			});
			notify.success(`Sent to ${entry.moduleName}`);
			delete values[entry.moduleId];
			onAnswered?.();
		} catch (err) {
			notify.error(err instanceof Error ? err.message : 'Failed to send input');
		} finally {
			submittingId = null;
		}
	}
</script>

<Dialog bind:open>
	<DialogContent class="max-w-lg">
		<DialogHeader>
			<DialogTitle class="flex items-center gap-2">
				<KeyRound class="size-5 text-amber-500" />
				Module input needed
			</DialogTitle>
			<DialogDescription>
				{prompts.length === 1
					? 'A module is waiting on input before it can continue.'
					: `${prompts.length} modules are waiting on input before they can continue.`}
			</DialogDescription>
		</DialogHeader>

		<div class="space-y-4">
			{#each prompts as entry (entry.moduleId)}
				{@const prompt = entry.prompt}
				{#if prompt}
					{@const busy = submittingId === entry.moduleId}
					<div class="rounded-lg border border-amber-500/30 bg-amber-500/10 p-4">
						<p class="text-xs font-medium text-muted-foreground">{entry.moduleName}</p>
						<p class="mt-1 text-sm font-semibold">{prompt.title || 'Input needed'}</p>
						{#if prompt.message}
							<p class="mt-0.5 text-sm text-muted-foreground">{prompt.message}</p>
						{/if}
						<div class="mt-3 flex items-center gap-2">
							{#if prompt.kind === ModuleConfigFieldType.SELECT}
								<Select
									type="single"
									value={values[entry.moduleId] ?? ''}
									onValueChange={(v) => (values[entry.moduleId] = v ?? '')}
								>
									<SelectTrigger class="flex-1">
										{values[entry.moduleId] || 'Select...'}
									</SelectTrigger>
									<SelectContent>
										{#each prompt.options as opt (opt.value)}
											<SelectItem value={opt.value}>{opt.label || opt.value}</SelectItem>
										{/each}
									</SelectContent>
								</Select>
							{:else}
								<Input
									class="flex-1"
									type={prompt.kind === ModuleConfigFieldType.PASSWORD ? 'password' : 'text'}
									placeholder={prompt.placeholder}
									bind:value={values[entry.moduleId]}
									onkeydown={(e) => {
										if (e.key === 'Enter') submit(entry);
									}}
								/>
							{/if}
							<Button size="sm" disabled={busy || !values[entry.moduleId]} onclick={() => submit(entry)}>
								{#if busy}
									<Loader2 class="size-4 animate-spin" />
								{:else}
									<Check class="size-4" />
								{/if}
								Submit
							</Button>
						</div>
					</div>
				{/if}
			{/each}
		</div>
	</DialogContent>
</Dialog>
