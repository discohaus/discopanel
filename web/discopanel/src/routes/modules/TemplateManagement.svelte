<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { EmptyState, ConfirmDialog } from '$lib/components/app';
	import DynamicIcon from '$lib/components/ui/DynamicIcon.svelte';
	import { rpcClient } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import type { ModuleTemplate } from '$lib/proto/discopanel/v1/storage_pb';
	import { ModuleTemplateType } from '$lib/proto/discopanel/v1/storage_pb';
	import { Plus, Trash2, Settings, Layers, Search } from '@lucide/svelte';
	import ModuleTemplateCreateDialog from '$lib/components/server/ModuleTemplateCreateDialog.svelte';
	import { onMount } from 'svelte';
	import { registerRefresh } from '$lib/stores/refresh';

	let {
		createOpen = $bindable(false),
		category = $bindable(null),
		categories = $bindable([])
	}: {
		createOpen?: boolean;
		category?: string | null;
		categories?: string[];
	} = $props();

	let templates = $state<ModuleTemplate[]>([]);
	let loading = $state(true);

	let editDialogOpen = $state(false);
	let selectedTemplate = $state<ModuleTemplate | null>(null);
	let deleteTarget = $state<ModuleTemplate | null>(null);
	let deleteOpen = $state(false);

	onMount(() => {
		loadTemplates();
		return registerRefresh(() => loadTemplates(true));
	});

	async function loadTemplates(silent = false) {
		try {
			if (!silent) loading = true;
			const response = await rpcClient.module.listModuleTemplates({});
			templates = response.templates;
		} catch {
			if (!silent) notify.error('Failed to load module templates');
		} finally {
			if (!silent) loading = false;
		}
	}

	function requestDelete(template: ModuleTemplate) {
		deleteTarget = template;
		deleteOpen = true;
	}

	async function confirmDelete() {
		if (!deleteTarget) return;
		const template = deleteTarget;
		try {
			await rpcClient.module.deleteModuleTemplate({ id: template.id });
			notify.success(`Template "${template.name}" deleted`);
			await loadTemplates(true);
		} catch (error) {
			notify.error(
				`Failed to delete template: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		}
	}

	function openEditDialog(template: ModuleTemplate) {
		selectedTemplate = template;
		editDialogOpen = true;
	}

	// Shares the category list with the page submenu
	$effect(() => {
		const cats: string[] = [];
		templates.forEach((t) => {
			if (t.category && !cats.includes(t.category)) cats.push(t.category);
		});
		categories = cats.sort();
	});

	// Clears filter when its category disappears
	$effect(() => {
		if (category && !categories.includes(category)) {
			category = null;
		}
	});

	let filteredTemplates = $derived.by(() => {
		if (!category) return templates;
		return templates.filter((t) => t.category === category);
	});
</script>

<div class="space-y-4">
	{#if loading && templates.length === 0}
		<div class="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
			{#each Array(3) as _, i (i)}
				<Skeleton class="h-40 rounded-lg" />
			{/each}
		</div>
	{:else if templates.length === 0}
		<div class="rounded-lg border bg-card">
			<EmptyState
				icon={Layers}
				title="No templates found"
				description="You don't have any module templates configured yet."
			>
				<Button size="sm" onclick={() => (createOpen = true)}>
					<Plus class="size-4" />
					Create template
				</Button>
			</EmptyState>
		</div>
	{:else if filteredTemplates.length === 0}
		<div class="rounded-lg border bg-card">
			<EmptyState
				icon={Search}
				title="No matching templates"
				description="No templates in this category anymore."
			>
				<Button variant="outline" size="sm" onclick={() => (category = null)}>Clear filter</Button>
			</EmptyState>
		</div>
	{:else}
		<div class="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
			{#each filteredTemplates as template (template.id)}
				<div
					class="group flex flex-col rounded-lg border bg-card p-4 transition-colors hover:border-primary/20"
				>
					<div class="flex items-start gap-3">
						<div
							class="flex size-10 shrink-0 items-center justify-center rounded-lg border border-border/60 bg-muted/40 text-muted-foreground"
						>
							<DynamicIcon name={template.icon} class="size-5" fallback="Package" />
						</div>
						<div class="min-w-0 flex-1">
							<h3 class="truncate text-sm font-medium">{template.name}</h3>
							<div class="mt-1 flex flex-wrap items-center gap-1">
								{#if template.type === ModuleTemplateType.BUILTIN}
									<Badge variant="secondary">Built-in</Badge>
								{:else}
									<Badge variant="outline">Custom</Badge>
								{/if}
								{#if template.category}
									<Badge variant="secondary">{template.category}</Badge>
								{/if}
							</div>
						</div>
					</div>

					<p class="mt-3 line-clamp-2 flex-1 text-sm text-muted-foreground">
						{template.description || 'No description provided'}
					</p>

					<div class="mt-3 flex items-center justify-between gap-2 border-t pt-2.5">
						<div class="min-w-0 truncate font-mono text-xs text-muted-foreground">
							{template.dockerImage}
						</div>

						{#if template.type === ModuleTemplateType.CUSTOM}
							<div
								class="flex shrink-0 items-center gap-1 opacity-60 transition-opacity group-hover:opacity-100"
							>
								<Button
									size="icon"
									variant="ghost"
									class="size-7"
									onclick={() => openEditDialog(template)}
									title="Edit template"
								>
									<Settings class="size-3.5" />
								</Button>
								<Button
									size="icon"
									variant="ghost"
									class="size-7 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
									onclick={() => requestDelete(template)}
									title="Delete template"
								>
									<Trash2 class="size-3.5" />
								</Button>
							</div>
						{:else}
							<span class="shrink-0 text-xs text-muted-foreground">Read-only</span>
						{/if}
					</div>
				</div>
			{/each}
		</div>
		<p class="tabular text-xs text-muted-foreground">
			{#if category}
				{filteredTemplates.length} of {templates.length} templates
			{:else}
				{templates.length}
				{templates.length === 1 ? 'template' : 'templates'}
			{/if}
		</p>
	{/if}
</div>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete template {deleteTarget?.name ?? ''}?"
	description="This cannot be undone and will not affect existing instances."
	confirmLabel="Delete template"
	destructive
	onConfirm={confirmDelete}
/>

<ModuleTemplateCreateDialog
	bind:open={createOpen}
	mode="create"
	onSuccess={() => loadTemplates(true)}
/>

{#if selectedTemplate}
	<ModuleTemplateCreateDialog
		bind:open={editDialogOpen}
		mode="edit"
		template={selectedTemplate}
		onSuccess={() => loadTemplates(true)}
	/>
{/if}
