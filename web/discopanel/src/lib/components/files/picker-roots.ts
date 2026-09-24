// Root vocabulary the path picker anchors browsing to
import { rpcClient } from '$lib/api/rpc-client';

// One browsable root with its emitted value prefix
export interface PickerRoot {
	id: string;
	label: string;
	context: string;
	backend: 'server' | 'host' | 'container';
	serverId?: string;
	moduleId?: string;
	resolvedPath?: string;
	emitBase?: string;
}

// Roots given directly or resolved when the picker opens
export type RootsInput = PickerRoot[] | (() => Promise<PickerRoot[]>);

// Server data root emitting paths relative to the server dir
export function serverDataRoot(
	serverId: string,
	opts: { emitBase?: string; context?: string } = {}
): PickerRoot {
	return {
		id: 'server-data',
		label: 'Server data',
		context: opts.context ?? 'relative to the server directory',
		backend: 'server',
		serverId,
		emitBase: opts.emitBase
	};
}

// Root browsing inside a server or module container
export function containerRoot(opts: { serverId?: string; moduleId?: string }): PickerRoot {
	return {
		id: 'container',
		label: 'Container filesystem',
		context: 'absolute path inside the container',
		backend: 'container',
		serverId: opts.serverId,
		moduleId: opts.moduleId,
		resolvedPath: '/'
	};
}

// Alias anchored roots offered for volume source fields
export async function volumeSourceRoots(opts: {
	serverId?: string;
	moduleId?: string;
}): Promise<PickerRoot[]> {
	const roots: PickerRoot[] = [];
	let resolved: Record<string, string> = {};
	try {
		const res = await rpcClient.module.getResolvedAliases({
			serverId: opts.serverId,
			moduleId: opts.moduleId
		});
		resolved = res.aliases;
	} catch {
		// Aliases unavailable, host root still works for admins
	}

	if (opts.serverId) {
		roots.push({
			id: 'server-data',
			label: 'Server data',
			context: '{{server.data_path}}',
			backend: 'server',
			serverId: opts.serverId,
			emitBase: '{{server.data_path}}'
		});
	}

	const hostAliases: [string, string][] = [
		['{{module.data_path}}', 'Module data'],
		['{{config.storage.data_dir}}', 'Panel data'],
		['{{config.storage.backup_dir}}', 'Panel backups']
	];
	for (const [alias, label] of hostAliases) {
		const path = resolved[alias];
		if (path) {
			roots.push({
				id: alias,
				label,
				context: alias,
				backend: 'host',
				resolvedPath: path,
				emitBase: alias
			});
		}
	}

	roots.push({
		id: 'host',
		label: 'Host filesystem',
		context: 'absolute host path, admin only',
		backend: 'host',
		resolvedPath: '/'
	});
	return roots;
}
