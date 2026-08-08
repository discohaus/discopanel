<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Card, CardContent } from '$lib/components/ui/card';
	import { Switch } from '$lib/components/ui/switch';
	import { KeyValueRowsEditor } from '$lib/components/app';
	import { Plus, AlertCircle, Code, ChevronDown, ChevronRight, X } from '@lucide/svelte';
	import type { DockerOverrides, VolumeMount } from '$lib/proto/discopanel/v1/storage_pb';
	import { DockerOverridesSchema, VolumeMountSchema } from '$lib/proto/discopanel/v1/storage_pb';
	import { create } from '@bufbuild/protobuf';
	import { Badge } from '$lib/components/ui/badge';
	import { notify } from '$lib/stores/activity.svelte';

	interface Props {
		overrides?: DockerOverrides;
		disabled?: boolean;
		onchange?: (overrides: DockerOverrides | undefined) => void;
	}

	let { overrides = $bindable(), disabled = false, onchange }: Props = $props();

	const ENTRYPOINT_PLACEHOLDER = '/bin/sh, -c, echo "hello"';

	let showAdvanced = $state(false);
	let jsonMode = $state(false);
	let jsonText = $state('');
	let jsonError = $state('');

	interface KVRow {
		key: string;
		value: string;
	}

	// Draft rows so blank keys never commit junk
	let envRows = $state<KVRow[]>([]);
	let labelRows = $state<KVRow[]>([]);
	let suppressRowSync = false;

	function rowsFromMap(map: Record<string, string> | undefined): KVRow[] {
		return Object.entries(map ?? {}).map(([key, value]) => ({ key, value }));
	}

	// Resync drafts when overrides change externally
	$effect(() => {
		void overrides;
		if (suppressRowSync) {
			suppressRowSync = false;
			return;
		}
		envRows = rowsFromMap(overrides?.environment);
		labelRows = rowsFromMap(overrides?.labels);
	});

	function mapFromRows(rows: KVRow[]): Record<string, string> {
		const map: Record<string, string> = {};
		for (const row of rows) {
			const key = row.key.trim();
			if (key) map[key] = row.value;
		}
		return map;
	}

	let activeCount = $derived.by(() => {
		if (!overrides) return 0;
		let count = 0;
		if (overrides.environment && Object.keys(overrides.environment).length > 0) count++;
		if (overrides.volumes && overrides.volumes.length > 0) count++;
		if (overrides.cpuLimit) count++;
		if (overrides.memoryLimit) count++;
		if (overrides.networkMode) count++;
		if (overrides.privileged) count++;
		if (overrides.restartPolicy) count++;
		if (overrides.user) count++;
		if (overrides.capAdd && overrides.capAdd.length > 0) count++;
		if (overrides.capDrop && overrides.capDrop.length > 0) count++;
		if (overrides.devices && overrides.devices.length > 0) count++;
		if (overrides.dns && overrides.dns.length > 0) count++;
		if (overrides.labels && Object.keys(overrides.labels).length > 0) count++;
		if (overrides.entrypoint && overrides.entrypoint.length > 0) count++;
		if (overrides.extraHosts && overrides.extraHosts.length > 0) count++;
		if (overrides.readOnly) count++;
		if (overrides.securityOpt && overrides.securityOpt.length > 0) count++;
		if (overrides.shmSize) count++;
		if (overrides.workingDir) count++;
		if (overrides.command && overrides.command.length > 0) count++;
		return count;
	});

	$effect(() => {
		if (jsonMode) {
			jsonText = JSON.stringify(
				overrides || {},
				(key, v) => {
					if (key.startsWith('$')) return undefined;
					return typeof v === 'bigint' ? Number(v) : v;
				},
				2
			);
		}
	});

	function toggleJsonMode() {
		if (jsonMode) {
			try {
				const parsed = jsonText.trim() ? JSON.parse(jsonText) : {};
				overrides =
					Object.keys(parsed).length > 0 ? create(DockerOverridesSchema, parsed) : undefined;
				jsonError = '';
				jsonMode = false;
				onchange?.(overrides);
			} catch (e) {
				jsonError = `Invalid JSON: ${e instanceof Error ? e.message : 'Unknown error'}`;
			}
		} else {
			jsonMode = true;
		}
	}

	function updateJsonText(value: string) {
		jsonText = value;
		try {
			if (value.trim()) {
				JSON.parse(value);
			}
			jsonError = '';
		} catch (e) {
			jsonError = `Invalid JSON: ${e instanceof Error ? e.message : 'Unknown error'}`;
		}
	}

	function cloneCurrent(): DockerOverrides {
		const updates = create(DockerOverridesSchema, {});
		if (!overrides) return updates;
		if (overrides.environment && Object.keys(overrides.environment).length > 0)
			updates.environment = { ...overrides.environment };
		if (overrides.volumes && overrides.volumes.length > 0) updates.volumes = [...overrides.volumes];
		if (overrides.capAdd && overrides.capAdd.length > 0) updates.capAdd = [...overrides.capAdd];
		if (overrides.capDrop && overrides.capDrop.length > 0) updates.capDrop = [...overrides.capDrop];
		if (overrides.devices && overrides.devices.length > 0) updates.devices = [...overrides.devices];
		if (overrides.networkMode) updates.networkMode = overrides.networkMode;
		if (overrides.privileged !== undefined) updates.privileged = overrides.privileged;
		if (overrides.user) updates.user = overrides.user;
		if (overrides.memoryLimit) updates.memoryLimit = overrides.memoryLimit;
		if (overrides.cpuLimit) updates.cpuLimit = overrides.cpuLimit;
		if (overrides.restartPolicy) updates.restartPolicy = overrides.restartPolicy;
		if (overrides.entrypoint && overrides.entrypoint.length > 0)
			updates.entrypoint = [...overrides.entrypoint];
		if (overrides.dns && overrides.dns.length > 0) updates.dns = [...overrides.dns];
		if (overrides.labels && Object.keys(overrides.labels).length > 0)
			updates.labels = { ...overrides.labels };
		if (overrides.extraHosts && overrides.extraHosts.length > 0)
			updates.extraHosts = [...overrides.extraHosts];
		if (overrides.readOnly) updates.readOnly = overrides.readOnly;
		if (overrides.securityOpt && overrides.securityOpt.length > 0)
			updates.securityOpt = [...overrides.securityOpt];
		if (overrides.shmSize) updates.shmSize = overrides.shmSize;
		if (overrides.workingDir) updates.workingDir = overrides.workingDir;
		if (overrides.command && overrides.command.length > 0) updates.command = [...overrides.command];
		return updates;
	}

	function updateOverride<K extends keyof Omit<DockerOverrides, '$typeName' | '$unknown'>>(
		key: K,
		value: DockerOverrides[K] | undefined
	) {
		const updates = cloneCurrent();

		if (
			value === undefined ||
			value === null ||
			(typeof value === 'string' && !value) ||
			(Array.isArray(value) && value.length === 0) ||
			(typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === 0) ||
			(typeof value === 'number' && value === 0) ||
			(typeof value === 'bigint' && value === 0n)
		) {
			if (key == 'environment') {
				updates.environment = {};
			} else if (key == 'volumes') {
				updates.volumes = [];
			} else if (key == 'entrypoint') {
				updates.entrypoint = [];
			} else if (key == 'dns') {
				updates.dns = [];
			} else if (key == 'labels') {
				updates.labels = {};
			} else {
				delete updates[key];
			}
		} else {
			updates[key] = value;
		}

		// Proto metadata keys never count as user values
		const hasValues = Object.entries(updates).some(
			([k, v]) =>
				!k.startsWith('$') &&
				v !== undefined &&
				v !== null &&
				v !== '' &&
				v !== 0 &&
				v !== 0n &&
				v !== false &&
				(!Array.isArray(v) || v.length > 0) &&
				(typeof v !== 'object' || Array.isArray(v) || Object.keys(v).length > 0)
		);

		suppressRowSync = true;
		if (hasValues) {
			overrides = create(DockerOverridesSchema, updates);
		} else {
			overrides = undefined;
		}

		onchange?.(overrides);
	}

	function addEnvRow() {
		envRows = [...envRows, { key: '', value: '' }];
	}

	function commitEnvRows() {
		updateOverride('environment', mapFromRows(envRows));
	}

	function addLabelRow() {
		labelRows = [...labelRows, { key: '', value: '' }];
	}

	function commitLabelRows(changedKey?: string) {
		if (changedKey && changedKey.startsWith('discopanel.')) {
			notify.error('This namespace is reserved for system use.');
		}
		updateOverride('labels', mapFromRows(labelRows));
	}

	function addVolume() {
		const volumes = [...(overrides?.volumes || [])];
		volumes.push(
			create(VolumeMountSchema, {
				source: '',
				target: '',
				readOnly: false,
				type: 'bind'
			})
		);
		updateOverride('volumes', volumes);
	}

	function updateVolume(index: number, volume: VolumeMount | null) {
		const volumes = [...(overrides?.volumes || [])];
		if (volume) {
			volumes[index] = volume;
		} else {
			volumes.splice(index, 1);
		}
		updateOverride('volumes', volumes.length > 0 ? volumes : undefined);
	}

	function updateVolumeField(index: number, field: keyof VolumeMount, value: unknown) {
		const volumes = [...(overrides?.volumes || [])];
		if (volumes[index]) {
			volumes[index] = create(VolumeMountSchema, {
				...volumes[index],
				[field]: value
			});
			updateOverride('volumes', volumes);
		}
	}
