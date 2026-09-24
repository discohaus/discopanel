import { SvelteSet } from 'svelte/reactivity';
import { rpcClient } from '$lib/api/rpc-client';
import { notify } from '$lib/stores/activity.svelte';
import type { FileInfo } from '$lib/proto/discopanel/v1/file_pb';

// One archive extension list for every file surface
export const archiveExts = [
	'zip',
	'tar',
	'gz',
	'tgz',
	'rar',
	'7z',
	'bz2',
	'xz',
	'lz',
	'zst',
	'tbz2',
	'txz'
];

// True when the file looks like an archive
export function isArchiveFile(f: FileInfo | null): boolean {
	if (!f || f.isDir) return false;
	const ext = f.name.toLowerCase().split('.').pop() || '';
	return archiveExts.includes(ext);
}

// Depth of an entry inside the tree
export function fileDepth(file: FileInfo): number {
	return file.path.split('/').length - 1;
}

export interface FlattenOptions {
	expanded?: { has(path: string): boolean };
	filter?: string;
	dirsOnly?: boolean;
}

// Renders the tree as visible rows for flat lists
export function flattenTree(files: FileInfo[], opts: FlattenOptions = {}): FileInfo[] {
	const filter = opts.filter?.toLowerCase() ?? '';
	const result: FileInfo[] = [];
	function walk(items: FileInfo[]) {
		for (const item of items) {
			if (opts.dirsOnly && !item.isDir) continue;
			if (filter) {
				if (item.name.toLowerCase().includes(filter)) result.push(item);
				if (item.isDir && item.children) walk(item.children);
			} else {
				result.push(item);
				if (item.isDir && item.children && opts.expanded?.has(item.path)) {
					walk(item.children);
				}
			}
		}
	}
	walk(files);
	return result;
}

// Finds one entry by exact path
export function findFile(files: FileInfo[], path: string): FileInfo | undefined {
	for (const item of files) {
		if (item.path === path) return item;
		if (item.isDir && item.children) {
			const hit = findFile(item.children, path);
			if (hit) return hit;
		}
	}
	return undefined;
}

// Collects entries whose paths sit in the set
export function collectFiles(files: FileInfo[], paths: { has(path: string): boolean }): FileInfo[] {
	const result: FileInfo[] = [];
	function walk(items: FileInfo[]) {
		for (const item of items) {
			if (paths.has(item.path)) result.push(item);
			if (item.isDir && item.children) walk(item.children);
		}
	}
	walk(files);
	return result;
}

// Fetches and holds one server's file tree with expansion
export class FileTreeState {
	serverId = $state('');
	files = $state<FileInfo[]>([]);
	loading = $state(true);
	expanded = new SvelteSet<string>();

	constructor(serverId = '') {
		this.serverId = serverId;
	}

	// Fetches the whole tree in one call
	load = async () => {
		try {
			this.loading = true;
			const response = await rpcClient.file.listFiles({
				serverId: this.serverId,
				path: '',
				tree: true
			});
			this.files = response.files;
		} catch {
			notify.error('Failed to load files');
		} finally {
			this.loading = false;
		}
	};

	// Clears state for a new server
	reset = (serverId: string) => {
		this.serverId = serverId;
		this.files = [];
		this.loading = true;
		this.expanded.clear();
	};

	// Flips one directory open or closed
	toggle = (path: string) => {
		if (this.expanded.has(path)) {
			this.expanded.delete(path);
		} else {
			this.expanded.add(path);
		}
	};
}
