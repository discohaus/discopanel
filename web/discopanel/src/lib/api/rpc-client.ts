import {
	createClient,
	type Client,
	type Interceptor,
	ConnectError,
	Code
} from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { authStore } from '$lib/stores/auth';
import { toast } from 'svelte-sonner';
import { loadingStore } from '$lib/stores/loading.svelte';

// Rpc state
let loggingOut = false;

// SERVICES
import { AuthService } from '$lib/proto/discopanel/v1/auth_pb';
import { PropertiesService } from '$lib/proto/discopanel/v1/properties_pb';
import { FileService } from '$lib/proto/discopanel/v1/file_pb';
import { MinecraftService } from '$lib/proto/discopanel/v1/minecraft_pb';
import { ModService } from '$lib/proto/discopanel/v1/mod_pb';
import { ModpackService } from '$lib/proto/discopanel/v1/modpack_pb';
import { ProxyService } from '$lib/proto/discopanel/v1/proxy_pb';
import { ServerService } from '$lib/proto/discopanel/v1/server_pb';
import { SupportService } from '$lib/proto/discopanel/v1/support_pb';
import { TaskService } from '$lib/proto/discopanel/v1/task_pb';
import { UploadService } from '$lib/proto/discopanel/v1/upload_pb';
import { UserService } from '$lib/proto/discopanel/v1/user_pb';
import { RoleService } from '$lib/proto/discopanel/v1/role_pb';
import { ModuleService } from '$lib/proto/discopanel/v1/module_pb';

// Bare backend reason for inline error surfaces
export function rpcErrorMessage(error: unknown, fallback: string): string {
	if (error instanceof ConnectError) return error.rawMessage || fallback;
	return error instanceof Error ? error.message : fallback;
}

// Header to mark requests as silent / no loader
const SILENT_HEADER = 'X-Silent-Request';

export const silentCallOptions = { headers: new Headers({ [SILENT_HEADER]: 'true' }) };

// Login auth interception
const authInterceptor: Interceptor = (next) => async (req) => {
	// Auth headers
	const authHeaders = authStore.getHeaders();
	Object.entries(authHeaders).forEach(([key, value]) => {
		req.header.set(key, value as string);
	});

	// Check for silence
	const isSilent = req.header.get(SILENT_HEADER) === 'true';

	// Operation ID for loading tracking
	const operationId = `rpc-${req.service.typeName}-${req.method.name}-${Date.now()}`;

	// Show loading indicator
	const showLoading = !isSilent && !req.method.name.toLowerCase().includes('status');
	if (showLoading) {
		loadingStore.start(operationId);
	}

	try {
		const res = await next(req);
		return res;
	} catch (error) {
		const onLoginPage = typeof window !== 'undefined' && window.location.pathname === '/login';

		// Log out on expired/invalid session
		if (error instanceof ConnectError && error.code === Code.Unauthenticated) {
			if (!onLoginPage && !loggingOut) {
				loggingOut = true;
				authStore.logout().finally(() => {
					loggingOut = false;
				});
			}
			// Never toast auth errors - the auto-logout redirect handles them
			throw error;
		}

		if (!isSilent && !onLoginPage) {
			const message = error instanceof Error ? error.message : 'An error occurred';
			toast.error(message);
		}
		throw error;
	} finally {
		if (showLoading) {
			loadingStore.stop(operationId);
		}
	}
};

// Transport w/ auth
const transport = createConnectTransport({
	baseUrl: '',
	interceptors: [authInterceptor]
});

// Clients for each service
export class RpcClient {
	public readonly auth: Client<typeof AuthService>;
	public readonly properties: Client<typeof PropertiesService>;
	public readonly file: Client<typeof FileService>;
	public readonly minecraft: Client<typeof MinecraftService>;
	public readonly mod: Client<typeof ModService>;
	public readonly modpack: Client<typeof ModpackService>;
	public readonly proxy: Client<typeof ProxyService>;
	public readonly server: Client<typeof ServerService>;
	public readonly support: Client<typeof SupportService>;
	public readonly task: Client<typeof TaskService>;
	public readonly upload: Client<typeof UploadService>;
	public readonly user: Client<typeof UserService>;
	public readonly role: Client<typeof RoleService>;
	public readonly module: Client<typeof ModuleService>;

	constructor() {
		this.auth = createClient(AuthService, transport);
		this.properties = createClient(PropertiesService, transport);
		this.file = createClient(FileService, transport);
		this.minecraft = createClient(MinecraftService, transport);
		this.mod = createClient(ModService, transport);
		this.modpack = createClient(ModpackService, transport);
		this.proxy = createClient(ProxyService, transport);
		this.server = createClient(ServerService, transport);
		this.support = createClient(SupportService, transport);
		this.task = createClient(TaskService, transport);
		this.upload = createClient(UploadService, transport);
		this.user = createClient(UserService, transport);
		this.role = createClient(RoleService, transport);
		this.module = createClient(ModuleService, transport);
	}
}

// Singleton
export const rpcClient = new RpcClient();
