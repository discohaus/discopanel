<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { Switch } from '$lib/components/ui/switch';
	import { ServerAvatar } from '$lib/components/app';
	import { rpcClient } from '$lib/api/rpc-client';
	import { create } from '@bufbuild/protobuf';
	import { notify } from '$lib/stores/activity.svelte';
	import { Loader2, Save, RotateCcw, Lock, Camera } from '@lucide/svelte';
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import { ServerStatus } from '$lib/proto/discopanel/v1/storage_pb';
	import type { UpdateServerRequest } from '$lib/proto/discopanel/v1/server_pb';
	import { UpdateServerRequestSchema } from '$lib/proto/discopanel/v1/server_pb';
	import type {
		GetMinecraftVersionsResponse,
		GetDockerImagesResponse,
		ModLoaderInfo
	} from '$lib/proto/discopanel/v1/minecraft_pb';
	import { loadModLoaders } from '$lib/stores/loaders';
	import { NetworkPortRowsEditor } from '$lib/components/app';
	import DockerOverridesEditor from '$lib/components/docker-overrides-editor.svelte';
	import { containerRoot, volumeSourceRoots } from '$lib/components/files/picker-roots';
	import MemorySlider from '$lib/components/memory-slider.svelte';
	import { getUniqueDockerImages } from '$lib/utils';

	interface Props {
		server: Server;
		onUpdate?: () => void;
	}

	let { server, onUpdate }: Props = $props();

	// Old section anchors stay valid for shared links
	const SECTION_IDS = ['general', 'runtime', 'network', 'lifecycle', 'container'];

	const FIELD_KEYS = [
		'name',
		'description',
		'mcVersion',
		'modLoader',
		'memory',
		'memoryMin',
		'memoryMax',
		'maxPlayers',
		'dockerImage',
		'port',
		'additionalPorts',
		'detached',
		'autoStart',
		'dockerOverrides'
	] as const;

	let saving = $state(false);
	let loadingOptions = $state(true);
	let highlighted = $state<string | null>(null);
	let iconUploading = $state(false);
	let iconInput = $state<HTMLInputElement | null>(null);
	let uploadedFavicon = $state('');

	let minecraftVersions = $state<GetMinecraftVersionsResponse | null>(null);
	let modLoaders = $state<ModLoaderInfo[]>([]);
	let dockerImages = $state<GetDockerImagesResponse | null>(null);

	let stopped = $derived(server.status === ServerStatus.STOPPED);
	let proxied = $derived((server.proxyHostnames ?? []).length > 0);
	let displayFavicon = $derived(uploadedFavicon || server.favicon);
	let usedPorts = $state<Record<number, boolean>>({});

	function safeToString(data?: unknown): string | undefined {
		if (!data) return undefined;
		try {
			return JSON.stringify(data, (_, value) =>
				typeof value === 'bigint' ? value.toString() : value
			);
		} catch (e) {
			console.error('Failed to serialize overrides:', e);
			return undefined;
		}
	}

	function buildFormData(): UpdateServerRequest {
		return create(UpdateServerRequestSchema, {
			id: server.id,
			name: server.name,
			description: server.description || '',
			port: server.port,
			maxPlayers: server.maxPlayers,
			memory: server.memory,
			memoryMin: server.memoryMin,
			memoryMax: server.memoryMax,
			modLoader: server.modLoader,
			mcVersion: server.mcVersion,
			dockerImage: server.dockerImage,
			detached: server.detached,
			autoStart: server.autoStart,
			modpackId: '',
			modpackVersionId: '',
			additionalPorts: server.additionalPorts || [],
			dockerOverrides: server.dockerOverrides
		});
	}

	let formData = $state<UpdateServerRequest>(buildFormData());

	function fieldDirty(key: string): boolean {
		switch (key) {
			case 'name':
				return formData.name !== server.name;
			case 'description':
				return formData.description !== (server.description || '');
			case 'mcVersion':
				return formData.mcVersion !== server.mcVersion;
			case 'modLoader':
				return formData.modLoader !== server.modLoader;
			case 'memory':
				return formData.memory !== server.memory;
			case 'memoryMin':
				return formData.memoryMin !== server.memoryMin;
			case 'memoryMax':
				return formData.memoryMax !== server.memoryMax;
			case 'maxPlayers':
				return formData.maxPlayers !== server.maxPlayers;
			case 'dockerImage':
				return formData.dockerImage !== server.dockerImage;
			case 'port':
				return formData.port !== server.port;
			case 'additionalPorts':
				return (
					safeToString(formData.additionalPorts) !== safeToString(server.additionalPorts || [])
				);
			case 'detached':
				return formData.detached !== server.detached;
			case 'autoStart':
				return formData.autoStart !== server.autoStart;
			case 'dockerOverrides':
				return (
					safeToString($state.snapshot(formData.dockerOverrides)) !==
					safeToString(server.dockerOverrides)
				);
			default:
				return false;
		}
	}

	let dirtyCount = $derived(FIELD_KEYS.filter((key) => fieldDirty(key)).length);
	let dirty = $derived(dirtyCount > 0);

	let hostTotalMb = $state(0);
	let memoryAllocations = $state<{ serverId: string; memory: number }[]>([]);
	let occupiedMb = $derived(
		memoryAllocations.filter((a) => a.serverId !== server.id).reduce((sum, a) => sum + a.memory, 0)
	);

	let selectableLoaders = $derived(
		modLoaders.filter((l) => l.provisionable || l.loader === server.modLoader)
	);
	let loaderDisplay = $derived(
		modLoaders.find((l) => l.loader === formData.modLoader)?.displayName || 'Select a mod loader'
	);

	// Rebuild the form when viewing a different server
	let previousServerId = $state(server.id);
	$effect(() => {
		if (server.id !== previousServerId) {
			previousServerId = server.id;
			untrack(() => {
				formData = buildFormData();
				saving = false;
				uploadedFavicon = '';
				loadOptions();
			});
		}
	});

	onMount(() => {
		loadOptions();
		checkUrlHash();
		window.addEventListener('hashchange', checkUrlHash);
		return () => window.removeEventListener('hashchange', checkUrlHash);
	});

	async function loadOptions() {
		try {
			loadingOptions = true;
			const [versions, loaders, images, hostMemory, portData] = await Promise.all([
				rpcClient.minecraft.getMinecraftVersions({}),
				loadModLoaders(),
				rpcClient.minecraft.getDockerImages({}),
				rpcClient.server.getHostMemory({}),
				rpcClient.server.getNextAvailablePort({}).catch(() => null)
			]);
			minecraftVersions = versions;
			modLoaders = loaders;
			dockerImages = images;
			// Backend registry is authoritative, this only hints early
			const ownExtra = new Set((server.additionalPorts ?? []).map((p) => p.hostPort));
			usedPorts = Object.fromEntries(
				portData?.usedPorts?.filter((p) => !ownExtra.has(p.port)).map((p) => [p.port, true]) ?? []
			);
			hostTotalMb = Number(hostMemory.totalMb);
			memoryAllocations = hostMemory.allocations.map((a) => ({
				serverId: a.serverId,
				memory: a.memory
			}));
		} catch {
			notify.error('Failed to load version options');
		} finally {
			loadingOptions = false;
		}
	}

	async function save() {
		if (!dirty || saving) return;
		saving = true;
		try {
			await rpcClient.server.updateServer(
				create(UpdateServerRequestSchema, {
					...formData,
					clearAdditionalPorts: (formData.additionalPorts?.length ?? 0) === 0
				})
			);
			notify.success(
				stopped ? 'Settings saved' : 'Settings saved. Restart the server to apply changes.'
			);
			onUpdate?.();
		} catch (error) {
			notify.error('Failed to save settings');
			console.error(error);
		} finally {
			saving = false;
		}
	}

	function reset() {
		formData = buildFormData();
	}

	async function handleIconSelect(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		input.value = '';
		if (!file) return;
		if (file.size > 4 * 1024 * 1024) {
			notify.error('Icon images must be under 4 MB');
			return;
		}
		iconUploading = true;
		try {
			const image = new Uint8Array(await file.arrayBuffer());
			const response = await rpcClient.server.uploadServerIcon({ id: server.id, image });
			uploadedFavicon = response.favicon;
			notify.success(stopped ? 'Server icon updated' : 'Server icon updated. Shows after restart.');
			onUpdate?.();
		} catch {
			notify.error('Failed to update the server icon');
		} finally {
			iconUploading = false;
		}
	}

	function flashField(id: string) {
		setTimeout(() => {
			const element = document.getElementById(id);
			if (element) {
				element.scrollIntoView({ behavior: 'smooth', block: 'center' });
				highlighted = id;
				setTimeout(() => (highlighted = null), 3000);
			}
		}, 100);
	}

	function checkUrlHash() {
		const hash = window.location.hash.slice(1);
		if (!hash) return;

		if (SECTION_IDS.includes(hash)) {
			setTimeout(() => {
				document.getElementById(hash)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
			}, 100);
			return;
		}

		if (FIELD_KEYS.some((key) => `panel-${key}` === hash)) {
			flashField(hash);
			return;
		}

		// Unknown hashes are property links, go to properties tab
		goto(`${resolve(`/servers/${server.id}`)}?tab=properties#${hash}`, {
			noScroll: true,
			keepFocus: true
		});
	}

	function handleMaxPlayersInput(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		if (Number(input.value) <= 0) {
			input.value = '1';
			formData.maxPlayers = 1;
		}
	}

	function setDetached(checked: boolean) {
		if (checked && proxied) {
			notify.error('Proxied servers cannot run detached');
			formData.detached = false;
			return;
		}
		formData.detached = checked;
		if (checked) {
			formData.autoStart = false;
		}
	}

	function setAutoStart(checked: boolean) {
		if (formData.detached) {
			notify.error('Detached servers cannot auto start');
			formData.autoStart = false;
			return;
		}
		formData.autoStart = checked;
	}

	function ring(id: string): string {
		return highlighted === id
			? 'rounded-md ring-2 ring-primary ring-offset-4 ring-offset-card'
			: '';
	}
