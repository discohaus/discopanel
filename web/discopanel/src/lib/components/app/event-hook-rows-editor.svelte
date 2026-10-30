<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { enumDesc } from '$lib/proto-meta';
	import type { ModuleEventHook } from '$lib/proto/discopanel/v1/storage_pb';
	import { ModuleEventAction, ModuleEventActionSchema } from '$lib/proto/discopanel/v1/storage_pb';
	import {
		SERVER_EVENT_TYPES,
		EVENT_ACTION_OPTIONS,
		getEventTypeLabel,
		getEventActionLabel
	} from '$lib/utils/events';
	import { Trash2 } from '@lucide/svelte';

	let { hooks = $bindable([]) }: { hooks?: ModuleEventHook[] } = $props();

	const uid = $props.id();

	function removeHook(hook: ModuleEventHook) {
		hooks = hooks.filter((h) => h !== hook);
	}
</script>

<div class="space-y-3">
	{#each hooks as hook, i (hook)}
		<div class="space-y-3 rounded-lg border bg-card p-4">
			<div class="flex items-center justify-between">
				<span class="stat-label">Hook {i + 1}</span>
				<Button
					variant="ghost"
					size="icon"
					class="size-8 text-muted-foreground hover:text-destructive"
					onclick={() => removeHook(hook)}
				>
					<Trash2 class="size-4" />
					<span class="sr-only">Remove hook</span>
				</Button>
			</div>

			<div class="grid gap-3 sm:grid-cols-3">
				<div class="space-y-1.5">
					<Label>Event</Label>
					<Select
						type="single"
						value={String(hook.event)}
						onValueChange={(v) => {
							if (v) hook.event = Number(v);
						}}
					>
						<SelectTrigger class="w-full">
							<span class="truncate">{getEventTypeLabel(hook.event)}</span>
						</SelectTrigger>
						<SelectContent>
							{#each SERVER_EVENT_TYPES as { type, label } (type)}
								<SelectItem value={String(type)}>{label}</SelectItem>
							{/each}
						</SelectContent>
					</Select>
				</div>
				<div class="space-y-1.5">
					<Label>Action</Label>
					<Select
						type="single"
						value={String(hook.action)}
						onValueChange={(v) => {
							if (v) hook.action = Number(v);
						}}
					>
						<SelectTrigger class="w-full">
							<span class="truncate">{getEventActionLabel(hook.action)}</span>
						</SelectTrigger>
						<SelectContent>
							{#each EVENT_ACTION_OPTIONS as a (a)}
								<SelectItem value={String(a)}>
									<div class="flex flex-col">
										<span>{getEventActionLabel(a)}</span>
										{#if enumDesc(ModuleEventActionSchema, a)}
											<span class="text-xs text-muted-foreground">
												{enumDesc(ModuleEventActionSchema, a)}
											</span>
										{/if}
									</div>
								</SelectItem>
							{/each}
						</SelectContent>
					</Select>
				</div>
				<div class="space-y-1.5">
					<Label for="{uid}-{i}-delay">Delay (seconds)</Label>
					<Input id="{uid}-{i}-delay" type="number" bind:value={hook.delaySeconds} min={0} />
				</div>
			</div>

			{#if hook.action === ModuleEventAction.EXEC || hook.action === ModuleEventAction.RCON}
				<div class="space-y-1.5">
					<Label for="{uid}-{i}-command">Command</Label>
					<Input
						id="{uid}-{i}-command"
						bind:value={hook.command}
						placeholder={hook.action === ModuleEventAction.RCON ? 'say Hello' : '/bin/sh -c "..."'}
						class="font-mono"
					/>
				</div>
			{/if}

			<div class="space-y-1.5">
				<Label for="{uid}-{i}-condition">Condition (optional)</Label>
				<Input
					id="{uid}-{i}-condition"
					bind:value={hook.condition}
					placeholder={'{{server.players_online}} == 0'}
					class="font-mono"
				/>
			</div>
		</div>
	{/each}
</div>
