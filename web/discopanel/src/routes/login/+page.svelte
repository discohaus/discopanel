<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { create } from '@bufbuild/protobuf';
	import { authStore } from '$lib/stores/auth';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { ValidateInviteRequestSchema } from '$lib/proto/discopanel/v1/auth_pb';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Card, CardContent } from '$lib/components/ui/card';
	import { Alert, AlertDescription } from '$lib/components/ui/alert';
	import { Tabs, TabsContent, TabsList, TabsTrigger } from '$lib/components/ui/tabs';
	import { DiscoLogo } from '$lib/components/app';
	import { notify } from '$lib/stores/activity.svelte';
	import { Loader2, AlertCircle, TicketCheck, KeyRound } from '@lucide/svelte';

	let mode = $state<'login' | 'register'>('login');
	let username = $state('');
	let email = $state('');
	let password = $state('');
	let confirmPassword = $state('');
	let loading = $state(false);
	let error = $state('');
	let authStatus = $state({
		enabled: false,
		firstUserSetup: false,
		allowRegistration: false
	});
	let oidcEnabled = $state(false);
	let localAuthEnabled = $state(true);

	// Recovery state
	let showRecovery = $state(false);
	let recoveryKey = $state('');

	// Blocks the form while an SSO callback validates
	let validatingSso = $state(false);

	// Invite state
	let inviteCode = $state('');
	let inviteValid = $state(false);
	let inviteRequiresPin = $state(false);
	let inviteDescription = $state('');
	let invitePin = $state('');

	onMount(() => {
		// Token in URL fragment means OIDC callback landed
		const urlParams = new URLSearchParams(window.location.search);
		const token = new URLSearchParams(window.location.hash.slice(1)).get('token');
		if (token) {
			validatingSso = true;
			authStore.setToken(token);
			window.history.replaceState({}, '', resolve('/login'));
			authStore
				.validateSession()
				.then(async (valid) => {
					if (valid) {
						goto(resolve('/'));
						return;
					}
					error = 'Session validation failed. Please try again.';
					await loadAuthStatus(null);
					validatingSso = false;
				})
				.catch(async () => {
					error = 'Session validation failed. Please try again.';
					await loadAuthStatus(null);
					validatingSso = false;
				});
			return;
		}

		const invite = urlParams.get('invite');

		if ($authStore.isAuthenticated) {
			goto(resolve('/'));
			return;
		}

		loadAuthStatus(invite);
	});

	async function loadAuthStatus(invite: string | null) {
		try {
			const status = await authStore.checkAuthStatus();
			authStatus = status;
			oidcEnabled = $authStore.oidcEnabled;
			localAuthEnabled = $authStore.localAuthEnabled;

			if (!status.enabled && !status.firstUserSetup) {
				goto(resolve('/'));
				return;
			}

			if (status.firstUserSetup) {
				mode = 'register';
				return;
			}

			if (invite) {
				try {
					const resp = await rpcClient.auth.validateInvite(
						create(ValidateInviteRequestSchema, { code: invite }),
						silentCallOptions
					);
					if (resp.valid) {
						inviteCode = invite;
						inviteValid = true;
						inviteRequiresPin = resp.requiresPin;
						inviteDescription = resp.description;
						mode = 'register';
					}
				} catch {
					// Invalid invite falls back to normal login
				}
				window.history.replaceState({}, '', resolve('/login'));
			}
		} catch (err) {
			console.error('Failed to load auth status:', err);
			error = 'Could not reach the server. Refresh to try again.';
		}
	}

	async function handleLogin() {
		error = '';
		loading = true;

		try {
			await authStore.login(username, password);
			notify.success('Logged in successfully');
			setTimeout(() => {
				goto(resolve('/'));
			}, 100);
		} catch (err: unknown) {
			error = err instanceof Error ? err.message : 'Login failed';
			loading = false;
		}
	}

	async function handleRegister() {
		error = '';

		if (password !== confirmPassword) {
			error = 'Passwords do not match';
			return;
		}

		if (password.length < 8) {
			error = 'Password must be at least 8 characters';
			return;
		}

		loading = true;

		try {
			await authStore.register(
				username,
				email,
				password,
				inviteValid ? inviteCode : undefined,
				inviteValid && inviteRequiresPin ? invitePin : undefined
			);
			notify.success(
				authStatus.firstUserSetup
					? 'Admin account created successfully'
					: 'Account created successfully'
			);
			setTimeout(() => {
				goto(resolve('/'));
			}, 100);
		} catch (err: unknown) {
			error = err instanceof Error ? err.message : 'Registration failed';
			loading = false;
		}
	}

	async function handleOIDCLogin() {
		try {
			const response = await rpcClient.auth.getOIDCLoginURL({});
			if (response.loginUrl) {
				window.location.href = response.loginUrl;
			}
		} catch (err: unknown) {
			error = err instanceof Error ? err.message : 'Failed to initiate SSO login';
		}
	}

	async function handleRecovery() {
		error = '';
		loading = true;
		try {
			await authStore.useRecoveryKey(recoveryKey);
			notify.success('Panel reset to first-user setup');
			window.location.reload();
		} catch (err: unknown) {
			error = err instanceof Error ? err.message : 'Invalid recovery key';
			loading = false;
		}
	}

	function handleSubmit(e: Event) {
		e.preventDefault();

		if (mode === 'login') {
			handleLogin();
		} else {
			handleRegister();
		}
	}
