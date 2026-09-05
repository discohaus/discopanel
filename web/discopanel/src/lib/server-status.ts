import { ServerStatus, ServerStatusSchema } from '$lib/proto/discopanel/v1/storage_pb';
import { enumDesc, enumLabelOr } from '$lib/proto-meta';

export type StatusTone = 'ok' | 'busy' | 'warn' | 'danger' | 'sleep' | 'idle';

export interface StatusMeta {
	label: string;
	tone: StatusTone;
	desc: string;
	transitional: boolean;
}

// Local UI tone and transition flag per status
const STATUS_UI: Record<ServerStatus, { tone: StatusTone; transitional: boolean }> = {
	[ServerStatus.UNSPECIFIED]: { tone: 'idle', transitional: false },
	[ServerStatus.CREATING]: { tone: 'busy', transitional: true },
	[ServerStatus.STARTING]: { tone: 'busy', transitional: true },
	[ServerStatus.RUNNING]: { tone: 'ok', transitional: false },
	[ServerStatus.STOPPING]: { tone: 'busy', transitional: true },
	[ServerStatus.STOPPED]: { tone: 'idle', transitional: false },
	[ServerStatus.ERROR]: { tone: 'danger', transitional: false },
	[ServerStatus.UNHEALTHY]: { tone: 'warn', transitional: false },
	[ServerStatus.PROVISIONING]: { tone: 'busy', transitional: true },
	[ServerStatus.PAUSED]: { tone: 'sleep', transitional: false }
};

export function statusMeta(status: ServerStatus): StatusMeta {
	const ui = STATUS_UI[status] ?? STATUS_UI[ServerStatus.UNSPECIFIED];
	return {
		label: enumLabelOr(ServerStatusSchema, status),
		desc:
			enumDesc(ServerStatusSchema, status) ||
			enumDesc(ServerStatusSchema, ServerStatus.UNSPECIFIED),
		tone: ui.tone,
		transitional: ui.transitional
	};
}

export const TONE_TEXT: Record<StatusTone, string> = {
	ok: 'text-status-ok',
	busy: 'text-status-busy',
	warn: 'text-status-warn',
	danger: 'text-status-danger',
	sleep: 'text-status-sleep',
	idle: 'text-status-idle'
};

export const TONE_BG: Record<StatusTone, string> = {
	ok: 'bg-status-ok',
	busy: 'bg-status-busy',
	warn: 'bg-status-warn',
	danger: 'bg-status-danger',
	sleep: 'bg-status-sleep',
	idle: 'bg-status-idle'
};

export const TONE_BADGE: Record<StatusTone, string> = {
	ok: 'border-status-ok/25 bg-status-ok/10 text-status-ok',
	busy: 'border-status-busy/25 bg-status-busy/10 text-status-busy',
	warn: 'border-status-warn/25 bg-status-warn/10 text-status-warn',
	danger: 'border-status-danger/25 bg-status-danger/10 text-status-danger',
	sleep: 'border-status-sleep/25 bg-status-sleep/10 text-status-sleep',
	idle: 'border-status-idle/25 bg-status-idle/10 text-status-idle'
};

export function canStart(status: ServerStatus): boolean {
	return status === ServerStatus.STOPPED || status === ServerStatus.ERROR;
}

export function canStop(status: ServerStatus): boolean {
	return (
		status === ServerStatus.RUNNING ||
		status === ServerStatus.UNHEALTHY ||
		status === ServerStatus.STARTING ||
		status === ServerStatus.PAUSED ||
		status === ServerStatus.PROVISIONING ||
		status === ServerStatus.ERROR
	);
}

export function canRestart(status: ServerStatus): boolean {
	return (
		status === ServerStatus.RUNNING ||
		status === ServerStatus.UNHEALTHY ||
		status === ServerStatus.ERROR
	);
}

export function isUp(status: ServerStatus): boolean {
	return status === ServerStatus.RUNNING || status === ServerStatus.UNHEALTHY;
}

export function tpsTone(tps: number | undefined): StatusTone {
	if (!tps) return 'idle';
	if (tps >= 18) return 'ok';
	if (tps >= 15) return 'busy';
	return 'danger';
}
