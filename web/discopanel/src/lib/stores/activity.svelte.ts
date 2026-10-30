import type { StatusTone } from '$lib/server-status';

export type ActivityKind = 'ok' | 'err' | 'warn' | 'info';

// Button shown on an event in the status bar history
export interface ActivityAction {
	label: string;
	run: (event: ActivityEvent) => void | Promise<void>;
}

export interface ActivityEvent {
	id: number;
	kind: ActivityKind;
	message: string;
	detail?: string;
	at: number;
	actions?: ActivityAction[];
}

// Maps event kinds onto shared status tones
export const KIND_TONE: Record<ActivityKind, StatusTone> = {
	ok: 'ok',
	err: 'danger',
	warn: 'warn',
	info: 'sleep'
};

const MAX_EVENTS = 50;
const DUPE_MS = 3500;
const HOLD_MS = 4000;
const HOLD_ALERT_MS = 12000;

// Feeds the status bar and its history
class ActivityStore {
	events = $state<ActivityEvent[]>([]);
	fresh = $state(false);
	unseenErrors = $state(0);
	private nextId = 1;
	private holdTimer: ReturnType<typeof setTimeout> | null = null;

	current = $derived(this.events[0] ?? null);

	report(
		kind: ActivityKind,
		message: string,
		detail?: string,
		holdMs?: number,
		actions?: ActivityAction[]
	) {
		const head = this.events[0];
		// Same message twice in a row folds into one
		if (head && head.kind === kind && head.message === message && Date.now() - head.at < DUPE_MS) {
			head.at = Date.now();
		} else {
			const event = { id: this.nextId++, kind, message, detail, at: Date.now(), actions };
			this.events = [event, ...this.events].slice(0, MAX_EVENTS);
			if (kind === 'err') this.unseenErrors += 1;
		}
		this.wake(kind, holdMs);
	}

	// Vivid bar state decays on a timer
	private wake(kind: ActivityKind, holdMs?: number) {
		this.fresh = true;
		if (this.holdTimer) clearTimeout(this.holdTimer);
		const hold = holdMs ?? (kind === 'err' || kind === 'warn' ? HOLD_ALERT_MS : HOLD_MS);
		this.holdTimer = setTimeout(() => (this.fresh = false), hold);
	}

	markSeen() {
		this.unseenErrors = 0;
	}

	remove(id: number) {
		this.events = this.events.filter((event) => event.id !== id);
	}

	clear() {
		if (this.holdTimer) clearTimeout(this.holdTimer);
		this.holdTimer = null;
		this.events = [];
		this.fresh = false;
		this.unseenErrors = 0;
	}
}

export const activity = new ActivityStore();

interface NotifyOpts {
	description?: string;
	duration?: number;
	actions?: ActivityAction[];
}

// Same call shape the old toast api had
export const notify = {
	success: (message: string, opts?: NotifyOpts) =>
		activity.report('ok', message, opts?.description, opts?.duration, opts?.actions),
	error: (message: string, opts?: NotifyOpts) =>
		activity.report('err', message, opts?.description, opts?.duration, opts?.actions),
	warning: (message: string, opts?: NotifyOpts) =>
		activity.report('warn', message, opts?.description, opts?.duration, opts?.actions),
	info: (message: string, opts?: NotifyOpts) =>
		activity.report('info', message, opts?.description, opts?.duration, opts?.actions)
};
