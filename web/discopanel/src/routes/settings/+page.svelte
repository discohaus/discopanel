<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import DefaultProperties from '$lib/components/default-properties.svelte';
	import UserSettings from '$lib/components/user-settings.svelte';
	import RoleSettings from '$lib/components/role-settings.svelte';
	import NetworkTopology from '$lib/components/network/network-topology.svelte';
	import AuthSettings from '$lib/components/auth-settings.svelte';
	import SupportSettings from '$lib/components/support-settings.svelte';
	import LogsSettings from '$lib/components/logs-settings.svelte';
	import { PageHeader, EmptyState, TabRail } from '$lib/components/app';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { registerRefresh } from '$lib/stores/refresh';
	import { notify } from '$lib/stores/activity.svelte';
	import { Settings } from '@lucide/svelte';
	import type { PropertyCategory } from '$lib/proto/discopanel/v1/properties_pb';
	import { rpcClient } from '$lib/api/rpc-client';
	import {
		authStore,
		canReadSettings,
		canReadUsers,
		canReadRoles,
		authEnabled
	} from '$lib/stores/auth';

	const TABS = [
		{
			key: 'server-defaults',
			label: 'Server defaults',
			desc: 'Default properties applied to newly created servers'
		},
		{
			key: 'network',
			label: 'Network',
			desc: 'Live map of listeners, routes, and everything they reach'
		},
		{ key: 'auth', label: 'Auth', desc: 'Login methods, registration, and single sign-on' },
		{ key: 'logs', label: 'Logs', desc: 'Live DiscoPanel application logs' },
		{ key: 'support', label: 'Support', desc: 'Diagnostic bundles for troubleshooting' },
		{ key: 'users', label: 'Users', desc: 'Accounts, roles, and registration invites' },
		{ key: 'roles', label: 'Roles', desc: 'Permission sets assignable to users' }
	] as const;

	let globalConfig = $state<PropertyCategory[]>([]);
	let loading = $state(true);
	let saving = $state(false);
	let tabPane = $state<HTMLDivElement | null>(null);

	let showSettings = $derived($canReadSettings);
	let showUsers = $derived($canReadUsers && $authEnabled);
	let showRoles = $derived($canReadRoles && $authEnabled);

	let visibleTabs = $derived(
		TABS.filter((t) => {
			if (t.key === 'users') return showUsers;
			if (t.key === 'roles') return showRoles;
			return showSettings;
		})
	);

	let defaultTab = $derived(
		showSettings ? 'server-defaults' : showUsers ? 'users' : showRoles ? 'roles' : ''
	);

	let activeTab = $derived.by(() => {
		// Old routing bookmarks land on the network tab
		const raw = page.url.searchParams.get('tab');
		const requested = raw === 'routing' ? 'network' : raw;
		if (requested && visibleTabs.some((t) => t.key === requested)) return requested;
		return defaultTab;
	});

	// Fresh tab always opens scrolled to the top
	$effect(() => {
		void activeTab;
		tabPane?.scrollTo({ top: 0 });
	});

	function setTab(tab: string | undefined) {
		if (!tab || tab === activeTab) return;
		const base = resolve('/settings');
		const target = tab === defaultTab ? base : `${base}?tab=${tab}`;
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- base is resolved, only query varies
		goto(target, { noScroll: true, keepFocus: true });
	}

	async function loadGlobalSettings(silent = false) {
		if (!silent) loading = true;
		try {
			const response = await rpcClient.properties.getGlobalSettings({});
			globalConfig = response.categories;
		} catch (error) {
			notify.error('Failed to load global settings');
			console.error(error);
		} finally {
			loading = false;
		}
	}

	async function saveGlobalSettings(updates: Record<string, string>) {
		saving = true;
		try {
			const response = await rpcClient.properties.updateGlobalSettings({
				updates
			});

			globalConfig = response.categories;
			notify.success('Global settings saved successfully');
		} catch (error) {
			notify.error('Failed to save global settings');
			console.error(error);
		} finally {
			saving = false;
		}
	}

	// Fetch once when permission is known
	let fetchedGlobal = false;
	$effect(() => {
		if (showSettings && !fetchedGlobal) {
			fetchedGlobal = true;
			loadGlobalSettings();
		}
	});

	$effect(() => {
		if (activeTab !== 'server-defaults' || !showSettings) return;
		return registerRefresh(() => loadGlobalSettings(true));
	});
</script>

<svelte:head>
	<title>Settings · DiscoPanel</title>
</svelte:head>

<div class="flex min-h-0 flex-1 flex-col">
	<TabRail tabs={visibleTabs} value={activeTab} onValueChange={setTab}>
		{#snippet header()}
			<PageHeader
				title="Settings"
				description={visibleTabs.find((t) => t.key === activeTab)?.desc ??
					'Configure DiscoPanel and default server settings'}
				class="pt-5 pb-4"
			/>
		{/snippet}
	</TabRail>

	{#if showSettings && activeTab === 'network'}
		<NetworkTopology />
	{:else if showSettings && (activeTab === 'server-defaults' || activeTab === 'logs')}
		<div class="mx-auto flex min-h-0 w-full max-w-6xl flex-1 flex-col p-4 sm:p-6 2xl:max-w-7xl">
			{#if activeTab === 'server-defaults'}
				{#if loading}
					<div class="space-y-3">
						<Skeleton class="h-10 rounded-lg" />
						<Skeleton class="h-72 rounded-lg" />
					</div>
				{:else}
					<DefaultProperties categories={globalConfig} onSave={saveGlobalSettings} {saving} />
				{/if}
			{:else}
				<LogsSettings />
			{/if}
		</div>
	{:else}
		<div bind:this={tabPane} class="min-h-0 flex-1 overflow-y-auto">
			<div class="mx-auto w-full max-w-6xl p-4 sm:p-6 2xl:max-w-7xl">
				{#if visibleTabs.length > 0}
					{#if activeTab === 'auth' && showSettings}
						<AuthSettings />
					{:else if activeTab === 'support' && showSettings}
						<SupportSettings />
					{:else if activeTab === 'users' && showUsers}
						<UserSettings />
					{:else if activeTab === 'roles' && showRoles}
						<RoleSettings />
					{/if}
				{:else if !$authStore.isLoading}
					<div class="rounded-lg border bg-card">
						<EmptyState
							icon={Settings}
							title="No settings available"
							description="You do not have permission to view any settings sections."
						/>
					</div>
				{/if}
			</div>
		</div>
	{/if}
</div>
