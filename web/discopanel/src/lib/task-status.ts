// Execution status display metadata for task history
import { AlertCircle, CheckCircle2, Clock, Loader2, XCircle } from '@lucide/svelte';
import { ExecutionStatus, ExecutionStatusSchema } from '$lib/proto/discopanel/v1/storage_pb';
import { enumLabelOr } from '$lib/proto-meta';
import { TONE_BADGE, type StatusTone } from '$lib/server-status';

// Local icon and tone per execution status
const EXECUTION_UI: Partial<Record<ExecutionStatus, { icon: typeof Clock; tone?: StatusTone }>> = {
	[ExecutionStatus.COMPLETED]: { icon: CheckCircle2, tone: 'ok' },
	[ExecutionStatus.FAILED]: { icon: XCircle, tone: 'danger' },
	[ExecutionStatus.RUNNING]: { icon: Loader2, tone: 'busy' },
	[ExecutionStatus.TIMEOUT]: { icon: Clock, tone: 'danger' },
	[ExecutionStatus.SKIPPED]: { icon: AlertCircle },
	[ExecutionStatus.CANCELLED]: { icon: XCircle }
};

export interface ExecutionMeta {
	label: string;
	icon: typeof Clock;
	class: string;
}

// Badge label, icon, and tone classes for one status
export function executionMeta(status: ExecutionStatus): ExecutionMeta {
	const ui = EXECUTION_UI[status] ?? { icon: Clock };
	return {
		label: enumLabelOr(ExecutionStatusSchema, status),
		icon: ui.icon,
		class: ui.tone ? TONE_BADGE[ui.tone] : 'text-muted-foreground'
	};
}
