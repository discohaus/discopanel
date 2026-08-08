<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Popover, PopoverContent, PopoverTrigger } from '$lib/components/ui/popover';
	import { rpcClient } from '$lib/api/rpc-client';
	import { AliasCategory, AliasCategorySchema, type AliasInfo } from '$lib/proto/discopanel/v1/module_pb';
	import { enumLabel } from '$lib/proto-meta';
	import { SvelteMap } from 'svelte/reactivity';
	import { Braces, Server, Box, Sparkles, Loader2, Check, Copy } from '@lucide/svelte';
	import { notify } from '$lib/stores/activity.svelte';
	import { copyToClipboard } from '$lib/utils/clipboard';

	interface Props {
		serverId?: string;
		moduleId?: string;
		showLabel?: boolean;
	}

	let { serverId, moduleId, showLabel = false }: Props = $props();

	let aliases = $state<AliasInfo[]>([]);
	let loading = $state(false);
	let open = $state(false);

	async function loadAliases() {
		if (aliases.length > 0) return; // Already loaded
		loading = true;
		try {
			const response = await rpcClient.module.getAvailableAliases({
				serverId,
				moduleId
			});
			aliases = response.aliases;
		} catch (error) {
			console.error('Failed to load aliases:', error);
		} finally {
			loading = false;
		}
	}

	function handleOpenChange(isOpen: boolean) {
		open = isOpen;
		if (isOpen) {
			loadAliases();
		}
	}

	let copiedAlias = $state<string | null>(null);

	async function handleCopy(alias: string) {
		const success = await copyToClipboard(alias);
		if (success) {
			copiedAlias = alias;
			notify.success('Copied to clipboard', { description: alias });
			setTimeout(() => {
				copiedAlias = null;
			}, 2000);
		} else {
			notify.error('Failed to copy to clipboard');
		}
	}

	function getCategoryIcon(category: AliasCategory) {
		switch (category) {
			case AliasCategory.SERVER:
				return Server;
			case AliasCategory.MODULE:
				return Box;
			case AliasCategory.SPECIAL:
				return Sparkles;
			default:
				return Braces;
		}
	}

	function getCategoryLabel(category: AliasCategory): string {
		return enumLabel(AliasCategorySchema, category) || enumLabel(AliasCategorySchema, AliasCategory.UNSPECIFIED);
	}

	function getCategoryColor(category: AliasCategory): string {
		switch (category) {
			case AliasCategory.SERVER:
				return 'text-blue-500';
			case AliasCategory.MODULE:
				return 'text-purple-500';
			case AliasCategory.SPECIAL:
				return 'text-amber-500';
			default:
				return 'text-gray-500';
		}
	}

	// Group aliases by category
	let groupedAliases = $derived.by(() => {
		const groups = new SvelteMap<AliasCategory, AliasInfo[]>();
		for (const alias of aliases) {
			if (!groups.has(alias.category)) {
				groups.set(alias.category, []);
			}
			groups.get(alias.category)!.push(alias);
		}
		return groups;
	});
</script>

<Popover bind:open onOpenChange={handleOpenChange}>
	<PopoverTrigger>
		{#if showLabel}
			<Button variant="outline" size="sm" class="h-7 gap-1.5 text-xs">
				<Braces class="h-3.5 w-3.5" />
				Aliases
			</Button>
		{:else}
			<Button variant="ghost" size="icon" class="h-7 w-7" title="Available Aliases">
				<Braces class="h-4 w-4" />
			</Button>
		{/if}
	</PopoverTrigger>
	<PopoverContent class="max-h-96 w-96 overflow-y-auto p-0" align="end">
		<div class="border-b p-3">
			<h4 class="text-sm font-medium">Available Aliases</h4>
			<p class="mt-1 text-xs text-muted-foreground">Click to copy an alias to clipboard</p>
		</div>

		{#if loading}
			<div class="flex items-center justify-center py-8">
				<Loader2 class="h-6 w-6 animate-spin text-muted-foreground" />
			</div>
		{:else}
			<div class="divide-y">
				{#each [...groupedAliases.entries()] as [category, categoryAliases] (category)}
					{@const CategoryIcon = getCategoryIcon(category)}
					<div class="p-2">
						<div
							class="flex items-center gap-2 px-2 py-1.5 text-xs font-medium text-muted-foreground"
						>
							<CategoryIcon class="h-3.5 w-3.5 {getCategoryColor(category)}" />
							{getCategoryLabel(category)}
						</div>
						<div class="space-y-1">
							{#each categoryAliases as alias (alias.alias)}
								<button
									type="button"
									class="group w-full rounded-md p-2 text-left transition-colors hover:bg-muted/50"
									onclick={() => handleCopy(alias.alias)}
								>
									<div class="flex items-center justify-between">
										<code
											class="rounded bg-primary/10 px-1.5 py-0.5 font-mono text-xs text-primary group-hover:bg-primary/20"
										>
											{alias.alias}
										</code>
										<div class="flex items-center gap-2">
											{#if alias.exampleValue}
												<span
													class="max-w-25 truncate font-mono text-xs text-muted-foreground"
													title={alias.exampleValue}
												>
													= {alias.exampleValue}
												</span>
											{/if}
											{#if copiedAlias === alias.alias}
												<Check class="h-3.5 w-3.5 text-green-500" />
											{:else}
												<Copy
													class="h-3.5 w-3.5 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100"
												/>
											{/if}
										</div>
									</div>
									<p class="mt-1 truncate text-xs text-muted-foreground">
										{alias.description}
									</p>
								</button>
							{/each}
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</PopoverContent>
</Popover>
