import { derived, readable } from 'svelte/store';
import { rpcClient } from '$lib/api/rpc-client';
import type { ModLoaderInfo } from '$lib/proto/discopanel/v1/minecraft_pb';
import type { ModLoader } from '$lib/proto/discopanel/v1/storage_pb';

let cache: Promise<ModLoaderInfo[]> | null = null;

// Registry-backed loader facts, fetched once per session
export function loadModLoaders(): Promise<ModLoaderInfo[]> {
	cache ??= rpcClient.minecraft.getModLoaders({}).then((r) => r.modloaders);
	return cache;
}

const modLoaderInfos = readable<ModLoaderInfo[]>([], (set) => {
	loadModLoaders()
		.then(set)
		.catch(() => {});
});

export const loaderDisplayName = derived(
	modLoaderInfos,
	(infos) => (loader: ModLoader) => infos.find((l) => l.loader === loader)?.displayName ?? ''
);

// Directory jars install into, empty when the loader has none
export async function modsDirectoryFor(loader: ModLoader): Promise<string> {
	const loaders = await loadModLoaders();
	return loaders.find((l) => l.loader === loader)?.modsDirectory ?? '';
}
