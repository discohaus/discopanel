<script lang="ts">
	import { onMount } from 'svelte';
	import { registerRefresh } from '$lib/stores/refresh';
	import { authStore, canCreateUsers, canUpdateUsers, canDeleteUsers } from '$lib/stores/auth';
	import { EmptyState, ConfirmDialog } from '$lib/components/app';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Badge } from '$lib/components/ui/badge';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import {
		Table,
		TableBody,
		TableCell,
		TableHead,
		TableHeader,
		TableRow
	} from '$lib/components/ui/table';
	import {
		Dialog,
		DialogContent,
		DialogDescription,
		DialogFooter,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { notify } from '$lib/stores/activity.svelte';
	import {
		Users,
		UserPlus,
		Trash2,
		Pencil,
		Loader2,
		TicketPlus,
		Copy,
		Check,
		Save
	} from '@lucide/svelte';
	import { Switch } from '$lib/components/ui/switch';
	import { create } from '@bufbuild/protobuf';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import type { User, Role, RegistrationInvite } from '$lib/proto/discopanel/v1/storage_pb';
	import { AuthProvider, AuthProviderSchema } from '$lib/proto/discopanel/v1/storage_pb';
	import { enumLabel } from '$lib/proto-meta';
	import {
		CreateUserRequestSchema,
		UpdateUserRequestSchema,
		DeleteUserRequestSchema
	} from '$lib/proto/discopanel/v1/user_pb';
	import {
		CreateInviteRequestSchema,
		DeleteInviteRequestSchema
	} from '$lib/proto/discopanel/v1/auth_pb';
	import { getRoleBadgeVariant } from '$lib/utils/role-colors';
	import { formatDate, formatDateTime } from '$lib/utils/time';
	import { copyToClipboard } from '$lib/utils/clipboard';

	let users = $state<User[]>([]);
	let availableRoles = $state<Role[]>([]);
	let loading = $state(true);
	let canCreate = $derived($canCreateUsers);
	let canUpdate = $derived($canUpdateUsers);
	let canDelete = $derived($canDeleteUsers);
	let showCreateDialog = $state(false);
	let showEditDialog = $state(false);
	let editingUser = $state<User | null>(null);
	let deleteTarget = $state<User | null>(null);
	let deleteOpen = $state(false);
	let savingUser = $state(false);

	let newUserForm = $state({
		username: '',
		email: '',
		password: '',
		roles: [] as string[]
	});

	let editUserForm = $state({
		email: '',
		roles: [] as string[],
		isActive: true
	});

	// Invite state
	let invites = $state<RegistrationInvite[]>([]);
	let invitesLoading = $state(false);
	let showCreateInviteDialog = $state(false);
	let creatingInvite = $state(false);

	let newInviteForm = $state({
		description: '',
		roles: [] as string[],
		maxUses: null as number | null,
		pin: '',
		expiresValue: null as number | null,
		expiresUnit: 'hours' as 'hours' | 'days' | 'weeks'
	});

	function getInviteStatus(invite: RegistrationInvite): 'active' | 'expired' | 'exhausted' {
		if (invite.expiresAt && new Date(Number(invite.expiresAt.seconds) * 1000) < new Date())
			return 'expired';
		if (invite.maxUses > 0 && invite.useCount >= invite.maxUses) return 'exhausted';
		return 'active';
	}

	function getStatusVariant(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
		if (status === 'active') return 'default';
		if (status === 'expired') return 'secondary';
		return 'destructive';
	}

	async function loadUsers() {
		loading = true;
		try {
			const [usersResponse, rolesResponse] = await Promise.all([
				rpcClient.user.listUsers({}),
				rpcClient.role.listRoles({})
			]);
			users = usersResponse.users;
			availableRoles = rolesResponse.roles;
		} catch (error: unknown) {
			notify.error('Failed to load users');
			console.error(error);
		} finally {
			loading = false;
		}
	}

	async function fetchRoles() {
		try {
			const resp = await rpcClient.role.listRoles({});
			availableRoles = resp.roles;
		} catch (error: unknown) {
			console.error('Failed to fetch roles:', error);
		}
	}

	async function loadInvites() {
		invitesLoading = true;
		try {
			const resp = await rpcClient.auth.listInvites({});
			invites = resp.invites;
		} catch {
			// User may not have permission
		} finally {
			invitesLoading = false;
		}
	}

	async function createUser() {
		if (!newUserForm.username || !newUserForm.password) {
			notify.error('Username and password are required');
			return;
		}

		if (newUserForm.password.length < 8) {
			notify.error('Password must be at least 8 characters');
			return;
		}

		savingUser = true;
		try {
			const request = create(CreateUserRequestSchema, {
				username: newUserForm.username,
				email: newUserForm.email,
				password: newUserForm.password,
				roles: newUserForm.roles
			});
			await rpcClient.user.createUser(request);

			notify.success('User created successfully');
			showCreateDialog = false;
			newUserForm = {
				username: '',
				email: '',
				password: '',
				roles: []
			};
			await loadUsers();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to create user');
		} finally {
			savingUser = false;
		}
	}

	async function updateUser() {
		if (!editingUser) return;

		savingUser = true;
		try {
			const request = create(UpdateUserRequestSchema, {
				id: editingUser.id,
				email: editUserForm.email || undefined,
				roles: editUserForm.roles,
				isActive: editUserForm.isActive
			});
			await rpcClient.user.updateUser(request);

			notify.success('User updated successfully');
			showEditDialog = false;
			editingUser = null;
			await loadUsers();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to update user');
		} finally {
			savingUser = false;
		}
	}

	// Opens the delete confirmation dialog
	function requestDelete(user: User) {
		deleteTarget = user;
		deleteOpen = true;
	}

	async function confirmDelete() {
		const user = deleteTarget;
		if (!user) return;

		try {
			const request = create(DeleteUserRequestSchema, { id: user.id });
			await rpcClient.user.deleteUser(request);

			notify.success('User deleted successfully');
			await loadUsers();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to delete user');
		}
	}

	async function createInvite() {
		creatingInvite = true;
		let inviteUrl = '';
		try {
			const unitMultipliers = { hours: 1, days: 24, weeks: 168 };
			const expiresInHours = newInviteForm.expiresValue
				? newInviteForm.expiresValue * unitMultipliers[newInviteForm.expiresUnit]
				: undefined;
			const req = create(CreateInviteRequestSchema, {
				description: newInviteForm.description,
				roles: newInviteForm.roles,
				maxUses: newInviteForm.maxUses || 0,
				pin: newInviteForm.pin || undefined,
				expiresInHours
			});
			const resp = await rpcClient.auth.createInvite(req, silentCallOptions);
			if (resp.invite) {
				invites = [resp.invite, ...invites];
				inviteUrl = `${window.location.origin}/login?invite=${resp.invite.code}`;
			}
			showCreateInviteDialog = false;
			newInviteForm = {
				description: '',
				roles: [],
				maxUses: null,
				pin: '',
				expiresValue: null,
				expiresUnit: 'hours'
			};
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to create invite');
		} finally {
			creatingInvite = false;
		}
		if (!inviteUrl) return;
		// Invite exists either way, copying is only a bonus
		const copied = await copyToClipboard(inviteUrl);
		notify.success(copied ? 'Invite created and URL copied' : 'Invite created, copy this link', {
			description: inviteUrl,
			duration: 15000
		});
	}

	async function copyInviteUrl(code: string) {
		const url = `${window.location.origin}/login?invite=${code}`;
		const copied = await copyToClipboard(url);
		if (copied) {
			notify.success('Invite URL copied to clipboard');
		} else {
			notify.info('Copy this invite link', { description: url, duration: 15000 });
		}
	}

	async function deleteInvite(id: string) {
		try {
			await rpcClient.auth.deleteInvite(create(DeleteInviteRequestSchema, { id }), silentCallOptions);
			invites = invites.filter((i) => i.id !== id);
			notify.success('Invite revoked');
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to revoke invite');
		}
	}

	function openEditDialog(user: User) {
		fetchRoles();
		editingUser = user;
		editUserForm = {
			email: user.email || '',
			roles: [...(user.roles || [])],
			isActive: user.isActive
		};
		showEditDialog = true;
	}

	function toggleRole(form: { roles: string[] }, roleName: string) {
		const idx = form.roles.indexOf(roleName);
		if (idx >= 0) {
			form.roles = form.roles.filter((r) => r !== roleName);
		} else {
			form.roles = [...form.roles, roleName];
		}
	}

	function toggleInviteRole(roleName: string) {
		if (newInviteForm.roles.includes(roleName)) {
			newInviteForm.roles = newInviteForm.roles.filter((r) => r !== roleName);
		} else {
			newInviteForm.roles = [...newInviteForm.roles, roleName];
		}
	}

	let inviteRoleNames = $derived(
		availableRoles.filter((r) => r.name !== 'anonymous').map((r) => r.name)
	);

	onMount(() => {
		loadUsers();
		loadInvites();
		return registerRefresh(() => Promise.all([loadUsers(), loadInvites()]));
	});
</script>

{#snippet rolePicker(form: { roles: string[] }, roleNames: string[], disabled: boolean)}
	<div class="flex flex-wrap gap-1.5">
		{#each roleNames as role (role)}
			<button
				type="button"
				class="inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs font-medium transition-colors {form.roles.includes(
					role
				)
					? 'border-primary/40 bg-primary/10 text-primary'
					: 'text-muted-foreground hover:bg-accent hover:text-foreground'}"
				onclick={() => toggleRole(form, role)}
				{disabled}
			>
				{#if form.roles.includes(role)}
					<Check class="size-3" />
				{/if}
				{role}
			</button>
		{/each}
	</div>
{/snippet}

<div class="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_22rem]">
	<section class="overflow-hidden rounded-xl border bg-card">
		<header
			class="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3"
		>
			<div class="min-w-0">
				<h3 class="text-sm font-semibold">Users</h3>
				<p class="mt-0.5 text-xs text-muted-foreground">Accounts and role assignments</p>
			</div>
			{#if canCreate}
				<Button
					size="sm"
					onclick={() => {
						fetchRoles();
						showCreateDialog = true;
					}}
				>
					<UserPlus class="size-4" />
					Add user
				</Button>
			{/if}
		</header>

		{#if loading}
			<div class="flex items-center justify-center py-16">
				<Loader2 class="size-8 animate-spin text-muted-foreground" />
			</div>
		{:else if users.length === 0}
			<EmptyState icon={Users} title="No users found" />
		{:else}
			<Table>
				<TableHeader>
					<TableRow>
						<TableHead>User</TableHead>
						<TableHead>Roles</TableHead>
						<TableHead>Status</TableHead>
						<TableHead>Created</TableHead>
						{#if canUpdate || canDelete}
							<TableHead class="text-right">Actions</TableHead>
						{/if}
					</TableRow>
				</TableHeader>
				<TableBody>
					{#each users as user (user.id)}
						{@const isSelf = user.id === $authStore.user?.id}
						<TableRow class="group">
							<TableCell>
								<div class="flex items-center gap-2.5">
									<span
										class="flex size-7 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-semibold uppercase"
									>
										{user.username.slice(0, 2)}
									</span>
									<div class="min-w-0">
										<p class="flex items-center gap-1.5 truncate text-sm font-medium">
											{user.username}
											{#if isSelf}
												<span class="text-[10px] font-normal text-muted-foreground">(you)</span>
											{/if}
											{#if user.authProvider && user.authProvider !== AuthProvider.LOCAL}
												<Badge variant="outline" class="text-[10px]">
													{enumLabel(AuthProviderSchema, user.authProvider)}
												</Badge>
											{/if}
										</p>
										{#if user.email}
											<p class="truncate text-xs text-muted-foreground">{user.email}</p>
										{/if}
									</div>
								</div>
							</TableCell>
							<TableCell>
								<div class="flex flex-wrap gap-1">
									{#each user.roles || [] as role (role)}
										<Badge variant={getRoleBadgeVariant(role)}>
											{role}
										</Badge>
									{/each}
									{#if !user.roles?.length}
										<span class="text-sm text-muted-foreground">None</span>
									{/if}
								</div>
							</TableCell>
							<TableCell>
								{#if user.isActive}
									<Badge
										variant="outline"
										class="border-status-ok/25 bg-status-ok/10 text-status-ok"
									>
										Active
									</Badge>
								{:else}
									<Badge
										variant="outline"
										class="border-status-danger/25 bg-status-danger/10 text-status-danger"
									>
										Inactive
									</Badge>
								{/if}
							</TableCell>
							<TableCell class="text-sm whitespace-nowrap text-muted-foreground">
								{user.createdAt ? formatDateTime(user.createdAt) : 'Unknown'}
							</TableCell>
							{#if canUpdate || canDelete}
								<TableCell class="text-right">
									<div
										class="flex justify-end gap-0.5 opacity-60 transition-opacity group-hover:opacity-100"
									>
										{#if canUpdate}
											<Button
												size="icon"
												variant="ghost"
												class="size-8"
												title="Edit user"
												onclick={() => openEditDialog(user)}
											>
												<Pencil class="size-4" />
											</Button>
										{/if}
										{#if canDelete}
											<Button
												size="icon"
												variant="ghost"
												class="size-8 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
												title="Delete user"
												onclick={() => requestDelete(user)}
												disabled={isSelf}
											>
												<Trash2 class="size-4" />
											</Button>
										{/if}
									</div>
								</TableCell>
							{/if}
						</TableRow>
					{/each}
				</TableBody>
			</Table>
		{/if}
	</section>

	<section class="overflow-hidden rounded-xl border bg-card">
		<header
			class="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3"
		>
			<div class="min-w-0">
				<h3 class="text-sm font-semibold">Invite links</h3>
				<p class="mt-0.5 text-xs text-muted-foreground">Controlled registration via URLs</p>
			</div>
			{#if canCreate}
				<Button
					size="sm"
					variant="outline"
					onclick={() => {
						fetchRoles();
						showCreateInviteDialog = true;
					}}
				>
					<TicketPlus class="size-4" />
					New invite
				</Button>
			{/if}
		</header>

		{#if invitesLoading}
			<div class="flex items-center justify-center py-8">
				<Loader2 class="size-6 animate-spin text-muted-foreground" />
			</div>
		{:else if invites.length === 0}
			<EmptyState
				icon={TicketPlus}
				title="No invite links yet"
				description="Create one to allow controlled registration."
			/>
		{:else}
			<div class="divide-y">
				{#each invites as invite (invite.id)}
					{@const status = getInviteStatus(invite)}
					<div class="group px-4 py-3">
						<div class="flex items-center justify-between gap-2">
							<div class="flex min-w-0 items-center gap-2">
								<span class="truncate text-sm font-medium">{invite.description || 'Untitled'}</span>
								<Badge variant={getStatusVariant(status)} class="shrink-0 text-[10px]">
									{status}
								</Badge>
								{#if invite.hasPin}
									<Badge variant="outline" class="shrink-0 text-[10px]">PIN</Badge>
								{/if}
							</div>
							<div
								class="flex shrink-0 items-center gap-0.5 opacity-60 transition-opacity group-hover:opacity-100"
							>
								<Button
									variant="ghost"
									size="icon"
									class="size-7"
									onclick={() => copyInviteUrl(invite.code)}
									title="Copy invite URL"
								>
									<Copy class="size-3.5" />
								</Button>
								{#if canDelete}
									<Button
										variant="ghost"
										size="icon"
										class="size-7 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
										onclick={() => deleteInvite(invite.id)}
										title="Revoke invite"
									>
										<Trash2 class="size-3.5" />
									</Button>
								{/if}
							</div>
						</div>
						<div
							class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-muted-foreground"
						>
							<span class="tabular">
								{invite.useCount}{invite.maxUses > 0 ? `/${invite.maxUses}` : '/∞'} uses
							</span>
							{#if invite.roles && invite.roles.length > 0}
								<span class="text-muted-foreground/50">·</span>
								<span class="truncate">{invite.roles.join(', ')}</span>
							{/if}
							{#if invite.expiresAt}
								<span class="text-muted-foreground/50">·</span>
								<span>
									expires {formatDate(invite.expiresAt)}
								</span>
							{/if}
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</section>
</div>

<Dialog open={showCreateDialog} onOpenChange={(open) => (showCreateDialog = open)}>
	<DialogContent class="sm:max-w-md">
		<DialogHeader>
			<DialogTitle>Create user</DialogTitle>
			<DialogDescription>
				Local account with password authentication. Roles control what the user can access.
			</DialogDescription>
		</DialogHeader>

		<div class="space-y-4">
			<div class="space-y-2">
				<Label for="new-username">Username</Label>
				<Input
					id="new-username"
					type="text"
					bind:value={newUserForm.username}
					placeholder="username"
					required
				/>
			</div>
			<div class="space-y-2">
				<Label for="new-email">
					Email <span class="text-xs font-normal text-muted-foreground">(optional)</span>
				</Label>
				<Input
					id="new-email"
					type="email"
					bind:value={newUserForm.email}
					placeholder="user@example.com"
				/>
			</div>
			<div class="space-y-2">
				<Label for="new-password">Password</Label>
				<Input
					id="new-password"
					type="password"
					bind:value={newUserForm.password}
					placeholder="Minimum 8 characters"
					required
				/>
				<p class="text-xs text-muted-foreground">The user can change their password later</p>
			</div>
			<div class="space-y-2">
				<Label>Roles</Label>
				{@render rolePicker(
					newUserForm,
					availableRoles.map((r) => r.name),
					savingUser
				)}
			</div>
		</div>

		<DialogFooter>
			<Button variant="outline" onclick={() => (showCreateDialog = false)} disabled={savingUser}>
				Cancel
			</Button>
			<Button
				onclick={createUser}
				disabled={savingUser || !newUserForm.username || !newUserForm.password}
			>
				{#if savingUser}
					<Loader2 class="size-4 animate-spin" />
				{:else}
					<UserPlus class="size-4" />
				{/if}
				Create user
			</Button>
		</DialogFooter>
	</DialogContent>
</Dialog>

<Dialog open={showEditDialog} onOpenChange={(open) => (showEditDialog = open)}>
	<DialogContent class="sm:max-w-md">
		<DialogHeader>
			<DialogTitle>Edit {editingUser?.username ?? 'user'}</DialogTitle>
			<DialogDescription>
				{editingUser?.authProvider && editingUser.authProvider !== AuthProvider.LOCAL
					? `Managed by ${enumLabel(AuthProviderSchema, editingUser.authProvider)}. `
					: ''}Role and status changes take effect immediately.
			</DialogDescription>
		</DialogHeader>

		{#if editingUser}
			<div class="space-y-4">
				<div class="space-y-2">
					<Label for="edit-email">Email</Label>
					<Input
						id="edit-email"
						type="email"
						bind:value={editUserForm.email}
						placeholder="user@example.com"
					/>
				</div>
				<div class="space-y-2">
					<Label>Roles</Label>
					{@render rolePicker(
						editUserForm,
						availableRoles.map((r) => r.name),
						savingUser
					)}
				</div>
				<label
					class="flex cursor-pointer items-center justify-between gap-3 rounded-lg border px-3.5 py-3 text-sm"
				>
					<span>
						Account active
						<span class="block text-xs font-normal text-muted-foreground">
							Inactive accounts cannot log in
						</span>
					</span>
					<Switch
						checked={editUserForm.isActive}
						onCheckedChange={(checked) => (editUserForm.isActive = checked)}
					/>
				</label>
			</div>
		{/if}

		<DialogFooter>
			<Button variant="outline" onclick={() => (showEditDialog = false)} disabled={savingUser}>
				Cancel
			</Button>
			<Button onclick={updateUser} disabled={savingUser}>
				{#if savingUser}
					<Loader2 class="size-4 animate-spin" />
				{:else}
					<Save class="size-4" />
				{/if}
				Save changes
			</Button>
		</DialogFooter>
	</DialogContent>
</Dialog>

<Dialog open={showCreateInviteDialog} onOpenChange={(open) => (showCreateInviteDialog = open)}>
	<DialogContent class="sm:max-w-md">
		<DialogHeader>
			<DialogTitle>Create invite</DialogTitle>
			<DialogDescription>
				Generates a registration URL and copies it to your clipboard. Selected roles apply
				automatically on signup.
			</DialogDescription>
		</DialogHeader>

		<div class="space-y-4">
			<div class="space-y-2">
				<Label for="invite-desc">Description</Label>
				<Input
					id="invite-desc"
					bind:value={newInviteForm.description}
					placeholder="e.g. For server admins"
					disabled={creatingInvite}
				/>
			</div>

			<div class="space-y-2">
				<Label>Roles</Label>
				<div class="flex flex-wrap gap-1.5">
					{#each inviteRoleNames as role (role)}
						<button
							type="button"
							class="inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs font-medium transition-colors {newInviteForm.roles.includes(
								role
							)
								? 'border-primary/40 bg-primary/10 text-primary'
								: 'text-muted-foreground hover:bg-accent hover:text-foreground'}"
							onclick={() => toggleInviteRole(role)}
							disabled={creatingInvite}
						>
							{#if newInviteForm.roles.includes(role)}
								<Check class="size-3" />
							{/if}
							{role}
						</button>
					{/each}
				</div>
				<p class="text-xs text-muted-foreground">If none selected, default roles are used</p>
			</div>

			<div class="grid grid-cols-2 gap-4">
				<div class="space-y-2">
					<Label for="invite-max-uses">Max uses</Label>
					<Input
						id="invite-max-uses"
						type="number"
						min="1"
						bind:value={newInviteForm.maxUses}
						placeholder="Unlimited"
						disabled={creatingInvite}
					/>
				</div>
				<div class="space-y-2">
					<Label>Expiration</Label>
					<div class="flex gap-2">
						<Input
							type="number"
							min="1"
							class="min-w-0 flex-1"
							bind:value={newInviteForm.expiresValue}
							placeholder="Never"
							disabled={creatingInvite}
						/>
						<Select
							value={newInviteForm.expiresUnit}
							type="single"
							onValueChange={(v) => {
								if (v) newInviteForm.expiresUnit = v as 'hours' | 'days' | 'weeks';
							}}
							disabled={creatingInvite || !newInviteForm.expiresValue}
						>
							<SelectTrigger class="h-9 w-24">
								<span class="capitalize">{newInviteForm.expiresUnit}</span>
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="hours">Hours</SelectItem>
								<SelectItem value="days">Days</SelectItem>
								<SelectItem value="weeks">Weeks</SelectItem>
							</SelectContent>
						</Select>
					</div>
				</div>
			</div>

			<div class="space-y-2">
				<Label for="invite-pin">
					PIN protection <span class="text-xs font-normal text-muted-foreground">(optional)</span>
				</Label>
				<Input
					id="invite-pin"
					type="password"
					bind:value={newInviteForm.pin}
					placeholder="Users must enter this PIN to register"
					disabled={creatingInvite}
				/>
			</div>
		</div>

		<DialogFooter>
			<Button
				variant="outline"
				onclick={() => (showCreateInviteDialog = false)}
				disabled={creatingInvite}
			>
				Cancel
			</Button>
			<Button onclick={createInvite} disabled={creatingInvite}>
				{#if creatingInvite}
					<Loader2 class="size-4 animate-spin" />
					Creating...
				{:else}
					<TicketPlus class="size-4" />
					Create & copy URL
				{/if}
			</Button>
		</DialogFooter>
	</DialogContent>
</Dialog>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete user {deleteTarget?.username ?? ''}?"
	description="The account is removed permanently and cannot log in again."
	confirmLabel="Delete user"
	destructive
	onConfirm={confirmDelete}
/>