</script>

<svelte:head>
	<title>Sign in · DiscoPanel</title>
</svelte:head>

{#snippet loginForm()}
	<div class="space-y-4">
		{#if localAuthEnabled}
			<form onsubmit={handleSubmit} class="space-y-4">
				<div class="space-y-2">
					<Label for="username">Username</Label>
					<Input
						id="username"
						type="text"
						bind:value={username}
						required
						disabled={loading}
						placeholder="Enter your username"
					/>
				</div>
				<div class="space-y-2">
					<Label for="password">Password</Label>
					<Input
						id="password"
						type="password"
						bind:value={password}
						required
						disabled={loading}
						placeholder="Enter your password"
					/>
				</div>
				<Button type="submit" class="glow-primary w-full" disabled={loading}>
					{#if loading}
						<Loader2 class="size-4 animate-spin" />
						Signing in...
					{:else}
						Sign in
					{/if}
				</Button>
			</form>
		{/if}

		{#if oidcEnabled}
			{#if localAuthEnabled}
				<div class="relative my-4">
					<div class="absolute inset-0 flex items-center">
						<span class="w-full border-t"></span>
					</div>
					<div class="relative flex justify-center text-xs uppercase">
						<span class="bg-card px-2 text-muted-foreground">Or</span>
					</div>
				</div>
			{/if}
			<Button
				type="button"
				variant={localAuthEnabled ? 'outline' : 'default'}
				class="w-full"
				onclick={handleOIDCLogin}
				disabled={loading}
			>
				Sign in with SSO
			</Button>
		{/if}
	</div>
{/snippet}

{#snippet registerForm()}
	<form onsubmit={handleSubmit} class="space-y-4">
		{#if inviteValid && inviteDescription}
			<Alert>
				<TicketCheck class="size-4" />
				<AlertDescription>{inviteDescription}</AlertDescription>
			</Alert>
		{/if}
		<div class="space-y-2">
			<Label for="reg-username">Username</Label>
			<Input
				id="reg-username"
				type="text"
				bind:value={username}
				required
				disabled={loading}
				placeholder="Choose a username"
			/>
		</div>
		<div class="space-y-2">
			<Label for="reg-email">Email (optional)</Label>
			<Input
				id="reg-email"
				type="email"
				bind:value={email}
				disabled={loading}
				placeholder="your@email.com"
			/>
		</div>
		<div class="space-y-2">
			<Label for="reg-password">Password</Label>
			<Input
				id="reg-password"
				type="password"
				bind:value={password}
				required
				disabled={loading}
				placeholder="Choose a password"
			/>
		</div>
		<div class="space-y-2">
			<Label for="reg-confirm">Confirm password</Label>
			<Input
				id="reg-confirm"
				type="password"
				bind:value={confirmPassword}
				required
				disabled={loading}
				placeholder="Confirm your password"
			/>
		</div>
		{#if inviteValid && inviteRequiresPin}
			<div class="space-y-2">
				<Label for="reg-pin">Invite PIN</Label>
				<Input
					id="reg-pin"
					type="password"
					bind:value={invitePin}
					required
					disabled={loading}
					placeholder="Enter invite PIN"
				/>
			</div>
		{/if}
		<Button type="submit" class="glow-primary w-full" disabled={loading}>
			{#if loading}
				<Loader2 class="size-4 animate-spin" />
				Creating account...
			{:else}
				Create account
			{/if}
		</Button>
	</form>
{/snippet}

<div
	class="relative flex min-h-screen items-center justify-center overflow-hidden bg-background p-4"
>
	<!-- Single deliberate brand glow -->
	<div
		class="pointer-events-none absolute top-1/2 left-1/2 size-[42rem] -translate-x-1/2 -translate-y-1/2 rounded-full opacity-40 blur-3xl"
		style="background: radial-gradient(circle, color-mix(in oklch, var(--primary) 22%, transparent) 0%, transparent 65%)"
	></div>

	<div class="relative w-full max-w-md space-y-6">
		<div class="flex flex-col items-center gap-3">
			<div class="flex items-center gap-3">
				<DiscoLogo class="size-12" spotlight />
				<span class="font-mono text-3xl font-bold tracking-tight"
					>disco<span class="text-[#55aa55]">panel</span></span
				>
			</div>
			<p class="text-sm text-muted-foreground">
				{#if authStatus.firstUserSetup}
					Welcome! Create your admin account to get started.
				{:else}
					Sign in to manage your Minecraft servers
				{/if}
			</p>
		</div>

		<Card>
			<CardContent>
				{#if error}
					<Alert variant="destructive" class="mb-4">
						<AlertCircle class="size-4" />
						<AlertDescription>{error}</AlertDescription>
					</Alert>
				{/if}

				{#if validatingSso}
					<div class="flex flex-col items-center gap-3 py-10 text-muted-foreground">
						<Loader2 class="size-6 animate-spin" />
						<p class="text-sm">Completing sign in...</p>
					</div>
				{:else if authStatus.firstUserSetup}
					<form onsubmit={handleSubmit} class="space-y-4">
						<div class="space-y-2">
							<Label for="admin-username">Admin username</Label>
							<Input
								id="admin-username"
								type="text"
								bind:value={username}
								required
								disabled={loading}
								placeholder="Choose admin username"
							/>
						</div>
						<div class="space-y-2">
							<Label for="admin-email">Email (optional)</Label>
							<Input
								id="admin-email"
								type="email"
								bind:value={email}
								disabled={loading}
								placeholder="admin@example.com"
							/>
						</div>
						<div class="space-y-2">
							<Label for="admin-password">Password</Label>
							<Input
								id="admin-password"
								type="password"
								bind:value={password}
								required
								disabled={loading}
								placeholder="Choose a strong password"
							/>
						</div>
						<div class="space-y-2">
							<Label for="admin-confirm">Confirm password</Label>
							<Input
								id="admin-confirm"
								type="password"
								bind:value={confirmPassword}
								required
								disabled={loading}
								placeholder="Confirm your password"
							/>
						</div>
						<Alert>
							<AlertCircle class="size-4" />
							<AlertDescription>
								{#if oidcEnabled}
									A local admin account is required for initial setup, even with SSO enabled. This
									ensures you always have a fallback login to manage the system if your identity
									provider becomes unavailable.
								{:else}
									This will be the admin account with full system access.
								{/if}
							</AlertDescription>
						</Alert>
						<Button type="submit" class="glow-primary w-full" disabled={loading}>
							{#if loading}
								<Loader2 class="size-4 animate-spin" />
								Creating admin account...
							{:else}
								Create admin account
							{/if}
						</Button>
					</form>
				{:else if (authStatus.allowRegistration || inviteValid) && localAuthEnabled}
					<Tabs bind:value={mode} class="w-full">
						<TabsList class="grid w-full grid-cols-2">
							<TabsTrigger value="login">Log in</TabsTrigger>
							<TabsTrigger value="register">Register</TabsTrigger>
						</TabsList>

						<TabsContent value="login" class="pt-2">
							{@render loginForm()}
						</TabsContent>

						<TabsContent value="register" class="pt-2">
							{@render registerForm()}
						</TabsContent>
					</Tabs>
				{:else}
					{@render loginForm()}
				{/if}

				{#if showRecovery}
					<div class="mt-4 space-y-4">
						<Alert variant="destructive">
							<AlertCircle class="size-4" />
							<AlertDescription>
								This will delete all users, sessions, and invites. Server configs and data are
								preserved. This cannot be undone.
							</AlertDescription>
						</Alert>
						<div class="space-y-2">
							<Label for="recovery-key">Recovery key</Label>
							<Input
								id="recovery-key"
								type="password"
								bind:value={recoveryKey}
								disabled={loading}
								placeholder="Paste your recovery key"
							/>
						</div>
						<div class="flex gap-2">
							<Button
								variant="outline"
								class="flex-1"
								onclick={() => {
									showRecovery = false;
									error = '';
								}}
								disabled={loading}
							>
								Cancel
							</Button>
							<Button
								variant="destructive"
								class="flex-1"
								onclick={handleRecovery}
								disabled={loading || !recoveryKey}
							>
								{#if loading}
									<Loader2 class="size-4 animate-spin" />
									Resetting...
								{:else}
									Reset panel
								{/if}
							</Button>
						</div>
					</div>
				{:else if !authStatus.firstUserSetup}
					<div class="mt-4 text-center">
						<button
							type="button"
							class="inline-flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
							onclick={() => (showRecovery = true)}
						>
							<KeyRound class="size-3" />
							Forgot access? Recovery
						</button>
					</div>
				{/if}

				{#if $authStore.anonymousAccessEnabled}
					<div class="relative my-4">
						<div class="absolute inset-0 flex items-center">
							<span class="w-full border-t"></span>
						</div>
						<div class="relative flex justify-center text-xs uppercase">
							<span class="bg-card px-2 text-muted-foreground">Or</span>
						</div>
					</div>
					<Button variant="ghost" class="w-full" onclick={() => goto(resolve('/'))}>
						Continue as guest
					</Button>
				{/if}
			</CardContent>
		</Card>
	</div>
</div>