</script>

{#snippet fieldLabel(key: string, forId: string, text: string, locked: boolean = false)}
	<div class="mb-1.5 flex items-center gap-1.5">
		<Label for={forId} class="text-xs font-medium text-muted-foreground">{text}</Label>
		{#if locked}
			<span title="Stop the server to change">
				<Lock class="size-3 text-muted-foreground/50" />
			</span>
		{/if}
		{#if fieldDirty(key)}
			<span class="size-1.5 rounded-full bg-status-busy" title="Unsaved change"></span>
		{/if}
	</div>
{/snippet}

<div class="flex min-h-0 flex-1 flex-col gap-3">
	{#if !stopped}
		<p
			class="flex shrink-0 items-center gap-2 rounded-lg border border-status-busy/25 bg-status-busy/5 px-3 py-2 text-xs text-muted-foreground"
		>
			<Lock class="size-3.5 shrink-0 text-status-busy" />
			Stop the server to change the version, loader, or image. Everything else applies on restart.
		</p>
	{/if}

	<div class="min-h-0 flex-1 overflow-y-auto">
		<div class="w-full space-y-7 px-2 py-2">
			<section id="runtime" class="scroll-mt-4">
				<h3 class="mb-2 text-sm font-medium">Runtime</h3>
				<div
					class="grid gap-x-4 gap-y-4 rounded-xl border bg-card p-4 sm:grid-cols-2 xl:grid-cols-3"
				>
					<div id="panel-mcVersion" class={ring('panel-mcVersion')}>
						{@render fieldLabel('mcVersion', 'mc_version', 'Minecraft version', !stopped)}
						<Select
							type="single"
							disabled={loadingOptions || !stopped}
							value={formData.mcVersion}
							onValueChange={(value: string | undefined) => (formData.mcVersion = value || '')}
						>
							<SelectTrigger id="mc_version" class="h-9 w-full">
								<span class="truncate">{formData.mcVersion || 'Select a version'}</span>
							</SelectTrigger>
							<SelectContent>
								{#if minecraftVersions}
									{#each minecraftVersions.versions as version (version.id)}
										<SelectItem value={version.id}>{version.id}</SelectItem>
									{/each}
								{/if}
							</SelectContent>
						</Select>
					</div>

					<div id="panel-modLoader" class={ring('panel-modLoader')}>
						{@render fieldLabel('modLoader', 'mod_loader', 'Mod loader', !stopped)}
						<Select
							type="single"
							disabled={loadingOptions || !stopped}
							value={selectableLoaders.find((l) => l.loader === formData.modLoader)?.name ?? ''}
							onValueChange={(value: string) => {
								const picked = selectableLoaders.find((l) => l.name === value);
								if (picked) formData.modLoader = picked.loader;
							}}
						>
							<SelectTrigger id="mod_loader" class="h-9 w-full">
								<span class="truncate">{loaderDisplay}</span>
							</SelectTrigger>
							<SelectContent>
								{#if formData.mcVersion}
									{#each selectableLoaders as loader (loader.name)}
										<SelectItem value={loader.name}>{loader.displayName}</SelectItem>
									{/each}
								{/if}
							</SelectContent>
						</Select>
					</div>

					<div id="panel-maxPlayers" class={ring('panel-maxPlayers')}>
						{@render fieldLabel('maxPlayers', 'max_players', 'Max players')}
						<Input
							id="max_players"
							type="number"
							bind:value={formData.maxPlayers}
							oninput={handleMaxPlayersInput}
							min="1"
							max="1000"
							class="h-9"
						/>
					</div>

					<div id="panel-memory" class="sm:col-span-2 xl:col-span-3 {ring('panel-memory')}">
						<MemorySlider
							bind:memory={formData.memory}
							bind:memoryMin={formData.memoryMin}
							bind:memoryMax={formData.memoryMax}
							totalMb={hostTotalMb}
							{occupiedMb}
							disabled={saving}
							dirty={fieldDirty('memory') || fieldDirty('memoryMin') || fieldDirty('memoryMax')}
						/>
					</div>
				</div>
			</section>

			<div class="grid items-start gap-7 lg:grid-cols-5 lg:gap-4">
				<section id="general" class="scroll-mt-4 lg:col-span-3">
					<h3 class="mb-2 text-sm font-medium">Identity</h3>
					<div class="flex flex-col gap-5 rounded-xl border bg-card p-4 sm:flex-row">
						<div class="flex shrink-0 flex-col items-center gap-1.5">
							<button
								type="button"
								class="group relative shrink-0 rounded-xl outline-offset-2"
								onclick={() => iconInput?.click()}
								disabled={iconUploading}
								title="Change server icon"
							>
								<ServerAvatar
									name={formData.name || server.name}
									favicon={displayFavicon}
									size="xl"
								/>
								<span
									class="absolute inset-0 flex items-center justify-center rounded-xl bg-black/55 opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100 {iconUploading
										? 'opacity-100'
										: ''}"
								>
									{#if iconUploading}
										<Loader2 class="size-5 animate-spin text-white" />
									{:else}
										<Camera class="size-5 text-white" />
									{/if}
								</span>
							</button>
							<span class="text-[11px] text-muted-foreground">Server icon</span>
						</div>
						<input
							bind:this={iconInput}
							type="file"
							accept="image/png,image/jpeg,image/webp,image/gif"
							class="hidden"
							onchange={handleIconSelect}
						/>
						<div class="grid min-w-0 flex-1 content-start gap-4">
							<div id="panel-name" class={ring('panel-name')}>
								{@render fieldLabel('name', 'name', 'Server name')}
								<Input id="name" bind:value={formData.name} placeholder="Server name" class="h-9" />
							</div>
							<div id="panel-description" class={ring('panel-description')}>
								{@render fieldLabel('description', 'description', 'Description')}
								<Input
									id="description"
									bind:value={formData.description}
									placeholder="Add a description"
									class="h-9"
								/>
							</div>
						</div>
					</div>
				</section>

				<section class="scroll-mt-4 lg:col-span-2">
					<h3 class="mb-2 text-sm font-medium">Runtime image</h3>
					<div
						id="panel-dockerImage"
						class="rounded-xl border bg-card p-4 {ring('panel-dockerImage')}"
					>
						{@render fieldLabel('dockerImage', 'docker_image', 'Java image', !stopped)}
						<Select
							type="single"
							disabled={loadingOptions || !stopped}
							value={formData.dockerImage}
							onValueChange={(value: string | undefined) => (formData.dockerImage = value || '')}
						>
							<SelectTrigger id="docker_image" class="h-9 w-full">
								<span class="truncate">{formData.dockerImage || 'Select an image'}</span>
							</SelectTrigger>
							<SelectContent>
								{#each getUniqueDockerImages(dockerImages?.images || []) as image (image.tag)}
									<SelectItem value={image.tag}>{image.displayName || image.tag}</SelectItem>
								{/each}
							</SelectContent>
						</Select>
						{#if server.javaVersion || server.runtimeDigest}
							<p class="mt-1.5 font-mono text-[11px] text-muted-foreground/70">
								{#if server.javaVersion}Java {server.javaVersion}{/if}
								{#if server.javaVersion && server.runtimeDigest}·{/if}
								{#if server.runtimeDigest}build {server.runtimeDigest
										.split(':')
										.pop()
										?.slice(0, 12)}{/if}
							</p>
						{/if}
					</div>
				</section>
			</div>

			<section id="network" class="scroll-mt-4">
				<h3 class="mb-2 text-sm font-medium">Network</h3>
				<div class="grid gap-6 rounded-xl border bg-card p-4 lg:grid-cols-[240px_1fr]">
					<div id="panel-port" class={ring('panel-port')}>
						{@render fieldLabel('port', 'port', 'Port', proxied)}
						<Input
							id="port"
							type="number"
							bind:value={formData.port}
							min="1"
							max="65535"
							disabled={proxied}
							class="h-9"
						/>
						{#if proxied}
							<p class="mt-1.5 text-[11px] text-muted-foreground">
								Routed through the proxy. Manage it in the Network tab.
							</p>
						{/if}
					</div>
					<div id="panel-additionalPorts" class="min-w-0 {ring('panel-additionalPorts')}">
						<NetworkPortRowsEditor
							bind:ports={formData.additionalPorts}
							{usedPorts}
							disabled={saving}
							proxyAvailable={proxied}
							serverHosts={server.proxyHostnames}
							allowAdd
							title="Additional ports"
							description="Extra ports for mods, plugins, or services like BlueMap, voice chat, or dynmap"
						/>
					</div>
				</div>
			</section>

			<section id="lifecycle" class="scroll-mt-4">
				<h3 class="mb-2 text-sm font-medium">Startup</h3>
				<div class="grid gap-4 sm:grid-cols-2">
					<div
						id="panel-autoStart"
						class="flex items-center justify-between gap-4 rounded-xl border bg-card px-4 py-3 {ring(
							'panel-autoStart'
						)}"
					>
						<div class="min-w-0">
							<div class="flex items-center gap-1.5">
								<Label for="auto_start" class="text-sm font-medium">Auto start</Label>
								{#if fieldDirty('autoStart')}
									<span class="size-1.5 rounded-full bg-status-busy" title="Unsaved change"></span>
								{/if}
							</div>
							<p class="mt-0.5 text-xs text-muted-foreground">
								{formData.detached
									? 'Not available while detached'
									: 'Start this server when DiscoPanel starts'}
							</p>
						</div>
						<Switch
							id="auto_start"
							checked={formData.autoStart}
							disabled={formData.detached}
							onCheckedChange={setAutoStart}
						/>
					</div>

					<div
						id="panel-detached"
						class="flex items-center justify-between gap-4 rounded-xl border bg-card px-4 py-3 {ring(
							'panel-detached'
						)}"
					>
						<div class="min-w-0">
							<div class="flex items-center gap-1.5">
								<Label for="detached" class="text-sm font-medium">Detached</Label>
								{#if fieldDirty('detached')}
									<span class="size-1.5 rounded-full bg-status-busy" title="Unsaved change"></span>
								{/if}
							</div>
							<p class="mt-0.5 text-xs text-muted-foreground">
								{proxied
									? 'Not available for proxied servers'
									: 'Keep the container running when DiscoPanel stops'}
							</p>
						</div>
						<Switch
							id="detached"
							checked={formData.detached}
							disabled={proxied}
							onCheckedChange={setDetached}
						/>
					</div>
				</div>
			</section>

			<section id="container" class="scroll-mt-4">
				<h3 class="mb-2 text-sm font-medium">Advanced</h3>
				<div
					id="panel-dockerOverrides"
					class="rounded-xl border bg-card p-4 {ring('panel-dockerOverrides')}"
				>
					<DockerOverridesEditor
						bind:overrides={formData.dockerOverrides}
						disabled={saving}
						sourceRoots={() => volumeSourceRoots({ serverId: server.id })}
						targetRoots={[containerRoot({ serverId: server.id })]}
						onchange={(overrides) => (formData.dockerOverrides = overrides)}
					/>
				</div>
			</section>
		</div>
	</div>

	{#if dirty}
		<div
			class="flex shrink-0 flex-wrap items-center justify-between gap-3 rounded-xl border bg-card px-4 py-3"
		>
			<span class="text-sm font-medium">
				{dirtyCount} unsaved {dirtyCount === 1 ? 'change' : 'changes'}
			</span>
			<div class="flex items-center gap-2">
				<Button variant="outline" size="sm" onclick={reset} disabled={saving}>
					<RotateCcw class="size-4" />
					Discard
				</Button>
				<Button size="sm" onclick={save} disabled={saving} class="min-w-28">
					{#if saving}
						<Loader2 class="size-4 animate-spin" />
					{:else}
						<Save class="size-4" />
					{/if}
					Save changes
				</Button>
			</div>
		</div>
	{/if}
</div>