</script>

<div class="space-y-4">
	<div class="flex items-center justify-between">
		<button
			type="button"
			onclick={() => (showAdvanced = !showAdvanced)}
			{disabled}
			class="flex items-center gap-2 text-sm font-medium transition-colors hover:text-primary"
		>
			{#if showAdvanced}
				<ChevronDown class="size-4" />
			{:else}
				<ChevronRight class="size-4" />
			{/if}
			<span>Docker container overrides</span>
			{#if activeCount > 0}
				<Badge variant="secondary" class="ml-1">{activeCount}</Badge>
			{/if}
			<span class="ml-1 text-xs text-muted-foreground">(advanced)</span>
		</button>

		{#if showAdvanced}
			<Button
				type="button"
				variant="ghost"
				size="sm"
				onclick={toggleJsonMode}
				{disabled}
				class="h-8 gap-2"
			>
				<Code class="size-3.5" />
				{jsonMode ? 'Visual editor' : 'JSON editor'}
			</Button>
		{/if}
	</div>

	{#if showAdvanced}
		<Card class="overflow-hidden py-0">
			{#if jsonMode}
				<CardContent class="py-5">
					<div class="space-y-3">
						<Textarea
							bind:value={jsonText}
							oninput={(e) => updateJsonText(e.currentTarget.value)}
							{disabled}
							placeholder={'{}'}
							class="min-h-50 font-mono text-xs {jsonError ? 'border-destructive' : ''}"
						/>
						{#if jsonError}
							<div class="flex items-center gap-2 text-xs text-destructive">
								<AlertCircle class="size-3" />
								{jsonError}
							</div>
						{/if}
						<div class="flex justify-end">
							<Button
								type="button"
								variant="default"
								size="sm"
								onclick={toggleJsonMode}
								disabled={disabled || !!jsonError}
							>
								Apply JSON
							</Button>
						</div>
					</div>
				</CardContent>
			{:else}
				<CardContent class="space-y-6 py-5">
					<div class="space-y-3">
						<div class="flex items-center justify-between">
							<Label class="text-sm font-medium">Environment variables</Label>
							<Button
								type="button"
								variant="ghost"
								size="sm"
								onclick={addEnvRow}
								{disabled}
								class="h-7 gap-1 text-xs"
							>
								<Plus class="size-3" />
								Add variable
							</Button>
						</div>
						{#if envRows.length > 0}
							<div class="rounded-lg border p-3">
								<KeyValueRowsEditor
									bind:rows={envRows}
									variant="compact"
									keyPlaceholder="VARIABLE_NAME"
									valuePlaceholder="value"
									entryLabel="variable"
									{disabled}
									onrowchange={() => commitEnvRows()}
								/>
							</div>
						{:else}
							<div class="text-xs text-muted-foreground italic">
								No environment variables configured
							</div>
						{/if}
					</div>

					<div class="space-y-3">
						<div class="flex items-center justify-between">
							<Label class="text-sm font-medium">Volume mounts</Label>
							<Button
								type="button"
								variant="ghost"
								size="sm"
								onclick={addVolume}
								{disabled}
								class="h-7 gap-1 text-xs"
							>
								<Plus class="size-3" />
								Add volume
							</Button>
						</div>
						{#if overrides?.volumes && overrides.volumes.length > 0}
							<div class="space-y-3 rounded-lg border p-3">
								{#each overrides.volumes as volume, i (i)}
									<div class="space-y-2 rounded-md border bg-muted/30 p-2">
										<div class="flex items-center gap-2">
											<div class="grid flex-1 grid-cols-2 gap-2">
												<Input
													value={volume.source}
													onchange={(e) => updateVolumeField(i, 'source', e.currentTarget.value)}
													placeholder="/host/path or volume-name"
													{disabled}
													class="h-8 font-mono text-xs"
												/>
												<Input
													value={volume.target}
													onchange={(e) => updateVolumeField(i, 'target', e.currentTarget.value)}
													placeholder="/container/path"
													{disabled}
													class="h-8 font-mono text-xs"
												/>
											</div>
											<Button
												type="button"
												variant="ghost"
												size="icon"
												onclick={() => updateVolume(i, null)}
												{disabled}
												class="size-8 hover:bg-destructive/10 hover:text-destructive"
											>
												<X class="size-3" />
											</Button>
										</div>
										<div class="flex items-center gap-4 pl-1">
											<label class="flex items-center gap-2">
												<input
													type="checkbox"
													checked={volume.readOnly}
													onchange={(e) => {
														updateVolumeField(i, 'readOnly', e.currentTarget.checked);
														if (e.currentTarget.checked) updateVolumeField(i, 'createDir', false);
													}}
													{disabled}
													class="size-3"
												/>
												<span class="text-xs text-muted-foreground">Read only</span>
											</label>
											<label class="flex items-center gap-2">
												<input
													type="checkbox"
													checked={volume.createDir}
													onchange={(e) => {
														updateVolumeField(i, 'createDir', e.currentTarget.checked);
														if (e.currentTarget.checked) {
															updateVolumeField(i, 'readOnly', false);
														}
													}}
													{disabled}
													class="size-3"
												/>
												<span class="text-xs text-muted-foreground">Pre-create dir</span>
											</label>
											<select
												value={volume.type || 'bind'}
												onchange={(e) => updateVolumeField(i, 'type', e.currentTarget.value)}
												{disabled}
												class="h-6 rounded border bg-background px-1 text-xs"
											>
												<option value="bind">Bind mount</option>
												<option value="volume">Volume</option>
											</select>
										</div>
									</div>
								{/each}
							</div>
						{:else}
							<div class="text-xs text-muted-foreground italic">No volume mounts configured</div>
						{/if}
					</div>

					<div class="grid grid-cols-2 gap-4">
						<div class="space-y-2">
							<Label for="cpu-limit" class="text-sm">CPU limit (cores)</Label>
							<Input
								id="cpu-limit"
								type="number"
								step="0.5"
								min="0"
								placeholder="e.g. 2.5"
								value={overrides?.cpuLimit || ''}
								onchange={(e) =>
									updateOverride(
										'cpuLimit',
										e.currentTarget.value ? Number(e.currentTarget.value) : undefined
									)}
								{disabled}
								class="h-8 text-xs"
							/>
						</div>
						<div class="space-y-2">
							<Label for="memory-limit" class="text-sm">Memory limit (MB)</Label>
							<Input
								id="memory-limit"
								type="number"
								min="512"
								placeholder="e.g. 8192"
								value={overrides?.memoryLimit ? Number(overrides.memoryLimit) : ''}
								onchange={(e) =>
									updateOverride(
										'memoryLimit',
										e.currentTarget.value ? BigInt(Number(e.currentTarget.value)) : undefined
									)}
								{disabled}
								class="h-8 text-xs"
							/>
						</div>
					</div>

					<div class="grid grid-cols-2 gap-4">
						<div class="space-y-2">
							<Label for="network-mode" class="text-sm">Network mode</Label>
							<Input
								id="network-mode"
								type="text"
								placeholder="bridge (default)"
								value={overrides?.networkMode || ''}
								onchange={(e) => updateOverride('networkMode', e.currentTarget.value || undefined)}
								{disabled}
								class="h-8 text-xs"
							/>
						</div>
						<div class="space-y-2">
							<Label for="restart-policy" class="text-sm">Restart policy</Label>
							<Input
								id="restart-policy"
								type="text"
								placeholder="unless-stopped"
								value={overrides?.restartPolicy || ''}
								onchange={(e) =>
									updateOverride('restartPolicy', e.currentTarget.value || undefined)}
								{disabled}
								class="h-8 text-xs"
							/>
						</div>
					</div>

					<div class="space-y-2">
						<Label for="dns-servers" class="text-sm">DNS servers</Label>
						<Input
							id="dns-servers"
							type="text"
							placeholder="e.g. 8.8.8.8, 1.1.1.1"
							value={overrides?.dns?.join(', ') || ''}
							onchange={(e) => {
								const value = e.currentTarget.value;
								if (!value) {
									updateOverride('dns', undefined);
								} else {
									const servers = value
										.split(',')
										.map((s) => s.trim())
										.filter((s) => s);
									updateOverride('dns', servers.length > 0 ? servers : undefined);
								}
							}}
							{disabled}
							class="h-8 text-xs"
						/>
						<p class="text-xs text-muted-foreground">Comma-separated DNS server addresses</p>
					</div>

					<div class="space-y-3">
						<Label class="text-sm font-medium">Security</Label>
						<div class="flex flex-wrap gap-4 pl-1">
							<label class="flex items-center gap-2">
								<Switch
									checked={overrides?.privileged || false}
									onCheckedChange={(checked) => updateOverride('privileged', checked)}
									{disabled}
								/>
								<span class="text-sm">Privileged mode</span>
							</label>
						</div>
					</div>

					<div class="grid grid-cols-2 gap-4">
						<div class="space-y-2">
							<Label for="user" class="text-sm">Run as user</Label>
							<Input
								id="user"
								type="text"
								placeholder="1000:1000 or username"
								value={overrides?.user || ''}
								onchange={(e) => updateOverride('user', e.currentTarget.value || undefined)}
								{disabled}
								class="h-8 text-xs"
							/>
						</div>
						<div class="space-y-2">
							<Label for="entrypoint" class="text-sm">Entrypoint</Label>
							<Input
								id="entrypoint"
								type="text"
								placeholder={ENTRYPOINT_PLACEHOLDER}
								value={overrides?.entrypoint?.join(', ') || ''}
								onchange={(e) => {
									const value = e.currentTarget.value;
									if (!value) {
										updateOverride('entrypoint', undefined);
									} else {
										const parts = value
											.split(',')
											.map((s) => s.trim())
											.filter((s) => s);
										updateOverride('entrypoint', parts.length > 0 ? parts : undefined);
									}
								}}
								{disabled}
								class="h-8 text-xs"
							/>
						</div>
					</div>

					<div class="space-y-3">
						<div class="flex items-center justify-between">
							<Label class="text-sm font-medium">Labels</Label>
							<Button
								type="button"
								variant="ghost"
								size="sm"
								onclick={addLabelRow}
								{disabled}
								class="h-7 gap-1 text-xs"
							>
								<Plus class="size-3" />
								Add label
							</Button>
						</div>
						{#if labelRows.length > 0}
							<div class="rounded-lg border p-3">
								<KeyValueRowsEditor
									bind:rows={labelRows}
									variant="compact"
									keyPlaceholder="label.name"
									valuePlaceholder="value"
									entryLabel="label"
									{disabled}
									onrowchange={(key) => commitLabelRows(key)}
								/>
							</div>
						{:else}
							<div class="text-xs text-muted-foreground italic">No labels configured</div>
						{/if}
					</div>

					{#if overrides?.extraHosts?.length || overrides?.securityOpt?.length || overrides?.workingDir || overrides?.command?.length || overrides?.readOnly || overrides?.shmSize}
						<p class="text-xs text-muted-foreground">
							Extra hosts, security options, working dir, command, read-only root, and shm size are
							set. Edit them in the JSON editor.
						</p>
					{/if}
				</CardContent>
			{/if}
		</Card>
	{/if}
</div>
