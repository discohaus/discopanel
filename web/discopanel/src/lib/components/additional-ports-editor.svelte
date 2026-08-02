<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { Plus, X, AlertCircle } from '@lucide/svelte';
	import type { NetworkPort } from '$lib/proto/discopanel/v1/storage_pb';
	import {
		NetworkPortSchema,
		ModuleProtocol,
		ModuleProtocolSchema
	} from '$lib/proto/discopanel/v1/storage_pb';
	import { create } from '@bufbuild/protobuf';
	import { enumLabel } from '$lib/proto-meta';
	import { isRelayProtocol } from '$lib/components/network/topology-data';
	import HostnameListInput from '$lib/components/network/hostname-list-input.svelte';
	import { hostnameSlug } from '$lib/hostname';

	interface Props {
		ports?: NetworkPort[];
		disabled?: boolean;
		usedPorts?: Record<number, boolean>;
		proxyAvailable?: boolean;
		serverHostnames?: string[];
		onchange?: (ports: NetworkPort[]) => void;
	}

	let {
		ports = $bindable([]),
		disabled = false,
		usedPorts = {},
		proxyAvailable = false,
		serverHostnames = [],
		onchange
	}: Props = $props();

	let protocolOptions = $derived(
		proxyAvailable
			? [ModuleProtocol.TCP, ModuleProtocol.UDP, ModuleProtocol.MINECRAFT, ModuleProtocol.HTTP]
			: [ModuleProtocol.TCP, ModuleProtocol.UDP]
	);

	// Derived per row so removals never misalign errors
	let portErrors = $derived.by(() => {
		return ports.map((port, index) => {
			const value = Number(port.hostPort);
			if (!value) return '';
			if (value < 1 || value > 65535) return 'Port must be between 1 and 65535';
			if (!port.proxyEnabled && usedPorts[value]) return `Port ${value} is already in use`;
			const duplicate = ports.some((p, i) => {
				if (i === index || Number(p.hostPort) !== value) return false;
				if (p.protocol !== port.protocol) return false;
				// Routed rows may share a port on different hostnames
				const routed = port.proxyEnabled && p.proxyEnabled && !isRelayProtocol(port.protocol);
				if (routed) return (p.hostnames ?? []).join(',') === (port.hostnames ?? []).join(',');
				return true;
			});
			if (duplicate)
				return `Duplicate port ${value}/${enumLabel(ModuleProtocolSchema, port.protocol || ModuleProtocol.TCP)}`;
			return '';
		});
	});

	function addPort() {
		const newPort = create(NetworkPortSchema, {
			name: '',
			containerPort: findNextAvailablePort(),
			hostPort: findNextAvailablePort(),
			protocol: ModuleProtocol.TCP
		});
		ports = [...ports, newPort];
		onchange?.(ports);
	}

	function removePort(index: number) {
		ports = ports.filter((_, i) => i !== index);
		onchange?.(ports);
	}

	function updatePort(
		index: number,
		field: keyof NetworkPort,
		value: string | number | boolean | string[]
	) {
		ports[index] = {
			...ports[index],
			[field]: value
		};
		onchange?.(ports);
	}

	function findNextAvailablePort(startFrom: number = 25566): number {
		let port = startFrom;
		while (port <= 65535) {
			if (!usedPorts[port] && !ports.some((p) => p.hostPort === port)) {
				return port;
			}
			port++;
		}
		return 25566;
	}
</script>

