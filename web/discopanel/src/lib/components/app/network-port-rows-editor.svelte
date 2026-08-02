<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import HostnameListInput from '$lib/components/network/hostname-list-input.svelte';
	import { hostnameSlug } from '$lib/hostname';
	import { portRowErrors } from '$lib/utils/ports';
	import { enumLabel } from '$lib/proto-meta';
	import type { NetworkPort } from '$lib/proto/discopanel/v1/storage_pb';
	import { ModuleProtocol, ModuleProtocolSchema } from '$lib/proto/discopanel/v1/storage_pb';
	import { AlertCircle, Info, Trash2 } from '@lucide/svelte';

	let {
		ports = $bindable([]),
		locked = false,
		showRouting = true,
		serverHosts = [],
		usedPorts = {}
	}: {
		ports?: NetworkPort[];
		// Panel owned modules only allow network edits
		locked?: boolean;
		// Hides hostname and relay details for template defaults
		showRouting?: boolean;
		serverHosts?: string[];
		usedPorts?: Record<number, boolean>;
	} = $props();

	const uid = $props.id();

	const PROTOCOL_OPTIONS = [
		ModuleProtocol.TCP,
		ModuleProtocol.UDP,
		ModuleProtocol.MINECRAFT,
		ModuleProtocol.HTTP
	];

	let errors = $derived(portRowErrors(ports, usedPorts));

	function removePort(port: NetworkPort) {
		ports = ports.filter((p) => p !== port);
	}

	function isRelay(p: ModuleProtocol): boolean {
		return p === ModuleProtocol.TCP || p === ModuleProtocol.UDP;
	}

	function isHostnamed(p: ModuleProtocol): boolean {
		return p === ModuleProtocol.HTTP || p === ModuleProtocol.MINECRAFT;
	}
</script>

<div class="space-y-3">
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
					>
						<Trash2 class="size-4" />
						<span class="sr-only">Remove port</span>
					</Button>
				{/if}
			</div>

			<div class="grid gap-3 sm:grid-cols-4">
				<div class="space-y-1.5">
					<Label for="{uid}-{i}-name">Name</Label>
					<Input id="{uid}-{i}-name" bind:value={port.name} placeholder="http" disabled={locked} />
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
						disabled={locked}
					/>
				</div>
				<div class="space-y-1.5">
					<Label>Protocol</Label>
					<Select
						type="single"
						value={String(port.protocol)}
						disabled={locked}
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
							{#each PROTOCOL_OPTIONS as proto (proto)}
								<SelectItem value={String(proto)}>
									{enumLabel(ModuleProtocolSchema, proto)}
								</SelectItem>
							{/each}
						</SelectContent>
					</Select>
				</div>
			</div>

			<div class="flex flex-wrap items-center gap-3">
				<label class="flex w-fit cursor-pointer items-center gap-2">
					<Checkbox bind:checked={port.proxyEnabled} />
					<span class="text-sm">Route through proxy</span>
				</label>
				{#if showRouting && port.proxyEnabled && isRelay(port.protocol)}
					<span class="rounded-full border px-1.5 py-px text-[10px] text-muted-foreground">
						relay
					</span>
				{/if}
			</div>

			{#if showRouting && port.proxyEnabled && isRelay(port.protocol)}
				<p class="text-xs text-muted-foreground">Forwards raw traffic through the listener port</p>
			{/if}

			{#if showRouting && port.proxyEnabled && isHostnamed(port.protocol)}
				<div class="space-y-1.5">
					<Label>Hostnames</Label>
					<HostnameListInput
						bind:hostnames={port.hostnames}
						label={hostnameSlug(port.name)}
						disabled={locked}
						requireLabel
						placeholder={serverHosts.join(', ') ||
							(port.protocol === ModuleProtocol.HTTP ? 'map.example.com' : 'needs a hostname')}
					/>
				</div>
			{/if}

			{#if showRouting && port.proxyEnabled && port.protocol === ModuleProtocol.MINECRAFT && port.hostnames.length === 0 && serverHosts.length === 0}
				<div
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
						>
							Fix: switch to direct binding
						</Button>
					</div>
				</div>
			{/if}

			{#if errors[i]}
				<div class="flex items-center gap-1.5 text-destructive">
					<AlertCircle class="size-3" />
					<span class="text-xs">{errors[i]}</span>
				</div>
			{/if}
		</div>
	{/each}
</div>
