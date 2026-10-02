<script lang="ts">
	import { onMount } from 'svelte';
	import { registerRefresh } from '$lib/stores/refresh';
	import { create } from '@bufbuild/protobuf';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Switch } from '$lib/components/ui/switch';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import {
		Accordion,
		AccordionContent,
		AccordionItem,
		AccordionTrigger
	} from '$lib/components/ui/accordion';
	import SettingRow from '$lib/components/app/setting-row.svelte';
	import { rpcClient } from '$lib/api/rpc-client';
	import { canUpdateSettings } from '$lib/stores/auth';
	import { notify } from '$lib/stores/activity.svelte';
	import { UpdateAuthSettingsRequestSchema } from '$lib/proto/discopanel/v1/auth_pb';
	import type { GetAuthConfigResponse } from '$lib/proto/discopanel/v1/auth_pb';
	import {
		Loader2,
		Check,
		X,
		Save,
		Globe,
		FileCode,
		Container,
		Terminal,
		RotateCcw
	} from '@lucide/svelte';

	let loading = $state(true);
	let saving = $state(false);
	let config = $state<GetAuthConfigResponse | null>(null);

	// Editable form state
	let localAuthEnabled = $state(true);
	let allowRegistration = $state(false);
	let anonymousAccess = $state(false);
	let sessionTimeoutHours = $state(24);

	let canEdit = $derived($canUpdateSettings);

	let hasChanges = $derived(
		config != null &&
			(localAuthEnabled !== config.localAuthEnabled ||
				allowRegistration !== config.allowRegistration ||
				anonymousAccess !== config.anonymousAccess ||
				Math.round(sessionTimeoutHours * 3600) !== config.sessionTimeout)
	);

	function applyConfig(response: GetAuthConfigResponse) {
		config = response;
		localAuthEnabled = response.localAuthEnabled;
		allowRegistration = response.allowRegistration;
		anonymousAccess = response.anonymousAccess;
		sessionTimeoutHours = Math.round((response.sessionTimeout / 3600) * 100) / 100;
	}

	async function loadConfig() {
		loading = true;
		try {
			const response = await rpcClient.auth.getAuthConfig({});
			applyConfig(response);
		} catch (error) {
			console.error('Failed to load auth config:', error);
		} finally {
			loading = false;
		}
	}

	function discardChanges() {
		if (config) applyConfig(config);
	}

	async function saveSettings() {
		if (!config) return;
		saving = true;
		try {
			const req = create(UpdateAuthSettingsRequestSchema, {});

			if (localAuthEnabled !== config.localAuthEnabled) {
				req.localAuthEnabled = localAuthEnabled;
			}
			if (allowRegistration !== config.allowRegistration) {
				req.allowRegistration = allowRegistration;
			}
			if (anonymousAccess !== config.anonymousAccess) {
				req.anonymousAccess = anonymousAccess;
			}
			const newTimeout = Math.round(sessionTimeoutHours * 3600);
			if (newTimeout !== config.sessionTimeout) {
				req.sessionTimeout = newTimeout;
			}

			const response = await rpcClient.auth.updateAuthSettings(req);
			if (response.config) {
				applyConfig(response.config);
			}
			notify.success('Authentication settings updated');
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to update settings');
		} finally {
			saving = false;
		}
	}

	onMount(() => {
		loadConfig();
		return registerRefresh(loadConfig);
	});
</script>

