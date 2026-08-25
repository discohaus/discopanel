<script lang="ts">
	import '../app.css';
	import { ModeWatcher, toggleMode, mode } from 'mode-watcher';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { resolve as resolvePath } from '$app/paths';
	import {
		SidebarProvider,
		SidebarInset,
		Sidebar,
		SidebarContent,
		SidebarGroup,
		SidebarGroupLabel,
		SidebarGroupContent,
		SidebarMenu,
		SidebarMenuItem,
		SidebarMenuButton,
		SidebarHeader,
		SidebarFooter,
		SidebarTrigger
	} from '$lib/components/ui/sidebar';
	import { Button } from '$lib/components/ui/button';
	import { Avatar, AvatarFallback } from '$lib/components/ui/avatar';
	import {
		DropdownMenu,
		DropdownMenuContent,
		DropdownMenuItem,
		DropdownMenuLabel,
		DropdownMenuSeparator,
		DropdownMenuTrigger
	} from '$lib/components/ui/dropdown-menu';
	import { get } from 'svelte/store';
	import { serversStore, activitySortedServers } from '$lib/stores/servers';
	import { systemModules } from '$lib/stores/system-modules.svelte';
	import { runPageRefreshers } from '$lib/stores/refresh';
	import { authStore, currentUser, canAccessSettings, authEnabled } from '$lib/stores/auth';
	import { onMount } from 'svelte';
	import { CommandPalette, StatusDot, ServerAvatar, DiscoLogo, StatusBar } from '$lib/components/app';
	import {
		House,
		Server,
		Settings,
		Package,
		User as UserIcon,
		LogOut,
		LogIn,
		FileText,
		Sun,
		Moon,
		Puzzle,
		Search,
		ChevronRight,
		RefreshCcw
	} from '@lucide/svelte';
	import type { User } from '$lib/proto/discopanel/v1/storage_pb';

	let { children } = $props();

	let servers = $derived($activitySortedServers);
	let user = $derived($currentUser);
	let showSettingsNav = $derived($canAccessSettings);
	let loading = $state(true);
	let isAuthEnabled = $derived($authEnabled);
	let paletteOpen = $state(false);
	let refreshing = $state(false);

	// Spins long enough for the click to read
	async function refresh() {
		if (refreshing) return;
		refreshing = true;
		const spin = new Promise((r) => setTimeout(r, 600));
		try {
			systemModules.refresh();
			await Promise.all([serversStore.fetchServers(true, true), runPageRefreshers(), spin]);
		} catch (error) {
			// Interceptor already reports, just log
			console.error('Refresh failed:', error);
		} finally {
			refreshing = false;
		}
	}

	let systemRunning = $derived(systemModules.running);
	let systemTitle = $derived(
		`System modules running: ${systemRunning.map((m) => m.name).join(', ')}`
	);

	let currentServerName = $derived.by(() => {
		const match = page.url.pathname.match(/^\/servers\/([^/]+)/);
		if (!match || match[1] === 'new') return null;
		return servers.find((s) => s.id === match[1])?.name ?? null;
	});

	let crumb = $derived.by(() => {
		const path = page.url.pathname;
		if (path === '/') return { section: 'Home', detail: null };
		if (path === '/servers') return { section: 'Servers', detail: null };
		if (path === '/servers/new') return { section: 'Servers', detail: 'New server' };
		if (path.startsWith('/servers/')) return { section: 'Servers', detail: currentServerName };
		if (path.startsWith('/modpacks')) return { section: 'Modpacks', detail: null };
		if (path.startsWith('/modules')) return { section: 'Modules', detail: null };
		if (path.startsWith('/settings')) return { section: 'Settings', detail: null };
		if (path.startsWith('/profile')) return { section: 'Profile', detail: null };
		if (path.startsWith('/docs/api')) return { section: 'API reference', detail: null };
		return { section: 'DiscoPanel', detail: null };
	});

	function getUserInitials(u: User) {
		if (!u) return '';
		return u.username.slice(0, 2).toUpperCase();
	}

	function getDisplayRole(u: User): string {
		if (!u?.roles?.length) return 'No roles';
		return u.roles[0];
	}

	async function handleLogout() {
		await authStore.logout();
	}

	let statusPollingInterval: ReturnType<typeof setInterval> | null = null;

	async function bootstrap() {
		try {
			const authStatus = await authStore.checkAuthStatus();
			loading = false;
			if (authStatus.enabled) {
				if (authStatus.firstUserSetup) {
					goto(resolvePath('/login'));
					return;
				}
				// Session already validated inside checkAuthStatus
				const state = get(authStore);
				if (!state.isAuthenticated && !state.anonymousAccessEnabled) {
					goto(resolvePath('/login'));
					return;
				}
			}
		} catch (err) {
			loading = false;
			console.debug(`DiscoPanel auth bootstrap error: ${err}`);
		}
	}

	function stopPolling() {
		if (statusPollingInterval) {
			clearInterval(statusPollingInterval);
			statusPollingInterval = null;
		}
	}

	let sessionKey = $derived.by(() => {
		if (loading) return null;
		if (page.url.pathname === '/login') return null;
		const auth = $authStore;
		const enabled = auth.localAuthEnabled || auth.oidcEnabled;
		if (enabled && !auth.isAuthenticated && !auth.anonymousAccessEnabled) return null;
		return auth.token ?? 'anon';
	});

	let activeSessionKey: string | null = null;

	$effect(() => {
		const key = sessionKey;
		if (key === activeSessionKey) return;
		activeSessionKey = key;
		stopPolling();
		if (key === null) return;
		// Full first fetch seeds live statuses for the sidebar
		serversStore.fetchServers(false, true).catch((err) => {
			console.error('Failed to fetch initial servers:', err);
		});
		statusPollingInterval = setInterval(() => {
			serversStore.fetchServers(true);
		}, 10000);
	});

	// System module indicator polls only for privileged sessions
	$effect(() => {
		if (sessionKey === null || !showSettingsNav) {
			systemModules.stop();
			return;
		}
		systemModules.start();
		return () => systemModules.stop();
	});

	onMount(() => {
		bootstrap();
		return () => stopPolling();
	});
