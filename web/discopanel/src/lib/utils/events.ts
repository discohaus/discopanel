import {
	ModuleEventAction,
	ModuleEventActionSchema,
	TriggeredEventType,
	TriggeredEventTypeSchema
} from '$lib/proto/discopanel/v1/storage_pb';
import { enumDesc, enumLabel, enumLabelOr } from '$lib/proto-meta';

export interface EventTypeMeta {
	type: TriggeredEventType;
	label: string;
	description: string;
}

// Display order of user selectable event types
const EVENT_TYPE_ORDER: TriggeredEventType[] = [
	TriggeredEventType.SERVER_START,
	TriggeredEventType.SERVER_STOP,
	TriggeredEventType.SERVER_RESTART,
	TriggeredEventType.SERVER_HEALTHY,
	TriggeredEventType.PLAYER_JOIN,
	TriggeredEventType.PLAYER_LEAVE,
	TriggeredEventType.PLAYER_DEATH,
	TriggeredEventType.PLAYER_ADVANCEMENT,
	TriggeredEventType.PLAYER_CHAT
];

// Ordered catalog of user selectable server event types
export const SERVER_EVENT_TYPES: EventTypeMeta[] = EVENT_TYPE_ORDER.map((type) => ({
	type,
	label: enumLabel(TriggeredEventTypeSchema, type),
	description: enumDesc(TriggeredEventTypeSchema, type)
}));

// Resolves display label for an event type
export function getEventTypeLabel(type: TriggeredEventType): string {
	return enumLabelOr(TriggeredEventTypeSchema, type);
}

// Display order for event action choices
export const EVENT_ACTION_OPTIONS: ModuleEventAction[] = [
	ModuleEventAction.START,
	ModuleEventAction.STOP,
	ModuleEventAction.RESTART,
	ModuleEventAction.EXEC,
	ModuleEventAction.RCON
];

// Resolves display label for an event action
export function getEventActionLabel(action: ModuleEventAction): string {
	return enumLabelOr(ModuleEventActionSchema, action);
}
