<script lang="ts">
	import { untrack } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Switch } from '$lib/components/ui/switch';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Dialog, DialogContent } from '$lib/components/ui/dialog';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import AliasHelper from '$lib/components/ui/AliasHelper.svelte';
	import { EmptyState } from '$lib/components/app';
	import { rpcClient } from '$lib/api/rpc-client';
	import { toast } from 'svelte-sonner';
	import {
		ModuleConfigFieldSchema,
		ModuleConfigFieldType,
		ModuleConfigFieldTypeSchema,
		ModuleConfigOptionSchema,
		ModuleConfigSeverity,
		ModuleConfigSeveritySchema,
		ModuleEventAction,
		ModuleEventActionSchema,
		ModuleEventHookSchema,
		NetworkPortSchema,
		ModuleProtocol,
		ModuleProtocolSchema,
		TriggeredEventType,
		VolumeMountSchema,
		type ModuleConfigField,
		type ModuleEventHook,
		type NetworkPort,
		type ModuleTemplate,
		type VolumeMount
	} from '$lib/proto/discopanel/v1/storage_pb';
	import { create, clone } from '@bufbuild/protobuf';
	import { enumDesc, enumLabel } from '$lib/proto-meta';
	import { SERVER_EVENT_TYPES, getEventTypeLabel } from '$lib/utils/events';
	import {
		Loader2,
		Plus,
		SlidersHorizontal,
		Trash2,
		X,
		FileText,
		Container,
		Network,
		Variable,
		HardDrive,
		Wrench
	} from '@lucide/svelte';

	interface Props {
		open: boolean;
		mode?: 'create' | 'edit';
		template?: ModuleTemplate;
		onSuccess: () => void;
	}

	interface EnvVar {
		key: string;
		value: string;
	}

	interface MetadataEntry {
		key: string;
		value: string;
	}

	type ConfigSection =
		| 'basic'
		| 'docker'
		| 'fields'
		| 'ports'
		| 'environment'
		| 'volumes'
		| 'advanced';

	let { open = $bindable(), mode = 'create', template, onSuccess }: Props = $props();

	let submitting = $state(false);
	let activeSection = $state<ConfigSection>('basic');

	// Form state
	let name = $state('');
	let description = $state('');
	let dockerImage = $state('');
	let healthCheckPath = $state('');
	let healthCheckPort = $state(0);
	let requiresServer = $state(true);
	let supportsProxy = $state(true);
	let icon = $state('');
	let category = $state('');
	let documentation = $state('');
	let defaultUid = $state('');
	let defaultGid = $state('');
	let defaultSecurityOpt = $state('');
	let defaultInitCommand = $state('');
	let defaultInitCommandDelay = $state(0);
	let defaultRestartAfterInit = $state(false);
	let envVars = $state<EnvVar[]>([]);
	let volumes = $state<VolumeMount[]>([]);
	let ports = $state<NetworkPort[]>([]);
	let suggestedDependencies = $state('');
	let defaultHooks = $state<ModuleEventHook[]>([]);
	let metadata = $state<MetadataEntry[]>([]);
	let configFields = $state<ModuleConfigField[]>([]);

	const navItems: {
		id: ConfigSection;
		label: string;
		title: string;
		desc: string;
		icon: typeof FileText;
	}[] = [
		{
			id: 'basic',
			label: 'Basic info',
			title: 'Basic information',
			desc: 'Template name, description, and appearance',
			icon: FileText
		},
		{
			id: 'docker',
			label: 'Docker',
			title: 'Docker configuration',
			desc: 'Container image, health check, and behavior',
			icon: Container
		},
		{
			id: 'fields',
			label: 'Config fields',
			title: 'Config fields',
			desc: 'Typed inputs shown when creating instances',
			icon: SlidersHorizontal
		},
		{
			id: 'ports',
			label: 'Ports',
			title: 'Port configuration',
			desc: 'Default port mappings for the container',
			icon: Network
		},
		{
			id: 'environment',
			label: 'Environment',
			title: 'Environment variables',
			desc: 'Default environment variables for new instances',
			icon: Variable
		},
		{
			id: 'volumes',
			label: 'Volumes',
			title: 'Volume mounts',
			desc: 'Default volume mounts for new instances',
			icon: HardDrive
		},
		{
			id: 'advanced',
			label: 'Advanced',
			title: 'Advanced settings',
			desc: 'Dependencies, hooks, init command, and metadata',
			icon: Wrench
		}
	];

	let activeItem = $derived(navItems.find((item) => item.id === activeSection) ?? navItems[0]);

	// Collects env rows into the proto map field
	function envVarsToMap(): { [key: string]: string } {
		const map: { [key: string]: string } = {};
		for (const env of envVars) {
			if (env.key.trim()) {
				map[env.key.trim()] = env.value;
			}
		}
		return map;
	}

	function addEnvVar() {
		envVars = [...envVars, { key: '', value: '' }];
	}

	function removeEnvVar(index: number) {
		envVars = envVars.filter((_, i) => i !== index);
	}

	function addVolume() {
		volumes = [...volumes, create(VolumeMountSchema, {})];
	}

	function removeVolume(index: number) {
		volumes = volumes.filter((_, i) => i !== index);
	}

	function addPort() {
		ports = [
			...ports,
			create(NetworkPortSchema, {
				name: '',
				containerPort: 0,
				hostPort: 0,
				protocol: ModuleProtocol.TCP,
				proxyEnabled: supportsProxy
			})
		];
	}

	function removePort(index: number) {
		ports = ports.filter((_, i) => i !== index);
	}

	function addDefaultHook() {
		defaultHooks = [
			...defaultHooks,
			create(ModuleEventHookSchema, {
				event: TriggeredEventType.SERVER_START,
				action: ModuleEventAction.START,
				command: '',
				delaySeconds: 0,
				condition: ''
			})
		];
	}

	function removeDefaultHook(index: number) {
		defaultHooks = defaultHooks.filter((_, i) => i !== index);
	}

	function addMetadataEntry() {
		metadata = [...metadata, { key: '', value: '' }];
	}

	function removeMetadataEntry(index: number) {
		metadata = metadata.filter((_, i) => i !== index);
	}

	function metadataToMap(): { [key: string]: string } {
		const map: { [key: string]: string } = {};
		for (const entry of metadata) {
			if (entry.key.trim()) {
				map[entry.key.trim()] = entry.value;
			}
		}
		return map;
	}

	function addConfigField() {
		configFields = [
			...configFields,
			create(ModuleConfigFieldSchema, {
				type: ModuleConfigFieldType.STRING,
				severity: ModuleConfigSeverity.WARN
			})
		];
	}

	function removeConfigField(index: number) {
		configFields = configFields.filter((_, i) => i !== index);
	}

	function addFieldOption(field: ModuleConfigField) {
		field.options = [...field.options, create(ModuleConfigOptionSchema, {})];
	}

	function removeFieldOption(field: ModuleConfigField, index: number) {
		field.options = field.options.filter((_, i) => i !== index);
	}

	// Display order for field type choices
	const FIELD_TYPE_OPTIONS: ModuleConfigFieldType[] = [
		ModuleConfigFieldType.STRING,
		ModuleConfigFieldType.PASSWORD,
		ModuleConfigFieldType.INT,
		ModuleConfigFieldType.BOOL,
		ModuleConfigFieldType.SELECT,
		ModuleConfigFieldType.MULTILINE
	];

	const FIELD_SEVERITY_OPTIONS: ModuleConfigSeverity[] = [
		ModuleConfigSeverity.WARN,
		ModuleConfigSeverity.DENY
	];

	// Regex only makes sense for free text kinds
	function fieldSupportsRegex(type: ModuleConfigFieldType): boolean {
		return (
			type === ModuleConfigFieldType.STRING ||
			type === ModuleConfigFieldType.PASSWORD ||
			type === ModuleConfigFieldType.MULTILINE
		);
	}

	// Display order for event action choices
	const EVENT_ACTION_OPTIONS: ModuleEventAction[] = [
		ModuleEventAction.START,
		ModuleEventAction.STOP,
		ModuleEventAction.RESTART,
		ModuleEventAction.EXEC,
		ModuleEventAction.RCON
	];

	function getEventActionLabel(action: ModuleEventAction): string {
		return (
			enumLabel(ModuleEventActionSchema, action) ||
			enumLabel(ModuleEventActionSchema, ModuleEventAction.UNSPECIFIED)
		);
	}

	// Snapshots template once so reloads keep edits
	$effect(() => {
		if (open) {
			untrack(() => {
				if (mode === 'edit' && template) {
					loadTemplateData(template);
				} else if (mode === 'create') {
					resetForm();
				}
			});
		} else {
			resetForm();
		}
	});

	function loadTemplateData(t: ModuleTemplate) {
		name = t.name;
		description = t.description;
		dockerImage = t.dockerImage;
		healthCheckPath = t.healthCheckPath;
		healthCheckPort = t.healthCheckPort;
		requiresServer = t.requiresServer;
		supportsProxy = t.supportsProxy;
		icon = t.icon;
		category = t.category;
		documentation = t.documentation;
		defaultUid = t.defaultUid;
		defaultGid = t.defaultGid;
		defaultSecurityOpt = t.defaultSecurityOpt.join(', ');
		defaultInitCommand = t.defaultInitCommand;
		defaultInitCommandDelay = t.defaultInitCommandDelay;
		defaultRestartAfterInit = t.defaultRestartAfterInit;

		envVars = Object.entries(t.defaultEnv).map(([key, value]) => ({ key, value }));
		volumes = t.defaultVolumes.map((v) => clone(VolumeMountSchema, v));
		ports = t.ports.map((p) => clone(NetworkPortSchema, p));
		configFields = t.configFields.map((f) => clone(ModuleConfigFieldSchema, f));
		suggestedDependencies = t.suggestedDependencies.join(', ');
		defaultHooks = t.defaultHooks.map((h) => clone(ModuleEventHookSchema, h));

		metadata = Object.entries(t.metadata || {}).map(([key, value]) => ({ key, value }));
		activeSection = 'basic';
	}

	function resetForm() {
		name = '';
		description = '';
		dockerImage = '';
		healthCheckPath = '';
		healthCheckPort = 0;
		requiresServer = true;
		supportsProxy = true;
		icon = '';
		category = '';
		documentation = '';
		defaultUid = '';
		defaultGid = '';
		defaultSecurityOpt = '';
		defaultInitCommand = '';
		defaultInitCommandDelay = 0;
		defaultRestartAfterInit = false;
		envVars = [];
		volumes = [];
		ports = [];
		suggestedDependencies = '';
		defaultHooks = [];
		metadata = [];
		configFields = [];
		activeSection = 'basic';
	}

	async function handleSubmit() {
		if (!name.trim() || !dockerImage.trim()) return;

		submitting = true;
		try {
			const validPorts = ports.filter((p) => p.containerPort > 0);
			const droppedPorts = ports.length - validPorts.length;
			if (droppedPorts > 0) {
				toast.warning(
					`Ignored ${droppedPorts} port row${droppedPorts === 1 ? '' : 's'} without a container port`
				);
			}
			const validFields = configFields.filter((f) => f.env.trim());
			const droppedFields = configFields.length - validFields.length;
			if (droppedFields > 0) {
				toast.warning(
					`Ignored ${droppedFields} config field${droppedFields === 1 ? '' : 's'} without an env name`
				);
			}
			for (const f of validFields) {
				f.env = f.env.trim();
				f.options = f.options.filter((o) => o.value.trim());
			}
			for (const v of volumes) {
				v.source = v.source.trim();
				v.target = v.target.trim();
			}
			const payload = {
				name: name.trim(),
				description: description.trim(),
				dockerImage: dockerImage.trim(),
				configFields: validFields,
				defaultEnv: envVarsToMap(),
				defaultVolumes: volumes.filter((v) => v.source && v.target),
				healthCheckPath: healthCheckPath.trim(),
				healthCheckPort,
				requiresServer,
				supportsProxy,
				icon: icon.trim(),
				category: category.trim(),
				documentation: documentation.trim(),
				ports: validPorts,
				suggestedDependencies: suggestedDependencies.trim()
					? suggestedDependencies
							.split(',')
							.map((s) => s.trim())
							.filter((s) => s)
					: [],
				defaultHooks,
				metadata: metadataToMap(),
				defaultUid,
				defaultGid,
				defaultSecurityOpt: defaultSecurityOpt.trim()
					? defaultSecurityOpt
							.split(',')
							.map((s) => s.trim())
							.filter((s) => s)
					: [],
				defaultInitCommand,
				defaultInitCommandDelay,
				defaultRestartAfterInit
			};

			if (mode === 'edit' && template) {
				await rpcClient.module.updateModuleTemplate({ id: template.id, ...payload });
				toast.success(`Template "${name}" updated`);
			} else {
				await rpcClient.module.createModuleTemplate(payload);
				toast.success(`Template "${name}" created`);
			}

			open = false;
			onSuccess();
		} catch (error) {
			toast.error(
				`Failed to ${mode} template: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			submitting = false;
		}
	}
</script>

<Dialog bind:open>
	<DialogContent
		class="flex h-[85vh]! w-[95vw]! max-w-4xl! flex-col gap-0! overflow-hidden p-0!"
		showCloseButton={false}
	>
		<div class="flex h-full min-h-0">
			<!-- Section nav sidebar -->
			<div class="flex w-64 shrink-0 flex-col border-r bg-card/40">
				<div class="border-b px-5 py-4">
					<p class="stat-label">Custom template</p>
					<h3 class="mt-1 truncate text-sm font-semibold">
						{name || (mode === 'create' ? 'New template' : 'Edit template')}
					</h3>
				</div>

				<nav class="flex-1 space-y-0.5 overflow-y-auto p-3">
					{#each navItems as item (item.id)}
						{@const Icon = item.icon}
						<button
							type="button"
							onclick={() => (activeSection = item.id)}
							class="flex w-full items-center gap-2.5 rounded-md px-3 py-2 text-left text-sm transition-colors {activeSection ===
							item.id
								? 'bg-accent font-medium text-foreground'
								: 'text-muted-foreground hover:bg-accent/40 hover:text-foreground'}"
						>
							<Icon class="size-4 shrink-0" />
							{item.label}
						</button>
					{/each}
				</nav>

				<div class="border-t p-3">
					<div class="rounded-lg border bg-card p-3">
						<p class="text-sm font-medium">Template aliases</p>
						<p class="mt-1 mb-3 text-xs text-muted-foreground">
							Use aliases for dynamic values in any configuration field.
						</p>
						<AliasHelper showLabel />
					</div>
				</div>
			</div>

			<!-- Section content -->
			<div class="flex min-w-0 flex-1 flex-col">
				<div class="flex items-start justify-between gap-4 border-b px-6 py-4">
					<div class="min-w-0">
						<h2 class="text-lg font-semibold">{activeItem.title}</h2>
						<p class="mt-0.5 text-sm text-muted-foreground">{activeItem.desc}</p>
					</div>
					<Button
						variant="ghost"
						size="icon"
						onclick={() => (open = false)}
						class="size-8 shrink-0"
					>
						<X class="size-4" />
					</Button>
				</div>

				<div class="min-h-0 flex-1 overflow-y-auto px-6 py-5">
					{#if activeSection === 'basic'}
						<div class="space-y-6">
							<div class="space-y-2">
								<Label for="tpl-name">Template name *</Label>
								<Input id="tpl-name" bind:value={name} placeholder="My Custom Module" />
								<p class="text-xs text-muted-foreground">
									A descriptive name for this module template
								</p>
							</div>

							<div class="space-y-2">
								<Label for="tpl-description">Description</Label>
								<Textarea
									id="tpl-description"
									bind:value={description}
									placeholder="What does this module do? Describe its purpose and features."
									rows={4}
								/>
							</div>

							<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
								<div class="space-y-2">
									<Label for="tpl-category">Category</Label>
									<Input
										id="tpl-category"
										bind:value={category}
										placeholder="monitoring, maps, voice..."
									/>
									<p class="text-xs text-muted-foreground">Group similar templates</p>
								</div>
								<div class="space-y-2">
									<Label for="tpl-icon">Icon</Label>
									<Input
										id="tpl-icon"
										bind:value={icon}
										placeholder="chart-bar, map, microphone..."
									/>
									<p class="text-xs text-muted-foreground">
										Lucide icon name from <a
											href="https://lucide.dev"
											target="_blank"
											rel="noopener noreferrer"
											class="underline underline-offset-2 hover:text-foreground">lucide.dev</a
										>
									</p>
								</div>
							</div>
						</div>
					{:else if activeSection === 'docker'}
						<div class="space-y-6">
							<div class="space-y-2">
								<Label for="tpl-image">Docker image *</Label>
								<Input
									id="tpl-image"
									bind:value={dockerImage}
									placeholder="nginx:latest, redis:alpine, myregistry/myimage:v1"
									class="font-mono"
								/>
								<p class="text-xs text-muted-foreground">
									The Docker image to pull and run for this module
								</p>
							</div>

							<div class="rounded-lg border bg-card">
								<div class="border-b px-4 py-3">
									<span class="stat-label">Health check</span>
									<p class="mt-1 text-xs text-muted-foreground">
										Configure how to verify the container is healthy
									</p>
								</div>
								<div class="grid grid-cols-1 gap-4 p-4 sm:grid-cols-2">
									<div class="space-y-2">
										<Label for="tpl-hc-path">Health check path</Label>
										<Input
											id="tpl-hc-path"
											bind:value={healthCheckPath}
											placeholder="/health or /api/status"
										/>
										<p class="text-xs text-muted-foreground">HTTP endpoint to check</p>
									</div>
									<div class="space-y-2">
										<Label for="tpl-hc-port">Health check port</Label>
										<Input
											id="tpl-hc-port"
											type="number"
											bind:value={healthCheckPort}
											min={0}
											max={65535}
										/>
										<p class="text-xs text-muted-foreground">0 = use first configured port</p>
									</div>
								</div>
							</div>

							<div class="rounded-lg border bg-card">
								<div class="border-b px-4 py-3">
									<span class="stat-label">Container user</span>
									<p class="mt-1 text-xs text-muted-foreground">
										Default UID/GID for the container process
									</p>
								</div>
								<div class="grid grid-cols-1 gap-4 p-4 sm:grid-cols-2">
									<div class="space-y-2">
										<Label for="tpl-uid">Default UID</Label>
										<Input
											id="tpl-uid"
											bind:value={defaultUid}
											placeholder={'{{host.uid}}'}
											class="font-mono"
										/>
										<p class="text-xs text-muted-foreground">User ID or alias</p>
									</div>
									<div class="space-y-2">
										<Label for="tpl-gid">Default GID</Label>
										<Input
											id="tpl-gid"
											bind:value={defaultGid}
											placeholder={'{{host.gid}}'}
											class="font-mono"
										/>
										<p class="text-xs text-muted-foreground">Group ID or alias</p>
									</div>
								</div>
							</div>

							<div class="rounded-lg border bg-card">
								<div class="border-b px-4 py-3">
									<span class="stat-label">Security options</span>
									<p class="mt-1 text-xs text-muted-foreground">
										Docker security options applied to the container
									</p>
								</div>
								<div class="space-y-2 p-4">
									<Label for="tpl-secopt">Security options</Label>
									<Input
										id="tpl-secopt"
										bind:value={defaultSecurityOpt}
										placeholder="seccomp=unconfined, apparmor=unconfined"
										class="font-mono"
									/>
									<p class="text-xs text-muted-foreground">
										Comma-separated, e.g. for containers that need user namespaces
									</p>
								</div>
							</div>

							<div class="space-y-3">
								<span class="stat-label">Behavior flags</span>
								<label
									class="flex cursor-pointer items-center justify-between gap-4 rounded-lg border bg-card p-4 transition-colors hover:bg-accent/50"
								>
									<div class="space-y-0.5">
										<span class="text-sm font-medium">Requires server</span>
										<p class="text-xs text-muted-foreground">
											This module must be attached to a game server
										</p>
									</div>
									<Switch bind:checked={requiresServer} />
								</label>
								<label
									class="flex cursor-pointer items-center justify-between gap-4 rounded-lg border bg-card p-4 transition-colors hover:bg-accent/50"
								>
									<div class="space-y-0.5">
										<span class="text-sm font-medium">Supports proxy</span>
										<p class="text-xs text-muted-foreground">
											Can be accessed through the server's proxy hostname
										</p>
									</div>
									<Switch bind:checked={supportsProxy} />
								</label>
							</div>
						</div>
					{:else if activeSection === 'fields'}
						<div class="space-y-4">
							<div class="flex items-start justify-between gap-4">
								<div>
									<p class="text-sm font-medium">
										{configFields.length} field{configFields.length !== 1 ? 's' : ''} defined
									</p>
									<p class="mt-0.5 text-xs text-muted-foreground">
										Fields render as a form and validate instance config
									</p>
								</div>
								<Button size="sm" onclick={addConfigField}>
									<Plus class="size-4" />
									Add field
								</Button>
							</div>

							{#if configFields.length > 0}
								<div class="space-y-3">
									{#each configFields as field, i (i)}
										<div class="space-y-4 rounded-lg border bg-card p-4">
											<div class="flex items-center justify-between">
												<span class="stat-label">Field {i + 1}</span>
												<Button
													variant="ghost"
													size="icon"
													onclick={() => removeConfigField(i)}
													class="size-7 text-muted-foreground hover:text-destructive"
												>
													<Trash2 class="size-4" />
												</Button>
											</div>

											<div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
												<div class="space-y-2">
													<Label>Env variable *</Label>
													<Input
														bind:value={field.env}
														placeholder="SECRET_KEY"
														class="font-mono"
													/>
												</div>
												<div class="space-y-2">
													<Label>Label</Label>
													<Input bind:value={field.label} placeholder="Agent secret key" />
												</div>
												<div class="space-y-2">
													<Label>Type</Label>
													<Select
														type="single"
														value={String(field.type)}
														onValueChange={(v) => {
															if (v) field.type = Number(v);
														}}
													>
														<SelectTrigger class="w-full">
															<span class="truncate">
																{enumLabel(
																	ModuleConfigFieldTypeSchema,
																	field.type || ModuleConfigFieldType.STRING
																)}
															</span>
														</SelectTrigger>
														<SelectContent>
															{#each FIELD_TYPE_OPTIONS as t (t)}
																<SelectItem value={String(t)}>
																	{enumLabel(ModuleConfigFieldTypeSchema, t)}
																</SelectItem>
															{/each}
														</SelectContent>
													</Select>
												</div>
											</div>

											<div class="space-y-2">
												<Label>Description</Label>
												<Input
													bind:value={field.description}
													placeholder="Help text shown under the input"
												/>
											</div>

											<div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
												<div class="space-y-2">
													<Label>Default value</Label>
													<Input
														bind:value={field.defaultValue}
														placeholder="value or {'{{alias}}'}"
														class="font-mono"
													/>
												</div>
												<div class="space-y-2">
													<Label>Placeholder</Label>
													<Input bind:value={field.placeholder} placeholder="Input hint" />
												</div>
												<div class="space-y-2">
													<Label>Group</Label>
													<Input bind:value={field.group} placeholder="Optional section heading" />
												</div>
											</div>

											<div class="flex flex-wrap items-end gap-4">
												<label class="flex cursor-pointer items-center gap-2 pb-2">
													<Checkbox bind:checked={field.required} />
													<span class="text-sm">Required</span>
												</label>
												{#if field.required}
													<div class="space-y-2">
														<Label>Required unless</Label>
														<Input
															bind:value={field.requiredUnless}
															placeholder="OTHER_ENV_KEY"
															class="w-48 font-mono"
														/>
													</div>
												{/if}
												<div class="space-y-2">
													<Label>On violation</Label>
													<Select
														type="single"
														value={String(field.severity)}
														onValueChange={(v) => {
															if (v) field.severity = Number(v);
														}}
													>
														<SelectTrigger class="w-40">
															<span class="truncate">
																{enumLabel(
																	ModuleConfigSeveritySchema,
																	field.severity || ModuleConfigSeverity.WARN
																)}
															</span>
														</SelectTrigger>
														<SelectContent>
															{#each FIELD_SEVERITY_OPTIONS as s (s)}
																<SelectItem value={String(s)}>
																	{enumLabel(ModuleConfigSeveritySchema, s)}
																</SelectItem>
															{/each}
														</SelectContent>
													</Select>
												</div>
											</div>

											{#if field.type === ModuleConfigFieldType.INT}
												<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
													<div class="space-y-2">
														<Label>Minimum</Label>
														<Input
															type="number"
															value={field.min ?? ''}
															oninput={(e) => {
																const v = e.currentTarget.value;
																field.min = v === '' ? undefined : Number(v);
															}}
															placeholder="No minimum"
														/>
													</div>
													<div class="space-y-2">
														<Label>Maximum</Label>
														<Input
															type="number"
															value={field.max ?? ''}
															oninput={(e) => {
																const v = e.currentTarget.value;
																field.max = v === '' ? undefined : Number(v);
															}}
															placeholder="No maximum"
														/>
													</div>
												</div>
											{/if}

											{#if fieldSupportsRegex(field.type)}
												<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
													<div class="space-y-2">
														<Label>Pattern (RE2)</Label>
														<Input
															bind:value={field.regex}
															placeholder="^[0-9]+$"
															class="font-mono"
														/>
													</div>
													<div class="space-y-2">
														<Label>Pattern message</Label>
														<Input
															bind:value={field.regexMessage}
															placeholder="Shown when the pattern fails"
														/>
													</div>
												</div>
											{/if}

											{#if field.type === ModuleConfigFieldType.SELECT}
												<div class="space-y-2">
													<div class="flex items-center justify-between">
														<Label>Options</Label>
														<Button
															variant="outline"
															size="sm"
															onclick={() => addFieldOption(field)}
														>
															<Plus class="size-4" />
															Add option
														</Button>
													</div>
													{#if field.options.length > 0}
														<div class="space-y-2">
															{#each field.options as opt, oi (oi)}
																<div class="flex items-center gap-2">
																	<Input
																		bind:value={opt.value}
																		placeholder="stored value"
																		class="w-48 font-mono"
																	/>
																	<Input
																		bind:value={opt.label}
																		placeholder="display label"
																		class="flex-1"
																	/>
																	<Button
																		variant="ghost"
																		size="icon"
																		onclick={() => removeFieldOption(field, oi)}
																		class="size-7 shrink-0 text-muted-foreground hover:text-destructive"
																	>
																		<Trash2 class="size-4" />
																	</Button>
																</div>
															{/each}
														</div>
													{:else}
														<p class="text-xs text-muted-foreground">
															Select fields need at least one option
														</p>
													{/if}
												</div>
											{/if}
										</div>
									{/each}
								</div>
							{:else}
								<div class="rounded-xl border border-dashed">
									<EmptyState
										icon={SlidersHorizontal}
										title="No config fields defined"
										description="Add typed inputs so instances get a real form"
									>
										<Button variant="outline" size="sm" onclick={addConfigField}>
											<Plus class="size-4" />
											Add field
										</Button>
									</EmptyState>
								</div>
							{/if}
						</div>
					{:else if activeSection === 'ports'}
						<div class="space-y-4">
							<div class="flex items-start justify-between gap-4">
								<div>
									<p class="text-sm font-medium">
										{ports.length} port{ports.length !== 1 ? 's' : ''} configured
									</p>
									<p class="mt-0.5 text-xs text-muted-foreground">
										Host port 0 = auto-allocate when creating module instances
									</p>
								</div>
								<Button size="sm" onclick={addPort}>
									<Plus class="size-4" />
									Add port
								</Button>
							</div>

							{#if ports.length > 0}
								<div class="space-y-3">
									{#each ports as port, i (i)}
										<div class="space-y-4 rounded-lg border bg-card p-4">
											<div class="flex items-center justify-between">
												<span class="stat-label">Port {i + 1}</span>
												<Button
													variant="ghost"
													size="icon"
													onclick={() => removePort(i)}
													class="size-7 text-muted-foreground hover:text-destructive"
												>
													<Trash2 class="size-4" />
												</Button>
											</div>

											<div class="space-y-2">
												<Label>Port name</Label>
												<Input bind:value={port.name} placeholder="Web UI, API, Metrics..." />
											</div>

											<div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
												<div class="space-y-2">
													<Label>Host port</Label>
													<Input
														type="number"
														bind:value={port.hostPort}
														min={0}
														max={65535}
														placeholder="0 = auto"
													/>
												</div>
												<div class="space-y-2">
													<Label>Container port</Label>
													<Input
														type="number"
														bind:value={port.containerPort}
														min={1}
														max={65535}
														placeholder="8080"
													/>
												</div>
												<div class="space-y-2">
													<Label>Protocol</Label>
													<Select
														type="single"
														value={String(port.protocol)}
														onValueChange={(v) => {
															if (v) port.protocol = Number(v);
														}}
													>
														<SelectTrigger class="w-full">
															<span class="uppercase">
																{enumLabel(
																	ModuleProtocolSchema,
																	port.protocol || ModuleProtocol.TCP
																)}
															</span>
														</SelectTrigger>
														<SelectContent>
															{#each [ModuleProtocol.TCP, ModuleProtocol.UDP, ModuleProtocol.MINECRAFT, ModuleProtocol.HTTP] as proto (proto)}
																<SelectItem value={String(proto)}>
																	{enumLabel(ModuleProtocolSchema, proto)}
																</SelectItem>
															{/each}
														</SelectContent>
													</Select>
												</div>
											</div>

											<label class="flex w-fit cursor-pointer items-center gap-2">
												<Checkbox bind:checked={port.proxyEnabled} />
												<span class="text-sm">Route through proxy</span>
											</label>
										</div>
									{/each}
								</div>
							{:else}
								<div class="rounded-xl border border-dashed">
									<EmptyState
										icon={Network}
										title="No ports configured"
										description="Add ports to expose container services"
									>
										<Button variant="outline" size="sm" onclick={addPort}>
											<Plus class="size-4" />
											Add port
										</Button>
									</EmptyState>
								</div>
							{/if}
						</div>
					{:else if activeSection === 'environment'}
						<div class="space-y-4">
							<div class="flex items-start justify-between gap-4">
								<div>
									<p class="text-sm font-medium">
										{envVars.length} variable{envVars.length !== 1 ? 's' : ''} defined
									</p>
									<p class="mt-0.5 text-xs text-muted-foreground">
										Use template aliases like {'{{server.data_path}}'} for dynamic values
									</p>
								</div>
								<Button size="sm" onclick={addEnvVar}>
									<Plus class="size-4" />
									Add variable
								</Button>
							</div>

							{#if envVars.length > 0}
								<div class="space-y-2">
									{#each envVars as env, i (i)}
										<div class="flex items-center gap-2 rounded-lg border bg-card px-3 py-2.5">
											<Input
												bind:value={env.key}
												placeholder="VARIABLE_NAME"
												class="w-56 font-mono"
											/>
											<span class="font-mono text-sm text-muted-foreground">=</span>
											<Input
												bind:value={env.value}
												placeholder="value or {'{{alias}}'}"
												class="flex-1 font-mono"
											/>
											<Button
												variant="ghost"
												size="icon"
												onclick={() => removeEnvVar(i)}
												class="size-7 shrink-0 text-muted-foreground hover:text-destructive"
											>
												<Trash2 class="size-4" />
											</Button>
										</div>
									{/each}
								</div>
							{:else}
								<div class="rounded-xl border border-dashed">
									<EmptyState
										icon={Variable}
										title="No environment variables"
										description="Add default variables for container configuration"
									>
										<Button variant="outline" size="sm" onclick={addEnvVar}>
											<Plus class="size-4" />
											Add variable
										</Button>
									</EmptyState>
								</div>
							{/if}
						</div>
					{:else if activeSection === 'volumes'}
						<div class="space-y-4">
							<div class="flex items-start justify-between gap-4">
								<div>
									<p class="text-sm font-medium">
										{volumes.length} volume{volumes.length !== 1 ? 's' : ''} configured
									</p>
									<p class="mt-0.5 text-xs text-muted-foreground">
										Use template aliases like {'{{module.data_path}}'} for dynamic paths
									</p>
								</div>
								<Button size="sm" onclick={addVolume}>
									<Plus class="size-4" />
									Add volume
								</Button>
							</div>

							{#if volumes.length > 0}
								<div class="space-y-3">
									{#each volumes as vol, i (i)}
										<div class="space-y-4 rounded-lg border bg-card p-4">
											<div class="flex items-center justify-between">
												<span class="stat-label">Volume {i + 1}</span>
												<Button
													variant="ghost"
													size="icon"
													onclick={() => removeVolume(i)}
													class="size-7 text-muted-foreground hover:text-destructive"
												>
													<Trash2 class="size-4" />
												</Button>
											</div>

											<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
												<div class="space-y-2">
													<Label>Host path</Label>
													<Input
														bind:value={vol.source}
														placeholder="/host/path or {'{{alias}}'}"
														class="font-mono"
													/>
												</div>
												<div class="space-y-2">
													<Label>Container path</Label>
													<Input
														bind:value={vol.target}
														placeholder="/container/path"
														class="font-mono"
													/>
												</div>
											</div>

											<div class="flex items-center gap-6">
												<label class="flex cursor-pointer items-center gap-2">
													<Checkbox
														checked={vol.readOnly}
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
							{:else}
								<div class="rounded-xl border border-dashed">
									<EmptyState
										icon={HardDrive}
										title="No volumes configured"
										description="Mount host directories for persistent data"
									>
										<Button variant="outline" size="sm" onclick={addVolume}>
											<Plus class="size-4" />
											Add volume
										</Button>
									</EmptyState>
								</div>
							{/if}
						</div>
					{:else if activeSection === 'advanced'}
						<div class="space-y-8">
							<!-- Suggested dependencies group -->
							<div class="space-y-3">
								<div>
									<h3 class="text-sm font-medium">Suggested dependencies</h3>
									<p class="mt-0.5 text-xs text-muted-foreground">
										Template IDs this module commonly needs (comma-separated)
									</p>
								</div>
								<Input
									bind:value={suggestedDependencies}
									placeholder="redis, mysql, prometheus..."
									class="max-w-xl font-mono"
								/>
							</div>

							<!-- Default hooks group -->
							<div class="space-y-3">
								<div class="flex items-start justify-between gap-4">
									<div>
										<h3 class="text-sm font-medium">Default event hooks</h3>
										<p class="mt-0.5 text-xs text-muted-foreground">
											Pre-configured hooks for server lifecycle events
										</p>
									</div>
									<Button variant="outline" size="sm" onclick={addDefaultHook}>
										<Plus class="size-4" />
										Add hook
									</Button>
								</div>

								{#if defaultHooks.length > 0}
									<div class="space-y-3">
										{#each defaultHooks as hook, i (i)}
											<div class="space-y-4 rounded-lg border bg-card p-4">
												<div class="flex items-center justify-between">
													<span class="stat-label">Hook {i + 1}</span>
													<Button
														variant="ghost"
														size="icon"
														onclick={() => removeDefaultHook(i)}
														class="size-7 text-muted-foreground hover:text-destructive"
													>
														<Trash2 class="size-4" />
													</Button>
												</div>

												<div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
													<div class="space-y-2">
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
													<div class="space-y-2">
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
																				<span class="text-xs text-muted-foreground"
																					>{enumDesc(ModuleEventActionSchema, a)}</span
																				>
																			{/if}
																		</div>
																	</SelectItem>
																{/each}
															</SelectContent>
														</Select>
													</div>
													<div class="space-y-2">
														<Label>Delay (seconds)</Label>
														<Input type="number" bind:value={hook.delaySeconds} min={0} />
													</div>
												</div>

												{#if hook.action === ModuleEventAction.EXEC || hook.action === ModuleEventAction.RCON}
													<div class="space-y-2">
														<Label>Command</Label>
														<Input
															bind:value={hook.command}
															placeholder={hook.action === ModuleEventAction.RCON
																? 'say Hello'
																: '/bin/sh -c "..."'}
															class="font-mono"
														/>
													</div>
												{/if}

												<div class="space-y-2">
													<Label>Condition (optional)</Label>
													<Input
														bind:value={hook.condition}
														placeholder={'{{server.players_online}} == 0'}
														class="font-mono"
													/>
												</div>
											</div>
										{/each}
									</div>
								{:else}
									<div
										class="rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground"
									>
										No default event hooks configured
									</div>
								{/if}
							</div>

							<!-- Init command group -->
							<div class="space-y-3">
								<div>
									<h3 class="text-sm font-medium">Default init command</h3>
									<p class="mt-0.5 text-xs text-muted-foreground">
										Command to exec inside the container after it starts
									</p>
								</div>

								<div class="space-y-4 rounded-lg border bg-card p-4">
									<div class="space-y-2">
										<Label>Command</Label>
										<Input
											bind:value={defaultInitCommand}
											placeholder="sh -c 'sed -i ...'"
											class="font-mono"
										/>
										<p class="text-xs text-muted-foreground">
											Shell command to exec inside the container after start
										</p>
									</div>
									<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
										<div class="space-y-2">
											<Label>Delay (seconds)</Label>
											<Input type="number" bind:value={defaultInitCommandDelay} min={0} />
											<p class="text-xs text-muted-foreground">
												Seconds to wait after start before running
											</p>
										</div>
										<label class="flex cursor-pointer items-start gap-2 sm:pt-7">
											<Checkbox bind:checked={defaultRestartAfterInit} />
											<div class="space-y-0.5">
												<span class="text-sm font-medium">Restart after init</span>
												<p class="text-xs text-muted-foreground">
													Restart the container after the command runs
												</p>
											</div>
										</label>
									</div>
								</div>
							</div>

							<!-- Default metadata group -->
							<div class="space-y-3">
								<div class="flex items-start justify-between gap-4">
									<div>
										<h3 class="text-sm font-medium">Default metadata</h3>
										<p class="mt-0.5 text-xs text-muted-foreground">
											Custom key-value pairs for notes, instructions, or links
										</p>
									</div>
									<Button variant="outline" size="sm" onclick={addMetadataEntry}>
										<Plus class="size-4" />
										Add entry
									</Button>
								</div>

								{#if metadata.length > 0}
									<div class="space-y-2">
										{#each metadata as entry, i (i)}
											<div class="flex items-center gap-2 rounded-lg border bg-card px-3 py-2.5">
												<Input bind:value={entry.key} placeholder="key" class="w-48 font-mono" />
												<span class="font-mono text-sm text-muted-foreground">:</span>
												<Input
													bind:value={entry.value}
													placeholder="value"
													class="flex-1 font-mono"
												/>
												<Button
													variant="ghost"
													size="icon"
													onclick={() => removeMetadataEntry(i)}
													class="size-7 shrink-0 text-muted-foreground hover:text-destructive"
												>
													<Trash2 class="size-4" />
												</Button>
											</div>
										{/each}
									</div>
								{:else}
									<div
										class="rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground"
									>
										No metadata entries
									</div>
								{/if}
							</div>

							<!-- Documentation group -->
							<div class="space-y-3">
								<div>
									<h3 class="text-sm font-medium">Documentation</h3>
									<p class="mt-0.5 text-xs text-muted-foreground">
										Usage instructions, configuration notes, or helpful information
									</p>
								</div>
								<Textarea
									bind:value={documentation}
									placeholder="# Getting Started&#10;&#10;Describe how to configure and use this module..."
									rows={8}
									class="font-mono"
								/>
							</div>
						</div>
					{/if}
				</div>

				<!-- Footer actions -->
				<div class="flex items-center justify-end gap-2 border-t px-6 py-4">
					<Button variant="outline" onclick={() => (open = false)}>Cancel</Button>
					<Button
						onclick={handleSubmit}
						disabled={!name.trim() || !dockerImage.trim() || submitting}
						class="min-w-[120px]"
					>
						{#if submitting}
							<Loader2 class="size-4 animate-spin" />
							{mode === 'create' ? 'Creating...' : 'Saving...'}
						{:else}
							{mode === 'create' ? 'Create template' : 'Save changes'}
						{/if}
					</Button>
				</div>
			</div>
		</div>
	</DialogContent>
</Dialog>
