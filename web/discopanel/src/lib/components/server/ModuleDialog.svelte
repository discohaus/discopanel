<script lang="ts">
	import { untrack } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Switch } from '$lib/components/ui/switch';
	import { Dialog, DialogContent } from '$lib/components/ui/dialog';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { Textarea } from '$lib/components/ui/textarea';
	import {
		ConfirmDialog,
		CopyButton,
		EmptyState,
		EventHookRowsEditor,
		InitCommandFields,
		KeyValueRowsEditor,
		NetworkPortRowsEditor,
		SectionedDialogLayout,
		VolumeMountRowsEditor
	} from '$lib/components/app';
	import AliasHelper from '$lib/components/ui/AliasHelper.svelte';
	import DynamicIcon from '$lib/components/ui/DynamicIcon.svelte';
	import ModuleTemplateMenu from './ModuleTemplateMenu.svelte';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { toast } from 'svelte-sonner';
	import { cn } from '$lib/utils';
	import { TONE_BADGE } from '$lib/server-status';
	import { moduleStatusMeta } from '$lib/module-status';
	import type { Server } from '$lib/proto/discopanel/v1/storage_pb';
	import type {
		ModuleTemplate,
		Module,
		ModuleConfigField,
		NetworkPort,
		ModuleDependency,
		ModuleEventHook,
		VolumeMount
	} from '$lib/proto/discopanel/v1/storage_pb';
	import type { ModulePrompt } from '$lib/proto/discopanel/v1/module_pb';
	import {
		ModuleStatus,
		ModuleConfigFieldType,
		ModuleConfigSeverity,
		ModuleEventAction,
		TriggeredEventType,
		ModuleProtocol,
		NetworkPortSchema,
		ModuleDependencySchema,
		ModuleEventHookSchema,
		VolumeMountSchema
	} from '$lib/proto/discopanel/v1/storage_pb';
	import { create, clone } from '@bufbuild/protobuf';
	import { evaluateConfigField, groupedConfigFields } from '$lib/module-config';
	import type { ConfigFieldIssue } from '$lib/module-config';
	import {
		AlertTriangle,
		ArrowLeft,
		Check,
		HardDrive,
		Heart,
		Info,
		KeyRound,
		Loader2,
		Network,
		Play,
		Plus,
		Save,
		Settings,
		ShieldCheck,
		SlidersHorizontal,
		Trash2,
		Variable,
		Wrench,
		X
	} from '@lucide/svelte';

	interface Props {
		open: boolean;
		mode: 'create' | 'edit';
		server?: Server;
		templates?: ModuleTemplate[];
		module?: Module;
		onSuccess: () => void;
		onTemplateDeleted?: () => void;
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
		| 'general'
		| 'configuration'
		| 'ports'
		| 'environment'
		| 'volumes'
		| 'advanced';

	let {
		open = $bindable(),
		mode,
		server,
		templates,
		module,
		onSuccess,
		onTemplateDeleted
	}: Props = $props();

	let step = $state<'select' | 'configure'>('select');
	let selectedTemplate = $state<ModuleTemplate | null>(null);
	let editTemplate = $state<ModuleTemplate | null>(null);
	let submitting = $state(false);
	let activeSection = $state<ConfigSection>('general');
	let configValues = $state<Record<string, string>>({});

	// Form state
	let name = $state('');
	let certPem = $state('');
	let keyPem = $state('');
	let autoStart = $state(true);
	let followServerLifecycle = $state(true);
	let detached = $state(false);
	let memory = $state(512);
	let cpuLimit = $state(1.0);
	let uid = $state('');
	let gid = $state('');
	let initCommand = $state('');
	let initCommandDelay = $state(0);
	let restartAfterInit = $state(false);
	let startImmediately = $state(true);
	let envVars = $state<EnvVar[]>([]);
	let volumes = $state<VolumeMount[]>([]);
	let ports = $state<NetworkPort[]>([]);
	let dependencies = $state<ModuleDependency[]>([]);
	let healthCheckInterval = $state(30);
	let healthCheckTimeout = $state(5);
	let healthCheckRetries = $state(3);
	let eventHooks = $state<ModuleEventHook[]>([]);
	let metadata = $state<MetadataEntry[]>([]);
	let serverModules = $state<Module[]>([]);

	// Runtime input the module is waiting on
	let pendingPrompt = $state<ModulePrompt | null>(null);
	let promptValue = $state('');
	let promptSubmitting = $state(false);

	let serverId = $derived(mode === 'create' ? server?.id : module?.serverId);
	let serverHosts = $derived(
		(mode === 'create' ? server?.proxyHostnames : module?.serverProxyHostnames) ?? []
	);
	let activeTemplate = $derived(mode === 'create' ? selectedTemplate : editTemplate);
	let configFields = $derived(activeTemplate?.configFields ?? []);
	// Panel owned modules only take network and resource edits
	let systemLocked = $derived(mode === 'edit' && !module?.serverId);

	let configIssues = $derived.by(() => {
		const issues: Record<string, ConfigFieldIssue | null> = {};
		for (const field of configFields) {
			if (!field.env) continue;
			issues[field.env] = evaluateConfigField(field, configValues);
		}
		return issues;
	});
	let configDenyCount = $derived(
		Object.values(configIssues).filter((i) => i?.severity === ModuleConfigSeverity.DENY).length
	);

	let navItems = $derived.by(() => {
		const items: { id: ConfigSection; label: string; icon: typeof Settings }[] = [
			{ id: 'general', label: 'General', icon: Settings }
		];
		if (configFields.length > 0) {
			items.push({ id: 'configuration', label: 'Configuration', icon: SlidersHorizontal });
		}
		items.push(
			{ id: 'ports', label: 'Ports', icon: Network },
			{ id: 'environment', label: 'Environment', icon: Variable },
			{ id: 'volumes', label: 'Volumes', icon: HardDrive },
			{ id: 'advanced', label: 'Advanced', icon: Wrench }
		);
		return items;
	});

	const sectionHeaders: Record<ConfigSection, { title: string; desc: string }> = {
		general: {
			title: 'General settings',
			desc: 'Configure basic module settings and lifecycle behavior'
		},
		configuration: {
			title: 'Configuration',
			desc: 'Settings this module needs to run'
		},
		ports: {
			title: 'Port configuration',
			desc: 'Define network ports for container communication'
		},
		environment: {
			title: 'Environment variables',
			desc: 'Set environment variables for the container'
		},
		volumes: {
			title: 'Volume mounts',
			desc: 'Mount host directories into the container'
		},
		advanced: {
			title: 'Advanced settings',
			desc: 'Dependencies, health checks, hooks, and metadata'
		}
	};

	function envVarsToMap(): { [key: string]: string } {
		const map: { [key: string]: string } = {};
		for (const env of envVars) {
			if (env.key.trim()) map[env.key.trim()] = env.value;
		}
		return map;
	}

	// Config fields win over hand added env rows
	function envPayload(): { [key: string]: string } {
		const map = envVarsToMap();
		for (const field of configFields) {
			if (field.env) map[field.env] = configValues[field.env] ?? '';
		}
		return map;
	}

	function selectOptionLabel(field: ModuleConfigField, value: string | undefined): string {
		const opt = field.options.find((o) => o.value === (value ?? ''));
		if (opt) return opt.label || opt.value;
		return value || 'Select...';
	}

	function seedConfigValues(fields: ModuleConfigField[], overrides: { [key: string]: string }) {
		configValues = Object.fromEntries(
			fields.filter((f) => f.env).map((f) => [f.env, overrides[f.env] ?? f.defaultValue])
		);
	}

	function parseEnvVars(m: { [key: string]: string } | undefined): EnvVar[] {
		return m ? Object.entries(m).map(([key, value]) => ({ key, value })) : [];
	}

	function parseMetadata(m: { [key: string]: string } | undefined): MetadataEntry[] {
		return m ? Object.entries(m).map(([key, value]) => ({ key, value })) : [];
	}

	function metadataToMap(): { [key: string]: string } {
		const map: { [key: string]: string } = {};
		for (const e of metadata) {
			if (e.key.trim()) map[e.key.trim()] = e.value;
		}
		return map;
	}

	function addEnvVar() {
		envVars = [...envVars, { key: '', value: '' }];
	}
	function addVolume() {
		volumes = [...volumes, create(VolumeMountSchema, {})];
	}
	function addPort() {
		ports = [
			...ports,
			create(NetworkPortSchema, {
				name: '',
				containerPort: 0,
				hostPort: 0,
				protocol: ModuleProtocol.TCP,
				proxyEnabled: true
			})
		];
	}
	function addDependency() {
		dependencies = [
			...dependencies,
			create(ModuleDependencySchema, { moduleId: '', waitForHealthy: true, timeoutSeconds: 60 })
		];
	}
	function removeDependency(i: number) {
		dependencies = dependencies.filter((_, idx) => idx !== i);
	}
	function addEventHook() {
		eventHooks = [
			...eventHooks,
			create(ModuleEventHookSchema, {
				event: TriggeredEventType.SERVER_START,
				action: ModuleEventAction.START,
				command: '',
				delaySeconds: 0,
				condition: ''
			})
		];
	}
	function addMetadataEntry() {
		metadata = [...metadata, { key: '', value: '' }];
	}

	async function loadServerModules() {
		try {
			const response = await rpcClient.module.listModules(
				{ serverId: serverId || '' },
				silentCallOptions
			);
			serverModules =
				mode === 'edit' && module
					? response.modules.filter((m) => m.id !== module.id)
					: response.modules;
		} catch {
			serverModules = [];
		}
	}

	function resetForm() {
		name = '';
		autoStart = true;
		followServerLifecycle = true;
		detached = false;
		memory = 512;
		cpuLimit = 1.0;
		uid = '';
		gid = '';
		initCommand = '';
		initCommandDelay = 0;
		restartAfterInit = false;
		envVars = [];
		volumes = [];
		startImmediately = true;
		ports = [];
		dependencies = [];
		activeSection = 'general';
		healthCheckInterval = 30;
		healthCheckTimeout = 5;
		healthCheckRetries = 3;
		eventHooks = [];
		metadata = [];
		serverModules = [];
		configValues = {};
		editTemplate = null;
		certPem = '';
		keyPem = '';
	}

	function backToTemplates() {
		step = 'select';
		selectedTemplate = null;
	}

	async function selectTemplate(template: ModuleTemplate) {
		selectedTemplate = template;
		name = template.name;
		certPem = '';
		keyPem = '';
		await loadServerModules();
		const fieldKeys = new Set(template.configFields.map((f) => f.env));
		envVars = parseEnvVars(template.defaultEnv).filter((e) => !fieldKeys.has(e.key));
		seedConfigValues(template.configFields, {});
		volumes = template.defaultVolumes.map((v) => clone(VolumeMountSchema, v));
		// Zero host ports stay zero, backend registry allocates them
		ports = template.ports.map((p) => clone(NetworkPortSchema, p));
		memory = template.defaultMemory;
		uid = template.defaultUid;
		gid = template.defaultGid;
		initCommand = template.defaultInitCommand;
		initCommandDelay = template.defaultInitCommandDelay;
		restartAfterInit = template.defaultRestartAfterInit;
		eventHooks = template.defaultHooks.map((h) => clone(ModuleEventHookSchema, h));
		metadata = parseMetadata(template.metadata);
		step = 'configure';
	}

	let templateToDelete = $state<ModuleTemplate | null>(null);
	let deleteTemplateOpen = $state(false);

	function handleDeleteTemplate(template: ModuleTemplate) {
		templateToDelete = template;
		deleteTemplateOpen = true;
	}

	async function confirmDeleteTemplate() {
		const template = templateToDelete;
		if (!template) return;
		try {
			await rpcClient.module.deleteModuleTemplate({ id: template.id });
			toast.success(`Template "${template.name}" deleted`);
			onTemplateDeleted?.();
		} catch (error) {
			toast.error(
				`Failed to delete template: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		}
	}

	let warnings = $state<string[]>([]);
	let warningResolve: ((proceed: boolean) => void) | null = null;

	function showWarnings(): Promise<boolean> {
		const w: string[] = [];

		const blindMinecraft = ports.some(
			(p) =>
				p.proxyEnabled &&
				p.protocol === ModuleProtocol.MINECRAFT &&
				p.hostnames.length === 0 &&
				serverHosts.length === 0
		);
		if (blindMinecraft) {
			w.push(
				'Minecraft ports route by hostname. Without one here or on the server, players cannot reach them.'
			);
		}

		if (ports.some((p) => p.hostPort === 0 && p.containerPort > 0)) {
			w.push('Ports without a host port will be auto-assigned a free one on save.');
		}

		if (memory < 64) {
			w.push(
				`Memory limit is set to ${memory}MB, which is very low and may cause the container to be killed.`
			);
		}

		for (const field of configFields) {
			const issue = configIssues[field.env];
			if (issue && issue.severity === ModuleConfigSeverity.WARN) {
				w.push(issue.message);
			}
		}

		if (w.length === 0) return Promise.resolve(true);

		warnings = w;
		return new Promise((resolve) => {
			warningResolve = resolve;
		});
	}

	function handleWarningProceed() {
		warnings = [];
		warningResolve?.(true);
		warningResolve = null;
	}

	function handleWarningCancel() {
		warnings = [];
		warningResolve?.(false);
		warningResolve = null;
	}

	// Snapshots module once on open so polling never wipes edits
	$effect(() => {
		if (open && mode === 'edit') {
			untrack(() => {
				if (!module) return;
				name = module.name;
				autoStart = module.autoStart;
				followServerLifecycle = module.followServerLifecycle;
				detached = module.detached;
				memory = module.memory;
				cpuLimit = module.cpuLimit;
				uid = module.uid;
				gid = module.gid;
				initCommand = module.initCommand;
				initCommandDelay = module.initCommandDelay;
				restartAfterInit = module.restartAfterInit;
				envVars = parseEnvVars(module.envOverrides);
				volumes = module.volumeOverrides.map((v) => clone(VolumeMountSchema, v));
				ports = module.ports.map((p) => clone(NetworkPortSchema, p));
				dependencies = module.dependencies.map((d) => clone(ModuleDependencySchema, d));
				healthCheckInterval = module.healthCheckInterval || 30;
				healthCheckTimeout = module.healthCheckTimeout || 5;
				healthCheckRetries = module.healthCheckRetries || 3;
				eventHooks = module.eventHooks.map((h) => clone(ModuleEventHookSchema, h));
				metadata = parseMetadata(module.metadata);
				loadServerModules();
				loadEditTemplate(module);
			});
		}
	});

	// Splits env rows into config fields once the template arrives
	async function loadEditTemplate(mod: Module) {
		editTemplate = null;
		try {
			const response = await rpcClient.module.getModuleTemplate(
				{ id: mod.templateId },
				silentCallOptions
			);
			const template = response.template ?? null;
			editTemplate = template;
			if (!template || template.configFields.length === 0) return;
			const fieldKeys = new Set(template.configFields.map((f) => f.env));
			seedConfigValues(template.configFields, mod.envOverrides);
			envVars = envVars.filter((e) => !fieldKeys.has(e.key));
		} catch {
			editTemplate = null;
		}
	}

	$effect(() => {
		if (!open) {
			step = 'select';
			selectedTemplate = null;
			deleteTemplateOpen = false;
			templateToDelete = null;
			pendingPrompt = null;
			promptValue = '';
			resetForm();
		}
	});

	// True when this module can raise runtime input prompts
	let promptCapable = $derived(
		mode === 'edit' &&
			!!module &&
			module.status === ModuleStatus.RUNNING &&
			activeTemplate?.metadata?.supports_prompts === 'true'
	);

	async function pollPrompt() {
		if (!module?.id) return;
		try {
			const res = await rpcClient.module.getModulePrompt({ id: module.id }, silentCallOptions);
			const next = res.pending ? (res.prompt ?? null) : null;
			// Reset the input only when a different prompt arrives
			if (next?.id !== pendingPrompt?.id) {
				promptValue = '';
			}
			pendingPrompt = next;
		} catch {
			pendingPrompt = null;
		}
	}

	// Polls the module for pending input while it is running
	$effect(() => {
		if (!open || !promptCapable) {
			pendingPrompt = null;
			return;
		}
		pollPrompt();
		const timer = setInterval(pollPrompt, 4000);
		return () => clearInterval(timer);
	});

	async function submitPrompt() {
		if (!module?.id || !pendingPrompt) return;
		promptSubmitting = true;
		try {
			await rpcClient.module.answerModulePrompt({
				id: module.id,
				promptId: pendingPrompt.id,
				value: promptValue
			});
			toast.success('Sent to module');
			pendingPrompt = null;
			promptValue = '';
			// Give the module a moment then re-check
			setTimeout(pollPrompt, 1500);
		} catch (err) {
			toast.error(err instanceof Error ? err.message : 'Failed to send input');
		} finally {
			promptSubmitting = false;
		}
	}

	async function handleSubmit() {
		if (configDenyCount > 0) {
			activeSection = 'configuration';
			toast.error('Fix the highlighted configuration fields first');
			return;
		}
		const proceed = await showWarnings();
		if (!proceed) return;

		submitting = true;
		try {
			const portsPayload = ports.filter((p) => p.containerPort > 0);
			const droppedPorts = ports.length - portsPayload.length;
			if (droppedPorts > 0) {
				toast.warning(
					`Ignored ${droppedPorts} port row${droppedPorts === 1 ? '' : 's'} without a container port`
				);
			}
			const depsPayload = dependencies.filter((d) => d.moduleId);
			for (const v of volumes) {
				v.source = v.source.trim();
				v.target = v.target.trim();
			}
			const volumesPayload = volumes.filter((v) => v.source && v.target);

			if (mode === 'create' && selectedTemplate) {
				await rpcClient.module.createModule({
					name,
					serverId: serverId || '',
					templateId: selectedTemplate.id,
					envOverrides: envPayload(),
					volumeOverrides: volumesPayload,
					memory,
					cpuLimit,
					autoStart,
					followServerLifecycle,
					detached,
					startImmediately,
					ports: portsPayload,
					dependencies: depsPayload,
					healthCheckInterval,
					healthCheckTimeout,
					healthCheckRetries,
					eventHooks,
					metadata: metadataToMap(),
					uid,
					gid,
					initCommand,
					initCommandDelay,
					restartAfterInit,
					certPem: certPem.trim(),
					keyPem: keyPem.trim()
				});
				toast.success(`Module "${name}" created`);
			} else if (module && systemLocked) {
				// Panel owns everything else on a system module
				await rpcClient.module.updateModule({
					id: module.id,
					memory,
					cpuLimit,
					ports: portsPayload,
					clearPorts: portsPayload.length === 0
				});
				toast.success(`Module "${module.name}" updated`);
			} else if (module) {
				await rpcClient.module.updateModule({
					id: module.id,
					name,
					envOverrides: envPayload(),
					volumeOverrides: volumesPayload,
					memory,
					cpuLimit,
					autoStart,
					followServerLifecycle,
					detached,
					ports: portsPayload,
					clearPorts: portsPayload.length === 0,
					dependencies: depsPayload,
					healthCheckInterval,
					healthCheckTimeout,
					healthCheckRetries,
					eventHooks,
					metadata: metadataToMap(),
					uid,
					gid,
					initCommand,
					initCommandDelay,
					restartAfterInit
				});
				toast.success(`Module "${name}" updated`);
			}
			open = false;
			onSuccess();
		} catch (error) {
			toast.error(`Failed: ${error instanceof Error ? error.message : 'Unknown error'}`);
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
		{#if mode === 'create' && step === 'select'}
			<div class="flex h-full min-h-0 flex-col">
				<div class="flex items-start justify-between gap-4 border-b px-6 py-4">
					<div>
						<h2 class="text-lg font-semibold tracking-tight">Add module</h2>
						<p class="mt-0.5 text-sm text-muted-foreground">
							Select a module template to get started
						</p>
					</div>
					<Button variant="ghost" size="icon" class="size-8" onclick={() => (open = false)}>
						<X class="size-4" />
						<span class="sr-only">Close</span>
					</Button>
				</div>

				<div class="flex-1 overflow-y-auto p-6">
					<ModuleTemplateMenu
						{templates}
						onSelect={selectTemplate}
						onDelete={handleDeleteTemplate}
					/>
				</div>
			</div>
		{:else}
			<SectionedDialogLayout
				bind:activeSection
				{navItems}
				title={sectionHeaders[activeSection].title}
				description={sectionHeaders[activeSection].desc}
				onclose={() => (open = false)}
			>
				{#snippet sidebarHeader()}
					<div class="border-b p-4">
						{#if mode === 'create'}
							<button
								type="button"
								class="mb-3 flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
								onclick={backToTemplates}
							>
								<ArrowLeft class="size-3.5" />
								Back to templates
							</button>
						{/if}
						<div class="flex items-center gap-3">
							<div
								class="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-muted/40 text-muted-foreground"
							>
								<DynamicIcon
									name={mode === 'create' ? selectedTemplate?.icon : undefined}
									class="size-5"
									fallback="Package"
								/>
							</div>
							<div class="min-w-0 flex-1">
								<h3 class="truncate text-sm font-semibold">
									{mode === 'create' ? selectedTemplate?.name : module?.templateName}
								</h3>
								{#if module}
									{@const meta = moduleStatusMeta(module.status)}
									<span
										class={cn(
											'mt-1 inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium',
											TONE_BADGE[meta.tone]
										)}
									>
										{meta.label}
									</span>
								{/if}
							</div>
						</div>
					</div>
				{/snippet}

				{#snippet navExtra(id)}
					{#if id === 'configuration' && configDenyCount > 0}
						<span
							class="ml-auto rounded-full bg-destructive/15 px-1.5 py-0.5 text-[10px] font-semibold text-destructive"
						>
							{configDenyCount}
						</span>
					{/if}
				{/snippet}

				{#snippet sidebarFooter()}
					<div class="space-y-3 border-t p-4">
						{#if module?.id}
							<div>
								<div class="stat-label mb-1.5">Module ID</div>
								<div class="flex items-center gap-1.5">
									<code
										class="min-w-0 flex-1 truncate rounded bg-muted px-2 py-1 font-mono text-xs"
									>
										{module.id}
									</code>
									<CopyButton text={module.id} label="Copy module ID" class="shrink-0" />
								</div>
							</div>
						{/if}
						<div class="rounded-lg border bg-muted/30 p-3">
							<p class="text-xs font-medium">Module aliases</p>
							<p class="mt-1 mb-2 text-xs text-muted-foreground">
								Use aliases for dynamic values in any configuration field.
							</p>
							<AliasHelper serverId={serverId || ''} moduleId={module?.id} showLabel />
						</div>
					</div>
				{/snippet}

				{#snippet banner()}
					{#if pendingPrompt}
						<div class="border-b border-amber-500/30 bg-amber-500/10 px-6 py-4">
							<div class="flex items-start gap-3">
								<KeyRound class="mt-0.5 size-5 shrink-0 text-amber-500" />
								<div class="min-w-0 flex-1">
									<p class="text-sm font-semibold">{pendingPrompt.title || 'Input needed'}</p>
									{#if pendingPrompt.message}
										<p class="mt-0.5 text-sm text-muted-foreground">{pendingPrompt.message}</p>
									{/if}
									<div class="mt-3 flex items-center gap-2">
										{#if pendingPrompt.kind === ModuleConfigFieldType.SELECT}
											<Select
												type="single"
												value={promptValue}
												onValueChange={(v) => (promptValue = v ?? '')}
											>
												<SelectTrigger class="w-64">
													{promptValue || 'Select...'}
												</SelectTrigger>
												<SelectContent>
													{#each pendingPrompt.options as opt (opt.value)}
														<SelectItem value={opt.value}>{opt.label || opt.value}</SelectItem>
													{/each}
												</SelectContent>
											</Select>
										{:else}
											<Input
												class="w-64"
												type={pendingPrompt.kind === ModuleConfigFieldType.PASSWORD
													? 'password'
													: 'text'}
												placeholder={pendingPrompt.placeholder}
												bind:value={promptValue}
												onkeydown={(e) => {
													if (e.key === 'Enter') submitPrompt();
												}}
											/>
										{/if}
										<Button
											size="sm"
											disabled={promptSubmitting || !promptValue}
											onclick={submitPrompt}
										>
											{#if promptSubmitting}
												<Loader2 class="size-4 animate-spin" />
											{:else}
												<Check class="size-4" />
											{/if}
											Submit
										</Button>
									</div>
								</div>
							</div>
						</div>
					{/if}

					{#if systemLocked}
						<div class="flex items-center gap-2 border-b bg-primary/[0.04] px-6 py-2.5">
							<ShieldCheck class="size-4 shrink-0 text-primary" />
							<p class="text-xs text-muted-foreground">
								Managed by DiscoPanel. Only network settings and resource limits can change.
							</p>
						</div>
					{/if}
				{/snippet}

				{#if activeSection === 'general'}
					<div class="space-y-6">
						<div class="space-y-2">
							<Label for="module-name">Module name</Label>
							<Input
								id="module-name"
								bind:value={name}
								placeholder="Enter module name"
								disabled={systemLocked}
							/>
							<p class="text-xs text-muted-foreground">
								A unique identifier for this module instance
							</p>
						</div>

						{#if mode === 'create' && selectedTemplate?.certMountPath}
							<div class="space-y-3">
								<h3 class="text-sm font-semibold">Certificate</h3>
								<div class="space-y-2">
									<Label for="module-cert">Certificate (PEM)</Label>
									<Textarea
										id="module-cert"
										bind:value={certPem}
										placeholder="-----BEGIN CERTIFICATE-----"
										rows={4}
										class="font-mono text-xs"
									/>
								</div>
								<div class="space-y-2">
									<Label for="module-key">Private key (PEM)</Label>
									<Textarea
										id="module-key"
										bind:value={keyPem}
										placeholder="-----BEGIN PRIVATE KEY-----"
										rows={4}
										class="font-mono text-xs"
									/>
								</div>
								<p class="text-xs text-muted-foreground">
									Optional. Mounted into {selectedTemplate.certMountPath} as tls.crt and tls.key
								</p>
							</div>
						{/if}

						<div class="space-y-3">
							<h3 class="text-sm font-semibold">Resource limits</h3>
							<div class="grid gap-4 sm:grid-cols-2">
								<div class="space-y-2">
									<Label for="module-memory">Memory (MB)</Label>
									<Input
										id="module-memory"
										type="number"
										bind:value={memory}
										min={64}
										max={32768}
									/>
									<p class="text-xs text-muted-foreground">Minimum: 64 MB</p>
								</div>
								<div class="space-y-2">
									<Label for="module-cpu">CPU limit (cores)</Label>
									<Input
										id="module-cpu"
										type="number"
										bind:value={cpuLimit}
										min={0.1}
										max={16}
										step={0.1}
									/>
									<p class="text-xs text-muted-foreground">Fraction of CPU cores</p>
								</div>
							</div>
						</div>

						<div class="space-y-3">
							<h3 class="text-sm font-semibold">Container user</h3>
							<div class="grid gap-4 sm:grid-cols-2">
								<div class="space-y-2">
									<Label for="module-uid">UID</Label>
									<Input
										id="module-uid"
										bind:value={uid}
										placeholder={'{{host.uid}}'}
										class="font-mono"
										disabled={systemLocked}
									/>
									<p class="text-xs text-muted-foreground">User ID or alias</p>
								</div>
								<div class="space-y-2">
									<Label for="module-gid">GID</Label>
									<Input
										id="module-gid"
										bind:value={gid}
										placeholder={'{{host.gid}}'}
										class="font-mono"
										disabled={systemLocked}
									/>
									<p class="text-xs text-muted-foreground">Group ID or alias</p>
								</div>
							</div>
						</div>

						{#if systemLocked}
							<div class="space-y-3">
								<h3 class="text-sm font-semibold">Lifecycle behavior</h3>
								<div class="flex items-start gap-2 rounded-lg border bg-card p-3">
									<ShieldCheck class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
									<p class="text-xs text-muted-foreground">
										This module runs for the whole panel. Use the Enabled switch on the Modules page
										to turn it on or off, and that choice survives panel restarts.
									</p>
								</div>
							</div>
						{:else}
							<div class="space-y-3">
								<h3 class="text-sm font-semibold">Lifecycle behavior</h3>
								<div class="divide-y rounded-lg border bg-card">
									<label class="flex cursor-pointer items-center justify-between gap-4 p-3">
										<div>
											<span class="text-sm font-medium">Auto-start</span>
											<p class="text-xs text-muted-foreground">
												Automatically start this module when the server starts
											</p>
										</div>
										<Switch bind:checked={autoStart} />
									</label>
									<label class="flex cursor-pointer items-center justify-between gap-4 p-3">
										<div>
											<span class="text-sm font-medium">Follow server lifecycle</span>
											<p class="text-xs text-muted-foreground">
												Stop this module when the server stops
											</p>
										</div>
										<Switch bind:checked={followServerLifecycle} />
									</label>
									<label class="flex cursor-pointer items-center justify-between gap-4 p-3">
										<div>
											<span class="text-sm font-medium">Detached mode</span>
											<p class="text-xs text-muted-foreground">
												Run independently of the server lifecycle
											</p>
										</div>
										<Switch bind:checked={detached} />
									</label>
								</div>
							</div>
						{/if}

						{#if mode === 'create'}
							<label
								class="flex cursor-pointer items-center justify-between gap-4 rounded-lg border border-primary/30 bg-primary/5 p-3"
							>
								<div>
									<span class="flex items-center gap-1.5 text-sm font-medium">
										<Play class="size-3.5" />
										Start immediately
									</span>
									<p class="text-xs text-muted-foreground">
										Launch the module as soon as it's created
									</p>
								</div>
								<Switch bind:checked={startImmediately} />
							</label>
						{/if}

						{#if module?.dataPath}
							<div class="space-y-2">
								<h3 class="text-sm font-semibold">Data path</h3>
								<div class="flex items-center gap-2 rounded-lg border bg-card p-3">
									<HardDrive class="size-4 shrink-0 text-muted-foreground" />
									<code class="min-w-0 flex-1 truncate font-mono text-xs">
										{module.dataPath}
									</code>
									<CopyButton text={module.dataPath} label="Copy data path" class="shrink-0" />
								</div>
							</div>
						{/if}
					</div>
				{:else if activeSection === 'configuration'}
					<div class="space-y-6">
						{#each groupedConfigFields(configFields) as [group, fields] (group)}
							<div class="space-y-4">
								{#if group}
									<h3 class="text-sm font-semibold">{group}</h3>
								{/if}
								{#each fields as field (field.env)}
									{@const issue = configIssues[field.env]}
									{@const issueBorder =
										issue?.severity === ModuleConfigSeverity.DENY
											? 'border-destructive'
											: issue
												? 'border-status-warn'
												: ''}
									<div class="space-y-1.5">
										{#if field.type === ModuleConfigFieldType.BOOL}
											<label
												class="flex cursor-pointer items-center justify-between gap-4 rounded-lg border bg-card p-3"
											>
												<div>
													<span class="text-sm font-medium">{field.label || field.env}</span>
													{#if field.description}
														<p class="text-xs text-muted-foreground">{field.description}</p>
													{/if}
												</div>
												<Switch
													checked={configValues[field.env] === 'true'}
													onCheckedChange={(v) => (configValues[field.env] = v ? 'true' : 'false')}
												/>
											</label>
										{:else}
											<Label for={`cfg-${field.env}`}>
												{field.label || field.env}
												{#if field.required}
													<span class="text-destructive">*</span>
												{/if}
											</Label>
											{#if field.type === ModuleConfigFieldType.SELECT}
												<Select
													type="single"
													value={configValues[field.env]}
													onValueChange={(v) => {
														if (v) configValues[field.env] = v;
													}}
												>
													<SelectTrigger class={cn('w-full', issueBorder)}>
														<span class="truncate">
															{selectOptionLabel(field, configValues[field.env])}
														</span>
													</SelectTrigger>
													<SelectContent>
														{#each field.options as opt (opt.value)}
															<SelectItem value={opt.value}>{opt.label || opt.value}</SelectItem>
														{/each}
													</SelectContent>
												</Select>
											{:else if field.type === ModuleConfigFieldType.MULTILINE}
												<Textarea
													id={`cfg-${field.env}`}
													bind:value={configValues[field.env]}
													placeholder={field.placeholder}
													class={cn('font-mono', issueBorder)}
												/>
											{:else}
												<Input
													id={`cfg-${field.env}`}
													type={field.type === ModuleConfigFieldType.PASSWORD
														? 'password'
														: field.type === ModuleConfigFieldType.INT
															? 'number'
															: 'text'}
													bind:value={configValues[field.env]}
													placeholder={field.placeholder}
													class={cn('font-mono', issueBorder)}
												/>
											{/if}
										{/if}
										{#if issue}
											<p
												class={cn(
													'text-xs',
													issue.severity === ModuleConfigSeverity.DENY
														? 'text-destructive'
														: 'text-status-warn'
												)}
											>
												{issue.message}
											</p>
										{:else if field.description && field.type !== ModuleConfigFieldType.BOOL}
											<p class="text-xs text-muted-foreground">{field.description}</p>
										{/if}
									</div>
								{/each}
							</div>
						{/each}
					</div>
				{:else if activeSection === 'ports'}
					<div class="space-y-4">
						<div class="flex items-center justify-between gap-2">
							<p class="text-sm text-muted-foreground">
								{ports.length} port{ports.length === 1 ? '' : 's'} configured
							</p>
							{#if !systemLocked}
								<Button size="sm" onclick={addPort}>
									<Plus class="size-4" />
									Add port
								</Button>
							{/if}
						</div>

						{#if ports.length > 0}
							<NetworkPortRowsEditor bind:ports locked={systemLocked} {serverHosts} />
						{:else}
							<div class="rounded-lg border border-dashed">
								<EmptyState
									icon={Network}
									title="No ports configured"
									description="Add ports to expose container services"
								>
									{#if !systemLocked}
										<Button variant="outline" size="sm" onclick={addPort}>
											<Plus class="size-4" />
											Add port
										</Button>
									{/if}
								</EmptyState>
							</div>
						{/if}
					</div>
				{:else if activeSection === 'environment'}
					<div class="space-y-4">
						<div class="flex items-center justify-between gap-2">
							<p class="text-sm text-muted-foreground">
								{envVars.length} variable{envVars.length === 1 ? '' : 's'} defined
							</p>
							{#if !systemLocked}
								<Button size="sm" onclick={addEnvVar}>
									<Plus class="size-4" />
									Add variable
								</Button>
							{/if}
						</div>

						{#if envVars.length > 0}
							<KeyValueRowsEditor
								bind:rows={envVars}
								keyPlaceholder="VARIABLE_NAME"
								valuePlaceholder="value"
								entryLabel="variable"
								disabled={systemLocked}
							/>
						{:else}
							<div class="rounded-lg border border-dashed">
								<EmptyState
									icon={Variable}
									title="No environment variables"
									description="Add variables to configure the container"
								>
									{#if !systemLocked}
										<Button variant="outline" size="sm" onclick={addEnvVar}>
											<Plus class="size-4" />
											Add variable
										</Button>
									{/if}
								</EmptyState>
							</div>
						{/if}
					</div>
				{:else if activeSection === 'volumes'}
					<div class="space-y-4">
						<div class="flex items-center justify-between gap-2">
							<p class="text-sm text-muted-foreground">
								{volumes.length} volume{volumes.length === 1 ? '' : 's'} mounted
							</p>
							{#if !systemLocked}
								<Button size="sm" onclick={addVolume}>
									<Plus class="size-4" />
									Add volume
								</Button>
							{/if}
						</div>

						{#if volumes.length > 0}
							<VolumeMountRowsEditor bind:volumes disabled={systemLocked} />
						{:else}
							<div class="rounded-lg border border-dashed">
								<EmptyState
									icon={HardDrive}
									title="No volumes mounted"
									description="Mount host directories to persist data"
								>
									{#if !systemLocked}
										<Button variant="outline" size="sm" onclick={addVolume}>
											<Plus class="size-4" />
											Add volume
										</Button>
									{/if}
								</EmptyState>
							</div>
						{/if}
					</div>
				{:else if activeSection === 'advanced'}
					<fieldset class="min-w-0 space-y-8" disabled={systemLocked}>
						<section class="space-y-3">
							<div class="flex items-start justify-between gap-2">
								<div>
									<h3 class="text-sm font-semibold">Dependencies</h3>
									<p class="mt-0.5 text-xs text-muted-foreground">
										Modules that must be running before this one starts
									</p>
								</div>
								<Button
									variant="outline"
									size="sm"
									onclick={addDependency}
									disabled={serverModules.length === 0}
								>
									<Plus class="size-4" />
									Add
								</Button>
							</div>

							{#if dependencies.length > 0}
								<div class="space-y-2">
									{#each dependencies as dep, i (i)}
										<div class="flex flex-wrap items-center gap-3 rounded-lg border bg-card p-3">
											<Select
												type="single"
												value={dep.moduleId}
												onValueChange={(v) => {
													if (v) dep.moduleId = v;
												}}
											>
												<SelectTrigger class="w-56">
													<span class="truncate">
														{serverModules.find((m) => m.id === dep.moduleId)?.name ||
															'Select module...'}
													</span>
												</SelectTrigger>
												<SelectContent>
													{#each serverModules as mod (mod.id)}
														<SelectItem value={mod.id}>{mod.name}</SelectItem>
													{/each}
												</SelectContent>
											</Select>

											<label class="flex cursor-pointer items-center gap-2">
												<Checkbox bind:checked={dep.waitForHealthy} />
												<span class="text-sm">Wait for healthy</span>
											</label>

											<div class="flex items-center gap-2">
												<Label class="text-sm whitespace-nowrap">Timeout (s)</Label>
												<Input type="number" bind:value={dep.timeoutSeconds} class="w-24" />
											</div>

											<Button
												variant="ghost"
												size="icon"
												class="ml-auto size-8 text-muted-foreground hover:text-destructive"
												onclick={() => removeDependency(i)}
											>
												<Trash2 class="size-4" />
												<span class="sr-only">Remove dependency</span>
											</Button>
										</div>
									{/each}
								</div>
							{:else}
								<div
									class="rounded-lg border border-dashed p-4 text-center text-sm text-muted-foreground"
								>
									{serverModules.length === 0
										? 'No other modules available on this server'
										: 'No dependencies configured'}
								</div>
							{/if}
						</section>

						<section class="space-y-3">
							<div>
								<h3 class="flex items-center gap-1.5 text-sm font-semibold">
									<Heart class="size-4" />
									Health check
								</h3>
								<p class="mt-0.5 text-xs text-muted-foreground">
									Configure how the module's health is monitored
								</p>
							</div>

							<div class="grid gap-4 rounded-lg border bg-card p-4 sm:grid-cols-3">
								<div class="space-y-1.5">
									<Label>Interval (seconds)</Label>
									<Input type="number" bind:value={healthCheckInterval} min={5} />
									<p class="text-xs text-muted-foreground">Time between checks</p>
								</div>
								<div class="space-y-1.5">
									<Label>Timeout (seconds)</Label>
									<Input type="number" bind:value={healthCheckTimeout} min={1} />
									<p class="text-xs text-muted-foreground">Max wait for response</p>
								</div>
								<div class="space-y-1.5">
									<Label>Retries</Label>
									<Input type="number" bind:value={healthCheckRetries} min={1} />
									<p class="text-xs text-muted-foreground">Failures before unhealthy</p>
								</div>
							</div>
						</section>

						<section class="space-y-3">
							<div>
								<h3 class="text-sm font-semibold">Init command</h3>
								<p class="mt-0.5 text-xs text-muted-foreground">
									Execute a command inside the container after it starts
								</p>
							</div>

							<InitCommandFields
								bind:command={initCommand}
								bind:delay={initCommandDelay}
								bind:restartAfterInit
							/>
						</section>

						<section class="space-y-3">
							<div class="flex items-start justify-between gap-2">
								<div>
									<h3 class="text-sm font-semibold">Event hooks</h3>
									<p class="mt-0.5 text-xs text-muted-foreground">
										Actions to run when specific events occur
									</p>
								</div>
								<Button variant="outline" size="sm" onclick={addEventHook}>
									<Plus class="size-4" />
									Add hook
								</Button>
							</div>

							{#if eventHooks.length > 0}
								<EventHookRowsEditor bind:hooks={eventHooks} />
							{:else}
								<div
									class="rounded-lg border border-dashed p-4 text-center text-sm text-muted-foreground"
								>
									No event hooks configured
								</div>
							{/if}
						</section>

						<section class="space-y-3">
							<div class="flex items-start justify-between gap-2">
								<div>
									<h3 class="flex items-center gap-1.5 text-sm font-semibold">
										<Info class="size-4" />
										Metadata
									</h3>
									<p class="mt-0.5 text-xs text-muted-foreground">
										Custom key-value pairs for module configuration
									</p>
								</div>
								<Button variant="outline" size="sm" onclick={addMetadataEntry}>
									<Plus class="size-4" />
									Add entry
								</Button>
							</div>

							{#if metadata.length > 0}
								<KeyValueRowsEditor
									bind:rows={metadata}
									separator=":"
									keyClass="w-48"
									keyPlaceholder="key"
									entryLabel="entry"
								/>
							{:else}
								<div
									class="rounded-lg border border-dashed p-4 text-center text-sm text-muted-foreground"
								>
									No metadata entries
								</div>
							{/if}
						</section>
					</fieldset>
				{/if}

				{#snippet footer()}
					{#if mode === 'create'}
						<Button variant="outline" onclick={backToTemplates}>Back</Button>
					{:else}
						<Button variant="outline" onclick={() => (open = false)}>Cancel</Button>
					{/if}
					<Button
						onclick={handleSubmit}
						disabled={submitting || !name.trim() || configDenyCount > 0}
					>
						{#if submitting}
							<Loader2 class="size-4 animate-spin" />
							{mode === 'create' ? 'Creating...' : 'Saving...'}
						{:else if mode === 'create'}
							<Check class="size-4" />
							Create module
						{:else}
							<Save class="size-4" />
							Save changes
						{/if}
					</Button>
				{/snippet}
			</SectionedDialogLayout>
		{/if}
	</DialogContent>
</Dialog>

<ConfirmDialog
	bind:open={deleteTemplateOpen}
	title="Delete template?"
	description={templateToDelete
		? `"${templateToDelete.name}" will be removed permanently.\nThis cannot be undone.`
		: ''}
	confirmLabel="Delete template"
	destructive
	onConfirm={confirmDeleteTemplate}
/>

<AlertDialog.Root
	open={warnings.length > 0}
	onOpenChange={(o) => {
		if (!o) handleWarningCancel();
	}}
>
	<AlertDialog.Content>
		<AlertDialog.Header>
			<AlertDialog.Title class="flex items-center gap-2">
				<AlertTriangle class="size-5 text-status-warn" />
				Review warnings
			</AlertDialog.Title>
			<AlertDialog.Description>
				The following issues were detected. You can still proceed, but you may want to review them
				first.
			</AlertDialog.Description>
		</AlertDialog.Header>
		<div class="space-y-2 py-2">
			{#each warnings as warning (warning)}
				<div
					class="flex items-start gap-2 rounded-md border border-status-warn/30 bg-status-warn/10 p-3 text-sm text-status-warn"
				>
					<AlertTriangle class="mt-0.5 size-4 shrink-0" />
					<span>{warning}</span>
				</div>
			{/each}
		</div>
		<AlertDialog.Footer>
			<AlertDialog.Cancel onclick={handleWarningCancel}>Go back</AlertDialog.Cancel>
			<AlertDialog.Action onclick={handleWarningProceed}>
				{mode === 'create' ? 'Create anyway' : 'Save anyway'}
			</AlertDialog.Action>
		</AlertDialog.Footer>
	</AlertDialog.Content>
</AlertDialog.Root>
