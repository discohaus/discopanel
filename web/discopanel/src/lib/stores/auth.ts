import { writable, derived, get } from 'svelte/store';
import { goto } from '$app/navigation';
import { resolve } from '$app/paths';
import { browser } from '$app/environment';
import { create } from '@bufbuild/protobuf';
import { rpcClient } from '$lib/api/rpc-client';
import { wsClient } from '$lib/stores/websocket.svelte';
import type { User, Permission } from '$lib/proto/discopanel/v1/storage_pb';
import { ResourceType, ActionType } from '$lib/proto/discopanel/options/v1/options_pb';
import {
	LoginRequestSchema,
	RegisterRequestSchema,
	ChangePasswordRequestSchema,
	UseRecoveryKeyRequestSchema
} from '$lib/proto/discopanel/v1/auth_pb';
import * as _ from 'lodash-es';

interface AuthState {
	user: User | null;
	token: string | null;
	permissions: Permission[];
	isAuthenticated: boolean;
	isLoading: boolean;
	localAuthEnabled: boolean;
	oidcEnabled: boolean;
	firstUserSetup: boolean;
	allowRegistration: boolean;
	anonymousAccessEnabled: boolean;
}

// Unspecified resource or action means wildcard
function checkPermission(
	permissions: Permission[],
	resource: ResourceType,
	action: ActionType,
	objectId?: string
): boolean {
	return permissions.some(
		(p) =>
			(p.resource === ResourceType.UNSPECIFIED || p.resource === resource) &&
			(p.action === ActionType.UNSPECIFIED || p.action === action) &&
			(p.objectId === '*' || !objectId || p.objectId === objectId)
	);
}

function createAuthStore() {
	const { subscribe, set, update } = writable<AuthState>({
		user: null,
		token: null,
		permissions: [],
		isAuthenticated: false,
		isLoading: true,
		localAuthEnabled: true,
		oidcEnabled: false,
		firstUserSetup: false,
		allowRegistration: false,
		anonymousAccessEnabled: false
	});

	// Load token from localStorage on init
	if (browser) {
		const token = localStorage.getItem('auth_token');
		if (token) {
			update((state) => ({ ...state, token }));
		}
	}

	return {
		subscribe,

		async checkAuthStatus() {
			try {
				const response = await rpcClient.auth.getAuthStatus({});

				const authEnabled = response.localAuthEnabled || response.oidcEnabled;

				update((state) => ({
					...state,
					localAuthEnabled: response.localAuthEnabled,
					oidcEnabled: response.oidcEnabled,
					firstUserSetup: response.firstUserSetup,
					allowRegistration: response.allowRegistration,
					anonymousAccessEnabled: response.anonymousAccessEnabled
				}));

				// Validates the stored token when auth is on
				let currentToken: string | null = null;
				update((state) => {
					currentToken = state.token;
					return state;
				});

				if (!authEnabled) {
					// Disabled auth still fetches the granted admin permissions
					await rpcClient.auth
						.getCurrentUser({})
						.then((r) =>
							update((state) => ({
								...state,
								user: r.user || null,
								permissions: r.permissions ?? [],
								isLoading: false
							}))
						)
						.catch(() => update((state) => ({ ...state, isLoading: false })));
				} else if (currentToken) {
					await this.validateSession();
				} else if (response.anonymousAccessEnabled) {
					try {
						const r = await rpcClient.auth.getCurrentUser({});
						update((state) => ({
							...state,
							permissions: r.permissions ?? [],
							isLoading: false
						}));
					} catch {
						update((state) => ({ ...state, isLoading: false }));
					}
				} else {
					update((state) => ({ ...state, isLoading: false }));
				}

				return {
					enabled: authEnabled,
					firstUserSetup: response.firstUserSetup,
					allowRegistration: response.allowRegistration
				};
			} catch (error) {
				console.error('Failed to check auth status:', error);
				update((state) => ({ ...state, isLoading: false }));
				return { enabled: false, firstUserSetup: false, allowRegistration: false };
			}
		},

		async login(username: string, password: string) {
			try {
				const request = create(LoginRequestSchema, { username, password });
				const response = await rpcClient.auth.login(request);

				// Store token
				if (browser && response.token) {
					localStorage.setItem('auth_token', response.token);
				}

				update((state) => ({
					...state,
					user: response.user || null,
					token: response.token,
					isAuthenticated: true,
					isLoading: false
				}));

				// Swaps any open socket onto the fresh token
				wsClient.reauthenticate();

				// Fetch permissions after login
				await this.validateSession();

				return response;
			} catch (error) {
				update((state) => ({ ...state, isLoading: false }));
				throw error;
			}
		},

		async logout() {
			const currentState: AuthState = get({ subscribe });

			try {
				if (currentState.token) {
					await rpcClient.auth.logout({});
				}
			} catch (error) {
				console.error('Logout error:', error);
			}

			// Clear local storage
			if (browser) {
				localStorage.removeItem('auth_token');
			}

			// Reset state
			set({
				user: null,
				token: null,
				permissions: [],
				isAuthenticated: false,
				isLoading: false,
				localAuthEnabled: currentState.localAuthEnabled,
				oidcEnabled: currentState.oidcEnabled,
				firstUserSetup: currentState.firstUserSetup,
				allowRegistration: currentState.allowRegistration,
				anonymousAccessEnabled: currentState.anonymousAccessEnabled
			});

			wsClient.disconnect();

			// Redirect to login
			goto(resolve('/login'));
		},

		async register(
			username: string,
			email: string,
			password: string,
			inviteCode?: string,
			invitePin?: string
		) {
			const request = create(RegisterRequestSchema, {
				username,
				email,
				password,
				inviteCode: inviteCode || undefined,
				invitePin: invitePin || undefined
			});
			await rpcClient.auth.register(request);

			// After successful registration, log them in
			return await this.login(username, password);
		},

		setToken(token: string) {
			if (browser) {
				localStorage.setItem('auth_token', token);
			}
			update((state) => ({ ...state, token }));
		},

		async changePassword(oldPassword: string, newPassword: string) {
			try {
				const request = create(ChangePasswordRequestSchema, {
					oldPassword,
					newPassword
				});
				const response = await rpcClient.auth.changePassword(request);
				return response;
			} catch (error) {
				throw new Error(_.get(error, 'message') || 'Failed to change password');
			}
		},

		async useRecoveryKey(key: string) {
			const request = create(UseRecoveryKeyRequestSchema, { recoveryKey: key });
			const response = await rpcClient.auth.useRecoveryKey(request);
			if (browser) {
				localStorage.removeItem('auth_token');
			}
			set({
				user: null,
				token: null,
				permissions: [],
				isAuthenticated: false,
				isLoading: false,
				localAuthEnabled: true,
				oidcEnabled: false,
				firstUserSetup: true,
				allowRegistration: false,
				anonymousAccessEnabled: false
			});
			return response;
		},

		async validateSession() {
			let currentToken: string | null = null;
			update((state) => {
				currentToken = state.token;
				return state;
			});

			if (!currentToken) {
				update((state) => ({ ...state, isLoading: false }));
				return false;
			}

			try {
				const response = await rpcClient.auth.getCurrentUser({});

				if (response.user) {
					update((state) => ({
						...state,
						user: response.user || null,
						permissions: response.permissions || [],
						isAuthenticated: true,
						isLoading: false
					}));
					return true;
				} else {
					// Invalid token, clear it
					if (browser) {
						localStorage.removeItem('auth_token');
					}
					update((state) => ({
						...state,
						user: null,
						token: null,
						permissions: [],
						isAuthenticated: false,
						isLoading: false
					}));
					return false;
				}
			} catch {
				update((state) => ({ ...state, isLoading: false }));
				return false;
			}
		},

		getHeaders() {
			const state = get({ subscribe });
			const headers: HeadersInit = {};
			if (state.token) {
				headers['Authorization'] = `Bearer ${state.token}`;
			}
			return headers;
		},

		getToken(): string | null {
			return get({ subscribe }).token;
		},

		hasRole(role: string): boolean {
			const state = get({ subscribe });
			return state.user?.roles?.includes(role) ?? false;
		},

		hasPermission(resource: ResourceType, action: ActionType, objectId?: string): boolean {
			const state = get({ subscribe });
			return checkPermission(state.permissions, resource, action, objectId);
		}
	};
}

