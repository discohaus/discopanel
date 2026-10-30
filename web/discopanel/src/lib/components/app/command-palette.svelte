<script lang="ts">
	import {
		CommandDialog,
		CommandEmpty,
		CommandGroup,
		CommandInput,
		CommandItem,
		CommandList,
		CommandSeparator
	} from '$lib/components/ui/command';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { toggleMode } from 'mode-watcher';
	import {
		House,
		Server as ServerIcon,
		Plus,
		Package,
		Puzzle,
		Settings,
		User as UserIcon,
		FileText,
		SunMoon,
		Play,
		Square
	} from '@lucide/svelte';
	import { activitySortedServers } from '$lib/stores/servers';
	import { canAccessSettings } from '$lib/stores/auth';
	import { canStart, canStop, statusMeta } from '$lib/server-status';
	import { runServerAction } from '$lib/server-actions';
	import StatusDot from './status-dot.svelte';

	let { open = $bindable(false) }: { open?: boolean } = $props();

	let servers = $derived($activitySortedServers);
	let showAdmin = $derived($canAccessSettings);

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'k' && (e.metaKey || e.ctrlKey) && !e.repeat) {
			e.preventDefault();
			open = !open;
		}
	}

	function run(action: () => void) {
		open = false;
		action();
	}
</script>

<svelte:window {onkeydown} />

<CommandDialog bind:open title="Command palette" description="Jump to a server or run an action">
	<CommandInput placeholder="Search servers, pages, actions..." />
	<CommandList>
		<CommandEmpty>No results found.</CommandEmpty>
		{#if servers.length > 0}
			<CommandGroup heading="Servers">
				{#each servers as server (server.id)}
					<CommandItem
						value={`open ${server.name} ${server.id}`}
						keywords={[server.name]}
						onSelect={() => run(() => goto(resolve(`/servers/${server.id}`)))}
					>
						<StatusDot status={server.status} />
						<span class="truncate">{server.name}</span>
						<span class="ml-auto text-xs text-muted-foreground">
							{statusMeta(server.status).label}
						</span>
					</CommandItem>
				{/each}
			</CommandGroup>
			<CommandGroup heading="Power">
				{#each servers as server (server.id)}
					{#if canStart(server.status)}
						<CommandItem
							value={`start ${server.name} ${server.id}`}
							keywords={[server.name]}
							onSelect={() => run(() => runServerAction('start', server))}
						>
							<Play class="size-4 text-status-ok" />
							<span>Start <span class="font-medium">{server.name}</span></span>
						</CommandItem>
					{:else if canStop(server.status)}
						<CommandItem
							value={`stop ${server.name} ${server.id}`}
							keywords={[server.name]}
							onSelect={() => run(() => runServerAction('stop', server))}
						>
							<Square class="size-4 text-status-danger" />
							<span>Stop <span class="font-medium">{server.name}</span></span>
						</CommandItem>
					{/if}
				{/each}
			</CommandGroup>
			<CommandSeparator />
		{/if}
		<CommandGroup heading="Go to">
			<CommandItem value="go home dashboard" onSelect={() => run(() => goto(resolve('/')))}>
				<House class="size-4" />
				<span>Home</span>
			</CommandItem>
			<CommandItem value="go servers list" onSelect={() => run(() => goto(resolve('/servers')))}>
				<ServerIcon class="size-4" />
				<span>Servers</span>
			</CommandItem>
			<CommandItem
				value="go new server create"
				onSelect={() => run(() => goto(resolve('/servers/new')))}
			>
				<Plus class="size-4" />
				<span>New server</span>
			</CommandItem>
			<CommandItem value="go modpacks" onSelect={() => run(() => goto(resolve('/modpacks')))}>
				<Package class="size-4" />
				<span>Modpacks</span>
			</CommandItem>
			{#if showAdmin}
				<CommandItem value="go modules" onSelect={() => run(() => goto(resolve('/modules')))}>
					<Puzzle class="size-4" />
					<span>Modules</span>
				</CommandItem>
				<CommandItem value="go settings" onSelect={() => run(() => goto(resolve('/settings')))}>
					<Settings class="size-4" />
					<span>Settings</span>
				</CommandItem>
			{/if}
			<CommandItem value="go profile account" onSelect={() => run(() => goto(resolve('/profile')))}>
				<UserIcon class="size-4" />
				<span>Profile</span>
			</CommandItem>
			<CommandItem
				value="go api docs reference"
				onSelect={() => run(() => goto(resolve('/docs/api')))}
			>
				<FileText class="size-4" />
				<span>API reference</span>
			</CommandItem>
		</CommandGroup>
		<CommandGroup heading="Preferences">
			<CommandItem value="toggle theme dark light" onSelect={() => run(() => toggleMode())}>
				<SunMoon class="size-4" />
				<span>Toggle theme</span>
			</CommandItem>
		</CommandGroup>
	</CommandList>
</CommandDialog>
