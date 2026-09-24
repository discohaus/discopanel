<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import DynamicIcon from '$lib/components/ui/DynamicIcon.svelte';
	import { EmptyState } from '$lib/components/app';
	import { Package, ChevronRight, Trash2 } from '@lucide/svelte';
	import { ModuleTemplateType, type ModuleTemplate } from '$lib/proto/discopanel/v1/storage_pb';

	interface Props {
		templates?: ModuleTemplate[];
		onSelect: (template: ModuleTemplate) => void;
		onDelete?: (template: ModuleTemplate) => void;
	}

	let { templates, onSelect, onDelete }: Props = $props();

	let selectedCategory = $state<string | null>(null);

	let categories = $derived.by(() => {
		if (!templates) return [];
		const cats: string[] = [];
		for (const t of templates) {
			if (t.category && !cats.includes(t.category)) cats.push(t.category);
		}
		return cats.sort();
	});

	let filteredTemplates = $derived.by(() => {
		if (!templates) return [];
		if (!selectedCategory) return templates;
		return templates.filter((t) => t.category === selectedCategory);
	});

	// Clears filter when its category disappears
	$effect(() => {
		if (selectedCategory && !categories.includes(selectedCategory)) {
			selectedCategory = null;
		}
	});
</script>

{#if categories.length > 0}
	<div class="mb-4 flex flex-wrap gap-2">
		<Button
			variant={selectedCategory === null ? 'secondary' : 'ghost'}
			size="sm"
			class="h-8"
			onclick={() => (selectedCategory = null)}
		>
			All
		</Button>
		{#each categories as cat (cat)}
			<Button
				variant={selectedCategory === cat ? 'secondary' : 'ghost'}
				size="sm"
				class="h-8"
				onclick={() => (selectedCategory = cat)}
			>
				{cat}
			</Button>
		{/each}
	</div>
{/if}

{#if !templates || templates.length === 0}
	<EmptyState
		icon={Package}
		title="No templates available"
		description="Create a custom template to get started with your module configuration"
	/>
{:else}
	<div class="space-y-2">
		{#each filteredTemplates as template (template.id)}
			<div
				class="group relative flex items-center gap-4 rounded-lg border bg-card p-4 transition-colors hover:bg-accent/40"
			>
				<button
					type="button"
					class="absolute inset-0 rounded-lg focus-visible:ring-2 focus-visible:ring-ring/60 focus-visible:outline-none"
					onclick={() => onSelect(template)}
				>
					<span class="sr-only">Select {template.name}</span>
				</button>
				<div
					class="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-muted/40 text-muted-foreground"
				>
					<DynamicIcon name={template.icon} class="size-5" fallback="Package" />
				</div>
				<div class="min-w-0 flex-1">
					<div class="flex flex-wrap items-center gap-2">
						<span class="text-sm font-medium">{template.name}</span>
						{#if template.category}
							<Badge variant="secondary">{template.category}</Badge>
						{/if}
					</div>
					<p class="mt-0.5 line-clamp-2 text-sm text-muted-foreground">
						{template.description || 'No description provided'}
					</p>
				</div>
				<div class="relative flex shrink-0 items-center gap-1">
					{#if template.type === ModuleTemplateType.CUSTOM && onDelete}
						<Button
							variant="ghost"
							size="icon"
							class="size-8 text-muted-foreground hover:bg-status-danger/10 hover:text-status-danger"
							onclick={(e) => {
								e.stopPropagation();
								onDelete?.(template);
							}}
						>
							<Trash2 class="size-4" />
							<span class="sr-only">Delete {template.name}</span>
						</Button>
					{/if}
					<ChevronRight
						class="size-4 text-muted-foreground transition-colors group-hover:text-foreground"
					/>
				</div>
			</div>
		{/each}
	</div>
{/if}