{#snippet stateBadge(on: boolean, onLabel: string, offLabel: string)}
	<Badge variant={on ? 'default' : 'outline'}>
		{#if on}
			<Check class="size-3" />
			{onLabel}
		{:else}
			<X class="size-3" />
			{offLabel}
		{/if}
	</Badge>
{/snippet}

{#if loading}
	<div class="space-y-4">
		<Skeleton class="h-64 rounded-xl" />
		<Skeleton class="h-64 rounded-xl" />
	</div>
{:else if config}
	{@const cfg = config}
	<div class="space-y-4">
		<section class="overflow-hidden rounded-xl border bg-card">
			<header class="border-b bg-muted/30 px-4 py-3">
				<h3 class="text-sm font-semibold">Authentication</h3>
				<p class="mt-0.5 text-xs text-muted-foreground">Login methods and access controls</p>
			</header>

			<div class="divide-y">
				<SettingRow
					id="local-auth"
					label="Local authentication"
					description="Username and password login against the panel's own user database"
					modified={config != null && localAuthEnabled !== config.localAuthEnabled}
				>
					<div class="flex h-9 items-center sm:justify-end">
						{#if canEdit}
							<Switch
								id="local-auth"
								checked={localAuthEnabled}
								onCheckedChange={(v) => {
									localAuthEnabled = v;
									if (!v) allowRegistration = false;
								}}
								disabled={saving}
							/>
						{:else}
							{@render stateBadge(localAuthEnabled, 'Enabled', 'Disabled')}
						{/if}
					</div>
				</SettingRow>

				<SettingRow
					id="allow-registration"
					label="User registration"
					description="Allow new users to create their own accounts on the login page"
					modified={config != null && allowRegistration !== config.allowRegistration}
					dimmed={!localAuthEnabled}
				>
					<div class="flex h-9 items-center sm:justify-end">
						{#if canEdit}
							<Switch
								id="allow-registration"
								checked={allowRegistration}
								onCheckedChange={(v) => {
									allowRegistration = v;
								}}
								disabled={saving || !localAuthEnabled}
							/>
						{:else}
							{@render stateBadge(allowRegistration, 'Allowed', 'Disabled')}
						{/if}
					</div>
				</SettingRow>

				<SettingRow
					id="anonymous-access"
					label="Anonymous access"
					description="Limited read-only browsing without signing in"
					modified={config != null && anonymousAccess !== config.anonymousAccess}
				>
					<div class="flex h-9 items-center sm:justify-end">
						{#if canEdit}
							<Switch
								id="anonymous-access"
								checked={anonymousAccess}
								onCheckedChange={(v) => {
									anonymousAccess = v;
								}}
								disabled={saving}
							/>
						{:else}
							{@render stateBadge(anonymousAccess, 'Enabled', 'Disabled')}
						{/if}
					</div>
				</SettingRow>

				<SettingRow
					id="session-timeout"
					label="Session timeout"
					description="How long a login session stays valid before users must sign in again"
					modified={config != null &&
						Math.round(sessionTimeoutHours * 3600) !== config.sessionTimeout}
				>
					{#if canEdit}
						<div class="flex items-center gap-2 sm:justify-end">
							<Input
								id="session-timeout"
								type="number"
								min="0.084"
								step="0.5"
								class="h-9 w-24"
								bind:value={sessionTimeoutHours}
								disabled={saving}
							/>
							<span class="text-sm text-muted-foreground">hours</span>
						</div>
					{:else}
						<div class="flex h-9 items-center sm:justify-end">
							<Badge variant="outline">{sessionTimeoutHours}h</Badge>
						</div>
					{/if}
				</SettingRow>
			</div>

			{#if canEdit && hasChanges}
				<div class="flex items-center justify-end gap-2 border-t bg-muted/20 px-4 py-3">
					<Button variant="outline" size="sm" onclick={discardChanges} disabled={saving}>
						<RotateCcw class="size-4" />
						Discard
					</Button>
					<Button size="sm" onclick={saveSettings} disabled={saving}>
						{#if saving}
							<Loader2 class="size-4 animate-spin" />
						{:else}
							<Save class="size-4" />
						{/if}
						Save changes
					</Button>
				</div>
			{/if}
		</section>

		<section class="overflow-hidden rounded-xl border bg-card">
			<header
				class="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3"
			>
				<div class="min-w-0">
					<h3 class="text-sm font-semibold">Single sign-on</h3>
					<p class="mt-0.5 text-xs text-muted-foreground">
						OIDC integration with an external identity provider
					</p>
				</div>
				{#if cfg.oidcEnabled}
					<Badge variant="outline" class="border-status-ok/25 bg-status-ok/10 text-status-ok">
						<Check class="size-3" />
						Connected
					</Badge>
				{:else}
					<Badge variant="outline">Not configured</Badge>
				{/if}
			</header>

			<div class="px-4 py-4">
				{#if cfg.oidcEnabled}
					<div class="divide-y rounded-lg border">
						{#if cfg.oidcIssuerUri}
							<div
								class="grid gap-1 px-3.5 py-2.5 sm:grid-cols-[10rem_minmax(0,1fr)] sm:items-baseline"
							>
								<Label class="text-xs text-muted-foreground">Issuer URI</Label>
								<p class="font-mono text-sm break-all">{cfg.oidcIssuerUri}</p>
							</div>
						{/if}
						{#if cfg.oidcClientId}
							<div
								class="grid gap-1 px-3.5 py-2.5 sm:grid-cols-[10rem_minmax(0,1fr)] sm:items-baseline"
							>
								<Label class="text-xs text-muted-foreground">Client ID</Label>
								<p class="font-mono text-sm break-all">{cfg.oidcClientId}</p>
							</div>
						{/if}
						{#if cfg.oidcRedirectUrl}
							<div
								class="grid gap-1 px-3.5 py-2.5 sm:grid-cols-[10rem_minmax(0,1fr)] sm:items-baseline"
							>
								<Label class="text-xs text-muted-foreground">Redirect URL</Label>
								<p class="font-mono text-sm break-all">{cfg.oidcRedirectUrl}</p>
							</div>
						{/if}
						{#if cfg.oidcScopes && cfg.oidcScopes.length > 0}
							<div
								class="grid gap-1 px-3.5 py-2.5 sm:grid-cols-[10rem_minmax(0,1fr)] sm:items-baseline"
							>
								<Label class="text-xs text-muted-foreground">Scopes</Label>
								<div class="flex flex-wrap gap-1.5">
									{#each cfg.oidcScopes as scope (scope)}
										<Badge variant="secondary" class="text-xs">{scope}</Badge>
									{/each}
								</div>
							</div>
						{/if}
						{#if cfg.oidcRoleClaim}
							<div
								class="grid gap-1 px-3.5 py-2.5 sm:grid-cols-[10rem_minmax(0,1fr)] sm:items-baseline"
							>
								<Label class="text-xs text-muted-foreground">Role claim</Label>
								<p class="font-mono text-sm">{cfg.oidcRoleClaim}</p>
							</div>
						{/if}
					</div>
				{:else}
					<div class="space-y-4">
						<div class="flex items-start gap-3 rounded-lg border border-dashed p-4">
							<Globe class="mt-0.5 size-5 shrink-0 text-muted-foreground" />
							<div class="min-w-0 text-sm text-muted-foreground">
								<p>
									OIDC lets users sign in with external identity providers like Keycloak, Authelia,
									Google, or any OpenID Connect compatible service.
								</p>
								<p class="mt-1 text-xs">
									It must be configured outside the UI using one of the methods below.
								</p>
							</div>
						</div>

						<Accordion type="single" class="w-full">
							<AccordionItem value="config-yaml">
								<AccordionTrigger class="text-sm">
									<span class="flex items-center gap-2">
										<FileCode class="size-4" />
										config.yaml
									</span>
								</AccordionTrigger>
								<AccordionContent>
									<pre class="overflow-x-auto rounded-md bg-muted p-3 text-xs"><code
											>auth:
  oidc:
    enabled: true
    issuer_uri: "https://your-provider/.well-known/openid-configuration"
    client_id: "discopanel"
    client_secret: "your-secret"
    redirect_url: "https://your-domain/api/v1/auth/oidc/callback"
    scopes: ["openid", "profile", "email", "groups"]
    role_claim: "groups"</code
										></pre>
								</AccordionContent>
							</AccordionItem>

							<AccordionItem value="docker-compose">
								<AccordionTrigger class="text-sm">
									<span class="flex items-center gap-2">
										<Container class="size-4" />
										Docker Compose
									</span>
								</AccordionTrigger>
								<AccordionContent>
									<pre class="overflow-x-auto rounded-md bg-muted p-3 text-xs"><code
											>services:
  discopanel:
    environment:
      DISCOPANEL_AUTH_OIDC_ENABLED: "true"
      DISCOPANEL_AUTH_OIDC_ISSUER_URI: "https://..."
      DISCOPANEL_AUTH_OIDC_CLIENT_ID: "discopanel"
      DISCOPANEL_AUTH_OIDC_CLIENT_SECRET: "your-secret"
      DISCOPANEL_AUTH_OIDC_REDIRECT_URL: "https://..."
      DISCOPANEL_AUTH_OIDC_SCOPES: "openid,profile,email,groups"
      DISCOPANEL_AUTH_OIDC_ROLE_CLAIM: "groups"</code
										></pre>
								</AccordionContent>
							</AccordionItem>

							<AccordionItem value="env-vars">
								<AccordionTrigger class="text-sm">
									<span class="flex items-center gap-2">
										<Terminal class="size-4" />
										Environment variables
									</span>
								</AccordionTrigger>
								<AccordionContent>
									<div class="space-y-1.5 font-mono text-xs">
										{#each ['ENABLED', 'ISSUER_URI', 'CLIENT_ID', 'CLIENT_SECRET', 'REDIRECT_URL', 'SCOPES', 'ROLE_CLAIM'] as suffix (suffix)}
											<p>
												<code class="rounded bg-muted px-1.5 py-0.5">
													DISCOPANEL_AUTH_OIDC_{suffix}
												</code>
											</p>
										{/each}
									</div>
								</AccordionContent>
							</AccordionItem>
						</Accordion>

						<p class="text-xs text-muted-foreground">
							Provider examples are available in the <a
								href="https://docs.discopanel.app/introduction/"
								target="_blank"
								rel="noopener noreferrer"
								class="underline hover:text-foreground">DiscoPanel docs</a
							>.
						</p>
					</div>
				{/if}
			</div>
		</section>
	</div>
{/if}
