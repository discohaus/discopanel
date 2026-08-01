<script lang="ts">
	import { onMount } from 'svelte';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import type {
		GetAccessStatusResponse,
		GetPortMappingsResponse,
		PortReachability
	} from '$lib/proto/discopanel/v1/proxy_pb';
	import {
		AddressSource,
		BaseUrlSource,
		DomainVerdict,
		HostnameSecurity
	} from '$lib/proto/discopanel/v1/proxy_pb';
	import { NetworkTransport } from '$lib/proto/discopanel/v1/storage_pb';
	import { timestampDate } from '@bufbuild/protobuf/wkt';
	import { Input } from '$lib/components/ui/input';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import { Tabs, TabsContent, TabsList, TabsTrigger } from '$lib/components/ui/tabs';
	import CertificateManager from '../certificate-manager.svelte';
	import SecureFlow from '../secure-flow.svelte';
	import { autoReasonCopy, domainVerdictCopy, portNoteCopy } from '../network-copy';
	import { toast } from 'svelte-sonner';
	import {
		AlertTriangle,
		Cable,
		Check,
		ChevronDown,
		ChevronRight,
		CircleCheck,
		CircleDashed,
		Copy,
		ExternalLink,
		Globe,
		Loader2,
		Lock,
		Network,
		RadioTower,
		RotateCcw,
		Save,
		ShieldCheck,
		Wifi,
		Zap
	} from '@lucide/svelte';

	let {
		enabled,
		running,
		baseUrl,
		effectiveBaseUrl,
		baseUrlSource,
		strictHttps,
		listenerCount,
		routeCount,
		hasProxiedWorkloads,
		initialPanel = '',
		onRequestDisable,
		onChanged
	}: {
		enabled: boolean;
		running: boolean;
		baseUrl: string;
		effectiveBaseUrl: string;
		baseUrlSource: BaseUrlSource;
		strictHttps: boolean;
		listenerCount: number;
		routeCount: number;
		hasProxiedWorkloads: boolean;
		initialPanel?: string;
		onRequestDisable: () => void;
		onChanged: () => Promise<void>;
	} = $props();

	let tab = $state(initialPanel === 'certs' ? 'certificates' : 'network');
	let status = $state<GetAccessStatusResponse | null>(null);
	let checking = $state(false);
	let loaded = $state(false);

	let draftEnabled = $state(false);
	let customDomain = $state('');
	let draftStrict = $state(false);
	let saving = $state(false);

	let mappings = $state<GetPortMappingsResponse | null>(null);
	let mapping = $state(false);
	let keepaliveBusy = $state(false);

	let secureOpen = $state(false);

	// Saved snapshot drives dirty detection
	let seeded = $state('');
	$effect(() => {
		const snapshot = `${enabled}|${baseUrl}|${strictHttps}`;
		if (seeded === snapshot) return;
		seeded = snapshot;
		draftEnabled = enabled;
		customDomain = baseUrl;
		draftStrict = strictHttps;
	});

	let draftBaseUrl = $derived(customDomain.trim().toLowerCase());
	let displayDomain = $derived(customDomain || status?.autoDomain || '');
	let dirty = $derived(
		draftEnabled !== enabled || draftBaseUrl !== baseUrl || draftStrict !== strictHttps
	);

	let checkedPorts = $derived(status?.ports.filter((p) => p.checked) ?? []);
	let portFailures = $derived(checkedPorts.filter((p) => !p.confirmed));
	let portsOk = $derived(checkedPorts.length > 0 && portFailures.length === 0);
	let localOnly = $derived(status?.domainVerdict === DomainVerdict.LOCAL);
	let wanOk = $derived(status?.domainEcho ?? false);
	let domainOk = $derived(wanOk || localOnly);
	// Failed verdicts warn, unchecked ones stay neutral
	let domainWarn = $derived.by(() => {
		if (!status || domainOk) return false;
		return (
			status.domainVerdict === DomainVerdict.UNRESOLVED ||
			status.domainVerdict === DomainVerdict.NO_ANSWER ||
			status.domainVerdict === DomainVerdict.ELSEWHERE
		);
	});

	// Internet section stays folded until it matters
	let internetOpen = $state(false);
	let internetSeeded = $state(false);
	$effect(() => {
		if (!loaded || internetSeeded) return;
		internetSeeded = true;
		internetOpen = !!baseUrl || (status?.domainEcho ?? false);
	});

	let plainOnly = $derived(
		status?.hostnames.filter(
			(h) => !h.certificateId && h.unsecurable && h.security !== HostnameSecurity.EDGE
		) ?? []
	);
	let uncovered = $derived(
		status?.hostnames.filter(
			(h) => !h.certificateId && !h.unsecurable && h.security !== HostnameSecurity.EDGE
		) ?? []
	);
	let edgeCovered = $derived(
		status?.hostnames.filter((h) => h.security === HostnameSecurity.EDGE).length ?? 0
	);
	let secured = $derived(
		status?.hostnames.filter((h) => !!h.certificateId || h.security === HostnameSecurity.EDGE)
			.length ?? 0
	);
	let securable = $derived((status?.hostnames.length ?? 0) - plainOnly.length);
	let renewalFailures = $derived(status?.renewalFailures ?? []);
	let autoFailures = $derived(status?.autoIssueFailures ?? []);
	let httpsOk = $derived(
		!!status && securable > 0 && uncovered.length === 0 && renewalFailures.length === 0
	);

	// Strict without a secured panel domain locks browsers out
	let panelUnsecured = $derived.by(() => {
		if (!status) return false;
		const entry = status.hostnames.find((h) => h.hostname === status?.effectiveDomain);
		if (!entry) return false;
		return !entry.certificateId && entry.security !== HostnameSecurity.EDGE;
	});

	let mappedAny = $derived(mappings?.results.some((r) => r.ok) ?? false);

	let checkedAgo = $derived.by(() => {
		if (!status?.checkedAt) return '';
		const mins = Math.floor((Date.now() - timestampDate(status.checkedAt).getTime()) / 60_000);
		if (mins < 1) return 'just now';
		if (mins < 60) return `${mins}m ago`;
		return `${Math.floor(mins / 60)}h ago`;
	});

	onMount(() => {
		loadStatus();
		rpcClient.proxy.getPortMappings({}, silentCallOptions).then((res) => (mappings = res));
	});

	async function loadStatus() {
		try {
			status = await rpcClient.proxy.getAccessStatus({}, silentCallOptions);
		} catch {
			// Snapshot failures leave the checklist pending
		}
		loaded = true;
	}

	async function recheck() {
		if (checking) return;
		checking = true;
		try {
			status = await rpcClient.proxy.checkAccess({}, silentCallOptions);
		} catch {
			toast.error('Probe failed');
		} finally {
			checking = false;
		}
	}

	async function runMapping() {
		if (mapping) return;
		mapping = true;
		try {
			mappings = await rpcClient.proxy.attemptPortMappings({});
			await recheck();
		} catch (error: unknown) {
			toast.error(error instanceof Error ? error.message : 'Mapping failed');
		} finally {
			mapping = false;
		}
	}

	async function toggleKeepalive(next: boolean) {
		keepaliveBusy = true;
		try {
			mappings = await rpcClient.proxy.setPortMappingKeepalive({ enabled: next });
		} catch (error: unknown) {
			toast.error(error instanceof Error ? error.message : 'Failed to update keepalive');
		} finally {
			keepaliveBusy = false;
		}
	}

	function toggleEnabled(next: boolean) {
		if (!next && enabled && hasProxiedWorkloads) {
			// Guided convert handles the disable end to end
			onRequestDisable();
			return;
		}
		draftEnabled = next;
	}

	function discard() {
		draftEnabled = enabled;
		customDomain = baseUrl;
		draftStrict = strictHttps;
	}

	async function save() {
		saving = true;
		try {
			await rpcClient.proxy.updateProxyConfig({
				enabled: draftEnabled,
				// Instant sslip names stay automatic and never persist
				baseUrl: draftBaseUrl.endsWith('.sslip.io') ? '' : draftBaseUrl,
				strictHttps: draftStrict
			});
			toast.success('Proxy configuration saved');
			await onChanged();
			// Fresh probes keep the checklist honest after a change
			recheck();
		} catch (error: unknown) {
			toast.error(error instanceof Error ? error.message : 'Failed to save proxy configuration');
		} finally {
			saving = false;
		}
	}

	function presetLabel(source: AddressSource): string {
		return source === AddressSource.PUBLIC ? 'External' : 'Internal';
	}

	function portLabel(p: PortReachability): string {
		return p.transport === NetworkTransport.UDP ? `:${p.port}/udp` : `:${p.port}`;
	}

	async function copyText(text: string) {
		try {
			await navigator.clipboard.writeText(text);
			toast.success('Copied');
		} catch {
			toast.error('Clipboard copy failed');
		}
	}
