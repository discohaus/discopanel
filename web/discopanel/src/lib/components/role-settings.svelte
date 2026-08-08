<script lang="ts">
	import { onMount } from 'svelte';
	import { registerRefresh } from '$lib/stores/refresh';
	import { canCreateRoles, canUpdateRoles, canDeleteRoles } from '$lib/stores/auth';
	import { EmptyState, ConfirmDialog } from '$lib/components/app';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Badge } from '$lib/components/ui/badge';
	import { Switch } from '$lib/components/ui/switch';
	import { Checkbox } from '$lib/components/ui/checkbox';
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
		Plus,
		Trash2,
		Pencil,
		Loader2,
		Check,
		X,
		ShieldAlert,
		KeyRound,
		Shield,
		Target,
		Save,
		ShieldCheck
	} from '@lucide/svelte';
	import { create, clone } from '@bufbuild/protobuf';
	import { rpcClient } from '$lib/api/rpc-client';
	import type { Role, Permission } from '$lib/proto/discopanel/v1/storage_pb';
	import { PermissionSchema } from '$lib/proto/discopanel/v1/storage_pb';
	import type { ScopeableObject, ResourceActions } from '$lib/proto/discopanel/v1/role_pb';
	import {
		CreateRoleRequestSchema,
		DeleteRoleRequestSchema,
		GetPermissionMatrixRequestSchema,
		UpdatePermissionsRequestSchema
	} from '$lib/proto/discopanel/v1/role_pb';
	import {
		ResourceType,
		ResourceTypeSchema,
		ActionType,
		ActionTypeSchema
	} from '$lib/proto/discopanel/options/v1/options_pb';
	import { enumLabel } from '$lib/proto-meta';
	import { getRoleBadgeVariant } from '$lib/utils/role-colors';

	type PermSection = 'global' | 'scoped';

	let roles = $state<Role[]>([]);
	let resourceActions = $state<ResourceActions[]>([]);
	let permissionMatrix = $state<Record<string, Permission[]>>({});
	let availableObjects = $state<ScopeableObject[]>([]);
	let loading = $state(true);
	let canCreate = $derived($canCreateRoles);
	let canUpdate = $derived($canUpdateRoles);
	let canDelete = $derived($canDeleteRoles);

	let showCreateDialog = $state(false);
	let showPermissionsDialog = $state(false);
	let editingRole = $state<Role | null>(null);
	let editingPermissions = $state<Record<string, boolean>>({});
	let savingPermissions = $state(false);
	let activeSection = $state<PermSection>('global');
	let deleteTarget = $state<Role | null>(null);
	let deleteOpen = $state(false);

	// Scoped permissions state, proto rows with real object ids
	let scopedPermissions = $state<Permission[]>([]);
	let scopedResource = $state<ResourceType>(ResourceType.UNSPECIFIED);

	let newRoleForm = $state({
		name: '',
		description: '',
		isDefault: false
	});

	const navItems: { id: PermSection; label: string; icon: typeof Shield }[] = [
		{ id: 'global', label: 'Global permissions', icon: Shield },
		{ id: 'scoped', label: 'Scoped permissions', icon: Target }
	];

	let scopeableResources = $derived([...new Set(availableObjects.map((o) => o.resource))]);

	// Maps resource to the resource its objects come from
	let scopeSourceMap = $derived.by(() => {
		const map: Partial<Record<ResourceType, ResourceType>> = {};
		for (const obj of availableObjects) {
			if (obj.scopeSource && !map[obj.resource]) {
				map[obj.resource] = obj.scopeSource;
			}
		}
		return map;
	});

	let scopedResourceActions = $derived(
		scopedResource
			? (resourceActions.find((ra) => ra.resource === scopedResource)?.actions ?? [])
			: []
	);

	let filteredObjects = $derived(
		scopedResource ? availableObjects.filter((o) => o.resource === scopedResource) : []
	);

	let totalPermCount = $derived(Object.values(editingPermissions).filter(Boolean).length);

	let scopedCount = $derived(scopedPermissions.length);

	let allActions = $derived(
		[...new Set(resourceActions.flatMap((ra) => ra.actions))].sort((a, b) => a - b)
	);

	// Unspecified pair is the wildcard row
	function hasFullAccess(roleName: string): boolean {
		const perms = permissionMatrix[roleName] || [];
		return perms.some(
			(p) => p.resource === ResourceType.UNSPECIFIED && p.action === ActionType.UNSPECIFIED
		);
	}

	function formatResourceName(resource: ResourceType): string {
		return enumLabel(ResourceTypeSchema, resource);
	}

	function getResourcePermCount(resource: ResourceType): number {
		const ra = resourceActions.find((r) => r.resource === resource);
		if (!ra) return 0;
		return ra.actions.filter((act) => editingPermissions[`${resource}:${act}`]).length;
	}

	async function loadRoles() {
		loading = true;
		try {
			const matrixRequest = create(GetPermissionMatrixRequestSchema, { includeObjects: true });
			const [rolesResponse, matrixResponse] = await Promise.all([
				rpcClient.role.listRoles({}),
				rpcClient.role.getPermissionMatrix(matrixRequest)
			]);
			roles = rolesResponse.roles;
			resourceActions = matrixResponse.resourceActions;
			availableObjects = matrixResponse.availableObjects;

			const matrix: Record<string, Permission[]> = {};
			for (const [roleName, rolePerms] of Object.entries(matrixResponse.rolePermissions)) {
				matrix[roleName] = rolePerms.permissions;
			}
			permissionMatrix = matrix;
		} catch (error: unknown) {
			notify.error('Failed to load roles');
			console.error(error);
		} finally {
			loading = false;
		}
	}

	async function createRole() {
		if (!newRoleForm.name) {
			notify.error('Role name is required');
			return;
		}

		try {
			const request = create(CreateRoleRequestSchema, {
				name: newRoleForm.name,
				description: newRoleForm.description,
				isDefault: newRoleForm.isDefault
			});
			await rpcClient.role.createRole(request);

			notify.success('Role created successfully');
			showCreateDialog = false;
			newRoleForm = { name: '', description: '', isDefault: false };
			await loadRoles();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to create role');
		}
	}

	// Opens the delete confirmation dialog
	function requestDelete(role: Role) {
		if (role.isSystem) {
			notify.error('Cannot delete system roles');
			return;
		}
		deleteTarget = role;
		deleteOpen = true;
	}

	async function confirmDelete() {
		const role = deleteTarget;
		if (!role) return;

		try {
			const request = create(DeleteRoleRequestSchema, { id: role.id });
			await rpcClient.role.deleteRole(request);

			notify.success('Role deleted successfully');
			await loadRoles();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to delete role');
		}
	}

	function openPermissionsDialog(role: Role) {
		editingRole = role;
		activeSection = 'global';
		scopedResource = ResourceType.UNSPECIFIED;

		const permMap: Record<string, boolean> = {};
		const scoped: Permission[] = [];
		const rolePerms = permissionMatrix[role.name] || [];

		for (const perm of rolePerms) {
			if (perm.resource === ResourceType.UNSPECIFIED && perm.action === ActionType.UNSPECIFIED) {
				for (const ra of resourceActions) {
					for (const act of ra.actions) {
						permMap[`${ra.resource}:${act}`] = true;
					}
				}
			} else if (perm.objectId && perm.objectId !== '*') {
				scoped.push(clone(PermissionSchema, perm));
			} else {
				permMap[`${perm.resource}:${perm.action}`] = true;
			}
		}
		editingPermissions = permMap;
		scopedPermissions = scoped;
		showPermissionsDialog = true;
	}

	async function savePermissions() {
		if (!editingRole) return;
		savingPermissions = true;

		const permissions: Permission[] = [];

		for (const [key, enabled] of Object.entries(editingPermissions)) {
			if (enabled) {
				const [resource, action] = key.split(':').map(Number);
				permissions.push(create(PermissionSchema, { resource, action, objectId: '*' }));
			}
		}

		permissions.push(...scopedPermissions);

		try {
			const request = create(UpdatePermissionsRequestSchema, {
				roleName: editingRole.name,
				permissions
			});
			await rpcClient.role.updatePermissions(request);

			notify.success('Permissions updated');
			showPermissionsDialog = false;
			editingRole = null;
			await loadRoles();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to update permissions');
		} finally {
			savingPermissions = false;
		}
	}

	function togglePermission(key: string) {
		editingPermissions = {
			...editingPermissions,
			[key]: !editingPermissions[key]
		};
	}

	function toggleResourceAll(resource: ResourceType) {
		const ra = resourceActions.find((r) => r.resource === resource);
		if (!ra) return;
		const allEnabled = ra.actions.every((act) => editingPermissions[`${resource}:${act}`]);
		const updated = { ...editingPermissions };
		for (const act of ra.actions) {
			updated[`${resource}:${act}`] = !allEnabled;
		}
		editingPermissions = updated;
	}

	function isResourceAllChecked(resource: ResourceType): boolean {
		const ra = resourceActions.find((r) => r.resource === resource);
		if (!ra) return false;
		return ra.actions.every((act) => editingPermissions[`${resource}:${act}`]);
	}

	function isResourceIndeterminate(resource: ResourceType): boolean {
		const ra = resourceActions.find((r) => r.resource === resource);
		if (!ra) return false;
		const checked = ra.actions.filter((act) => editingPermissions[`${resource}:${act}`]).length;
		return checked > 0 && checked < ra.actions.length;
	}

	function isScopedChecked(resource: ResourceType, action: ActionType, objectId: string): boolean {
		return scopedPermissions.some(
			(sp) => sp.resource === resource && sp.action === action && sp.objectId === objectId
		);
	}

	function toggleScopedPermission(resource: ResourceType, action: ActionType, objectId: string) {
		const index = scopedPermissions.findIndex(
			(sp) => sp.resource === resource && sp.action === action && sp.objectId === objectId
		);
		if (index >= 0) {
			scopedPermissions = scopedPermissions.filter((_, i) => i !== index);
		} else {
			scopedPermissions = [
				...scopedPermissions,
				create(PermissionSchema, { resource, action, objectId })
			];
		}
	}

	onMount(() => {
		loadRoles();
		return registerRefresh(loadRoles);
	});
