// Shared form helpers for module and template dialogs
import { create } from '@bufbuild/protobuf';
import {
	ModuleEventAction,
	ModuleEventHookSchema,
	ModuleProtocol,
	NetworkPortSchema,
	TriggeredEventType,
	type ModuleEventHook,
	type NetworkPort,
	type VolumeMount
} from '$lib/proto/discopanel/v1/storage_pb';
import { notify } from '$lib/stores/activity.svelte';

// One key value row for map backed fields
export interface KvRow {
	key: string;
	value: string;
}

// Map field to editable rows
export function mapToKv(m: { [key: string]: string } | undefined): KvRow[] {
	return m ? Object.entries(m).map(([key, value]) => ({ key, value })) : [];
}

// Editable rows back to a map field
export function kvToMap(rows: KvRow[]): { [key: string]: string } {
	const map: { [key: string]: string } = {};
	for (const row of rows) {
		if (row.key.trim()) map[row.key.trim()] = row.value;
	}
	return map;
}

// Fresh port row, proxy stance comes from the caller
export function newPort(proxyEnabled = true): NetworkPort {
	return create(NetworkPortSchema, { protocol: ModuleProtocol.TCP, proxyEnabled });
}

// Fresh hook row with lifecycle defaults
export function newHook(): ModuleEventHook {
	return create(ModuleEventHookSchema, {
		event: TriggeredEventType.SERVER_START,
		action: ModuleEventAction.START
	});
}

// Trims paths and drops rows missing either side
export function trimVolumes(volumes: VolumeMount[]): VolumeMount[] {
	for (const v of volumes) {
		v.source = v.source.trim();
		v.target = v.target.trim();
	}
	return volumes.filter((v) => v.source && v.target);
}

// Drops rows without a container port, warning once
export function dropEmptyPorts(ports: NetworkPort[]): NetworkPort[] {
	const valid = ports.filter((p) => p.containerPort > 0);
	const dropped = ports.length - valid.length;
	if (dropped > 0) {
		notify.warning(
			`Ignored ${dropped} port row${dropped === 1 ? '' : 's'} without a container port`
		);
	}
	return valid;
}
