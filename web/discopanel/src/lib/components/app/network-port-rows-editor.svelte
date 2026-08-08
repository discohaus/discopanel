<script lang="ts">
	import { slide } from 'svelte/transition';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import HostnameListInput from '$lib/components/network/hostname-list-input.svelte';
	import { hostnameSlug } from '$lib/hostname';
	import { portRowErrors } from '$lib/utils/ports';
	import { enumLabel } from '$lib/proto-meta';
	import { isRelayProtocol } from '$lib/components/network/topology-data';
	import type { NetworkPort } from '$lib/proto/discopanel/v1/storage_pb';
	import {
		NetworkPortSchema,
		ModuleProtocol,
		ModuleProtocolSchema
	} from '$lib/proto/discopanel/v1/storage_pb';
	import { create } from '@bufbuild/protobuf';
	import { AlertCircle, Info, Plus, Trash2 } from '@lucide/svelte';

	let {
		ports = $bindable([]),
		locked = false,
		disabled = false,
		showRouting = true,
		proxyAvailable = true,
		serverHosts = [],
		usedPorts = {},
		title = '',
		description = '',
		allowAdd = false
	}: {
		ports?: NetworkPort[];
		// Panel owned modules only allow network edits
		locked?: boolean;
		// Everything read only, for in flight saves
		disabled?: boolean;
		// Hides hostname and relay details for template defaults
		showRouting?: boolean;
		// Proxy off limits protocols and hides routing
		proxyAvailable?: boolean;
		serverHosts?: string[];
		usedPorts?: Record<number, boolean>;
		title?: string;
		description?: string;
		// Shows the built in add button and empty state
		allowAdd?: boolean;
	} = $props();

	const uid = $props.id();

	// Keeps a row's current protocol selectable with proxy off
	function rowProtocolOptions(current: ModuleProtocol): ModuleProtocol[] {
		const base = proxyAvailable
			? [ModuleProtocol.TCP, ModuleProtocol.UDP, ModuleProtocol.MINECRAFT, ModuleProtocol.HTTP]
			: [ModuleProtocol.TCP, ModuleProtocol.UDP];
		if (current && !base.includes(current)) base.push(current);
		return base;
	}

	let errors = $derived(portRowErrors(ports, usedPorts));

	function removePort(port: NetworkPort) {
		ports = ports.filter((p) => p !== port);
	}

	function addPort() {
		// Registry assigns zero host ports on save
		ports = [
			...ports,
			create(NetworkPortSchema, {
				name: '',
				containerPort: 0,
				hostPort: 0,
				protocol: ModuleProtocol.TCP
			})
		];
	}

	function isHostnamed(p: ModuleProtocol): boolean {
		return p === ModuleProtocol.HTTP || p === ModuleProtocol.MINECRAFT;
	}
</script>

