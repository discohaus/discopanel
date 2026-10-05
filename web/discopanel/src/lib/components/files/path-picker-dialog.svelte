<script lang="ts">
	import { untrack } from 'svelte';
	import { Dialog as DialogPrimitive } from 'bits-ui';
	import {
		DialogContent,
		DialogDescription,
		DialogFooter,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { SvelteSet } from 'svelte/reactivity';
	import {
		ChevronDown,
		ChevronRight,
		File as FileIcon,
		Folder,
		FolderOpen,
		MapPin,
		PencilLine,
		ShieldAlert
	} from '@lucide/svelte';
	import { Code, ConnectError } from '@connectrpc/connect';
	import { rpcClient, rpcErrorMessage } from '$lib/api/rpc-client';
	import type { FileInfo } from '$lib/proto/discopanel/v1/file_pb';
	import { flattenTree, fileDepth } from './tree.svelte';
	import type { PickerRoot, RootsInput } from './picker-roots';

	interface Props {
		open?: boolean;
		title: string;
		description?: string;
		mode?: 'dir' | 'file' | 'any';
		roots: RootsInput;
		value?: string;
		onConfirm: (value: string) => void;
	}

	let {
		open = $bindable(false),
		title,
		description = 'Browse and select, or type the path yourself',
		mode = 'any',
		roots,
		value = $bindable(''),
		onConfirm
	}: Props = $props();

	let resolvedRoots = $state<PickerRoot[]>([]);
	let activeRootId = $state('');
	let entries = $state<FileInfo[]>([]);
	let absBase = $state('');
	let loading = $state(false);
	let loadError = $state('');
	let denied = $state(false);
	const expanded = new SvelteSet<string>();
	const loadedDirs = new SvelteSet<string>();

	let activeRoot = $derived(resolvedRoots.find((r) => r.id === activeRootId));
	let visibleRows = $derived(flattenTree(entries, { expanded, dirsOnly: mode === 'dir' }));

	// Open alone triggers the reload, roots read untracked
	$effect(() => {
		if (!open) return;
		untrack(() => {
			void initialize();
		});
	});

	// Resolves roots and loads the first one
	async function initialize() {
		loading = true;
		resolvedRoots = [];
		activeRootId = '';
		entries = [];
		expanded.clear();
		loadedDirs.clear();
		absBase = '';
		loadError = '';
		denied = false;
		try {
			resolvedRoots = Array.isArray(roots) ? [...roots] : await roots();
		} catch {
			resolvedRoots = [];
		}
		if (resolvedRoots.length > 0) {
			await openRoot(resolvedRoots[0]);
		} else {
			loading = false;
		}
	}

	async function listLevel(root: PickerRoot, path: string): Promise<FileInfo[]> {
		if (root.backend === 'server') {
			const res = await rpcClient.file.listFiles({
				serverId: root.serverId ?? '',
				path,
				tree: false
			});
			return res.files;
		}
		if (root.backend === 'container') {
			const res = await rpcClient.file.listContainerFiles({
				serverId: root.serverId ?? '',
				moduleId: root.moduleId ?? '',
				path: path || '/'
			});
			if (!absBase) absBase = res.path;
			return res.files;
		}
		const res = await rpcClient.file.listHostFiles({ path: path || root.resolvedPath || '/' });
		if (!absBase) absBase = res.path;
		return res.files;
	}

	async function openRoot(root: PickerRoot) {
		activeRootId = root.id;
		entries = [];
		expanded.clear();
		loadedDirs.clear();
		absBase = '';
		denied = false;
		loadError = '';
		loading = true;
		try {
			entries = await listLevel(root, root.backend === 'server' ? '' : root.resolvedPath || '/');
		} catch (e) {
			handleLoadError(e);
		} finally {
			loading = false;
		}
	}

	function handleLoadError(e: unknown) {
		denied = e instanceof ConnectError && e.code === Code.PermissionDenied;
		loadError = denied
			? 'Administrator access is required to browse this location.'
			: rpcErrorMessage(e, 'Failed to browse this location');
	}

	async function toggleDir(node: FileInfo) {
		if (expanded.has(node.path)) {
			expanded.delete(node.path);
			return;
		}
		expanded.add(node.path);
		if (loadedDirs.has(node.path)) return;
		loadedDirs.add(node.path);
		try {
			node.children = await listLevel(activeRoot!, node.path);
		} catch (e) {
			handleLoadError(e);
		}
	}

	// Runtime browse anchor for absolute path roots
	function base(): string {
		return absBase || activeRoot?.resolvedPath || '/';
	}

	// Indent depth relative to the active root
	function displayDepth(node: FileInfo): number {
		if (activeRoot?.backend === 'server') return fileDepth(node);
		const b = base();
		const baseDepth = (b === '/' ? 0 : b.split('/').length - 1) + 1;
		return fileDepth(node) - baseDepth;
	}

	// Field value for one picked node path
	function emitFor(path: string): string {
		const root = activeRoot;
		if (!root) return path;
		if (root.backend === 'server') {
			if (root.emitBase) return path ? `${root.emitBase}/${path}` : root.emitBase;
			return path;
		}
		if (!root.emitBase) return path || base();
		const b = base();
		if (!path || path === b) return root.emitBase;
		const prefix = b.endsWith('/') ? b : b + '/';
		return path.startsWith(prefix) ? `${root.emitBase}/${path.slice(prefix.length)}` : path;
	}

	function selectNode(node: FileInfo) {
		if (node.isDir) {
			if (mode === 'file') {
				toggleDir(node);
				return;
			}
			value = emitFor(node.path);
			return;
		}
		if (mode !== 'dir') {
			value = emitFor(node.path);
		}
	}

	function confirm() {
		onConfirm(value.trim());
		open = false;
	}
</script>

<DialogPrimitive.Root bind:open>
	<DialogContent class="flex max-h-[80vh] !max-w-lg flex-col gap-3">
		<DialogHeader>
			<DialogTitle>{title}</DialogTitle>
			<DialogDescription>{description}</DialogDescription>
		</DialogHeader>

		{#if resolvedRoots.length > 1}
			<div class="space-y-1.5">
				<Label class="text-xs text-muted-foreground">Location</Label>
				<Select
					type="single"
					value={activeRoot?.id}
					onValueChange={(v) => {
						const root = resolvedRoots.find((r) => r.id === v);
						if (root) openRoot(root);
					}}
				>
					<SelectTrigger class="w-full">
						<span class="truncate">
							{activeRoot?.label}
							<span class="ml-1.5 font-mono text-xs text-muted-foreground">
								{activeRoot?.context}
							</span>
						</span>
					</SelectTrigger>
					<SelectContent>
						{#each resolvedRoots as root (root.id)}
							<SelectItem value={root.id} label={root.label}>
								{root.label}
								<span class="ml-1.5 font-mono text-xs text-muted-foreground">{root.context}</span>
							</SelectItem>
						{/each}
					</SelectContent>
				</Select>
			</div>
		{:else if activeRoot}
			<p class="flex items-center gap-1.5 text-xs text-muted-foreground">
				<MapPin class="size-3.5 shrink-0" />
				{activeRoot.label}
				<span class="truncate font-mono">{activeRoot.context}</span>
			</p>
		{/if}

		<div class="min-h-44 flex-1 overflow-auto rounded-lg border bg-muted/20">
			{#if loading}
				<div class="space-y-1.5 p-3">
					{#each Array(6) as _, i (i)}
						<Skeleton class="h-6" />
					{/each}
				</div>
			{:else if resolvedRoots.length === 0}
				<div class="flex items-start gap-2.5 p-4 text-sm text-muted-foreground">
					<PencilLine class="mt-0.5 size-4 shrink-0" />
					<span>Nothing to browse for this field yet. Type the path below instead.</span>
				</div>
			{:else if loadError}
				<div class="flex items-start gap-2.5 p-4 text-sm text-muted-foreground">
					{#if denied}
						<ShieldAlert class="mt-0.5 size-4 shrink-0 text-status-warn" />
					{:else}
						<PencilLine class="mt-0.5 size-4 shrink-0" />
					{/if}
					<span>{loadError} You can still type the path below.</span>
				</div>
			{:else}
				<div class="py-1">
					{#if mode !== 'file' && activeRoot && emitFor('')}
						<button
							class="flex h-7 w-full cursor-pointer items-center gap-1.5 px-3 text-left text-xs transition-colors select-none hover:bg-accent/40
								{value === emitFor('') ? 'bg-primary/10 font-medium' : ''}"
							onclick={() => (value = emitFor(''))}
						>
							<FolderOpen class="size-4 shrink-0 text-status-sleep" />
							<span class="truncate font-mono">{emitFor('')}</span>
						</button>
					{/if}
					{#each visibleRows as node (node.path)}
						<button
							class="group flex h-7 w-full cursor-pointer items-center pr-3 text-left text-xs transition-colors select-none hover:bg-accent/40
								{value === emitFor(node.path) ? 'bg-primary/10 font-medium' : ''}"
							style="padding-left: {displayDepth(node) * 16 + 12}px"
							onclick={() => selectNode(node)}
						>
							<span class="flex w-4 shrink-0 items-center justify-center">
								{#if node.isDir}
									<span
										role="button"
										tabindex="-1"
										class="p-0 text-muted-foreground transition-colors hover:text-foreground"
										onclick={(e) => {
											e.stopPropagation();
											toggleDir(node);
										}}
									>
										{#if expanded.has(node.path)}
											<ChevronDown class="size-3.5" />
										{:else}
											<ChevronRight class="size-3.5" />
										{/if}
									</span>
								{/if}
							</span>
							<span class="flex min-w-0 flex-1 items-center gap-1.5 pl-1">
								{#if node.isDir}
									{#if expanded.has(node.path)}
										<FolderOpen class="size-4 shrink-0 text-status-sleep" />
									{:else}
										<Folder class="size-4 shrink-0 text-status-sleep" />
									{/if}
								{:else}
									<FileIcon class="size-4 shrink-0 text-muted-foreground" />
								{/if}
								<span class="truncate font-mono"
									>{node.name}{#if node.isDir}/{/if}</span
								>
							</span>
						</button>
					{:else}
						<p class="px-3 py-4 text-xs text-muted-foreground">This directory is empty</p>
					{/each}
				</div>
			{/if}
		</div>

		<div class="space-y-1.5">
			<Label for="path-picker-value" class="text-xs text-muted-foreground">Path</Label>
			<Input
				id="path-picker-value"
				bind:value
				placeholder={mode === 'dir' ? 'path/to/folder' : 'path/to/file'}
				class="font-mono"
				onkeydown={(e) => {
					if (e.key === 'Enter' && value.trim()) confirm();
				}}
			/>
		</div>

		<DialogFooter>
			<Button variant="outline" onclick={() => (open = false)}>Cancel</Button>
			<Button onclick={confirm} disabled={!value.trim()}>Select</Button>
		</DialogFooter>
	</DialogContent>
</DialogPrimitive.Root>