</script>

<section class="overflow-hidden rounded-xl border bg-card">
	<header class="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
		<div class="min-w-0">
			<h3 class="text-sm font-semibold">Roles</h3>
			<p class="mt-0.5 text-xs text-muted-foreground">Group permissions and assign them to users</p>
		</div>
		{#if canCreate}
			<Button size="sm" onclick={() => (showCreateDialog = true)}>
				<Plus class="size-4" />
				Create role
			</Button>
		{/if}
	</header>

	{#if loading}
		<div class="flex items-center justify-center py-16">
			<Loader2 class="size-8 animate-spin text-muted-foreground" />
		</div>
	{:else}
		<Table>
			<TableHeader>
				<TableRow>
					<TableHead>Role</TableHead>
					<TableHead>Type</TableHead>
					<TableHead>Default</TableHead>
					<TableHead>Permissions</TableHead>
					{#if canDelete}
						<TableHead class="text-right">Actions</TableHead>
					{/if}
				</TableRow>
			</TableHeader>
			<TableBody>
				{#each roles as role (role.id)}
					<TableRow class="group">
						<TableCell>
							<div class="flex items-center gap-2">
								<Badge variant={getRoleBadgeVariant(role.name)}>{role.name}</Badge>
							</div>
							{#if role.description}
								<p class="mt-1 text-xs text-muted-foreground">{role.description}</p>
							{/if}
						</TableCell>
						<TableCell>
							{#if role.isSystem}
								<Badge variant="secondary">System</Badge>
							{:else}
								<Badge variant="outline">Custom</Badge>
							{/if}
						</TableCell>
						<TableCell>
							{#if role.isDefault}
								<span
									class="inline-flex items-center gap-1 text-xs font-medium text-status-ok"
									title="Assigned to new users automatically"
								>
									<Check class="size-3.5" />
									Default
								</span>
							{:else}
								<span class="text-xs text-muted-foreground">--</span>
							{/if}
						</TableCell>
						<TableCell>
							{#if hasFullAccess(role.name)}
								<Badge
									variant="outline"
									class="gap-1 border-status-danger/25 bg-status-danger/10 text-status-danger"
								>
									<ShieldCheck class="size-3" />
									Full access
								</Badge>
							{:else if canUpdate}
								<Button
									size="sm"
									variant="outline"
									class="h-7 gap-1.5 text-xs"
									onclick={() => openPermissionsDialog(role)}
								>
									<Pencil class="size-3" />
									Edit
									<span class="tabular text-muted-foreground">
										{role.permissions?.length || 0}
									</span>
								</Button>
							{:else}
								<span class="text-sm text-muted-foreground">
									{role.permissions?.length || 0} permissions
								</span>
							{/if}
						</TableCell>
						{#if canDelete}
							<TableCell class="text-right">
								{#if !role.isSystem}
									<Button
										size="icon"
										variant="ghost"
										class="size-8 text-status-danger opacity-60 transition-opacity group-hover:opacity-100 hover:bg-status-danger/10 hover:text-status-danger"
										title="Delete role"
										onclick={() => requestDelete(role)}
									>
										<Trash2 class="size-4" />
									</Button>
								{/if}
							</TableCell>
						{/if}
					</TableRow>
				{/each}
			</TableBody>
		</Table>
	{/if}
</section>

<Dialog open={showCreateDialog} onOpenChange={(open) => (showCreateDialog = open)}>
	<DialogContent class="sm:max-w-md">
		<DialogHeader>
			<DialogTitle>Create role</DialogTitle>
			<DialogDescription>
				Custom role with its own permission set, editable after creation.
			</DialogDescription>
		</DialogHeader>

		<div class="space-y-4">
			<div class="space-y-2">
				<Label for="role-name">Role name</Label>
				<Input
					id="role-name"
					type="text"
					bind:value={newRoleForm.name}
					placeholder="e.g., moderator"
					required
				/>
			</div>
			<div class="space-y-2">
				<Label for="role-desc">Description</Label>
				<Input
					id="role-desc"
					type="text"
					bind:value={newRoleForm.description}
					placeholder="What this role is for"
				/>
			</div>
			<label
				class="flex cursor-pointer items-center justify-between gap-3 rounded-lg border px-3.5 py-3 text-sm"
			>
				<span>
					Default role
					<span class="block text-xs font-normal text-muted-foreground">
						Assigned to new users automatically
					</span>
				</span>
				<Switch
					checked={newRoleForm.isDefault}
					onCheckedChange={(checked) => (newRoleForm.isDefault = checked)}
				/>
			</label>
		</div>

		<DialogFooter>
			<Button variant="outline" onclick={() => (showCreateDialog = false)}>Cancel</Button>
			<Button onclick={createRole} disabled={!newRoleForm.name.trim()}>
				<Plus class="size-4" />
				Create role
			</Button>
		</DialogFooter>
	</DialogContent>
</Dialog>

<!-- Permission matrix needs the room, full-size dialog is deliberate -->
<Dialog open={showPermissionsDialog} onOpenChange={(open) => (showPermissionsDialog = open)}>
	<DialogContent
		class="flex h-[85vh]! w-[95vw]! max-w-6xl! flex-col gap-0! overflow-hidden p-0!"
		showCloseButton={false}
	>
		<div class="flex h-full">
			<div class="hidden w-60 flex-col border-r bg-muted/30 sm:flex">
				<div class="border-b p-4">
					<div class="flex items-center gap-2.5">
						<div class="flex size-9 items-center justify-center rounded-lg bg-primary/10">
							<KeyRound class="size-4.5 text-primary" />
						</div>
						<div class="min-w-0 flex-1">
							<h3 class="truncate text-sm font-semibold">{editingRole?.name}</h3>
							<p class="text-xs text-muted-foreground">
								{editingRole?.isSystem ? 'System role' : 'Custom role'}
							</p>
						</div>
					</div>
				</div>

				<nav class="flex-1 space-y-1 p-3">
					{#each navItems as item (item.id)}
						{@const Icon = item.icon}
						<button
							onclick={() => (activeSection = item.id)}
							class="flex w-full items-center gap-2.5 rounded-md px-3 py-2 text-left text-sm transition-colors {activeSection ===
							item.id
								? 'bg-accent font-medium text-foreground'
								: 'text-muted-foreground hover:bg-accent/50 hover:text-foreground'}"
						>
							<Icon class="size-4" />
							<span class="flex-1">{item.label}</span>
							<span class="tabular text-xs text-muted-foreground">
								{item.id === 'global' ? totalPermCount : scopedCount}
							</span>
						</button>
					{/each}
				</nav>

				<div class="border-t p-3">
					<p class="text-xs leading-relaxed text-muted-foreground">
						Global permissions apply to all objects. Scoped permissions target specific objects like
						individual servers.
					</p>
				</div>
			</div>

			<div class="flex min-w-0 flex-1 flex-col">
				<div class="flex items-center justify-between gap-3 border-b bg-muted/30 px-5 py-3.5">
					<div class="min-w-0">
						<h2 class="text-sm font-semibold">
							{#if activeSection === 'global'}Global permissions
							{:else}Scoped permissions
							{/if}
						</h2>
						<p class="mt-0.5 truncate text-xs text-muted-foreground">
							{#if activeSection === 'global'}Toggle access to resources and their actions
							{:else}Grant access to specific objects instead of all objects of a type
							{/if}
						</p>
					</div>
					<Button
						variant="ghost"
						size="icon"
						onclick={() => (showPermissionsDialog = false)}
						class="size-8 shrink-0"
					>
						<X class="size-4" />
					</Button>
				</div>

				<div class="flex-1 overflow-y-auto p-5">
					{#if activeSection === 'global'}
						<div class="overflow-x-auto rounded-lg border">
							<Table>
								<TableHeader>
									<TableRow class="bg-muted/50">
										<TableHead class="sticky left-0 z-10 w-50 border-r bg-muted/50">
											Resource
										</TableHead>
										{#each allActions as action (action)}
											<TableHead class="px-3 text-center">
												<span class="text-xs font-medium">
													{enumLabel(ActionTypeSchema, action)}
												</span>
											</TableHead>
										{/each}
									</TableRow>
								</TableHeader>
								<TableBody>
									{#each resourceActions as ra (ra.resource)}
										{@const count = getResourcePermCount(ra.resource)}
										{@const total = ra.actions.length}
										<TableRow class="hover:bg-muted/30">
											<TableCell class="sticky left-0 z-10 border-r bg-background font-medium">
												<div class="flex items-center gap-2">
													<Checkbox
														checked={isResourceAllChecked(ra.resource)}
														indeterminate={isResourceIndeterminate(ra.resource)}
														onCheckedChange={() => toggleResourceAll(ra.resource)}
													/>
													<span class="text-sm">{formatResourceName(ra.resource)}</span>
													{#if count > 0}
														<Badge variant="secondary" class="ml-auto px-1.5 py-0 text-[10px]">
															{count}/{total}
														</Badge>
													{/if}
												</div>
											</TableCell>
											{#each allActions as action (action)}
												{@const key = `${ra.resource}:${action}`}
												{@const hasAction = ra.actions.includes(action)}
												{@const checked = hasAction && (editingPermissions[key] || false)}
												<TableCell class="px-3 text-center">
													{#if hasAction}
														<div class="flex justify-center">
															<Checkbox {checked} onCheckedChange={() => togglePermission(key)} />
														</div>
													{:else}
														<span class="text-muted-foreground/20">-</span>
													{/if}
												</TableCell>
											{/each}
										</TableRow>
									{/each}
								</TableBody>
							</Table>
						</div>
					{:else if scopeableResources.length === 0}
						<EmptyState
							icon={Target}
							title="No scopeable resources"
							description="No objects exist yet to scope permissions to. Create resources like servers first."
							class="rounded-xl border border-dashed"
						/>
					{:else}
						<div class="space-y-5">
							<div class="flex flex-wrap gap-1.5">
								{#each scopeableResources as res (res)}
									{@const objectCount = availableObjects.filter((o) => o.resource === res).length}
									{@const source = scopeSourceMap[res] ?? res}
									{@const isForeign = source !== res}
									<button
										onclick={() =>
											(scopedResource =
												scopedResource === res ? ResourceType.UNSPECIFIED : res)}
										class="inline-flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-xs font-medium transition-colors {scopedResource ===
										res
											? 'border-primary/40 bg-primary/10 text-primary'
											: 'text-muted-foreground hover:bg-accent hover:text-foreground'}"
									>
										{formatResourceName(res)}
										{#if isForeign}
											<span class="opacity-60">via {formatResourceName(source)}</span>
										{/if}
										<span class="tabular opacity-75">{objectCount}</span>
									</button>
								{/each}
							</div>

							{#if scopedResource}
								{@const activeSource = scopeSourceMap[scopedResource] ?? scopedResource}
								{@const activeForeign = activeSource !== scopedResource}
								{#if filteredObjects.length === 0}
									<EmptyState
										icon={ShieldAlert}
										title="No {formatResourceName(activeForeign ? activeSource : scopedResource)}"
										description={activeForeign
											? `No ${formatResourceName(activeSource)} exist to scope ${formatResourceName(scopedResource)} permissions.`
											: `No ${formatResourceName(scopedResource)} exist yet to scope permissions to.`}
										class="rounded-xl border border-dashed"
									/>
								{:else}
									<div class="overflow-x-auto rounded-lg border">
										<Table>
											<TableHeader>
												<TableRow class="bg-muted/50">
													<TableHead class="sticky left-0 z-10 w-50 border-r bg-muted/50">
														<span class="capitalize">
															{formatResourceName(activeForeign ? activeSource : scopedResource)}
														</span>
														{#if activeForeign}
															<div
																class="text-[10px] font-normal text-muted-foreground normal-case"
															>
																scoping {formatResourceName(scopedResource)}
															</div>
														{/if}
													</TableHead>
													{#each scopedResourceActions as action (action)}
														{@const coveredByGlobal =
															editingPermissions[`${scopedResource}:${action}`] || false}
														<TableHead class="px-3 text-center">
															<span
																class="text-xs font-medium {coveredByGlobal ? 'opacity-50' : ''}"
															>
																{enumLabel(ActionTypeSchema, action)}
															</span>
															{#if coveredByGlobal}
																<div class="text-[9px] font-normal text-muted-foreground">
																	(global)
																</div>
															{/if}
														</TableHead>
													{/each}
												</TableRow>
											</TableHeader>
											<TableBody>
												{#each filteredObjects as obj (obj.id)}
													<TableRow class="hover:bg-muted/30">
														<TableCell
															class="sticky left-0 z-10 border-r bg-background font-medium"
														>
															<span class="text-sm">{obj.name}</span>
														</TableCell>
														{#each scopedResourceActions as action (action)}
															{@const coveredByGlobal =
																editingPermissions[`${scopedResource}:${action}`] || false}
															{@const checked = isScopedChecked(scopedResource, action, obj.id)}
															<TableCell class="px-3 text-center">
																<div
																	class="flex justify-center {coveredByGlobal ? 'opacity-40' : ''}"
																>
																	<Checkbox
																		checked={checked || coveredByGlobal}
																		disabled={coveredByGlobal}
																		onCheckedChange={() =>
																			toggleScopedPermission(scopedResource, action, obj.id)}
																	/>
																</div>
															</TableCell>
														{/each}
													</TableRow>
												{/each}
											</TableBody>
										</Table>
									</div>
								{/if}
							{:else}
								<EmptyState
									icon={Target}
									title="No resource selected"
									description="Select a resource type above to manage scoped permissions"
									class="rounded-xl border border-dashed"
								/>
							{/if}
						</div>
					{/if}
				</div>

				<div class="flex items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3.5">
					<Button variant="outline" size="sm" onclick={() => (showPermissionsDialog = false)}>
						Cancel
					</Button>
					<Button size="sm" onclick={savePermissions} disabled={savingPermissions}>
						{#if savingPermissions}
							<Loader2 class="size-4 animate-spin" />
							Saving...
						{:else}
							<Save class="size-4" />
							Save permissions
						{/if}
					</Button>
				</div>
			</div>
		</div>
	</DialogContent>
</Dialog>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete role {deleteTarget?.name ?? ''}?"
	description="Users assigned this role lose its permissions."
	confirmLabel="Delete role"
	destructive
	onConfirm={confirmDelete}
/>
