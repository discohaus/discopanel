<script lang="ts">
	import { authStore, currentUser } from '$lib/stores/auth';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Badge } from '$lib/components/ui/badge';
	import {
		Dialog,
		DialogContent,
		DialogDescription,
		DialogFooter,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import {
		Table,
		TableBody,
		TableCell,
		TableHead,
		TableHeader,
		TableRow
	} from '$lib/components/ui/table';
	import { Alert, AlertDescription } from '$lib/components/ui/alert';
	import { PageHeader, SectionCard, EmptyState, ConfirmDialog } from '$lib/components/app';
	import { notify } from '$lib/stores/activity.svelte';
	import {
		Key,
		Loader2,
		Plus,
		Trash2,
		Copy,
		Check,
		AlertTriangle,
		KeyRound,
		User as UserIcon,
		LogIn
	} from '@lucide/svelte';
	import { getRoleBadgeVariant } from '$lib/utils/role-colors';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { registerRefresh } from '$lib/stores/refresh';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { formatDate, formatDateTime, timestampToDate } from '$lib/utils/time';
	import { TONE_BADGE } from '$lib/server-status';
	import type { Timestamp } from '@bufbuild/protobuf/wkt';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { onMount } from 'svelte';
	import type { ApiToken } from '$lib/proto/discopanel/v1/storage_pb';
	import { AuthProvider, AuthProviderSchema } from '$lib/proto/discopanel/v1/storage_pb';
	import { enumLabel } from '$lib/proto-meta';

	let user = $derived($currentUser);
	let passwordForm = $state({
		oldPassword: '',
		newPassword: '',
		confirmPassword: ''
	});
	let saving = $state(false);

	// API token state
	let apiTokens = $state<ApiToken[]>([]);
	let loadingTokens = $state(false);
	let showCreateTokenDialog = $state(false);
	let creatingToken = $state(false);
	let newTokenForm = $state({ name: '', expiresInDays: '' as string });
	let createdToken = $state<string | null>(null);
	let copied = $state(false);
	let deleteTokenTarget = $state<ApiToken | null>(null);
	let deleteTokenOpen = $state(false);

	let initials = $derived(
		user?.username
			? user.username
					.split(/[\s_-]+/)
					.slice(0, 2)
					.map((w) => w[0]?.toUpperCase() ?? '')
					.join('')
			: '?'
	);

	let primaryRole = $derived(user?.roles?.[0] ?? 'user');
	let providerLabel = $derived(
		enumLabel(AuthProviderSchema, user?.authProvider || AuthProvider.LOCAL).toUpperCase()
	);

	onMount(() => {
		loadTokens();
		return registerRefresh(loadTokens);
	});

	async function loadTokens() {
		loadingTokens = true;
		try {
			const resp = await rpcClient.auth.listAPITokens({}, silentCallOptions);
			apiTokens = resp.apiTokens;
		} catch {
			// Anonymous users simply have no tokens
		} finally {
			loadingTokens = false;
		}
	}

	async function createToken() {
		if (!newTokenForm.name.trim()) {
			notify.error('Token name is required');
			return;
		}

		creatingToken = true;
		try {
			const days = newTokenForm.expiresInDays ? parseInt(newTokenForm.expiresInDays) : undefined;
			const resp = await rpcClient.auth.createAPIToken({
				name: newTokenForm.name.trim(),
				expiresInDays: days
			});
			createdToken = resp.plaintextToken;
			notify.success('API token created');
			await loadTokens();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to create API token');
		} finally {
			creatingToken = false;
		}
	}

	function requestDeleteToken(token: ApiToken) {
		deleteTokenTarget = token;
		deleteTokenOpen = true;
	}

	async function confirmDeleteToken() {
		if (!deleteTokenTarget) return;
		try {
			await rpcClient.auth.deleteAPIToken({ id: deleteTokenTarget.id });
			notify.success('API token deleted');
			await loadTokens();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to delete API token');
		} finally {
			deleteTokenTarget = null;
		}
	}

	async function copyToken() {
		if (!createdToken) return;
		const ok = await copyToClipboard(createdToken);
		if (ok) {
			copied = true;
			notify.success('Token copied to clipboard');
			setTimeout(() => {
				copied = false;
			}, 2000);
		} else {
			notify.error('Failed to copy token');
		}
	}

	function closeCreateDialog() {
		showCreateTokenDialog = false;
		createdToken = null;
		copied = false;
		newTokenForm = { name: '', expiresInDays: '' };
	}

	function isExpired(ts: Timestamp | undefined): boolean {
		const date = timestampToDate(ts);
		return date ? date < new Date() : false;
	}

	async function changePassword() {
		if (!passwordForm.oldPassword || !passwordForm.newPassword) {
			notify.error('Please fill in all fields');
			return;
		}

		if (passwordForm.newPassword !== passwordForm.confirmPassword) {
			notify.error('New passwords do not match');
			return;
		}

		if (passwordForm.newPassword.length < 8) {
			notify.error('New password must be at least 8 characters');
			return;
		}

		saving = true;
		try {
			await authStore.changePassword(passwordForm.oldPassword, passwordForm.newPassword);
			notify.success('Password changed successfully');
			passwordForm = {
				oldPassword: '',
				newPassword: '',
				confirmPassword: ''
			};
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to change password');
		} finally {
			saving = false;
		}
	}
</script>

<svelte:head>
	<title>Profile · DiscoPanel</title>
</svelte:head>

<div class="mx-auto w-full max-w-4xl space-y-5 p-4 sm:p-6">
	{#if user}
		<div class="flex items-center gap-4">
			<div
				class="flex size-12 shrink-0 items-center justify-center rounded-xl bg-primary/15 text-lg font-bold text-primary"
			>
				{initials}
			</div>
			<PageHeader title={user.username} description="Your account, security, and API access">
				<Badge variant={getRoleBadgeVariant(primaryRole)}>{primaryRole}</Badge>
			</PageHeader>
		</div>

		<div class="grid gap-5 md:grid-cols-2">
			<SectionCard title="Account" description="Details and roles">
				<dl class="space-y-3 text-sm">
					<div class="flex items-center justify-between gap-3">
						<dt class="text-muted-foreground">Username</dt>
						<dd class="font-medium">{user.username}</dd>
					</div>
					{#if user.email}
						<div class="flex items-center justify-between gap-3">
							<dt class="text-muted-foreground">Email</dt>
							<dd class="truncate font-medium">{user.email}</dd>
						</div>
					{/if}
					<div class="flex items-center justify-between gap-3">
						<dt class="text-muted-foreground">Sign-in provider</dt>
						<dd><Badge variant="outline" class="text-xs">{providerLabel}</Badge></dd>
					</div>
					<div class="flex items-start justify-between gap-3">
						<dt class="text-muted-foreground">Roles</dt>
						<dd class="flex flex-wrap justify-end gap-1.5">
							{#each user.roles || [] as role (role)}
								<Badge variant={getRoleBadgeVariant(role)} class="text-xs">{role}</Badge>
							{/each}
							{#if !user.roles?.length}
								<span class="text-xs text-muted-foreground">No roles assigned</span>
							{/if}
						</dd>
					</div>
					<div class="flex items-center justify-between gap-3">
						<dt class="text-muted-foreground">Member since</dt>
						<dd class="font-medium">{formatDate(user.createdAt)}</dd>
					</div>
					{#if user.lastLogin}
						<div class="flex items-center justify-between gap-3">
							<dt class="text-muted-foreground">Last active</dt>
							<dd class="font-medium">{formatDateTime(user.lastLogin)}</dd>
						</div>
					{/if}
					<div class="flex items-center justify-between gap-3">
						<dt class="text-muted-foreground">Status</dt>
						<dd>
							<Badge
								variant="outline"
								class="text-xs {user.isActive ? TONE_BADGE.ok : TONE_BADGE.danger}"
							>
								{user.isActive ? 'Active' : 'Inactive'}
							</Badge>
						</dd>
					</div>
				</dl>
			</SectionCard>

			<SectionCard title="Security" description="Password and sign-in">
				{#if user.authProvider === AuthProvider.LOCAL || !user.authProvider}
					<form
						onsubmit={(e) => {
							e.preventDefault();
							changePassword();
						}}
						class="space-y-3"
					>
						<div class="space-y-1.5">
							<Label for="old-password">Current password</Label>
							<Input
								id="old-password"
								type="password"
								bind:value={passwordForm.oldPassword}
								required
								disabled={saving}
							/>
						</div>

						<div class="space-y-1.5">
							<Label for="new-password">New password</Label>
							<Input
								id="new-password"
								type="password"
								bind:value={passwordForm.newPassword}
								required
								disabled={saving}
								placeholder="Minimum 8 characters"
							/>
						</div>

						<div class="space-y-1.5">
							<Label for="confirm-password">Confirm new password</Label>
							<Input
								id="confirm-password"
								type="password"
								bind:value={passwordForm.confirmPassword}
								required
								disabled={saving}
							/>
						</div>

						<Button type="submit" disabled={saving} class="w-full">
							{#if saving}
								<Loader2 class="size-4 animate-spin" />
								Changing password...
							{:else}
								<Key class="size-4" />
								Change password
							{/if}
						</Button>
					</form>
				{:else}
					<div class="rounded-lg border border-dashed p-4 text-center">
						<Key class="mx-auto mb-2 size-6 text-muted-foreground" />
						<p class="text-sm text-muted-foreground">
							Your account uses <span class="font-medium">{providerLabel}</span> authentication. Password
							changes are managed by your identity provider.
						</p>
					</div>
				{/if}
			</SectionCard>
		</div>

		<SectionCard
			title="API tokens"
			description="Programmatic access tokens that inherit your identity and permissions"
		>
			{#snippet action()}
				<Button onclick={() => (showCreateTokenDialog = true)} size="sm">
					<Plus class="size-4" />
					Create token
				</Button>
			{/snippet}

			{#if loadingTokens}
				<div class="flex items-center justify-center py-8">
					<Loader2 class="size-6 animate-spin text-muted-foreground" />
				</div>
			{:else if apiTokens.length === 0}
				<EmptyState
					icon={KeyRound}
					title="No API tokens"
					description="Create a token to authenticate with the DiscoPanel API from scripts and tools."
				/>
			{:else}
				<div class="overflow-hidden rounded-lg border">
					<Table>
						<TableHeader>
							<TableRow>
								<TableHead>Name</TableHead>
								<TableHead>Created</TableHead>
								<TableHead>Expires</TableHead>
								<TableHead>Last used</TableHead>
								<TableHead class="w-16"></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{#each apiTokens as token (token.id)}
								<TableRow>
									<TableCell class="font-medium">
										<div class="flex items-center gap-2">
											<KeyRound class="size-3.5 text-muted-foreground" />
											{token.name}
										</div>
									</TableCell>
									<TableCell class="text-sm text-muted-foreground">
										{formatDate(token.createdAt)}
									</TableCell>
									<TableCell>
										{#if token.expiresAt}
											<Badge
												variant="outline"
												class="text-xs {isExpired(token.expiresAt) ? TONE_BADGE.danger : ''}"
											>
												{isExpired(token.expiresAt) ? 'Expired' : formatDate(token.expiresAt)}
											</Badge>
										{:else}
											<span class="text-sm text-muted-foreground">Never</span>
										{/if}
									</TableCell>
									<TableCell class="text-sm text-muted-foreground">
										{token.lastUsedAt ? formatDate(token.lastUsedAt) : 'Never'}
									</TableCell>
									<TableCell>
										<Button
											variant="ghost"
											size="icon"
											class="size-8 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
											onclick={() => requestDeleteToken(token)}
											title="Delete token"
										>
											<Trash2 class="size-4" />
										</Button>
									</TableCell>
								</TableRow>
							{/each}
						</TableBody>
					</Table>
				</div>
			{/if}
		</SectionCard>
	{:else}
		<div class="rounded-lg border bg-card">
			<EmptyState
				icon={UserIcon}
				title="Not signed in"
				description="You are browsing as a guest. Sign in to manage your account and API tokens."
			>
				<Button onclick={() => goto(resolve('/login'))}>
					<LogIn class="size-4" />
					Sign in
				</Button>
			</EmptyState>
		</div>
	{/if}
</div>

<Dialog
	open={showCreateTokenDialog}
	onOpenChange={(open) => {
		if (!open) closeCreateDialog();
	}}
>
	<DialogContent class="sm:max-w-lg">
		<DialogHeader>
			<DialogTitle>{createdToken ? 'Token created' : 'Create API token'}</DialogTitle>
			<DialogDescription>
				{createdToken
					? "Copy your token now, it won't be shown again"
					: 'Tokens inherit your full identity, roles, and permissions'}
			</DialogDescription>
		</DialogHeader>

		{#if createdToken}
			<div class="space-y-4">
				<div class="relative">
					<div
						class="rounded-lg border bg-muted/40 p-3 pr-12 font-mono text-sm break-all select-all"
					>
						{createdToken}
					</div>
					<Button
						variant="ghost"
						size="icon"
						class="absolute top-1.5 right-1.5 size-8"
						onclick={copyToken}
					>
						{#if copied}
							<Check class="size-4 text-status-ok" />
						{:else}
							<Copy class="size-4" />
						{/if}
					</Button>
				</div>

				<Alert class="border-status-warn/30 bg-status-warn/5">
					<AlertTriangle class="size-4 text-status-warn" />
					<AlertDescription class="text-sm">
						This token will not be shown again. If you lose it, create a new one.
					</AlertDescription>
				</Alert>

				<div class="space-y-1.5">
					<p class="stat-label">Example usage</p>
					<pre
						class="overflow-x-auto rounded-lg border bg-terminal p-3 font-mono text-xs whitespace-pre text-terminal-foreground transition-colors duration-300">curl {window
							.location.origin}/discopanel.v1.UserService/ListUsers \
  -X POST \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer {createdToken}' \
  -d '{'{}'}'</pre>
				</div>
			</div>
		{:else}
			<div class="space-y-4">
				<div class="space-y-2">
					<Label for="token-name">Token name</Label>
					<Input
						id="token-name"
						bind:value={newTokenForm.name}
						placeholder="e.g. CI/CD pipeline, monitoring script"
						disabled={creatingToken}
					/>
				</div>

				<div class="space-y-2">
					<Label>Expiration</Label>
					<Select
						value={newTokenForm.expiresInDays || 'never'}
						type="single"
						onValueChange={(v) => {
							if (v) newTokenForm.expiresInDays = v === 'never' ? '' : v;
						}}
						disabled={creatingToken}
					>
						<SelectTrigger class="w-full">
							<span>
								{#if !newTokenForm.expiresInDays}
									No expiration
								{:else if newTokenForm.expiresInDays === '365'}
									1 year
								{:else}
									{newTokenForm.expiresInDays} days
								{/if}
							</span>
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="never">No expiration</SelectItem>
							<SelectItem value="7">7 days</SelectItem>
							<SelectItem value="30">30 days</SelectItem>
							<SelectItem value="90">90 days</SelectItem>
							<SelectItem value="365">1 year</SelectItem>
						</SelectContent>
					</Select>
					<p class="text-xs text-muted-foreground">
						{newTokenForm.expiresInDays
							? `Token expires after ${newTokenForm.expiresInDays} days.`
							: 'Token never expires. You can revoke it at any time.'}
					</p>
				</div>
			</div>
		{/if}

		<DialogFooter>
			{#if createdToken}
				<Button onclick={copyToken} variant="outline">
					{#if copied}
						<Check class="size-4" />
						Copied
					{:else}
						<Copy class="size-4" />
						Copy token
					{/if}
				</Button>
				<Button onclick={closeCreateDialog}>Done</Button>
			{:else}
				<Button variant="outline" onclick={closeCreateDialog} disabled={creatingToken}>
					Cancel
				</Button>
				<Button onclick={createToken} disabled={creatingToken || !newTokenForm.name.trim()}>
					{#if creatingToken}
						<Loader2 class="size-4 animate-spin" />
						Creating...
					{:else}
						<KeyRound class="size-4" />
						Create token
					{/if}
				</Button>
			{/if}
		</DialogFooter>
	</DialogContent>
</Dialog>

<ConfirmDialog
	bind:open={deleteTokenOpen}
	title="Delete token {deleteTokenTarget?.name ?? ''}?"
	description="Anything authenticating with this token stops working immediately."
	confirmLabel="Delete token"
	destructive
	onConfirm={confirmDeleteToken}
/>