export const authStore = createAuthStore();

// Derived stores for convenience
export const isAuthenticated = derived(authStore, ($auth) => $auth.isAuthenticated);
export const currentUser = derived(authStore, ($auth) => $auth.user);
export const authEnabled = derived(
	authStore,
	($auth) => $auth.localAuthEnabled || $auth.oidcEnabled
);

// Permission-based derived stores
export const canReadUsers = derived(authStore, ($auth) =>
	checkPermission($auth.permissions, ResourceType.USERS, ActionType.READ)
);
export const canCreateUsers = derived(authStore, ($auth) =>
	checkPermission($auth.permissions, ResourceType.USERS, ActionType.CREATE)
);
export const canUpdateUsers = derived(authStore, ($auth) =>
	checkPermission($auth.permissions, ResourceType.USERS, ActionType.UPDATE)
);
export const canDeleteUsers = derived(authStore, ($auth) =>
	checkPermission($auth.permissions, ResourceType.USERS, ActionType.DELETE)
);
export const canReadRoles = derived(authStore, ($auth) =>
	checkPermission($auth.permissions, ResourceType.ROLES, ActionType.READ)
);
export const canCreateRoles = derived(authStore, ($auth) =>
	checkPermission($auth.permissions, ResourceType.ROLES, ActionType.CREATE)
);
export const canUpdateRoles = derived(authStore, ($auth) =>
	checkPermission($auth.permissions, ResourceType.ROLES, ActionType.UPDATE)
);
export const canDeleteRoles = derived(authStore, ($auth) =>
	checkPermission($auth.permissions, ResourceType.ROLES, ActionType.DELETE)
);
export const canReadSettings = derived(authStore, ($auth) =>
	checkPermission($auth.permissions, ResourceType.SETTINGS, ActionType.READ)
);
export const canUpdateSettings = derived(authStore, ($auth) =>
	checkPermission($auth.permissions, ResourceType.SETTINGS, ActionType.UPDATE)
);

// Check if user has any settings-adjacent permission (for sidebar visibility)
export const canAccessSettings = derived(
	authStore,
	($auth) =>
		checkPermission($auth.permissions, ResourceType.SETTINGS, ActionType.READ) ||
		checkPermission($auth.permissions, ResourceType.USERS, ActionType.READ) ||
		checkPermission($auth.permissions, ResourceType.ROLES, ActionType.READ)
);

export { checkPermission };