<div class="space-y-3">
	{#if title || allowAdd}
		<div class="flex flex-wrap items-center justify-between gap-2">
			<div>
				{#if title}
					<Label class="text-sm font-medium">{title}</Label>
				{/if}
				{#if description}
					<p class="mt-1 text-xs text-muted-foreground">{description}</p>
				{/if}
			</div>
			{#if allowAdd}
				<Button
					type="button"
					variant="outline"
					size="sm"
					onclick={addPort}
					disabled={disabled || locked}
					class="h-8"
				>
					<Plus class="size-3.5" />
					Add port
				</Button>
			{/if}
		</div>
	{/if}

	{#if ports.length === 0 && allowAdd}
		<div class="rounded-lg border border-dashed p-4">
			<p class="text-center text-sm text-muted-foreground">
				No additional ports configured. Add one to expose extra services.
			</p>
		</div>
	{/if}

	{#each ports as port, i (port)}
		<div class="space-y-3 rounded-lg border bg-card p-4">
			<div class="flex items-center justify-between">
				<span class="stat-label">Port {i + 1}</span>
				{#if !locked}
					<Button
						variant="ghost"
						size="icon"
						class="size-8 text-muted-foreground hover:text-destructive"
						onclick={() => removePort(port)}
						{disabled}
					>
						<Trash2 class="size-4" />
						<span class="sr-only">Remove port</span>
					</Button>
				{/if}
			</div>

			<div class="grid gap-3 sm:grid-cols-4">
				<div class="space-y-1.5">
					<Label for="{uid}-{i}-name">Name</Label>
					<Input
						id="{uid}-{i}-name"
						bind:value={port.name}
						placeholder="e.g. BlueMap Web"
						disabled={disabled || locked}
					/>
				</div>
				<div class="space-y-1.5">
					<Label for="{uid}-{i}-host">Host port</Label>
					<Input
						id="{uid}-{i}-host"
						type="number"
						bind:value={port.hostPort}
						min={0}
						max={65535}
						placeholder="0 = auto"
						{disabled}
						class={errors[i] ? 'border-destructive' : ''}
					/>
				</div>
				<div class="space-y-1.5">
					<Label for="{uid}-{i}-container">Container port</Label>
					<Input
						id="{uid}-{i}-container"
						type="number"
						bind:value={port.containerPort}
						min={1}
						max={65535}
						placeholder="8080"
						disabled={disabled || locked}
					/>
				</div>
				<div class="space-y-1.5">
					<Label>Protocol</Label>
					<Select
						type="single"
						value={String(port.protocol)}
						disabled={disabled || locked}
						onValueChange={(v) => {
							if (v) port.protocol = Number(v);
						}}
					>
						<SelectTrigger class="w-full">
							<span class="uppercase">
								{enumLabel(ModuleProtocolSchema, port.protocol || ModuleProtocol.TCP)}
							</span>
						</SelectTrigger>
						<SelectContent>
							{#each rowProtocolOptions(port.protocol) as proto (proto)}
								<SelectItem value={String(proto)}>
									{enumLabel(ModuleProtocolSchema, proto)}
								</SelectItem>
							{/each}
						</SelectContent>
					</Select>
				</div>
			</div>

			{#if proxyAvailable}
				<div class="flex flex-wrap items-center gap-3">
					<label class="flex w-fit cursor-pointer items-center gap-2">
						<Checkbox bind:checked={port.proxyEnabled} {disabled} />
						<span class="text-sm">Route through proxy</span>
					</label>
					{#if showRouting && port.proxyEnabled && isRelayProtocol(port.protocol)}
						<span class="rounded-full border px-1.5 py-px text-[10px] text-muted-foreground">
							relay
						</span>
					{/if}
				</div>

				{#if showRouting && port.proxyEnabled && isRelayProtocol(port.protocol)}
					<p class="text-xs text-muted-foreground">Forwards raw traffic through the listener port</p>
				{/if}

				{#if showRouting && port.proxyEnabled && isHostnamed(port.protocol)}
					<div class="space-y-1.5">
						<Label>Hostnames</Label>
						<HostnameListInput
							bind:hostnames={port.hostnames}
							label={hostnameSlug(port.name)}
							disabled={disabled || locked}
							requireLabel
							placeholder={serverHosts.length > 0
								? 'inherits the server hostnames'
								: port.protocol === ModuleProtocol.MINECRAFT
									? 'needs a hostname'
									: 'map.example.com'}
						/>
						{#if (port.hostnames ?? []).length === 0 && serverHosts.length > 0}
							<span class="text-[11px] text-muted-foreground">
								Empty inherits the server hostnames
							</span>
						{/if}
					</div>
				{/if}

				{#if port.proxyEnabled && isHostnamed(port.protocol)}
					<label class="flex w-fit cursor-pointer items-center gap-2">
						<Checkbox bind:checked={port.catchAll} disabled={disabled || locked} />
						<span class="text-sm">Catch all</span>
						<span class="text-xs text-muted-foreground">also answers unlisted addresses</span>
					</label>
				{/if}

				{#if showRouting && port.proxyEnabled && port.protocol === ModuleProtocol.MINECRAFT && !port.catchAll && port.hostnames.length === 0 && serverHosts.length === 0}
					<div
						transition:slide={{ duration: 150 }}
						class="flex items-start gap-2 rounded-md border border-status-warn/30 bg-status-warn/10 p-3"
					>
						<Info class="mt-0.5 size-4 shrink-0 text-status-warn" />
						<div class="flex-1 space-y-2 text-xs">
							<p class="text-status-warn">
								Minecraft routing matches hostnames. Without one this port cannot receive players.
							</p>
							<Button
								variant="outline"
								size="sm"
								class="h-7 text-xs"
								onclick={() => (port.proxyEnabled = false)}
								{disabled}
							>
								Fix: switch to direct binding
							</Button>
						</div>
					</div>
				{/if}
			{/if}

			{#if errors[i]}
				<div transition:slide={{ duration: 150 }} class="flex items-center gap-1.5 text-destructive">
					<AlertCircle class="size-3" />
					<span class="text-xs">{errors[i]}</span>
				</div>
			{/if}
		</div>
	{/each}
</div>
