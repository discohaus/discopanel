import { ModuleStatus, ModuleStatusSchema } from '$lib/proto/discopanel/v1/storage_pb';
import { enumLabelOr } from '$lib/proto-meta';
import type { StatusTone } from '$lib/server-status';

export interface ModuleStatusMeta {
	label: string;
	tone: StatusTone;
	transitional: boolean;
}

// Local UI tone and transition flag per status
const STATUS_UI: Record<ModuleStatus, { tone: StatusTone; transitional: boolean }> = {
	[ModuleStatus.UNSPECIFIED]: { tone: 'idle', transitional: false },
	[ModuleStatus.STOPPED]: { tone: 'idle', transitional: false },
	[ModuleStatus.STARTING]: { tone: 'busy', transitional: true },
	[ModuleStatus.RUNNING]: { tone: 'ok', transitional: false },
	[ModuleStatus.STOPPING]: { tone: 'busy', transitional: true },
	[ModuleStatus.ERROR]: { tone: 'danger', transitional: false },
	[ModuleStatus.CREATING]: { tone: 'busy', transitional: true }
};

export function moduleStatusMeta(status: ModuleStatus): ModuleStatusMeta {
	const ui = STATUS_UI[status] ?? STATUS_UI[ModuleStatus.UNSPECIFIED];
	return {
		label: enumLabelOr(ModuleStatusSchema, status),
		tone: ui.tone,
		transitional: ui.transitional
	};
}