<div class="space-y-3">
	<div class="flex flex-wrap items-center justify-between gap-2">
		<div>
			<Label class="text-sm font-medium">Additional ports</Label>
			<p class="mt-1 text-xs text-muted-foreground">
				Extra ports for mods, plugins, or services like BlueMap, voice chat, or dynmap
			</p>
		</div>
		<Button type="button" variant="outline" size="sm" onclick={addPort} {disabled} class="h-8">
			<Plus class="size-3.5" />
			Add port
		</Button>
	</div>

	{#if ports.length > 0}
		<div class="space-y-2 rounded-lg border p-3">
			<div class="hidden grid-cols-12 gap-2 px-1 text-xs font-medium text-muted-foreground sm:grid">
				<div class="col-span-4">Name</div>
				<div class="col-span-3">Container port</div>
				<div class="col-span-2">Host port</div>
				<div class="col-span-2">Protocol</div>
				<div class="col-span-1"></div>
			</div>

			{#each ports as port, index (index)}
				<div class="space-y-1.5">
					<div class="grid grid-cols-2 items-center gap-2 sm:grid-cols-12">
						<div class="col-span-2 sm:col-span-4">
							<Input
								type="text"
								placeholder="e.g. BlueMap Web"
								bind:value={port.name}
								{disabled}
								onchange={() => updatePort(index, 'name', port.name)}
								class="h-8 text-xs"
							/>
						</div>
						<div class="sm:col-span-3">
							<Input
								type="number"
								min="1"
								max="65535"
								placeholder="8100"
								bind:value={port.containerPort}
								{disabled}
								onchange={() => updatePort(index, 'containerPort', port.containerPort)}
								class="h-8 text-xs"
							/>
						</div>
						<div class="sm:col-span-2">
							<Input
								type="number"
								min="1"
								max="65535"
								placeholder="8100"
								bind:value={port.hostPort}
								{disabled}
								onchange={() => updatePort(index, 'hostPort', port.hostPort)}
								class="h-8 text-xs {portErrors[index] ? 'border-destructive' : ''}"
							/>
						</div>
						<div class="sm:col-span-2">
							<Select
								type="single"
								value={String(port.protocol)}
								onValueChange={(v) => updatePort(index, 'protocol', Number(v))}
								{disabled}
							>
								<SelectTrigger class="h-8 w-full text-xs">
									<span>{enumLabel(ModuleProtocolSchema, port.protocol || ModuleProtocol.TCP)}</span
									>
								</SelectTrigger>
								<SelectContent>
									{#each protocolOptions as proto (proto)}
										<SelectItem value={String(proto)}>
											{enumLabel(ModuleProtocolSchema, proto)}
										</SelectItem>
									{/each}
								</SelectContent>
							</Select>
						</div>
						<div class="flex justify-end sm:col-span-1">
							<Button
								type="button"
								variant="ghost"
								size="icon"
								onclick={() => removePort(index)}
								{disabled}
								class="size-8 hover:bg-destructive/10 hover:text-destructive"
								title="Remove port"
							>
								<X class="size-3.5" />
							</Button>
						</div>
					</div>

					{#if proxyAvailable}
						<div class="flex flex-wrap items-center gap-x-4 gap-y-1.5 pl-1">
							<label class="flex cursor-pointer items-center gap-2">
								<Checkbox
									checked={port.proxyEnabled}
									onCheckedChange={(v) => updatePort(index, 'proxyEnabled', !!v)}
									{disabled}
								/>
								<span class="text-xs">Route through proxy</span>
							</label>
							{#if port.proxyEnabled && isRelayProtocol(port.protocol)}
								<span class="rounded-full border px-1.5 py-px text-[10px] text-muted-foreground">
									relay
								</span>
							{/if}
						</div>
						{#if port.proxyEnabled && !isRelayProtocol(port.protocol)}
							<div class="space-y-1 pl-1">
								<HostnameListInput
									bind:hostnames={port.hostnames}
									label={hostnameSlug(port.name)}
									placeholder={serverHostnames.length > 0
										? 'inherits the server hostnames'
										: port.protocol === ModuleProtocol.MINECRAFT
											? 'hostname required'
											: 'map.example.com'}
									{disabled}
									requireLabel
									onchange={(names) => updatePort(index, 'hostnames', names)}
								/>
								{#if (port.hostnames ?? []).length === 0 && serverHostnames.length > 0}
									<span class="text-[11px] text-muted-foreground">
										Empty inherits the server hostnames
									</span>
								{:else if port.protocol === ModuleProtocol.MINECRAFT && (port.hostnames ?? []).length === 0 && serverHostnames.length === 0}
									<span class="text-[11px] text-status-busy">
										Minecraft routing matches hostnames. Without one this port cannot receive
										players.
									</span>
								{/if}
							</div>
						{/if}
					{/if}

					{#if portErrors[index]}
						<div class="flex items-center gap-1.5 pl-1 text-destructive">
							<AlertCircle class="size-3" />
							<span class="text-xs">{portErrors[index]}</span>
						</div>
					{/if}
				</div>
			{/each}
		</div>
	{:else}
		<div class="rounded-lg border border-dashed p-4">
			<p class="text-center text-sm text-muted-foreground">
				No additional ports configured. Add one to expose extra services.
			</p>
		</div>
	{/if}
</div>
