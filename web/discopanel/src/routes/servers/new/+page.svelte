<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Label } from '$lib/components/ui/label';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { Switch } from '$lib/components/ui/switch';
	import { Badge } from '$lib/components/ui/badge';
	import { CardStack, PageHeader, SectionCard, ServerAvatar } from '$lib/components/app';
	import { rpcClient, rpcErrorMessage } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import {
		ArrowLeft,
		Camera,
		Loader2,
		Package,
		Sparkles,
		X,
		ChevronDown,
		ChevronUp,
		Zap,
		MemoryStick,
		Cable,
		Globe,
		Rocket
	} from '@lucide/svelte';
	import { create } from '@bufbuild/protobuf';
	import type { CreateServerRequest } from '$lib/proto/discopanel/v1/server_pb';
	import { CreateServerRequestSchema } from '$lib/proto/discopanel/v1/server_pb';
	import {
		ModLoader,
		ReleaseType,
		ReleaseTypeSchema,
		type ProxyListener,
		type IndexedModpack
	} from '$lib/proto/discopanel/v1/storage_pb';
	import type { ModLoaderInfo, DockerImage } from '$lib/proto/discopanel/v1/minecraft_pb';
	import type { Version } from '$lib/proto/discopanel/v1/modpack_pb';
	import { enumLabel } from '$lib/proto-meta';
	import { loadModLoaders } from '$lib/stores/loaders';
	import ConnectivityCard from '$lib/components/connectivity-card.svelte';
	import DockerOverridesEditor from '$lib/components/docker-overrides-editor.svelte';
	import { volumeSourceRoots } from '$lib/components/files/picker-roots';
	import { NetworkPortRowsEditor } from '$lib/components/app';
	import MemorySlider from '$lib/components/memory-slider.svelte';
	import { getUniqueDockerImages, getDockerImageDisplayName } from '$lib/utils';
	import { fallbackAddresses, playerAddress } from '$lib/hostname';
	import type { GetProxyStatusResponse } from '$lib/proto/discopanel/v1/proxy_pb';
	import { uploadFile } from '$lib/utils/chunked-upload';

	let loading = $state(false);
	let loadingVersions = $state(true);
	let minecraftVersions = $state<string[]>([]);
	let modLoaders = $state<ModLoaderInfo[]>([]);
	let dockerImages = $state<DockerImage[]>([]);
	let latestVersion = $state('');
	let proxyEnabled = $state(false);
	let status = $state<GetProxyStatusResponse | null>(null);
	let proxyListeners = $state<ProxyListener[]>([]);
	let usedPorts = $state<Record<number, boolean>>({});
	let portError = $state('');
	let useProxyMode = $state(false);
	let proxyCatchAll = $state(false);
	let showAdvanced = $state(false);
	let hostTotalMb = $state(0);
	let occupiedMb = $state(0);

	// World zip picked now, uploaded on submit
	let worldFile = $state<File | null>(null);

	// Icon picked now, uploaded right after create
	let iconFile = $state<File | null>(null);
	let iconPreview = $state('');
	let iconInput = $state<HTMLInputElement | null>(null);

	// Modpack selection
	let sourceMode = $state<'blank' | 'modpack'>('blank');
	const modeFlavor = {
		blank: { name: 'Survival world', desc: 'Weekend survival with friends' },
		modpack: { name: 'Modded server', desc: 'Notes about the modpack' }
	};
	let namePlaceholder = $derived(modeFlavor[sourceMode].name);
	let serverDescPlaceholder = $derived(modeFlavor[sourceMode].desc);

	let selectedModpack = $state<IndexedModpack | null>(null);
	// Pack art previews as the icon until upload wins
	let avatarPreview = $derived(iconPreview || selectedModpack?.logoUrl || '');
	let favoriteModpacks = $state<IndexedModpack[]>([]);
	let modpackVersions = $state<Version[]>([]);
	let selectedVersionId = $state<string>('');
	let loadingModpackVersions = $state(false);

	let formData = $state<CreateServerRequest>(
		create(CreateServerRequestSchema, {
			port: 25565,
			maxPlayers: 20,
			memory: 2048,
			memoryMin: 1024,
			memoryMax: 1536
		})
	);

	// Tracks one auto filled field, user typed text wins
	function autoField(get: () => string, set: (v: string) => void) {
		let last = '';
		return {
			fill(value: string) {
				const current = get().trim();
				if (current && current !== last) return;
				set(value);
				last = value;
			},
			clear() {
				if (last && get().trim() === last) set('');
				last = '';
			}
		};
	}
	const autoName = autoField(
		() => formData.name,
		(v) => (formData.name = v)
	);
	const autoDesc = autoField(
		() => formData.description,
		(v) => (formData.description = v)
	);

	// Unwraps one tolerant settled result, logging failures
	function settled<T>(r: PromiseSettledResult<T>, label: string): T | null {
		if (r.status === 'fulfilled') return r.value;
		console.error(`Failed to load ${label}:`, r.reason);
		return null;
	}

	onMount(async () => {
		try {
			// Settle independently so one permission rejection cannot fail all
			const [versionsData, loadersData, imagesData, proxyStatus, listeners, hostMemory] =
				await Promise.allSettled([
					rpcClient.minecraft.getMinecraftVersions({}),
					loadModLoaders(),
					rpcClient.minecraft.getDockerImages({}),
					rpcClient.proxy.getProxyStatus({}),
					rpcClient.proxy.getProxyListeners({}),
					rpcClient.server.getHostMemory({})
				]);

			if (versionsData.status === 'fulfilled') {
				minecraftVersions = versionsData.value.versions.map((v) => v.id);
				latestVersion = versionsData.value.latest;
			} else {
				throw versionsData.reason;
			}
			if (loadersData.status === 'fulfilled') {
				modLoaders = loadersData.value;
			} else {
				throw loadersData.reason;
			}
			if (imagesData.status === 'fulfilled') {
				dockerImages = imagesData.value.images;
			} else {
				throw imagesData.reason;
			}

			status = settled(proxyStatus, 'proxy status');
			proxyEnabled = status?.enabled ?? false;

			const listenerRows = settled(listeners, 'proxy listeners');
			if (listenerRows) {
				proxyListeners = listenerRows.listeners
					.map((l) => l.listener)
					.filter((l): l is ProxyListener => l !== undefined && l.enabled);

				const defaultListener = proxyListeners.find((l) => l?.isDefault);
				if (defaultListener) {
					formData.proxyListenerId = defaultListener.id;
				} else if (proxyListeners.length > 0) {
					formData.proxyListenerId = proxyListeners[0]?.id || '';
				}
			}

			// Proxy routing is the preferred default when available
			useProxyMode = proxyEnabled && proxyListeners.length > 0;

			await refreshAvailablePort();

			const memory = settled(hostMemory, 'host memory');
			if (memory) {
				hostTotalMb = Number(memory.totalMb);
				occupiedMb = memory.allocations.reduce((sum, a) => sum + a.memory, 0);
			}

			if (!formData.mcVersion && latestVersion) {
				formData.mcVersion = latestVersion;
			}

			await loadFavoriteModpacks();

			const urlParams = new URLSearchParams(window.location.search);
			const modpackId = urlParams.get('modpack');
			if (modpackId) {
				try {
					const response = await rpcClient.modpack.getModpack({ id: modpackId });
					if (response.modpack) {
						sourceMode = 'modpack';
						await selectModpack(response.modpack);
					}
				} catch (error) {
					console.error('Failed to load modpack from URL:', error);
				}
			}
		} catch (error) {
			notify.error('Failed to load server configuration options');
			console.error(error);
		} finally {
			loadingVersions = false;
		}
	});

	async function loadFavoriteModpacks() {
		try {
			const result = await rpcClient.modpack.listFavorites({});
			favoriteModpacks = result.modpacks;
		} catch (error) {
			console.error('Failed to load favorite modpacks:', error);
		}
	}

	async function loadModpackVersions(modpackId: string) {
		loadingModpackVersions = true;
		modpackVersions = [];
		selectedVersionId = '';

		try {
			const data = await rpcClient.modpack.getModpackVersions({
				id: modpackId,
				gameVersion: '',
				modLoader: ''
			});
			modpackVersions = data.versions || [];
		} catch (error) {
			console.error('Failed to load modpack versions:', error);
			modpackVersions = [];
		} finally {
			loadingModpackVersions = false;
		}
	}

	async function selectModpack(modpack: IndexedModpack) {
		selectedModpack = modpack;

		try {
			const cfg = await rpcClient.modpack.getModpackConfig({ id: modpack.id });
			const config = cfg.config ?? {};
			const loaderName = (config['mod_loader'] || '').toLowerCase();
			const loaderInfo = modLoaders.find((l) => l.name === loaderName);
			if (!loaderInfo) {
				notify.error(`Unsupported mod loader "${loaderName}"`);
				selectedModpack = null;
				return;
			}
			autoName.fill(modpack.name || '');
			autoDesc.fill(modpack.summary || '');
			formData.modLoader = loaderInfo.loader;
			formData.mcVersion = config['mc_version'] || modpack.mcVersion || '';
			// Backend floors modpack memory at 4 GB
			formData.memory = Math.max(Number(config['memory']) || modpack.recommendedRam || 0, 4096);
			formData.dockerImage = config['docker_image'] || '';
			await loadModpackVersions(modpack.id);
		} catch (error) {
			notify.error('Failed to load modpack configuration');
			console.error(error);
			selectedModpack = null;
		}
	}

	function removeModpack() {
		selectedModpack = null;
		modpackVersions = [];
		selectedVersionId = '';
		autoName.clear();
		autoDesc.clear();
		formData.modLoader = ModLoader.UNSPECIFIED;
		formData.mcVersion = latestVersion || '';
		formData.dockerImage = '';
		formData.memory = 2048;
	}

	function setSourceMode(mode: 'blank' | 'modpack') {
		if (sourceMode === mode) return;
		sourceMode = mode;
		if (mode !== 'modpack' && selectedModpack) {
			removeModpack();
		}
	}

	function handleIconSelect(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		input.value = '';
		if (!file) return;
		if (file.size > 4 * 1024 * 1024) {
			notify.error('Icon images must be under 4 MB');
			return;
		}
		iconFile = file;
		const reader = new FileReader();
		reader.onload = () => (iconPreview = String(reader.result));
		reader.readAsDataURL(file);
	}

	function clearIcon() {
		iconFile = null;
		iconPreview = '';
	}

	function validatePort(port: number) {
		portError = '';

		if (port < 1 || port > 65535) {
			portError = 'Port must be between 1 and 65535';
			return false;
		}

		if (usedPorts[port]) {
			portError = 'This port is already in use';
			return false;
		}

		return true;
	}

	async function refreshAvailablePort() {
		try {
			const portData = await rpcClient.server.getNextAvailablePort({});
			formData.port = portData.port;
			usedPorts = Object.fromEntries(portData.usedPorts?.map((p) => [p.port, true]) || []);
			portError = '';
		} catch (error) {
			console.error('Failed to get available port:', error);
		}
	}

	// Marks the new server as the listener catch all
	async function claimCatchAll(serverId: string) {
		try {
			await rpcClient.proxy.updateServerRouting({
				serverId,
				proxyHostnames: formData.proxyHostnames,
				proxyListenerId: formData.proxyListenerId,
				proxyCatchAll: true
			});
		} catch (error) {
			console.error('Failed to claim catch all:', error);
			notify.warning('Another server already holds the catch all');
		}
	}

	let installableLoaders = $derived(modLoaders.filter((l) => l.provisionable));
	let selectedLoaderInfo = $derived(modLoaders.find((l) => l.loader === formData.modLoader));
	let selectedLoaderName = $derived(selectedLoaderInfo?.name ?? '');
	let selectedListener = $derived(proxyListeners.find((l) => l.id === formData.proxyListenerId));

	// Address preview mirrored in the summary rail
	let addressPreview = $derived.by(() => {
		if (proxyEnabled && useProxyMode) {
			const names = formData.proxyHostnames.length ? formData.proxyHostnames : ['your-hostname'];
			return names.map((name) => playerAddress(name, selectedListener?.port)).join(', ');
		}
		return fallbackAddresses(formData.port, status)[0];
	});

	let hostnameMissing = $derived(
		proxyEnabled && useProxyMode && formData.proxyHostnames.length === 0
	);

	let canSubmit = $derived(
		!loading &&
			!loadingVersions &&
			formData.name.trim().length > 0 &&
			!portError &&
			!hostnameMissing
	);

	async function handleSubmit(e: Event) {
		e.preventDefault();

		if (!formData.name.trim()) {
			notify.error('Server name is required');
			return;
		}

		if (!useProxyMode && !validatePort(formData.port)) {
			notify.error('Please select a valid port');
			return;
		}

		if (hostnameMissing) {
			notify.error('Please add a hostname for the proxy route');
			return;
		}

		loading = true;
		try {
			// Modrinth installs want the version number over the id
			const selectedVersion = modpackVersions.find((v) => v.id === selectedVersionId);
			const versionToSend =
				selectedModpack?.indexer === 'modrinth' && selectedVersion?.versionNumber
					? selectedVersion.versionNumber
					: selectedVersionId;

			let worldSessionId = '';
			if (worldFile) {
				const uploaded = await uploadFile(worldFile, {});
				worldSessionId = uploaded.sessionId;
			}

			const createRequest = {
				...formData,
				modpackId: selectedModpack?.id || '',
				modpackVersionId: versionToSend || '',
				worldUploadSessionId: worldSessionId,
				// Port zero routes through the proxy hostnames
				port: useProxyMode ? 0 : formData.port,
				// Direct mode keeps any typed hostnames out of the request
				proxyHostnames: useProxyMode ? formData.proxyHostnames : []
			};

			const response = await rpcClient.server.createServer(createRequest);
			const created = response.server;
			if (created && useProxyMode && proxyCatchAll) {
				await claimCatchAll(created.id);
			}
			if (iconFile && created) {
				try {
					const image = new Uint8Array(await iconFile.arrayBuffer());
					await rpcClient.server.uploadServerIcon({ id: created.id, image });
				} catch {
					notify.warning('Server created, but the icon upload failed');
				}
			}
			notify.success(`Server "${created?.name}" created!`);
			goto(resolve(`/servers/${created?.id}`));
		} catch (error) {
			notify.error(`Failed to create server: ${rpcErrorMessage(error, 'Unknown error')}`);
		} finally {
			loading = false;
		}
	}
