<script lang="ts">
	import { untrack } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Switch } from '$lib/components/ui/switch';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Dialog, DialogContent } from '$lib/components/ui/dialog';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import AliasHelper from '$lib/components/ui/AliasHelper.svelte';
	import {
		CollectionSection,
		EnumSelect,
		EventHookRowsEditor,
		InitCommandFields,
		KeyValueRowsEditor,
		LabeledInput,
		NetworkPortRowsEditor,
		SectionedDialogLayout,
		VolumeMountRowsEditor
	} from '$lib/components/app';
	import { rpcClient, rpcErrorMessage } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import {
		ModuleConfigFieldSchema,
		ModuleConfigFieldType,
		ModuleConfigFieldTypeSchema,
		ModuleConfigOptionSchema,
		ModuleConfigSeverity,
		ModuleConfigSeveritySchema,
		ModuleEventHookSchema,
		ModuleTemplateSchema,
		NetworkPortSchema,
		VolumeMountSchema,
		type ModuleConfigField,
		type ModuleEventHook,
		type NetworkPort,
		type ModuleTemplate,
		type VolumeMount
	} from '$lib/proto/discopanel/v1/storage_pb';
	import { create, clone } from '@bufbuild/protobuf';
	import {
		dropEmptyPorts,
		kvToMap,
		mapToKv,
		newHook,
		newPort,
		trimVolumes
	} from '$lib/module-form';
	import type { KvRow } from '$lib/module-form';
	import {
		Loader2,
		Plus,
		SlidersHorizontal,
		Trash2,
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
	let certMountPath = $state('');
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
	let envVars = $state<KvRow[]>([]);
	let volumes = $state<VolumeMount[]>([]);
	let ports = $state<NetworkPort[]>([]);
	let suggestedDependencies = $state('');
	let defaultHooks = $state<ModuleEventHook[]>([]);
	let metadata = $state<KvRow[]>([]);
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

	const addEnvVar = () => (envVars = [...envVars, { key: '', value: '' }]);
	const addVolume = () => (volumes = [...volumes, create(VolumeMountSchema, {})]);
	const addPort = () => (ports = [...ports, newPort(supportsProxy)]);
	const addDefaultHook = () => (defaultHooks = [...defaultHooks, newHook()]);
	const addMetadataEntry = () => (metadata = [...metadata, { key: '', value: '' }]);

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
		certMountPath = t.certMountPath;
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

		envVars = mapToKv(t.defaultEnv);
		volumes = t.defaultVolumes.map((v) => clone(VolumeMountSchema, v));
		ports = t.ports.map((p) => clone(NetworkPortSchema, p));
		configFields = t.configFields.map((f) => clone(ModuleConfigFieldSchema, f));
		suggestedDependencies = t.suggestedDependencies.join(', ');
		defaultHooks = t.defaultHooks.map((h) => clone(ModuleEventHookSchema, h));

		metadata = mapToKv(t.metadata);
		activeSection = 'basic';
	}

	// Empty template carries every create mode default
	function resetForm() {
		loadTemplateData(create(ModuleTemplateSchema, { requiresServer: true, supportsProxy: true }));
	}

	async function handleSubmit() {
		if (!name.trim() || !dockerImage.trim()) return;

		submitting = true;
		try {
			const validPorts = dropEmptyPorts(ports);
			const validFields = configFields.filter((f) => f.env.trim());
			const droppedFields = configFields.length - validFields.length;
			if (droppedFields > 0) {
				notify.warning(
					`Ignored ${droppedFields} config field${droppedFields === 1 ? '' : 's'} without an env name`
				);
			}
			for (const f of validFields) {
				f.env = f.env.trim();
				f.options = f.options.filter((o) => o.value.trim());
			}
			const payload = {
				name: name.trim(),
				description: description.trim(),
				dockerImage: dockerImage.trim(),
				certMountPath: certMountPath.trim(),
				configFields: validFields,
				defaultEnv: kvToMap(envVars),
				defaultVolumes: trimVolumes(volumes),
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
				metadata: kvToMap(metadata),
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
				notify.success(`Template "${name}" updated`);
			} else {
				await rpcClient.module.createModuleTemplate(payload);
				notify.success(`Template "${name}" created`);
			}

			open = false;
			onSuccess();
		} catch (error) {
			notify.error(`Failed to ${mode} template: ${rpcErrorMessage(error, 'Unknown error')}`);
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
		<SectionedDialogLayout
			bind:activeSection
			{navItems}
			title={activeItem.title}
			description={activeItem.desc}
			onclose={() => (open = false)}
		>
			{#snippet sidebarHeader()}
				<div class="border-b px-5 py-4">
					<p class="stat-label">Custom template</p>
					<h3 class="mt-1 truncate text-sm font-semibold">
						{name || (mode === 'create' ? 'New template' : 'Edit template')}
					</h3>
				</div>
			{/snippet}

			{#snippet sidebarFooter()}
				<div class="border-t p-3">
					<div class="rounded-lg border bg-card p-3">
						<p class="text-sm font-medium">Template aliases</p>
						<p class="mt-1 mb-3 text-xs text-muted-foreground">
							Use aliases for dynamic values in any configuration field.
						</p>
						<AliasHelper showLabel />
					</div>
				</div>
			{/snippet}

			{#if activeSection === 'basic'}
				<div class="space-y-6">
					<LabeledInput
						id="tpl-name"
						label="Template name *"
						bind:value={name}
						placeholder="My Custom Module"
						hint="A descriptive name for this module template"
					/>

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
						<LabeledInput
							id="tpl-category"
							label="Category"
							bind:value={category}
							placeholder="monitoring, maps, voice..."
							hint="Group similar templates"
						/>
						<div class="space-y-2">
							<Label for="tpl-icon">Icon</Label>
							<Input id="tpl-icon" bind:value={icon} placeholder="chart-bar, map, microphone..." />
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
					<LabeledInput
						id="tpl-image"
						label="Docker image *"
						bind:value={dockerImage}
						placeholder="nginx:latest, redis:alpine, myregistry/myimage:v1"
						class="font-mono"
						hint="The Docker image to pull and run for this module"
					/>

					<LabeledInput
						id="tpl-cert-mount"
						label="Certificate mount path"
						bind:value={certMountPath}
						placeholder="/etc/certs"
						class="font-mono"
						hint="Optional. Lets instances upload a cert pair mounted here as tls.crt and tls.key"
					/>

					<div class="rounded-lg border bg-card">
						<div class="border-b px-4 py-3">
							<span class="stat-label">Health check</span>
							<p class="mt-1 text-xs text-muted-foreground">
								Configure how to verify the container is healthy
							</p>
						</div>
						<div class="grid grid-cols-1 gap-4 p-4 sm:grid-cols-2">
							<LabeledInput
								id="tpl-hc-path"
								label="Health check path"
								bind:value={healthCheckPath}
								placeholder="/health or /api/status"
								hint="HTTP endpoint to check"
							/>
							<LabeledInput
								id="tpl-hc-port"
								label="Health check port"
								type="number"
								bind:value={healthCheckPort}
								min={0}
								max={65535}
								hint="0 = use first configured port"
							/>
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
							<LabeledInput
								id="tpl-uid"
								label="Default UID"
								bind:value={defaultUid}
								placeholder={'{{host.uid}}'}
								class="font-mono"
								hint="User ID or alias"
							/>
							<LabeledInput
								id="tpl-gid"
								label="Default GID"
								bind:value={defaultGid}
								placeholder={'{{host.gid}}'}
								class="font-mono"
								hint="Group ID or alias"
							/>
						</div>
					</div>

					<div class="rounded-lg border bg-card">
						<div class="border-b px-4 py-3">
							<span class="stat-label">Security options</span>
							<p class="mt-1 text-xs text-muted-foreground">
								Docker security options applied to the container
							</p>
						</div>
						<div class="p-4">
							<LabeledInput
								id="tpl-secopt"
								label="Security options"
								bind:value={defaultSecurityOpt}
								placeholder="seccomp=unconfined, apparmor=unconfined"
								class="font-mono"
								hint="Comma-separated, e.g. for containers that need user namespaces"
							/>
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
				<CollectionSection
					count={configFields.length}
					countLabel="field"
					countSuffix="defined"
					description="Fields render as a form and validate instance config"
					addLabel="Add field"
					onAdd={addConfigField}
					emptyIcon={SlidersHorizontal}
					emptyTitle="No config fields defined"
					emptyDescription="Add typed inputs so instances get a real form"
				>
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
										<Input bind:value={field.env} placeholder="SECRET_KEY" class="font-mono" />
									</div>
									<div class="space-y-2">
										<Label>Label</Label>
										<Input bind:value={field.label} placeholder="Agent secret key" />
									</div>
									<div class="space-y-2">
										<Label>Type</Label>
										<EnumSelect
											schema={ModuleConfigFieldTypeSchema}
											options={FIELD_TYPE_OPTIONS}
											bind:value={field.type}
										/>
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
										<EnumSelect
											schema={ModuleConfigSeveritySchema}
											options={FIELD_SEVERITY_OPTIONS}
											bind:value={field.severity}
											class="w-40"
										/>
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
											<Input bind:value={field.regex} placeholder="^[0-9]+$" class="font-mono" />
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
											<Button variant="outline" size="sm" onclick={() => addFieldOption(field)}>
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
				</CollectionSection>
			{:else if activeSection === 'ports'}
				<CollectionSection
					count={ports.length}
					countLabel="port"
					countSuffix="configured"
					description="Host port 0 = auto-allocate when creating module instances"
					addLabel="Add port"
					onAdd={addPort}
					emptyIcon={Network}
					emptyTitle="No ports configured"
					emptyDescription="Add ports to expose container services"
				>
					<NetworkPortRowsEditor bind:ports showRouting={false} />
				</CollectionSection>
			{:else if activeSection === 'environment'}
				<CollectionSection
					count={envVars.length}
					countLabel="variable"
					countSuffix="defined"
					description="Use template aliases like {'{{server.data_path}}'} for dynamic values"
					addLabel="Add variable"
					onAdd={addEnvVar}
					emptyIcon={Variable}
					emptyTitle="No environment variables"
					emptyDescription="Add default variables for container configuration"
				>
					<KeyValueRowsEditor
						bind:rows={envVars}
						keyPlaceholder="VARIABLE_NAME"
						valuePlaceholder={'value or {{alias}}'}
						entryLabel="variable"
					/>
				</CollectionSection>
			{:else if activeSection === 'volumes'}
				<CollectionSection
					count={volumes.length}
					countLabel="volume"
					countSuffix="configured"
					description="Use template aliases like {'{{module.data_path}}'} for dynamic paths"
					addLabel="Add volume"
					onAdd={addVolume}
					emptyIcon={HardDrive}
					emptyTitle="No volumes configured"
					emptyDescription="Mount host directories for persistent data"
				>
					<VolumeMountRowsEditor bind:volumes sourcePlaceholder={'/host/path or {{alias}}'} />
				</CollectionSection>
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
					<CollectionSection
						count={defaultHooks.length}
						title="Default event hooks"
						description="Pre-configured hooks for server lifecycle events"
						addLabel="Add hook"
						addOutline
						onAdd={addDefaultHook}
						emptyText="No default event hooks configured"
					>
						<EventHookRowsEditor bind:hooks={defaultHooks} />
					</CollectionSection>

					<!-- Init command group -->
					<div class="space-y-3">
						<div>
							<h3 class="text-sm font-medium">Default init command</h3>
							<p class="mt-0.5 text-xs text-muted-foreground">
								Command to exec inside the container after it starts
							</p>
						</div>

						<InitCommandFields
							bind:command={defaultInitCommand}
							bind:delay={defaultInitCommandDelay}
							bind:restartAfterInit={defaultRestartAfterInit}
						/>
					</div>

					<!-- Default metadata group -->
					<CollectionSection
						count={metadata.length}
						title="Default metadata"
						description="Custom key-value pairs for notes, instructions, or links"
						addLabel="Add entry"
						addOutline
						onAdd={addMetadataEntry}
						emptyText="No metadata entries"
					>
						<KeyValueRowsEditor
							bind:rows={metadata}
							separator=":"
							keyClass="w-48"
							keyPlaceholder="key"
							entryLabel="entry"
						/>
					</CollectionSection>

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

			{#snippet footer()}
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
			{/snippet}
		</SectionedDialogLayout>
	</DialogContent>
</Dialog>