</script>

{#snippet stepIcon(ok: boolean, warn: boolean)}
	{#if ok}
		<CircleCheck class="size-4 shrink-0 text-status-ok" />
	{:else if warn}
		<AlertTriangle class="size-4 shrink-0 text-status-busy" />
	{:else}
		<CircleDashed class="size-4 shrink-0 text-muted-foreground" />
	{/if}
{/snippet}

<div class="flex h-full min-h-0 flex-col">
	<div class="min-h-0 flex-1 overflow-y-auto p-4">
		<div class="flex items-center justify-between gap-3">
			<div class="flex min-w-0 items-center gap-3">
				<div
					class="flex size-9 shrink-0 items-center justify-center rounded-lg border {draftEnabled
						? 'border-primary/30 bg-primary/10 text-primary'
						: 'bg-muted/40 text-muted-foreground'}"
				>
					<Network class="size-4.5" />
				</div>
				<div class="min-w-0">
					<h3 class="text-sm font-semibold">Proxy routing</h3>
					<p class="text-xs text-muted-foreground">
						{#if enabled}
							{running ? 'Running' : 'Not running'} · {listenerCount}
							{listenerCount === 1 ? 'listener' : 'listeners'} · {routeCount}
							{routeCount === 1 ? 'route' : 'routes'}
						{:else}
							Off, servers use direct ports
						{/if}
					</p>
				</div>
			</div>
			<Switch
				checked={draftEnabled}
				onCheckedChange={toggleEnabled}
				disabled={saving}
				aria-label="Enable proxy"
			/>
		</div>

		<Tabs bind:value={tab} class="mt-4">
			<TabsList class="grid w-full grid-cols-2">
				<TabsTrigger value="network">Network</TabsTrigger>
				<TabsTrigger value="certificates">Certificates</TabsTrigger>
			</TabsList>

			<TabsContent value="network" class="mt-4 space-y-3">
				{#if !loaded}
					<p class="text-xs text-muted-foreground">Loading the access snapshot</p>
				{:else}
					<!-- Local play works out of the box -->
					<div class="space-y-1.5 rounded-lg border p-3">
						<div class="flex items-center gap-2">
							{#if status?.effectiveDomain}
								<CircleCheck class="size-4 shrink-0 text-status-ok" />
							{:else}
								<CircleDashed class="size-4 shrink-0 text-muted-foreground" />
							{/if}
							<Wifi class="size-3.5 shrink-0 text-muted-foreground" />
							<span class="text-xs font-medium">Your network</span>
							{#if status?.effectiveDomain}
								<span
									class="min-w-0 flex-1 truncate text-right font-mono text-[11px] text-muted-foreground"
								>
									{status.effectiveDomain}
								</span>
							{/if}
						</div>
						<p class="text-xs text-muted-foreground">
							{#if status?.effectiveDomain}
								Anyone on your network can join. Server addresses live under this name.
							{:else}
								Detecting your network address
							{/if}
						</p>
					</div>

					<!-- Internet access stays folded until wanted -->
					<div class="overflow-hidden rounded-lg border">
						<button
							type="button"
							class="flex w-full items-center gap-2 px-3 py-2.5 text-left transition-colors hover:bg-accent/40"
							onclick={() => (internetOpen = !internetOpen)}
						>
							<Globe class="size-4 shrink-0 text-muted-foreground" />
							<span class="min-w-0 flex-1">
								<span class="block text-xs font-medium">Internet access</span>
								<span class="block text-[11px] text-muted-foreground">
									{#if wanOk}
										Friends anywhere can join.
									{:else}
										Optional. Set this up so friends outside your network can join.
									{/if}
								</span>
							</span>
							{#if wanOk}
								<span
									class="shrink-0 rounded-full border border-status-ok/30 px-1.5 text-[10px] text-status-ok"
								>
									public
								</span>
							{:else if domainWarn && !!baseUrl}
								<span
									class="shrink-0 rounded-full border border-status-busy/30 px-1.5 text-[10px] text-status-busy"
								>
									incomplete
								</span>
							{/if}
							{#if internetOpen}
								<ChevronDown class="size-4 shrink-0 text-muted-foreground" />
							{:else}
								<ChevronRight class="size-4 shrink-0 text-muted-foreground" />
							{/if}
						</button>
						{#if internetOpen}
							<div class="space-y-3 border-t p-3">
								<div class="flex items-center justify-between gap-2">
									<span class="stat-label">Reachability</span>
									<div class="flex items-center gap-2">
										{#if checkedAgo}
											<span class="text-[10px] text-muted-foreground">checked {checkedAgo}</span>
										{/if}
										<Button
											size="sm"
											variant="ghost"
											class="h-7 px-2 text-xs"
											onclick={recheck}
											disabled={checking}
										>
											{#if checking}
												<Loader2 class="size-3.5 animate-spin" />
											{:else}
												<RadioTower class="size-3.5" />
											{/if}
											{status?.checkedAt ? 'Re-check' : 'Check'}
										</Button>
									</div>
								</div>

								{#if draftEnabled}
									<!-- Domain doubles as the address picker -->
									<div class="space-y-2 rounded-lg border p-3">
										<div class="flex items-center gap-2">
											{@render stepIcon(domainOk, domainWarn)}
											<span class="text-xs font-medium">Domain</span>
											<span
												class="min-w-0 flex-1 truncate text-right font-mono text-[11px] text-muted-foreground"
											>
												{status?.probedIp || 'detecting'}
											</span>
										</div>
										<Input
											type="text"
											value={displayDomain}
											oninput={(e) => (customDomain = e.currentTarget.value)}
											placeholder="minecraft.example.com"
											class="font-mono text-sm"
										/>
										<p class="text-xs text-muted-foreground">
											Server hostnames live under this domain. It is also where the panel answers.
										</p>
										{#if draftBaseUrl.endsWith('.sslip.io')}
											<p class="text-xs text-muted-foreground">
												Instant addresses follow your network on their own, so this stays on
												Automatic instead of being saved.
											</p>
										{/if}
										{#if status && status.candidates.length > 0}
											<div class="overflow-hidden rounded-lg border">
												<button
													type="button"
													class="flex w-full items-center gap-2 px-3 py-1.5 text-left transition-colors hover:bg-accent/40 {!draftBaseUrl
														? 'bg-primary/5'
														: ''}"
													onclick={() => (customDomain = '')}
												>
													<Zap class="size-3.5 shrink-0 text-primary" />
													<span class="min-w-0 flex-1">
														<span class="block text-xs font-medium">Automatic</span>
														{#if status.autoDomain}
															<span
																class="block truncate font-mono text-[11px] text-muted-foreground"
															>
																{status.autoDomain}
															</span>
														{/if}
													</span>
													{#if !draftBaseUrl}
														<Check class="size-3.5 shrink-0 text-primary" />
													{/if}
												</button>
												{#each status.candidates as candidate (candidate.ip)}
													{@const active = draftBaseUrl === candidate.domain}
													<button
														type="button"
														class="flex w-full items-center gap-2 border-t px-3 py-1.5 text-left transition-colors hover:bg-accent/40 {active
															? 'bg-primary/5'
															: ''}"
														onclick={() => (customDomain = candidate.domain)}
													>
														<Globe class="size-3.5 shrink-0 text-muted-foreground" />
														<span class="min-w-0 flex-1">
															<span class="block text-xs font-medium"
																>{presetLabel(candidate.source)}</span
															>
															<span
																class="block truncate font-mono text-[11px] text-muted-foreground"
															>
																{candidate.domain}
															</span>
														</span>
														{#if candidate.checked}
															<span
																class="shrink-0 rounded-full border px-1.5 text-[10px] {candidate.confirmed
																	? 'border-status-ok/30 text-status-ok'
																	: 'border-status-busy/30 text-status-busy'}"
															>
																{candidate.confirmed ? 'reachable' : 'unreachable'}
															</span>
														{/if}
														{#if active}
															<Check class="size-3.5 shrink-0 text-primary" />
														{/if}
													</button>
												{/each}
											</div>
										{/if}
										{#if status?.domainSetup && draftBaseUrl === status.effectiveDomain}
											{@const setup = status.domainSetup}
											{#if setup.providerFound}
												<div class="flex flex-wrap items-center gap-2 text-xs">
													<span
														class="rounded-full border px-1.5 text-[10px] text-muted-foreground"
													>
														{setup.providerName}
													</span>
													{#if setup.providerUrl}
														<a
															class="flex items-center gap-1 text-muted-foreground underline-offset-2 hover:underline"
															href={setup.providerUrl}
															target="_blank"
															rel="noopener"
														>
															<ExternalLink class="size-3" />
															Open provider
														</a>
													{/if}
												</div>
											{/if}
											{#each setup.records as entry (entry.record?.name)}
												<div class="flex items-center gap-2 rounded-lg border px-2.5 py-1 text-xs">
													<span class="w-6 shrink-0 font-mono text-muted-foreground"
														>{entry.record?.type}</span
													>
													<span class="min-w-0 flex-1 truncate font-mono">
														{entry.record?.name} → {entry.record?.value || '?'}
													</span>
													{#if entry.matches}
														<Check class="size-3.5 shrink-0 text-status-ok" />
													{:else if entry.exists}
														<span class="shrink-0 text-[10px] text-status-busy"
															>points at {entry.current}</span
														>
													{:else}
														<span class="shrink-0 text-[10px] text-muted-foreground"
															>not created yet</span
														>
													{/if}
													<Button
														size="icon"
														variant="ghost"
														class="size-5 shrink-0 text-muted-foreground"
														title="Copy the record"
														disabled={!entry.record?.value}
														onclick={() =>
															copyText(
																`${entry.record?.name}. IN ${entry.record?.type} ${entry.record?.value}`
															)}
													>
														<Copy class="size-3" />
													</Button>
												</div>
											{/each}
										{/if}
										{#if status}
											{@const verdict = draftBaseUrl
												? domainVerdictCopy(status)
												: autoReasonCopy(status.autoReason)}
											{#if verdict}
												<p class="text-xs {domainOk ? 'text-status-ok' : 'text-muted-foreground'}">
													{verdict}
												</p>
											{/if}
										{/if}
									</div>
								{/if}

								<!-- Ports -->
								<div class="space-y-2 rounded-lg border p-3">
									<div class="flex items-center gap-2">
										{@render stepIcon(portsOk, portFailures.length > 0)}
										<span class="text-xs font-medium">Ports</span>
										<span class="flex-1 text-right text-[11px] text-muted-foreground">
											{#if checkedPorts.length > 0}
												{checkedPorts.length - portFailures.length} of {checkedPorts.length} answer from
												outside
											{/if}
										</span>
									</div>
									{#if status && status.ports.length > 0}
										<div class="divide-y rounded-lg border">
											{#each status.ports as port (`${port.port}/${port.transport}`)}
												{@const note = portNoteCopy(port)}
												<div class="flex items-center justify-between gap-2 px-2.5 py-1 text-xs">
													<span class="min-w-0 truncate">
														<span class="font-mono">{portLabel(port)}</span>
														{#if port.detail}
															<span class="text-muted-foreground"> · {port.detail}</span>
														{/if}
														{#if note}
															<span class="text-muted-foreground"> · {note}</span>
														{/if}
													</span>
													{#if !port.checked}
														<span class="shrink-0 text-[10px] text-muted-foreground"
															>not probed</span
														>
													{:else if port.confirmed}
														<Check class="size-3.5 shrink-0 text-status-ok" />
													{:else}
														<AlertTriangle class="size-3.5 shrink-0 text-status-busy" />
													{/if}
												</div>
											{/each}
										</div>
									{/if}
									{#if portFailures.length > 0}
										<p class="text-xs text-muted-foreground">
											The flagged ports are not reachable from the internet yet. The button below
											asks your router to open them, or you can forward them yourself.
										</p>
									{/if}
									<div class="flex flex-wrap items-center gap-2">
										<Button
											size="sm"
											variant="outline"
											class="h-7 px-2 text-xs"
											disabled={mapping}
											onclick={runMapping}
										>
											{#if mapping}
												<Loader2 class="size-3.5 animate-spin" />
											{:else}
												<Cable class="size-3.5" />
											{/if}
											Open ports on router
										</Button>
										{#if mappings?.gateway}
											<a
												class="flex items-center gap-1 text-xs text-muted-foreground underline-offset-2 hover:underline"
												href={`http://${mappings.gateway}`}
												target="_blank"
												rel="noopener"
											>
												<ExternalLink class="size-3" />
												Router admin
											</a>
										{/if}
									</div>
									{#if mappings && mappings.results.length > 0}
										{#if !mappedAny}
											<p class="text-xs text-muted-foreground">
												Your router did not accept the request, so ports need forwarding by hand in
												its admin page.
												{#if mappings.results[0].error}
													<span class="block font-mono text-[10px]"
														>{mappings.results[0].error}</span
													>
												{/if}
											</p>
										{:else}
											<div class="divide-y rounded-lg border">
												{#each mappings.results as result (`${result.port}/${result.transport}`)}
													<div class="space-y-0.5 px-2.5 py-1 text-xs">
														<div class="flex items-center justify-between gap-2">
															<span class="font-mono">:{result.port}</span>
															{#if result.ok}
																<span class="flex items-center gap-1 text-status-ok">
																	<Check class="size-3.5" />
																	{result.method}{result.leaseSeconds > 0 ? ' lease' : ''}
																</span>
															{:else}
																<span class="text-status-busy">failed</span>
															{/if}
														</div>
														{#if !result.ok && result.error}
															<p class="text-[11px] text-status-busy">{result.error}</p>
														{/if}
													</div>
												{/each}
											</div>
										{/if}
										{#if mappedAny}
											<div class="flex items-center justify-between gap-2">
												<span class="text-xs">Renew leases on a loop</span>
												<Switch
													checked={mappings.keepalive}
													disabled={keepaliveBusy}
													onCheckedChange={toggleKeepalive}
													aria-label="Lease keepalive"
												/>
											</div>
										{/if}
									{/if}
								</div>

								{#if draftEnabled}
									<!-- Https reflects observed serving, not wishes -->
									<div class="space-y-2 rounded-lg border p-3">
										<div class="flex items-center gap-2">
											{@render stepIcon(
												httpsOk,
												renewalFailures.length > 0 || autoFailures.length > 0
											)}
											<span class="text-xs font-medium">HTTPS</span>
											<span class="flex-1 text-right text-[11px] text-muted-foreground">
												{#if status && securable > 0}
													{secured} of {securable}
													{securable === 1 ? 'hostname' : 'hostnames'} secured
												{/if}
											</span>
										</div>
										{#if renewalFailures.length > 0}
											<p class="text-xs text-status-busy">
												Renewal failed for {renewalFailures.join(', ')}.
											</p>
										{/if}
										{#each autoFailures as failure (failure.domains)}
											<p class="text-xs text-status-busy">
												Automatic HTTPS for {failure.domains} failed. {failure.error}
											</p>
										{/each}
										{#if httpsOk}
											<p class="flex items-center gap-1.5 text-xs text-status-ok">
												<ShieldCheck class="size-3.5" />
												Every reachable hostname serves HTTPS. Issuance and renewal run themselves.
											</p>
										{:else if uncovered.length > 0}
											<p class="text-xs text-muted-foreground">
												{uncovered.length}
												{uncovered.length === 1 ? 'hostname' : 'hostnames'} can get certificates once
												a validation path works. Upload your own or issue them now.
											</p>
											<Button
												size="sm"
												class="h-7 px-2 text-xs"
												onclick={() => (secureOpen = true)}
											>
												<ShieldCheck class="size-3.5" />
												Set up HTTPS
											</Button>
										{:else if status && status.hostnames.length === 0}
											<p class="text-xs text-muted-foreground">
												Hostnames appear here as servers and modules declare them.
											</p>
										{/if}
										{#if edgeCovered > 0}
											<p class="text-xs text-muted-foreground">
												{edgeCovered}
												{edgeCovered === 1 ? 'hostname is' : 'hostnames are'} already secured at your
												DNS provider's edge. Nothing to do there.
											</p>
										{/if}
										{#if plainOnly.length > 0}
											<p class="text-xs text-muted-foreground">
												{plainOnly.length}
												{plainOnly.length === 1 ? 'name only resolves' : 'names only resolve'} on your
												network and {plainOnly.length === 1 ? 'stays' : 'stay'} on plain HTTP.
											</p>
										{/if}
									</div>

									<!-- Panel https posture only -->
									<div class="space-y-2 rounded-lg border p-3">
										<label class="flex cursor-pointer items-center justify-between gap-3 text-sm">
											<span class="flex items-start gap-1.5 text-xs">
												<Lock class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
												<span>
													HTTPS only for the panel
													<span class="block text-[11px] font-normal text-muted-foreground">
														Plain HTTP requests to the panel redirect to HTTPS
													</span>
												</span>
											</span>
											<Switch checked={draftStrict} onCheckedChange={(v) => (draftStrict = v)} />
										</label>
										{#if draftStrict && panelUnsecured}
											<p class="text-xs text-status-busy">
												The panel domain has no certificate yet. Issue or upload one first, saving
												with this on will be refused until then.
											</p>
										{/if}
										<p class="text-[11px] text-muted-foreground">
											Each routed port picks its own HTTPS posture in its port settings.
										</p>
									</div>
								{/if}
							</div>
						{/if}
					</div>
				{/if}
			</TabsContent>

			<TabsContent value="certificates" class="mt-4">
				<CertificateManager baseDomain={effectiveBaseUrl} />
			</TabsContent>
		</Tabs>
	</div>

	<SecureFlow
		bind:open={secureOpen}
		hostnames={[]}
		baseDomain={effectiveBaseUrl}
		onDone={async () => {
			await onChanged();
			loadStatus();
		}}
	/>

	{#if dirty}
		<div class="flex items-center justify-end gap-2 border-t bg-muted/20 px-4 py-3">
			<Button variant="outline" size="sm" disabled={saving} onclick={discard}>
				<RotateCcw class="size-4" />
				Discard
			</Button>
			<Button size="sm" onclick={save} disabled={saving}>
				{#if saving}
					<Loader2 class="size-4 animate-spin" />
				{:else}
					<Save class="size-4" />
				{/if}
				Save changes
			</Button>
		</div>
	{/if}
</div>