</script>

<svelte:head>
	<title>New server · DiscoPanel</title>
</svelte:head>

{#snippet createButton(fullWidth: boolean)}
	<Button
		type="submit"
		form="create-server-form"
		disabled={!canSubmit}
		class="glow-primary {fullWidth ? 'w-full' : 'min-w-36'}"
	>
		{#if loading}
			<Loader2 class="size-4 animate-spin" />
			Creating...
		{:else}
			<Rocket class="size-4" />
			Create server
		{/if}
	</Button>
{/snippet}

<div class="mx-auto w-full max-w-6xl space-y-5 p-4 sm:p-6 2xl:max-w-7xl">
	<div class="flex items-center gap-3">
		<Button variant="ghost" size="icon" href={resolve('/servers')} class="size-8 shrink-0">
			<ArrowLeft class="size-4" />
			<span class="sr-only">Back to servers</span>
		</Button>
		<PageHeader title="Create a server" description="Set up a new Minecraft server" />
	</div>

	<div class="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_19rem]">
		<form id="create-server-form" onsubmit={handleSubmit} class="min-w-0 space-y-4">
			<SectionCard
				title="Source"
				description="Start from scratch or from a favorite modpack"
				contentClass="space-y-4"
			>
				<div class="grid grid-cols-2 gap-3">
					<button
						type="button"
						class="rounded-lg border p-4 text-left transition-colors {sourceMode === 'blank'
							? 'border-primary bg-primary/5'
							: 'hover:bg-accent/40'}"
						onclick={() => setSourceMode('blank')}
						disabled={loading}
					>
						<div class="flex items-center gap-2 text-sm font-medium">
							<Sparkles class="size-4 text-primary" />
							Start fresh
						</div>
						<p class="mt-1 text-xs text-muted-foreground">Pick a version and loader yourself</p>
					</button>
					<button
						type="button"
						class="rounded-lg border p-4 text-left transition-colors {sourceMode === 'modpack'
							? 'border-primary bg-primary/5'
							: 'hover:bg-accent/40'}"
						onclick={() => setSourceMode('modpack')}
						disabled={loading}
					>
						<div class="flex items-center gap-2 text-sm font-medium">
							<Package class="size-4 text-primary" />
							From a modpack
						</div>
						<p class="mt-1 text-xs text-muted-foreground">
							Version, loader, and memory come preset
						</p>
					</button>
				</div>

				{#if sourceMode === 'modpack'}
					{#if selectedModpack}
						<div class="rounded-lg border border-primary/30 bg-primary/5 p-4">
							<div class="flex items-start gap-3">
								{#if selectedModpack.logoUrl}
									<img
										src={selectedModpack.logoUrl}
										alt=""
										class="size-12 shrink-0 rounded-md object-cover"
									/>
								{/if}
								<div class="min-w-0 flex-1">
									<div class="flex items-center gap-2">
										<h4 class="truncate font-semibold">{selectedModpack.name}</h4>
										<Badge variant="secondary" class="text-xs capitalize">
											{selectedModpack.indexer}
										</Badge>
									</div>
									<p class="mt-0.5 line-clamp-2 text-sm text-muted-foreground">
										{selectedModpack.summary}
									</p>
									<div class="mt-2 flex flex-wrap gap-2">
										{#if selectedModpack.gameVersions.length > 0}
											<Badge variant="outline" class="text-xs">
												MC {selectedModpack.gameVersions[0]}
											</Badge>
										{/if}
										{#if selectedModpack.modLoaders.length > 0}
											<Badge variant="outline" class="text-xs capitalize">
												{selectedModpack.modLoaders[0]}
											</Badge>
										{/if}
									</div>

									{#if modpackVersions.length > 0}
										<div class="mt-3 max-w-xs space-y-1">
											<Label for="modpack_version" class="text-xs text-muted-foreground">
												Modpack version
											</Label>
											<Select
												type="single"
												value={selectedVersionId}
												onValueChange={(v) => (selectedVersionId = v || '')}
												disabled={loading || loadingModpackVersions}
											>
												<SelectTrigger id="modpack_version" class="h-8 w-full">
													<span class="truncate text-sm">
														{selectedVersionId
															? modpackVersions.find((v) => v.id === selectedVersionId)
																	?.displayName || 'Latest'
															: 'Latest version'}
													</span>
												</SelectTrigger>
												<SelectContent>
													<SelectItem value="">Latest version</SelectItem>
													{#each modpackVersions as version (version.id)}
														<SelectItem value={version.id}>
															{version.displayName}
															{#if version.releaseType && version.releaseType !== ReleaseType.RELEASE}
																({enumLabel(ReleaseTypeSchema, version.releaseType)})
															{/if}
														</SelectItem>
													{/each}
												</SelectContent>
											</Select>
										</div>
									{:else if loadingModpackVersions}
										<div class="mt-3 text-xs text-muted-foreground">
											<Loader2 class="mr-1 inline size-3 animate-spin" />
											Loading versions...
										</div>
									{/if}
								</div>
								<Button
									type="button"
									variant="ghost"
									size="icon"
									class="size-7 shrink-0"
									onclick={removeModpack}
									disabled={loading}
									title="Remove modpack"
								>
									<X class="size-4" />
								</Button>
							</div>
						</div>
					{:else if favoriteModpacks.length > 0}
						<CardStack
							items={favoriteModpacks}
							visible={2}
							columns={2}
							slotHeight="4rem"
							itemKey={(m: IndexedModpack) => m.id}
						>
							{#snippet card(modpack: IndexedModpack)}
								<button
									type="button"
									class="flex h-full w-full items-center gap-3 rounded-md p-3 text-left transition-colors hover:bg-accent/40"
									onclick={() => selectModpack(modpack)}
									disabled={loading}
								>
									{#if modpack.logoUrl}
										<img
											src={modpack.logoUrl}
											alt=""
											class="size-10 shrink-0 rounded-md object-cover"
										/>
									{:else}
										<div
											class="flex size-10 shrink-0 items-center justify-center rounded-md bg-muted"
										>
											<Package class="size-5 text-muted-foreground" />
										</div>
									{/if}
									<div class="min-w-0">
										<p class="truncate text-sm font-medium">{modpack.name}</p>
										<p class="truncate text-xs text-muted-foreground">{modpack.summary}</p>
									</div>
								</button>
							{/snippet}
						</CardStack>
						<p class="text-xs text-muted-foreground">
							Only favorites show here. Find more on the
							<a href={resolve('/modpacks')} class="text-primary hover:underline">Modpacks</a> page.
						</p>
					{:else}
						<p class="text-sm text-muted-foreground">
							No favorite modpacks yet. Browse the
							<a href={resolve('/modpacks')} class="text-primary hover:underline">Modpacks</a>
							page and star the ones you like, then they show up here.
						</p>
					{/if}
				{/if}
			</SectionCard>

			<SectionCard
				title="Basics"
				description="Name and icon players will see"
				contentClass="flex flex-col gap-5 sm:flex-row"
			>
				<div class="flex shrink-0 flex-col items-center gap-1.5">
					<button
						type="button"
						class="group relative shrink-0 rounded-xl outline-offset-2"
						onclick={() => iconInput?.click()}
						disabled={loading}
						title="Choose server icon"
					>
						<ServerAvatar name={formData.name.trim() || '?'} favicon={avatarPreview} size="xl" />
						<span
							class="absolute inset-0 flex items-center justify-center rounded-xl bg-black/55 opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100"
						>
							<Camera class="size-5 text-white" />
						</span>
					</button>
					<span class="text-[11px] text-muted-foreground">Server icon</span>
					{#if iconFile}
						<button
							type="button"
							class="text-[11px] text-muted-foreground underline-offset-2 hover:underline"
							onclick={clearIcon}
							disabled={loading}
						>
							Remove
						</button>
					{/if}
				</div>
				<input
					bind:this={iconInput}
					type="file"
					accept="image/png,image/jpeg,image/webp,image/gif"
					class="hidden"
					onchange={handleIconSelect}
				/>
				<div class="grid min-w-0 flex-1 content-start gap-4">
					<div class="grid gap-4 sm:grid-cols-[minmax(0,1fr)_8rem]">
						<div class="space-y-2">
							<Label for="name">Server name <span class="text-destructive">*</span></Label>
							<Input
								id="name"
								placeholder={namePlaceholder}
								bind:value={formData.name}
								required
								disabled={loading}
							/>
						</div>

						<div class="space-y-2">
							<Label for="max_players">Max players</Label>
							<Input
								id="max_players"
								type="number"
								min="1"
								max="1000"
								bind:value={formData.maxPlayers}
								disabled={loading}
							/>
						</div>
					</div>

					<div class="space-y-2">
						<Label for="description">
							Description <span class="text-xs text-muted-foreground">(optional)</span>
						</Label>
						<Textarea
							id="description"
							placeholder={serverDescPlaceholder}
							bind:value={formData.description}
							disabled={loading}
							class="min-h-20 resize-none"
						/>
					</div>
				</div>
			</SectionCard>

			<SectionCard
				title="Version & loader"
				description={selectedModpack ? 'Preset by the modpack' : 'Minecraft version and mod loader'}
				contentClass="grid gap-4 sm:grid-cols-2"
			>
				<div class="space-y-2">
					<Label for="mcVersion">Minecraft version</Label>
					{#if loadingVersions}
						<div class="flex h-9 items-center">
							<Loader2 class="size-4 animate-spin text-muted-foreground" />
						</div>
					{:else}
						<Select
							type="single"
							value={formData.mcVersion}
							onValueChange={(v: string | undefined) => (formData.mcVersion = v ?? '')}
							disabled={loading || !!selectedModpack}
						>
							<SelectTrigger id="mcVersion" class="w-full">
								<span>
									{formData.mcVersion || 'Select a version'}
									{formData.mcVersion === latestVersion ? ' (latest)' : ''}
								</span>
							</SelectTrigger>
							<SelectContent>
								{#each minecraftVersions as version (version)}
									<SelectItem value={version}>
										{version}
										{version === latestVersion ? '(latest)' : ''}
									</SelectItem>
								{/each}
							</SelectContent>
						</Select>
					{/if}
				</div>

				<div class="space-y-2">
					<Label for="modLoader">Mod loader</Label>
					<Select
						type="single"
						value={selectedLoaderName}
						onValueChange={(v: string | undefined) => {
							formData.modLoader =
								installableLoaders.find((l) => l.name === v)?.loader ?? ModLoader.VANILLA;
						}}
						disabled={loading || !!selectedModpack}
					>
						<SelectTrigger id="modLoader" class="w-full">
							<span>{selectedLoaderInfo?.displayName || 'Select a mod loader'}</span>
						</SelectTrigger>
						<SelectContent>
							{#each installableLoaders as loader (loader.name)}
								<SelectItem value={loader.name}>
									{loader.displayName}
								</SelectItem>
							{/each}
						</SelectContent>
					</Select>
					{#if selectedModpack}
						<p class="text-xs text-muted-foreground">Mod loader comes from the modpack</p>
					{:else if formData.modLoader === ModLoader.VANILLA}
						<p class="text-xs text-muted-foreground">Plain Minecraft, no mod support</p>
					{:else if selectedLoaderInfo?.supportsMods}
						<p class="text-xs text-muted-foreground">This loader supports mods</p>
					{/if}
				</div>
			</SectionCard>

			<SectionCard
				title="Connectivity"
				description="How players will reach the server"
				contentClass="p-0"
			>
				<ConnectivityCard
					{proxyEnabled}
					listeners={proxyListeners}
					serverName={formData.name}
					disabled={loading}
					{usedPorts}
					showCatchAll
					bind:useProxy={useProxyMode}
					bind:hostnames={formData.proxyHostnames}
					bind:listenerId={formData.proxyListenerId}
					bind:catchAll={proxyCatchAll}
					bind:port={formData.port}
					bind:portError
					onAutoAssignPort={refreshAvailablePort}
				/>
			</SectionCard>

			<SectionCard
				title="Memory"
				description="How much of the host's memory this server gets"
				contentClass="space-y-4"
			>
				<MemorySlider
					bind:memory={formData.memory}
					bind:memoryMin={formData.memoryMin}
					bind:memoryMax={formData.memoryMax}
					totalMb={hostTotalMb}
					{occupiedMb}
					disabled={loading}
				/>
			</SectionCard>

			<SectionCard
				title="Lifecycle"
				description="When the server starts and stops"
				contentClass="grid gap-3 sm:grid-cols-3"
			>
				<label
					class="flex cursor-pointer flex-col gap-1.5 rounded-lg border p-3 text-sm transition-colors hover:bg-accent/30"
				>
					<span class="flex items-center justify-between gap-2">
						<span class="font-medium">Start immediately</span>
						<Switch bind:checked={formData.startImmediately} disabled={loading} />
					</span>
					<span class="text-xs font-normal text-muted-foreground">
						Boot the server right after creation
					</span>
				</label>

				<label
					class="flex cursor-pointer flex-col gap-1.5 rounded-lg border p-3 text-sm transition-colors hover:bg-accent/30"
				>
					<span class="flex items-center justify-between gap-2">
						<span class="font-medium">Detached mode</span>
						<Switch
							bind:checked={formData.detached}
							disabled={loading || useProxyMode}
							onCheckedChange={(checked) => {
								if (checked && useProxyMode) {
									notify.error('Cannot detach proxied servers');
									formData.detached = false;
									return;
								}
								formData.detached = checked;
								if (checked) {
									formData.autoStart = false;
								}
							}}
						/>
					</span>
					<span class="text-xs font-normal text-muted-foreground">
						Keeps running when DiscoPanel stops. Not available for proxied servers.
					</span>
				</label>

				<label
					class="flex cursor-pointer flex-col gap-1.5 rounded-lg border p-3 text-sm transition-colors hover:bg-accent/30"
				>
					<span class="flex items-center justify-between gap-2">
						<span class="font-medium">Auto start</span>
						<Switch
							bind:checked={formData.autoStart}
							disabled={loading || formData.detached}
							onCheckedChange={(checked) => {
								if (formData.detached) {
									notify.error('Cannot enable auto-start for detached servers');
									formData.autoStart = false;
									return;
								}
								formData.autoStart = checked;
							}}
						/>
					</span>
					<span class="text-xs font-normal text-muted-foreground">
						Starts with DiscoPanel{formData.detached ? '. Disabled for detached servers.' : '.'}
					</span>
				</label>
			</SectionCard>

			<section class="overflow-hidden rounded-xl border bg-card">
				<button
					type="button"
					class="flex w-full cursor-pointer items-center justify-between gap-3 px-4 py-3 text-left transition-colors hover:bg-accent/30"
					onclick={() => (showAdvanced = !showAdvanced)}
				>
					<div class="min-w-0">
						<h3 class="text-sm font-semibold">Advanced</h3>
						<p class="mt-0.5 text-xs text-muted-foreground">
							Docker image, extra ports, and container overrides
						</p>
					</div>
					{#if showAdvanced}
						<ChevronUp class="size-4 shrink-0 text-muted-foreground" />
					{:else}
						<ChevronDown class="size-4 shrink-0 text-muted-foreground" />
					{/if}
				</button>

				{#if showAdvanced}
					<div class="space-y-5 border-t p-4">
						<div class="space-y-2">
							<Label for="world_zip">Import world</Label>
							<Input
								id="world_zip"
								type="file"
								accept=".zip"
								disabled={loading}
								onchange={(e: Event) => {
									const input = e.target as HTMLInputElement;
									worldFile = input.files?.[0] ?? null;
								}}
							/>
							<p class="text-xs text-muted-foreground">
								{worldFile
									? `${worldFile.name} imports at create`
									: 'World folder zip with level.dat, version detected automatically'}
							</p>
						</div>

						<div class="space-y-2">
							<Label for="docker_image">Docker image</Label>
							<Select
								type="single"
								value={formData.dockerImage}
								onValueChange={(v: string | undefined) => (formData.dockerImage = v ?? '')}
								disabled={loading || loadingVersions}
							>
								<SelectTrigger id="docker_image" class="w-full">
									<span>
										{formData.dockerImage
											? getDockerImageDisplayName(formData.dockerImage, dockerImages)
											: 'Auto-select (recommended)'}
									</span>
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="">Auto-select (recommended)</SelectItem>
									{#each getUniqueDockerImages(dockerImages) as image (image.tag)}
										<SelectItem value={image.tag}>
											{getDockerImageDisplayName(image)}
										</SelectItem>
									{/each}
								</SelectContent>
							</Select>
							<p class="text-xs text-muted-foreground">
								Leave on auto-select unless you have specific requirements
							</p>
						</div>

						<NetworkPortRowsEditor
							bind:ports={formData.additionalPorts}
							disabled={loading}
							{usedPorts}
							proxyAvailable={proxyEnabled && useProxyMode}
							serverHosts={formData.proxyHostnames}
							allowAdd
							title="Additional ports"
							description="Extra ports for mods, plugins, or services like BlueMap, voice chat, or dynmap"
						/>

						<DockerOverridesEditor
							bind:overrides={formData.dockerOverrides}
							disabled={loading}
							sourceRoots={() => volumeSourceRoots({})}
							targetRoots={[]}
							onchange={(overrides) => (formData.dockerOverrides = overrides)}
						/>
					</div>
				{/if}
			</section>
		</form>

		<aside class="sticky top-4 hidden space-y-3 lg:block">
			<div class="overflow-hidden rounded-xl border bg-card">
				<div class="border-b bg-muted/30 px-4 py-2.5">
					<span class="stat-label">Summary</span>
				</div>
				<div class="space-y-3 p-4">
					<div class="flex items-center gap-3">
						<ServerAvatar name={formData.name.trim() || '?'} favicon={avatarPreview} size="lg" />
						<div class="min-w-0">
							<p class="truncate text-sm font-semibold">
								{formData.name.trim() || 'Unnamed server'}
							</p>
							<p class="truncate text-xs text-muted-foreground">
								{selectedModpack ? selectedModpack.name : 'Disco server'}
							</p>
						</div>
					</div>

					<div class="flex flex-wrap gap-1.5">
						{#if formData.mcVersion}
							<Badge variant="secondary" class="text-xs">MC {formData.mcVersion}</Badge>
						{/if}
						{#if selectedLoaderInfo}
							<Badge variant="secondary" class="text-xs">{selectedLoaderInfo.displayName}</Badge>
						{/if}
					</div>

					<div class="space-y-2 border-t pt-3 text-xs">
						<div class="flex items-center justify-between gap-2">
							<span class="flex items-center gap-1.5 text-muted-foreground">
								<MemoryStick class="size-3.5" />
								Memory
							</span>
							<span class="tabular font-medium">{(formData.memory / 1024).toFixed(1)} GB</span>
						</div>
						<div class="flex items-center justify-between gap-2">
							<span class="flex items-center gap-1.5 text-muted-foreground">
								{#if proxyEnabled && useProxyMode}
									<Globe class="size-3.5" />
									Hostname
								{:else}
									<Cable class="size-3.5" />
									Address
								{/if}
							</span>
							<span class="tabular max-w-36 truncate font-mono font-medium" title={addressPreview}>
								{addressPreview}
							</span>
						</div>
						<div class="flex items-center justify-between gap-2">
							<span class="flex items-center gap-1.5 text-muted-foreground">
								<Zap class="size-3.5" />
								After creation
							</span>
							<span class="font-medium">
								{formData.startImmediately ? 'Starts right away' : 'Stays stopped'}
							</span>
						</div>
					</div>

					{#if !formData.name.trim()}
						<p class="rounded-md bg-muted/50 px-2.5 py-1.5 text-[11px] text-muted-foreground">
							Give the server a name to create it
						</p>
					{:else if portError}
						<p class="rounded-md bg-status-danger/10 px-2.5 py-1.5 text-[11px] text-status-danger">
							{portError}
						</p>
					{:else if hostnameMissing}
						<p class="rounded-md bg-muted/50 px-2.5 py-1.5 text-[11px] text-muted-foreground">
							Add a hostname so players can join through the proxy
						</p>
					{/if}

					{@render createButton(true)}
					<Button variant="ghost" href={resolve('/servers')} disabled={loading} class="w-full">
						Cancel
					</Button>
				</div>
			</div>
		</aside>
	</div>

	<div
		class="sticky bottom-4 z-10 flex items-center justify-between gap-3 rounded-xl border bg-card/95 px-4 py-3 shadow-lg backdrop-blur-sm lg:hidden"
	>
		<div class="min-w-0">
			<p class="truncate text-sm font-medium">{formData.name.trim() || 'Unnamed server'}</p>
			<p class="truncate font-mono text-xs text-muted-foreground">{addressPreview}</p>
		</div>
		<div class="flex shrink-0 gap-2">
			<Button variant="outline" href={resolve('/servers')} disabled={loading}>Cancel</Button>
			{@render createButton(false)}
		</div>
	</div>
</div>