</script>

<svelte:head>
	<title>DiscoPanel</title>
</svelte:head>

<ModeWatcher />
<StatusBar />

{#if page.url.pathname === '/login'}
	{@render children?.()}
{:else if loading}
	<div class="flex min-h-screen items-center justify-center">
		<div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary"></div>
	</div>
{:else}
	<CommandPalette bind:open={paletteOpen} />
	<SidebarProvider>
		<Sidebar collapsible="icon" class="inset-y-auto top-0 bottom-7 h-auto">
			<SidebarHeader>
				<a
					href={resolvePath('/')}
					class="flex items-center gap-2 rounded-md px-2 py-1.5 transition-colors group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-1 hover:bg-sidebar-accent"
				>
					<DiscoLogo class="size-6" />
					<span
						class="truncate font-mono text-base font-bold tracking-tight group-data-[collapsible=icon]:hidden"
						>disco<span class="text-[#55aa55]">panel</span></span
					>
				</a>
			</SidebarHeader>

			<SidebarContent>
				<SidebarGroup>
					<SidebarGroupContent>
						<SidebarMenu>
							<SidebarMenuItem>
								<SidebarMenuButton isActive={page.url.pathname === '/'} tooltipContent="Home">
									{#snippet child({ props })}
										<a href={resolvePath('/')} {...props}>
											<House class="size-4" />
											<span class="group-data-[collapsible=icon]:hidden">Home</span>
										</a>
									{/snippet}
								</SidebarMenuButton>
							</SidebarMenuItem>
							<SidebarMenuItem>
								<SidebarMenuButton
									isActive={page.url.pathname.startsWith('/servers')}
									tooltipContent="Servers"
								>
									{#snippet child({ props })}
										<a href={resolvePath('/servers')} {...props}>
											<Server class="size-4" />
											<span class="group-data-[collapsible=icon]:hidden">Servers</span>
										</a>
									{/snippet}
								</SidebarMenuButton>
							</SidebarMenuItem>
							<SidebarMenuItem>
								<SidebarMenuButton
									isActive={page.url.pathname.startsWith('/modpacks')}
									tooltipContent="Modpacks"
								>
									{#snippet child({ props })}
										<a href={resolvePath('/modpacks')} {...props}>
											<Package class="size-4" />
											<span class="group-data-[collapsible=icon]:hidden">Modpacks</span>
										</a>
									{/snippet}
								</SidebarMenuButton>
							</SidebarMenuItem>
							{#if showSettingsNav}
								<SidebarMenuItem>
									<SidebarMenuButton
										isActive={page.url.pathname.startsWith('/modules')}
										tooltipContent="Modules"
									>
										{#snippet child({ props })}
											<a href={resolvePath('/modules')} {...props}>
												<Puzzle class="size-4" />
												<span class="group-data-[collapsible=icon]:hidden">Modules</span>
												{#if systemRunning.length > 0}
													<span
														class="ml-auto flex items-center gap-1.5 group-data-[collapsible=icon]:hidden"
														title={systemTitle}
													>
														<span class="tabular text-xs text-muted-foreground"
															>{systemRunning.length}</span
														>
														<span class="glow-ok size-2 rounded-full bg-status-ok"></span>
													</span>
												{/if}
											</a>
										{/snippet}
									</SidebarMenuButton>
								</SidebarMenuItem>
								<SidebarMenuItem>
									<SidebarMenuButton
										isActive={page.url.pathname === '/settings'}
										tooltipContent="Settings"
									>
										{#snippet child({ props })}
											<a href={resolvePath('/settings')} {...props}>
												<Settings class="size-4" />
												<span class="group-data-[collapsible=icon]:hidden">Settings</span>
											</a>
										{/snippet}
									</SidebarMenuButton>
								</SidebarMenuItem>
							{/if}
						</SidebarMenu>
					</SidebarGroupContent>
				</SidebarGroup>

				{#if servers.length > 0}
					<SidebarGroup>
						<SidebarGroupLabel class="group-data-[collapsible=icon]:hidden">
							Your servers
						</SidebarGroupLabel>
						<SidebarGroupContent>
							<SidebarMenu>
								{#each servers as server (server.id)}
									<SidebarMenuItem>
										<SidebarMenuButton
											isActive={page.url.pathname === `/servers/${server.id}`}
											tooltipContent={server.name}
											class="group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:p-1!"
										>
											{#snippet child({ props })}
												<a href={resolvePath(`/servers/${server.id}`)} {...props}>
													<ServerAvatar
														name={server.name}
														favicon={server.favicon}
														size="sm"
														class="size-6 group-data-[collapsible=icon]:size-6"
													/>
													<span class="truncate group-data-[collapsible=icon]:hidden"
														>{server.name}</span
													>
													<StatusDot
														status={server.status}
														class="ml-auto group-data-[collapsible=icon]:hidden"
													/>
												</a>
											{/snippet}
										</SidebarMenuButton>
									</SidebarMenuItem>
								{/each}
							</SidebarMenu>
						</SidebarGroupContent>
					</SidebarGroup>
				{/if}
			</SidebarContent>

			<SidebarFooter class="gap-1 border-t border-sidebar-border">
				{#if isAuthEnabled && user}
					<DropdownMenu>
						<DropdownMenuTrigger class="w-full">
							{#snippet child({ props })}
								<Button
									{...props}
									variant="ghost"
									class="h-auto w-full justify-start px-2 py-1.5 group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-0"
								>
									<Avatar class="size-7">
										<AvatarFallback class="text-xs">{getUserInitials(user)}</AvatarFallback>
									</Avatar>
									<div class="ml-1 min-w-0 flex-1 text-left group-data-[collapsible=icon]:hidden">
										<p class="truncate text-sm leading-tight font-medium">{user.username}</p>
										<p class="truncate text-xs leading-tight text-muted-foreground capitalize">
											{getDisplayRole(user)}
										</p>
									</div>
								</Button>
							{/snippet}
						</DropdownMenuTrigger>
						<DropdownMenuContent class="w-56" align="start" side="top">
							<DropdownMenuLabel>
								<div class="flex flex-col space-y-1">
									<p class="text-sm leading-none font-medium">{user.username}</p>
									{#if user.email}
										<p class="text-xs leading-none text-muted-foreground">{user.email}</p>
									{/if}
									<p class="text-xs leading-none text-muted-foreground capitalize">
										{user.roles?.length ? user.roles.join(', ') : 'No roles'}
									</p>
								</div>
							</DropdownMenuLabel>
							<DropdownMenuSeparator />
							<DropdownMenuItem onclick={() => goto(resolvePath('/profile'))}>
								<UserIcon class="mr-2 size-4" />
								<span>Profile</span>
							</DropdownMenuItem>
							<DropdownMenuItem onclick={() => goto(resolvePath('/docs/api'))}>
								<FileText class="mr-2 size-4" />
								<span>API reference</span>
							</DropdownMenuItem>
							<DropdownMenuSeparator />
							<DropdownMenuItem onclick={handleLogout}>
								<LogOut class="mr-2 size-4" />
								<span>Log out</span>
							</DropdownMenuItem>
						</DropdownMenuContent>
					</DropdownMenu>
				{:else if isAuthEnabled && $authStore.anonymousAccessEnabled && !user}
					<Button
						variant="ghost"
						class="w-full justify-start px-2"
						onclick={() => goto(resolvePath('/login'))}
					>
						<LogIn class="size-4" />
						<span class="group-data-[collapsible=icon]:hidden">Log in</span>
					</Button>
				{:else}
					<Button
						variant="ghost"
						class="w-full justify-start px-2 group-data-[collapsible=icon]:hidden"
						onclick={() => goto(resolvePath('/docs/api'))}
					>
						<FileText class="size-4" />
						<span>API reference</span>
					</Button>
				{/if}
				<div class="flex items-center gap-1 group-data-[collapsible=icon]:flex-col">
					<span
						class="flex-1 truncate pl-2 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden"
					>
						{__APP_VERSION__}
					</span>
					<Button
						variant="ghost"
						size="icon"
						class="size-7 text-muted-foreground"
						onclick={toggleMode}
						title="Toggle theme"
					>
						{#if mode.current === 'light'}
							<Moon class="size-4" />
						{:else}
							<Sun class="size-4" />
						{/if}
					</Button>
				</div>
			</SidebarFooter>
		</Sidebar>

		<SidebarInset class="flex h-[calc(100svh-1.75rem)] flex-col overflow-hidden">
			<div class="page-ambient pointer-events-none absolute inset-0" aria-hidden="true"></div>
			<header
				class="relative flex h-13 shrink-0 items-center gap-2 border-b bg-background/80 px-4 backdrop-blur-sm"
			>
				<SidebarTrigger class="-ml-1" />
				<div class="flex min-w-0 items-center gap-1.5 text-sm">
					<span class={crumb.detail ? 'text-muted-foreground' : 'font-medium'}>
						{crumb.section}
					</span>
					{#if crumb.detail}
						<ChevronRight class="size-3.5 shrink-0 text-muted-foreground/60" />
						<span class="truncate font-medium">{crumb.detail}</span>
					{/if}
				</div>
				<div class="ml-auto flex flex-1 items-center justify-end gap-2">
					<Button
						variant="outline"
						size="sm"
						class="h-8 w-full max-w-xl justify-start gap-2 text-muted-foreground"
						onclick={() => (paletteOpen = true)}
					>
						<Search class="size-3.5" />
						<span class="hidden sm:inline">Search</span>
						<kbd
							class="pointer-events-none ml-auto hidden rounded border bg-muted px-1.5 font-mono text-[10px] font-medium sm:inline-block"
						>
							⌘K
						</kbd>
					</Button>
					<Button
						variant="ghost"
						size="icon"
						class="size-8 shrink-0 text-muted-foreground"
						onclick={refresh}
						disabled={refreshing}
						title="Refresh"
					>
						<RefreshCcw class="size-4 {refreshing ? 'animate-spin' : ''}" />
					</Button>
				</div>
			</header>
			<main class="relative flex min-h-0 flex-1 flex-col overflow-y-auto">
				{@render children?.()}
			</main>
		</SidebarInset>
	</SidebarProvider>
{/if}
