<script lang="ts">
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { registerRefresh } from '$lib/stores/refresh';
	import { notify } from '$lib/stores/activity.svelte';
	import { Input } from '$lib/components/ui/input';
	import { Button } from '$lib/components/ui/button';
	import { Label } from '$lib/components/ui/label';
	import { Badge } from '$lib/components/ui/badge';
	import { Switch } from '$lib/components/ui/switch';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import * as Select from '$lib/components/ui/select';
	import * as Dialog from '$lib/components/ui/dialog';
	import {
		ConfirmDialog,
		CopyButton,
		EmptyState,
		SectionCard,
		SectionedDialogLayout
	} from '$lib/components/app';
	import {
		Loader2,
		Plus,
		Play,
		Trash2,
		Clock,
		CheckCircle2,
		XCircle,
		AlertCircle,
		Terminal,
		RotateCcw,
		Square,
		Power,
		FileText,
		History,
		Archive,
		Wrench,
		X,
		Pencil,
		Webhook as WebhookIcon,
		Zap
	} from '@lucide/svelte';
	import type { Server, ScheduledTask, TaskExecution } from '$lib/proto/discopanel/v1/storage_pb';
	import type { GetSchedulerStatusResponse } from '$lib/proto/discopanel/v1/task_pb';
	import {
		CreateTaskRequestSchema,
		UpdateTaskRequestSchema,
		ToggleTaskRequestSchema,
		TriggerTaskRequestSchema,
		DeleteTaskRequestSchema,
		ListTasksRequestSchema,
		ListTaskExecutionsRequestSchema
	} from '$lib/proto/discopanel/v1/task_pb';
	import {
		TaskType,
		TaskTypeSchema,
		TaskStatus,
		ScheduleType,
		ScheduleTypeSchema,
		ExecutionStatus,
		ExecutionStatusSchema,
		TriggeredEventType,
		TaskTrigger,
		TaskTriggerSchema,
		CommandTaskConfigSchema,
		BackupTaskConfigSchema,
		ScriptTaskConfigSchema,
		WebhookTaskConfigSchema
	} from '$lib/proto/discopanel/v1/storage_pb';
	import { enumLabel } from '$lib/proto-meta';
	import { SERVER_EVENT_TYPES, getEventTypeLabel } from '$lib/utils/events';
	import { create, clone } from '@bufbuild/protobuf';
	import { timestampFromDate } from '@bufbuild/protobuf/wkt';
	import { timestampToDate, formatDateTime } from '$lib/utils/time';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import CodeEditor from '$lib/components/ui/code-editor.svelte';

	let { server, active }: { server: Server; active?: boolean } = $props();

	let loading = $state(true);
	let tasks = $state<ScheduledTask[]>([]);
	let initialized = $state(false);
	// svelte-ignore state_referenced_locally
	let previousServerId = $state(server.id);

	// Dialog state
	type DialogSection = 'general' | 'payload' | 'schedule' | 'advanced';
	let showCreateDialog = $state(false);
	let showHistoryDialog = $state(false);
	let selectedTask = $state<ScheduledTask | null>(null);
	let taskHistory = $state<TaskExecution[]>([]);
	let historyLoading = $state(false);
	let creating = $state(false);
	let activeSection = $state<DialogSection>('general');
	let deleteTarget = $state<ScheduledTask | null>(null);
	let deleteOpen = $state(false);
	let runningTaskId = $state<string | null>(null);
	let schedulerStatus = $state<GetSchedulerStatusResponse | null>(null);

	// Shared backup defaults for create and edit
	const BACKUP_DEFAULTS = { compress: true, retentionDays: 7, minBackups: 3, maxBackups: 0 };

	// Form state, common
	let taskName = $state('');
	let taskDescription = $state('');
	let taskType = $state<TaskType>(TaskType.COMMAND);
	let scheduleType = $state<ScheduleType>(ScheduleType.CRON);
	let cronExpr = $state('');
	let intervalSecs = $state(3600);
	let runAt = $state('');
	let timezone = $state('UTC');
	let timeout = $state(300);
	let retryCount = $state(0);
	let retryDelay = $state(60);
	let requireOnline = $state(true);
	let eventTriggers = $state<TriggeredEventType[]>([TriggeredEventType.SERVER_START]);

	// Shared webhook defaults for create and edit
	const WEBHOOK_DEFAULTS = { maxRetries: 3, retryDelayMs: 1000, timeoutMs: 5000 };

	// Form state, typed per type configs
	let commandConfig = $state(create(CommandTaskConfigSchema, {}));
	let scriptConfig = $state(create(ScriptTaskConfigSchema, {}));
	let scriptArgs = $state('');
	let backupConfig = $state(create(BackupTaskConfigSchema, { ...BACKUP_DEFAULTS }));
	let backupPaths = $state('');

	// Form state, webhook
	let webhookConfig = $state(create(WebhookTaskConfigSchema, { ...WEBHOOK_DEFAULTS }));
	let webhookSecret = $state('');
	let customizePayload = $state(false);
	let originalWebhookHasSecret = $state(false);

	const dialogSections = $derived<
		{
			id: DialogSection;
			label: string;
			icon: typeof FileText;
			title: string;
			description: string;
		}[]
	>([
		{
			id: 'general',
			label: 'General',
			icon: FileText,
			title: 'General',
			description: 'Task name, type, and configuration'
		},
		...(taskType === TaskType.WEBHOOK
			? [
					{
						id: 'payload' as DialogSection,
						label: 'Payload',
						icon: WebhookIcon,
						title: 'Payload',
						description: 'Customize the request body sent to the webhook'
					}
				]
			: []),
		{
			id: 'schedule',
			label: 'Schedule',
			icon: Clock,
			title: 'Schedule',
			description: 'When and how often the task runs'
		},
		{
			id: 'advanced',
			label: 'Advanced',
			icon: Wrench,
			title: 'Advanced',
			description: 'Timeouts, retries, and execution conditions'
		}
	]);

	const currentSection = $derived(
		dialogSections.find((s) => s.id === activeSection) ?? dialogSections[0]
	);
	const DialogTaskIcon = $derived(getTaskTypeIcon(taskType));

	// Keeps active section valid when sections change
	$effect(() => {
		if (!dialogSections.some((s) => s.id === activeSection)) {
			activeSection = 'general';
		}
	});

	// Static webhook payload presets resolved client side
	const webhookTemplatePresets: Record<string, string> = {
		generic: `{
  "event": "{{.event}}",
  "timestamp": "{{.timestamp}}",
  "server": {
    "id": "{{.server_id}}",
    "name": "{{.server_name}}",
    "status": "{{.server_status}}",
    "mc_version": "{{.server_mc_version}}",
    "mod_loader": "{{.server_mod_loader}}",
    "players_online": {{.server_players_online}},
    "max_players": {{.server_max_players}},
    "port": {{.server_port}}
  }
}`,
		discord: `{
  "embeds": [{
    "title": "{{.title}}",
    "description": "**{{.server_name}}** - {{.server_status}}",
    "color": {{if .is_server_start}}5763719{{else if .is_server_stop}}15548997{{else if .is_server_restart}}16705372{{else}}5793266{{end}},
    "timestamp": "{{.timestamp}}",
    "fields": [
      {"name": "Version", "value": "{{.server_mc_version}}", "inline": true},
      {"name": "Players", "value": "{{.server_players_online}}/{{.server_max_players}}", "inline": true},
      {"name": "Mod Loader", "value": "{{.server_mod_loader}}", "inline": true}
    ],
    "footer": {"text": "DiscoPanel"}
  }]
}`,
		slack: `{
  "blocks": [
    {
      "type": "header",
      "text": {"type": "plain_text", "text": "{{.title}}"}
    },
    {
      "type": "section",
      "text": {"type": "mrkdwn", "text": "*{{.server_name}}* - {{.server_status}}"}
    },
    {
      "type": "section",
      "fields": [
        {"type": "mrkdwn", "text": "*Version:*\\n{{.server_mc_version}}"},
        {"type": "mrkdwn", "text": "*Players:*\\n{{.server_players_online}}/{{.server_max_players}}"},
        {"type": "mrkdwn", "text": "*Mod Loader:*\\n{{.server_mod_loader}}"},
        {"type": "mrkdwn", "text": "*Port:*\\n{{.server_port}}"}
      ]
    },
    {
      "type": "context",
      "elements": [{"type": "mrkdwn", "text": "DiscoPanel | {{.timestamp}}"}]
    }
  ]
}`,
		teams: `{
  "type": "message",
  "attachments": [{
    "contentType": "application/vnd.microsoft.card.adaptive",
    "content": {
      "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
      "type": "AdaptiveCard",
      "version": "1.4",
      "body": [
        {
          "type": "TextBlock",
          "size": "medium",
          "weight": "bolder",
          "text": "{{.title}}"
        },
        {
          "type": "TextBlock",
          "text": "**{{.server_name}}** - {{.server_status}}",
          "wrap": true
        },
        {
          "type": "FactSet",
          "facts": [
            {"title": "Version", "value": "{{.server_mc_version}}"},
            {"title": "Players", "value": "{{.server_players_online}}/{{.server_max_players}}"},
            {"title": "Mod Loader", "value": "{{.server_mod_loader}}"},
            {"title": "Port", "value": "{{.server_port}}"}
          ]
        },
        {
          "type": "TextBlock",
          "text": "DiscoPanel | {{.timestamp}}",
          "size": "small",
          "isSubtle": true
        }
      ]
    }
  }]
}`,
		ntfy: `{
  "topic": "discopanel",
  "title": "{{.title}}",
  "message": "{{.server_name}} - {{.server_status}}",
  "tags": ["video_game"],
  "priority": 3
}`
	};

	const presetLabels: Record<string, string> = {
		discord: 'Discord',
		slack: 'Slack',
		teams: 'Teams',
		ntfy: 'ntfy',
		generic: 'Generic'
	};

	const TEMPLATE_VARIABLES: [string, string][] = [
		['{{.event}}', 'Event name'],
		['{{.is_<event>}}', 'True for the firing event'],
		['{{.timestamp}}', 'ISO 8601 timestamp'],
		['{{.title}}', 'Event title'],
		['{{.server_id}}', 'Server ID'],
		['{{.server_name}}', 'Server name'],
		['{{.server_status}}', 'Server status'],
		['{{.server_mc_version}}', 'MC version'],
		['{{.server_mod_loader}}', 'Mod loader'],
		['{{.server_players_online}}', 'Player count'],
		['{{.server_max_players}}', 'Max players'],
		['{{.server_port}}', 'Server port'],
		['{{.player}}', 'Player name (player events)']
	];

	// Shows custom template or the resolved preset
	let displayValue = $derived(
		customizePayload ? webhookConfig.payloadTemplate : getDefaultTemplate(webhookConfig.url)
	);

	function isDiscordUrl(url: string): boolean {
		return url.includes('discord.com/api/webhooks') || url.includes('discordapp.com/api/webhooks');
	}

	function getDefaultPresetKey(url: string): string {
		if (isDiscordUrl(url)) return 'discord';
		if (url.includes('hooks.slack.com')) return 'slack';
		if (url.includes('.webhook.office.com') || url.includes('outlook.office.com/webhook'))
			return 'teams';
		if (url.includes('ntfy.sh')) return 'ntfy';
		return 'generic';
	}

	function getDefaultTemplate(url: string): string {
		return webhookTemplatePresets[getDefaultPresetKey(url)] || '';
	}

	// Resets task state when server changes
	$effect(() => {
		if (server.id !== previousServerId) {
			previousServerId = server.id;
			loading = true;
			tasks = [];
			initialized = false;
		}
	});

	// Loads once the tab becomes active
	$effect(() => {
		if (active && !initialized) {
			initialized = true;
			loadTasks();
		}
	});

	$effect(() => {
		if (!active) return;
		return registerRefresh(loadTasks);
	});

	function toggleEventTrigger(trigger: TriggeredEventType) {
		if (eventTriggers.includes(trigger)) {
			eventTriggers = eventTriggers.filter((t) => t !== trigger);
		} else {
			eventTriggers = [...eventTriggers, trigger];
		}
	}

	function applyPreset(key: string) {
		const template = webhookTemplatePresets[key];
		if (template) {
			webhookConfig.payloadTemplate = template;
		}
	}

	// Refreshes list without blanking already loaded rows
	async function loadTasks() {
		try {
			const request = create(ListTasksRequestSchema, { serverId: server.id });
			const response = await rpcClient.task.listTasks(request);
			tasks = response.tasks;
			await loadSchedulerStatus();
		} catch (_e) {
			notify.error('Failed to load tasks');
		} finally {
			loading = false;
		}
	}

	function resetForm() {
		taskName = '';
		taskDescription = '';
		scheduleType = ScheduleType.CRON;
		cronExpr = '';
		intervalSecs = 3600;
		runAt = '';
		timezone = 'UTC';
		timeout = 300;
		retryCount = 0;
		retryDelay = 60;
		requireOnline = true;
		commandConfig = create(CommandTaskConfigSchema, {});
		scriptConfig = create(ScriptTaskConfigSchema, {});
		scriptArgs = '';
		backupConfig = create(BackupTaskConfigSchema, { ...BACKUP_DEFAULTS });
		backupPaths = '';
		activeSection = 'general';
		eventTriggers = [TriggeredEventType.SERVER_START];
		webhookConfig = create(WebhookTaskConfigSchema, { ...WEBHOOK_DEFAULTS });
		webhookSecret = '';
		customizePayload = false;
		originalWebhookHasSecret = false;
		selectedTask = null;
	}

	function openCreateDialog() {
		resetForm();
		taskType = TaskType.COMMAND;
		showCreateDialog = true;
	}

	// Formats a date for local datetime input fields
	function toDateTimeLocal(date: Date): string {
		const pad = (n: number) => String(n).padStart(2, '0');
		return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
	}

	function openEditDialog(task: ScheduledTask) {
		resetForm();
		selectedTask = task;
		taskName = task.name;
		taskDescription = task.description;
		taskType = task.taskType;
		scheduleType = task.schedule;
		cronExpr = task.cronExpr;
		intervalSecs = task.intervalSecs;
		const runDate = timestampToDate(task.runAt);
		if (runDate) {
			runAt = toDateTimeLocal(runDate);
		}
		timezone = task.timezone || 'UTC';
		timeout = task.timeout;
		retryCount = task.retryCount;
		retryDelay = task.retryDelay;
		requireOnline = task.requireOnline;

		if (task.commandConfig) {
			commandConfig = clone(CommandTaskConfigSchema, task.commandConfig);
		}
		if (task.scriptConfig) {
			scriptConfig = clone(ScriptTaskConfigSchema, task.scriptConfig);
			scriptArgs = task.scriptConfig.args.join(' ');
		}
		if (task.backupConfig) {
			backupConfig = clone(BackupTaskConfigSchema, task.backupConfig);
			backupPaths = task.backupConfig.paths.join(', ');
		}

		eventTriggers =
			task.eventTriggers && task.eventTriggers.length > 0
				? [...task.eventTriggers]
				: [TriggeredEventType.SERVER_START];

		if (task.taskType === TaskType.WEBHOOK && task.webhookConfig) {
			webhookConfig = clone(WebhookTaskConfigSchema, task.webhookConfig);
			webhookSecret = '';
			originalWebhookHasSecret = !!task.webhookConfig.secret;
			// Templates matching the URL preset count as uncustomized
			customizePayload =
				!!webhookConfig.payloadTemplate &&
				webhookConfig.payloadTemplate.trim() !== getDefaultTemplate(webhookConfig.url).trim();
		}

		showCreateDialog = true;
	}

	function closeDialog() {
		showCreateDialog = false;
		resetForm();
	}

	async function saveTask() {
		if (!taskName.trim()) {
			notify.error('Task name is required');
			return;
		}
		if (taskType === TaskType.COMMAND && !commandConfig.command.trim()) {
			notify.error('A command is required for command tasks');
			return;
		}
		if (taskType === TaskType.SCRIPT && !scriptConfig.scriptPath.trim()) {
			notify.error('A script path is required for script tasks');
			return;
		}
		if (scheduleType === ScheduleType.CRON && !cronExpr.trim()) {
			notify.error('A cron expression is required');
			return;
		}
		if (taskType === TaskType.WEBHOOK && !webhookConfig.url.trim()) {
			notify.error('Webhook URL is required');
			return;
		}
		if (scheduleType === ScheduleType.EVENT && eventTriggers.length === 0) {
			notify.error('At least one event trigger is required');
			return;
		}

		creating = true;
		try {
			commandConfig.command = commandConfig.command.trim();
			scriptConfig.scriptPath = scriptConfig.scriptPath.trim();
			scriptConfig.args = scriptArgs
				.split(' ')
				.map((a) => a.trim())
				.filter(Boolean);
			backupConfig.backupName = backupConfig.backupName.trim();
			backupConfig.paths = backupPaths
				.split(',')
				.map((p) => p.trim())
				.filter(Boolean);
			// Always sends a concrete resolved template
			webhookConfig.payloadTemplate = customizePayload
				? webhookConfig.payloadTemplate
				: getDefaultTemplate(webhookConfig.url);
			// Existing secret survives edit unless replaced
			if (webhookSecret) webhookConfig.secret = webhookSecret;

			const runAtTimestamp =
				scheduleType === ScheduleType.ONCE && runAt
					? timestampFromDate(new Date(runAt))
					: undefined;

			const isEventScheduled = scheduleType === ScheduleType.EVENT;
			const typedConfigs = {
				commandConfig: taskType === TaskType.COMMAND ? commandConfig : undefined,
				backupConfig: taskType === TaskType.BACKUP ? backupConfig : undefined,
				scriptConfig: taskType === TaskType.SCRIPT ? scriptConfig : undefined,
				webhookConfig: taskType === TaskType.WEBHOOK ? webhookConfig : undefined
			};
			if (selectedTask) {
				const request = create(UpdateTaskRequestSchema, {
					id: selectedTask.id,
					name: taskName,
					description: taskDescription,
					taskType: taskType,
					schedule: scheduleType,
					cronExpr: scheduleType === ScheduleType.CRON ? cronExpr : undefined,
					intervalSecs: scheduleType === ScheduleType.INTERVAL ? intervalSecs : undefined,
					runAt: runAtTimestamp,
					timezone: timezone,
					...typedConfigs,
					timeout: timeout,
					retryCount: retryCount,
					retryDelay: retryDelay,
					requireOnline: requireOnline,
					eventTriggers: isEventScheduled ? eventTriggers : [],
					clearEventTriggers: !isEventScheduled
				});
				await rpcClient.task.updateTask(request);
				notify.success('Task updated successfully');
			} else {
				const request = create(CreateTaskRequestSchema, {
					serverId: server.id,
					name: taskName,
					description: taskDescription,
					taskType: taskType,
					schedule: scheduleType,
					cronExpr: scheduleType === ScheduleType.CRON ? cronExpr : undefined,
					intervalSecs: scheduleType === ScheduleType.INTERVAL ? intervalSecs : 0,
					runAt: runAtTimestamp,
					timezone: timezone,
					...typedConfigs,
					timeout: timeout,
					retryCount: retryCount,
					retryDelay: retryDelay,
					requireOnline: requireOnline,
					eventTriggers: isEventScheduled ? eventTriggers : []
				});
				await rpcClient.task.createTask(request);
				notify.success('Task created successfully');
			}
			showCreateDialog = false;
			resetForm();
			await loadTasks();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to save task');
		} finally {
			creating = false;
		}
	}

	async function toggleTask(task: ScheduledTask) {
		try {
			const newStatus =
				task.status === TaskStatus.ENABLED ? TaskStatus.DISABLED : TaskStatus.ENABLED;
			const request = create(ToggleTaskRequestSchema, { id: task.id, status: newStatus });
			await rpcClient.task.toggleTask(request);
			notify.success(`Task ${newStatus === TaskStatus.ENABLED ? 'enabled' : 'disabled'}`);
			await loadTasks();
		} catch (_e) {
			notify.error('Failed to toggle task');
		}
	}

	async function triggerTask(task: ScheduledTask) {
		if (runningTaskId === task.id) return;
		runningTaskId = task.id;
		try {
			const request = create(TriggerTaskRequestSchema, { id: task.id });
			await rpcClient.task.triggerTask(request);
			notify.success('Task triggered successfully');
			await loadTasks();
		} catch (error: unknown) {
			notify.error(error instanceof Error ? error.message : 'Failed to trigger task');
		} finally {
			if (runningTaskId === task.id) runningTaskId = null;
		}
	}

	function requestDelete(task: ScheduledTask) {
		deleteTarget = task;
		deleteOpen = true;
	}

	async function confirmDelete() {
		if (!deleteTarget) return;
		try {
			const request = create(DeleteTaskRequestSchema, { id: deleteTarget.id });
			await rpcClient.task.deleteTask(request);
			notify.success('Task deleted successfully');
			await loadTasks();
		} catch (_e) {
			notify.error('Failed to delete task');
		}
	}

	async function viewHistory(task: ScheduledTask) {
		selectedTask = task;
		historyLoading = true;
		showHistoryDialog = true;
		try {
			const request = create(ListTaskExecutionsRequestSchema, { taskId: task.id, limit: 50 });
			const response = await rpcClient.task.listTaskExecutions(request);
			taskHistory = response.executions;
		} catch (_e) {
			notify.error('Failed to load task history');
		} finally {
			historyLoading = false;
		}
	}

	async function viewServerHistory() {
		selectedTask = null;
		historyLoading = true;
		showHistoryDialog = true;
		try {
			const response = await rpcClient.task.listServerExecutions({
				serverId: server.id,
				limit: 50
			});
			taskHistory = response.executions;
		} catch (_e) {
			notify.error('Failed to load run history');
		} finally {
			historyLoading = false;
		}
	}

	async function cancelExecution(executionId: string) {
		try {
			await rpcClient.task.cancelExecution({ id: executionId });
			notify.success('Execution cancelled');
			if (selectedTask) await viewHistory(selectedTask);
			else await viewServerHistory();
		} catch (_e) {
			notify.error('Failed to cancel execution');
		}
	}

	async function loadSchedulerStatus() {
		try {
			schedulerStatus = await rpcClient.task.getSchedulerStatus({}, silentCallOptions);
		} catch {
			schedulerStatus = null;
		}
	}

	function closeHistory() {
		showHistoryDialog = false;
		selectedTask = null;
		taskHistory = [];
	}

	// Display order for task type choices
	const TASK_TYPE_OPTIONS: TaskType[] = [
		TaskType.COMMAND,
		TaskType.BACKUP,
		TaskType.RESTART,
		TaskType.START,
		TaskType.STOP,
		TaskType.SCRIPT,
		TaskType.WEBHOOK
	];

	// Display order for schedule type choices
	const SCHEDULE_TYPE_OPTIONS: ScheduleType[] = [
		ScheduleType.EVENT,
		ScheduleType.CRON,
		ScheduleType.INTERVAL,
		ScheduleType.ONCE
	];

	function getTaskTypeLabel(type: TaskType): string {
		return enumLabel(TaskTypeSchema, type) || enumLabel(TaskTypeSchema, TaskType.UNSPECIFIED);
	}

	function getTaskTypeIcon(type: TaskType) {
		switch (type) {
			case TaskType.COMMAND:
				return Terminal;
			case TaskType.BACKUP:
				return Archive;
			case TaskType.RESTART:
				return RotateCcw;
			case TaskType.START:
				return Power;
			case TaskType.STOP:
				return Square;
			case TaskType.SCRIPT:
				return FileText;
			case TaskType.WEBHOOK:
				return WebhookIcon;
			default:
				return Clock;
		}
	}

	function getScheduleTypeLabel(s: ScheduleType): string {
		return (
			enumLabel(ScheduleTypeSchema, s) || enumLabel(ScheduleTypeSchema, ScheduleType.UNSPECIFIED)
		);
	}

	function getScheduleLabel(task: ScheduledTask): string {
		switch (task.schedule) {
			case ScheduleType.CRON:
				return `Cron: ${task.cronExpr}`;
			case ScheduleType.INTERVAL:
				return `Every ${formatInterval(task.intervalSecs)}`;
			case ScheduleType.ONCE:
				return task.runAt ? `Once at ${formatDateTime(task.runAt)}` : 'Once';
			case ScheduleType.EVENT:
				return `On ${(task.eventTriggers || []).map(getEventTypeLabel).join(', ') || 'none'}`;
			default:
				return 'Unknown';
		}
	}

	function formatInterval(seconds: number): string {
		if (seconds < 60) return `${seconds}s`;
		if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
		if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
		return `${Math.floor(seconds / 86400)}d`;
	}

	function executionBadge(status: ExecutionStatus): { icon: typeof Clock; class: string } {
		switch (status) {
			case ExecutionStatus.COMPLETED:
				return {
					icon: CheckCircle2,
					class: 'border-status-ok/30 bg-status-ok/10 text-status-ok'
				};
			case ExecutionStatus.FAILED:
				return {
					icon: XCircle,
					class: 'border-status-danger/30 bg-status-danger/10 text-status-danger'
				};
			case ExecutionStatus.RUNNING:
				return {
					icon: Loader2,
					class: 'border-status-busy/30 bg-status-busy/10 text-status-busy'
				};
			case ExecutionStatus.SKIPPED:
				return { icon: AlertCircle, class: 'text-muted-foreground' };
			case ExecutionStatus.TIMEOUT:
				return {
					icon: Clock,
					class: 'border-status-danger/30 bg-status-danger/10 text-status-danger'
				};
			case ExecutionStatus.CANCELLED:
				return { icon: XCircle, class: 'text-muted-foreground' };
			default:
				return { icon: Clock, class: 'text-muted-foreground' };
		}
	}

	function getExecutionStatusLabel(status: ExecutionStatus): string {
		return (
			enumLabel(ExecutionStatusSchema, status) ||
			enumLabel(ExecutionStatusSchema, ExecutionStatus.UNSPECIFIED)
		);
	}

	function formatDuration(ms: bigint): string {
		const seconds = Number(ms) / 1000;
		if (seconds < 1) return `${Number(ms)}ms`;
		if (seconds < 60) return `${seconds.toFixed(1)}s`;
		return `${Math.floor(seconds / 60)}m ${Math.floor(seconds % 60)}s`;
	}

	function formatNextRun(task: ScheduledTask): string {
		const date = timestampToDate(task.nextRun);
		if (!date) return 'Not scheduled';
		const diff = date.getTime() - Date.now();
		if (diff < 0) return 'Overdue';
		if (diff < 60000) return 'Less than a minute';
		if (diff < 3600000) return `${Math.floor(diff / 60000)}m`;
		if (diff < 86400000) return `${Math.floor(diff / 3600000)}h`;
		return formatDateTime(date);
	}

	function getWebhookUrlForDisplay(task: ScheduledTask): string {
		if (task.taskType !== TaskType.WEBHOOK) return '';
		return task.webhookConfig?.url ?? '';
	}

	// Copies a template variable with named feedback
	async function copyVariable(variable: string) {
		const ok = await copyToClipboard(variable);
		if (ok) notify.success(`Copied ${variable}`);
		else notify.error('Failed to copy to clipboard');
	}
</script>

<SectionCard title="Tasks" description="Scheduled backups, restarts, webhooks, and custom jobs">
	{#snippet action()}
		{#if schedulerStatus && !schedulerStatus.running}
			<span class="text-xs text-status-danger">Scheduler stopped</span>
		{:else if schedulerStatus && schedulerStatus.runningExecutions > 0}
			<span class="text-xs text-muted-foreground">
				{schedulerStatus.runningExecutions} running
			</span>
		{/if}
		<Button variant="outline" size="sm" onclick={viewServerHistory}>
			<History class="size-4" />
			History
		</Button>
		<Button size="sm" onclick={openCreateDialog}>
			<Plus class="size-4" />
			New task
		</Button>
	{/snippet}

	{#if loading}
		<div class="space-y-2">
			{#each Array(3) as _, i (i)}
				<Skeleton class="h-16 rounded-lg" />
			{/each}
		</div>
	{:else if tasks.length === 0}
		<EmptyState
			icon={Clock}
			title="No tasks yet"
			description="Create a scheduled task or a webhook to react to server events."
		>
			<Button size="sm" onclick={openCreateDialog}>
				<Plus class="size-4" />
				New task
			</Button>
		</EmptyState>
	{:else}
		<div class="overflow-hidden rounded-lg border">
			<div class="divide-y">
				{#each tasks as task (task.id)}
					{@const TaskIcon = getTaskTypeIcon(task.taskType)}
					{@const webhookUrlDisplay = getWebhookUrlForDisplay(task)}
					{@const enabled = task.status === TaskStatus.ENABLED}
					<div
						class="group flex items-start gap-3 px-3 py-3 transition-colors hover:bg-accent/40 sm:px-4 {enabled
							? ''
							: 'opacity-60'}"
					>
						<div
							class="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-md border {enabled
								? 'border-primary/20 bg-primary/5'
								: 'bg-muted/40'}"
						>
							<TaskIcon class="size-4 {enabled ? 'text-primary' : 'text-muted-foreground'}" />
						</div>
						<div class="min-w-0 flex-1">
							<div class="flex flex-wrap items-center gap-x-2 gap-y-1">
								<h4 class="truncate text-sm font-medium">{task.name}</h4>
								{#if !enabled}
									<Badge variant="secondary">Disabled</Badge>
								{/if}
								<Badge variant="outline">{getTaskTypeLabel(task.taskType)}</Badge>
								{#if task.schedule === ScheduleType.EVENT}
									{#each task.eventTriggers as trigger (trigger)}
										<Badge variant="outline">
											<Zap class="size-3" />
											{getEventTypeLabel(trigger)}
										</Badge>
									{/each}
								{/if}
							</div>
							{#if task.description}
								<p class="mt-0.5 truncate text-xs text-muted-foreground">{task.description}</p>
							{/if}
							{#if webhookUrlDisplay}
								<div class="mt-0.5 flex items-center gap-1 text-xs text-muted-foreground">
									<span class="max-w-[400px] truncate font-mono">{webhookUrlDisplay}</span>
									<CopyButton text={webhookUrlDisplay} label="Copy URL" class="size-6" />
								</div>
							{/if}
							<div
								class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground"
							>
								<span class="flex items-center gap-1">
									<Clock class="size-3" />
									{getScheduleLabel(task)}
								</span>
								{#if task.nextRun && task.status === TaskStatus.ENABLED && task.schedule !== ScheduleType.EVENT}
									<span class="tabular">Next: {formatNextRun(task)}</span>
								{/if}
								{#if task.lastRun}
									<span class="tabular">Last: {formatDateTime(task.lastRun)}</span>
								{/if}
							</div>
						</div>
						<div class="flex shrink-0 items-center gap-1.5">
							<div
								class="flex items-center gap-0.5 opacity-60 transition-opacity group-hover:opacity-100"
							>
								<Button
									variant="ghost"
									size="icon"
									class="size-8"
									onclick={() => triggerTask(task)}
									title="Run now"
									aria-label="Run now"
									disabled={!enabled || runningTaskId === task.id}
								>
									{#if runningTaskId === task.id}
										<Loader2 class="size-4 animate-spin" />
									{:else}
										<Play class="size-4" />
									{/if}
								</Button>
								<Button
									variant="ghost"
									size="icon"
									class="size-8"
									onclick={() => viewHistory(task)}
									title="View history"
									aria-label="View history"
								>
									<History class="size-4" />
								</Button>
								<Button
									variant="ghost"
									size="icon"
									class="size-8"
									onclick={() => openEditDialog(task)}
									title="Edit"
									aria-label="Edit"
								>
									<Pencil class="size-4" />
								</Button>
								<Button
									variant="ghost"
									size="icon"
									class="size-8 text-status-danger hover:bg-status-danger/10 hover:text-status-danger"
									onclick={() => requestDelete(task)}
									title="Delete"
									aria-label="Delete"
								>
									<Trash2 class="size-4" />
								</Button>
							</div>
							<Switch
								checked={enabled}
								onCheckedChange={() => toggleTask(task)}
								title={enabled ? 'Disable task' : 'Enable task'}
							/>
						</div>
					</div>
				{/each}
			</div>
		</div>
	{/if}
</SectionCard>

<!-- Create/Edit dialog -->
<Dialog.Root bind:open={showCreateDialog}>
	<Dialog.Content
		class="flex h-[80vh]! w-[95vw]! max-w-4xl! flex-col gap-0! overflow-hidden p-0!"
		showCloseButton={false}
	>
		<SectionedDialogLayout
			bind:activeSection
			navItems={dialogSections}
			title={currentSection.title}
			description={currentSection.description}
			sidebarClass="w-40 bg-muted/20 sm:w-52"
			onclose={closeDialog}
		>
			{#snippet sidebarHeader()}
				<div class="border-b p-4">
					<div class="flex items-center gap-2.5">
						<div class="flex size-9 shrink-0 items-center justify-center rounded-lg border bg-card">
							<DialogTaskIcon class="size-4 text-muted-foreground" />
						</div>
						<div class="min-w-0 flex-1">
							<h3 class="truncate text-sm font-medium">
								{taskName || (selectedTask ? 'Edit task' : 'New task')}
							</h3>
							<p class="truncate text-xs text-muted-foreground">{getTaskTypeLabel(taskType)}</p>
						</div>
					</div>
				</div>
			{/snippet}

			<div class="max-w-2xl space-y-5">
				{#if activeSection === 'general'}
					<div class="space-y-2">
						<Label for="taskName">Task name *</Label>
						<Input id="taskName" bind:value={taskName} placeholder="Daily Backup" />
					</div>

					<div class="space-y-2">
						<Label for="taskDescription">Description</Label>
						<Input
							id="taskDescription"
							bind:value={taskDescription}
							placeholder="Runs every day at midnight"
						/>
					</div>

					<div class="space-y-2">
						<Label>Task type</Label>
						<Select.Root
							type="single"
							name="taskType"
							value={taskType.toString()}
							onValueChange={(v) => {
								if (v) taskType = parseInt(v) as TaskType;
							}}
						>
							<Select.Trigger class="w-full">
								{getTaskTypeLabel(taskType)}
							</Select.Trigger>
							<Select.Content>
								{#each TASK_TYPE_OPTIONS as t (t)}
									<Select.Item value={t.toString()} label={getTaskTypeLabel(t)}>
										{getTaskTypeLabel(t)}
									</Select.Item>
								{/each}
							</Select.Content>
						</Select.Root>
					</div>

					{#if taskType === TaskType.COMMAND}
						<div class="space-y-2">
							<Label for="command">RCON command *</Label>
							<Input
								id="command"
								bind:value={commandConfig.command}
								placeholder="say Hello World!"
								class="font-mono"
							/>
							<p class="text-xs text-muted-foreground">The command to execute via RCON</p>
						</div>
					{:else if taskType === TaskType.SCRIPT}
						<div class="space-y-2">
							<Label for="scriptPath">Script path or executable *</Label>
							<Input
								id="scriptPath"
								bind:value={scriptConfig.scriptPath}
								placeholder="/data/scripts/cleanup.sh"
								class="font-mono"
							/>
							<p class="text-xs text-muted-foreground">
								Path to the script/executable inside the container
							</p>
						</div>
						<div class="space-y-2">
							<Label for="scriptArgs">Arguments</Label>
							<Input
								id="scriptArgs"
								bind:value={scriptArgs}
								placeholder="--verbose --level 2"
								class="font-mono"
							/>
							<p class="text-xs text-muted-foreground">
								Space-separated arguments to pass to the script/executable
							</p>
						</div>
					{:else if taskType === TaskType.BACKUP}
						<div class="space-y-2">
							<Label for="backupName">Backup name</Label>
							<Input
								id="backupName"
								bind:value={backupConfig.backupName}
								placeholder={taskName || 'Daily Backup'}
							/>
							<p class="text-xs text-muted-foreground">
								Used as the archive filename prefix. Defaults to the task name.
							</p>
						</div>
						<div class="space-y-2">
							<Label for="backupPaths">Paths to include</Label>
							<Input
								id="backupPaths"
								bind:value={backupPaths}
								placeholder="world, world_nether, world_the_end"
								class="font-mono"
							/>
							<p class="text-xs text-muted-foreground">
								Comma-separated paths relative to the server directory. Leave empty to back up the
								world directory.
							</p>
						</div>
						<label
							class="flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors hover:bg-accent/40"
						>
							<Switch bind:checked={backupConfig.compress} class="mt-0.5" />
							<div class="space-y-0.5">
								<span class="text-sm font-medium">Compress archive</span>
								<p class="text-xs text-muted-foreground">
									Smaller backups at the cost of more CPU while archiving
								</p>
							</div>
						</label>
						<div class="grid gap-4 sm:grid-cols-3">
							<div class="space-y-2">
								<Label for="retentionDays">Retention (days)</Label>
								<Input
									id="retentionDays"
									type="number"
									bind:value={backupConfig.retentionDays}
									min={0}
								/>
								<p class="text-xs text-muted-foreground">
									Delete backups older than this. 0 = keep forever
								</p>
							</div>
							<div class="space-y-2">
								<Label for="minBackups">Min backups</Label>
								<Input
									id="minBackups"
									type="number"
									bind:value={backupConfig.minBackups}
									min={0}
									disabled={backupConfig.retentionDays <= 0}
								/>
								<p class="text-xs text-muted-foreground">
									Never expire by age below this many, even past retention
								</p>
							</div>
							<div class="space-y-2">
								<Label for="maxBackups">Max backups</Label>
								<Input id="maxBackups" type="number" bind:value={backupConfig.maxBackups} min={0} />
								<p class="text-xs text-muted-foreground">
									Hard cap, oldest deleted first. 0 = unlimited
								</p>
							</div>
						</div>
						<p class="text-xs text-muted-foreground">
							World saving is automatically paused and flushed while the backup runs, then
							re-enabled.
						</p>
					{:else if taskType === TaskType.WEBHOOK}
						<div class="space-y-2">
							<Label for="url">Webhook URL *</Label>
							<Input
								id="url"
								bind:value={webhookConfig.url}
								placeholder="https://example.com/webhook"
								class="font-mono"
							/>
							<p class="text-xs text-muted-foreground">
								The endpoint the request is sent to. Discord/Slack/Teams/ntfy URLs are auto-detected
								for the default payload preset.
							</p>
						</div>
					{:else}
						<div class="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
							No additional configuration required for this task type.
						</div>
					{/if}
				{:else if activeSection === 'payload' && taskType === TaskType.WEBHOOK}
					<div class="flex items-center justify-between gap-4 rounded-lg border p-3">
						<div>
							<p class="text-sm font-medium">Customize payload</p>
							<p class="mt-0.5 text-xs text-muted-foreground">
								{#if customizePayload}
									Using a custom payload template
								{:else}
									Using the default {presetLabels[getDefaultPresetKey(webhookConfig.url)] ||
										'Generic'}
									preset
								{/if}
							</p>
						</div>
						<Switch
							checked={customizePayload}
							onCheckedChange={(checked) => {
								customizePayload = checked;
								if (checked && !webhookConfig.payloadTemplate) {
									webhookConfig.payloadTemplate = getDefaultTemplate(webhookConfig.url);
								}
							}}
						/>
					</div>

					<div class={!customizePayload ? 'pointer-events-none opacity-40' : ''}>
						<p class="stat-label mb-1.5">Presets</p>
						<div class="flex flex-wrap gap-1">
							{#each Object.keys(presetLabels) as key (key)}
								{#if webhookTemplatePresets[key]}
									<Button
										variant="outline"
										size="sm"
										class="h-7 text-xs"
										onclick={() => applyPreset(key)}
									>
										{presetLabels[key]}
									</Button>
								{/if}
							{/each}
						</div>
					</div>

					<div class={!customizePayload ? 'pointer-events-none opacity-50' : ''}>
						<CodeEditor
							value={displayValue}
							language="json-template"
							readOnly={!customizePayload}
							height="340px"
							onChange={(v) => {
								if (customizePayload) webhookConfig.payloadTemplate = v;
							}}
						/>
					</div>

					<div class={!customizePayload ? 'opacity-40' : ''}>
						<p class="stat-label mb-1.5">Available variables</p>
						<div
							class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 rounded-md border bg-muted/20 p-3 font-mono text-xs text-muted-foreground"
						>
							{#each TEMPLATE_VARIABLES as [variable, description] (variable)}
								<button
									type="button"
									class="cursor-pointer text-left transition-colors hover:text-foreground"
									title="Copy {variable}"
									onclick={() => copyVariable(variable)}>{variable}</button
								>
								<span class="font-sans">{description}</span>
							{/each}
						</div>
					</div>
				{:else if activeSection === 'schedule'}
					<div class="space-y-2">
						<Label>Schedule type</Label>
						<Select.Root
							type="single"
							name="scheduleType"
							value={scheduleType.toString()}
							onValueChange={(v) => {
								if (v) scheduleType = parseInt(v) as ScheduleType;
							}}
						>
							<Select.Trigger class="w-full">
								{getScheduleTypeLabel(scheduleType)}
							</Select.Trigger>
							<Select.Content>
								{#each SCHEDULE_TYPE_OPTIONS as s (s)}
									<Select.Item value={s.toString()} label={getScheduleTypeLabel(s)}>
										{getScheduleTypeLabel(s)}
									</Select.Item>
								{/each}
							</Select.Content>
						</Select.Root>
					</div>

					{#if scheduleType === ScheduleType.CRON}
						<div class="space-y-2">
							<Label for="cronExpr">Cron expression *</Label>
							<Input
								id="cronExpr"
								bind:value={cronExpr}
								placeholder="0 0 * * *"
								class="font-mono"
							/>
							<p class="text-xs text-muted-foreground">
								Format: minute hour day month weekday (e.g., "0 0 * * *" for daily at midnight)
							</p>
						</div>
					{:else if scheduleType === ScheduleType.INTERVAL}
						<div class="space-y-2">
							<Label for="intervalSecs">Interval (seconds)</Label>
							<Input id="intervalSecs" type="number" bind:value={intervalSecs} min={60} />
							<p class="text-xs text-muted-foreground">
								Minimum 60 seconds. Current: every {formatInterval(intervalSecs)}
							</p>
						</div>
					{:else if scheduleType === ScheduleType.ONCE}
						<div class="space-y-2">
							<Label for="runAt">Run at</Label>
							<Input id="runAt" type="datetime-local" bind:value={runAt} />
							<p class="text-xs text-muted-foreground">
								The task runs once at this time, then is disabled
							</p>
						</div>
					{:else if scheduleType === ScheduleType.EVENT}
						<div class="space-y-2">
							<Label>Events *</Label>
							<div class="space-y-1 rounded-lg border bg-muted/20 p-2">
								{#each SERVER_EVENT_TYPES as { type, label, description } (type)}
									<label
										class="flex cursor-pointer items-center gap-3 rounded-md p-2 transition-colors hover:bg-accent/40"
									>
										<Checkbox
											checked={eventTriggers.includes(type)}
											onCheckedChange={() => toggleEventTrigger(type)}
										/>
										<div>
											<span class="text-sm font-medium">{label}</span>
											<p class="text-xs text-muted-foreground">{description}</p>
										</div>
									</label>
								{/each}
							</div>
							<p class="text-xs text-muted-foreground">
								The task runs whenever any selected event fires.
							</p>
						</div>
					{/if}
				{:else if activeSection === 'advanced'}
					{#if taskType === TaskType.WEBHOOK}
						<div class="space-y-2">
							<Label for="secret">Secret (optional)</Label>
							<Input
								id="secret"
								type="password"
								bind:value={webhookSecret}
								placeholder={originalWebhookHasSecret ? '(unchanged)' : 'HMAC signing secret'}
							/>
							<p class="text-xs text-muted-foreground">
								Signs the payload with HMAC-SHA256 so the receiver can verify it.
							</p>
						</div>

						<div class="grid gap-4 sm:grid-cols-3">
							<div class="space-y-2">
								<Label for="maxRetries">Max retries</Label>
								<Input
									id="maxRetries"
									type="number"
									bind:value={webhookConfig.maxRetries}
									min={0}
									max={10}
								/>
								<p class="text-xs text-muted-foreground">Delivery attempts before giving up</p>
							</div>
							<div class="space-y-2">
								<Label for="retryDelayMs">Retry delay (ms)</Label>
								<Input
									id="retryDelayMs"
									type="number"
									bind:value={webhookConfig.retryDelayMs}
									min={100}
									max={60000}
								/>
								<p class="text-xs text-muted-foreground">Wait between delivery attempts</p>
							</div>
							<div class="space-y-2">
								<Label for="webhookTimeout">Timeout (ms)</Label>
								<Input
									id="webhookTimeout"
									type="number"
									bind:value={webhookConfig.timeoutMs}
									min={1000}
									max={30000}
								/>
								<p class="text-xs text-muted-foreground">Per-attempt request timeout</p>
							</div>
						</div>
					{:else}
						<div class="space-y-2">
							<Label for="timeout">Timeout (seconds)</Label>
							<Input id="timeout" type="number" bind:value={timeout} min={10} max={3600} />
							<p class="text-xs text-muted-foreground">
								Maximum execution time before the task is cancelled
							</p>
						</div>

						<div class="grid gap-4 sm:grid-cols-2">
							<div class="space-y-2">
								<Label for="retryCount">Retry count</Label>
								<Input id="retryCount" type="number" bind:value={retryCount} min={0} max={10} />
								<p class="text-xs text-muted-foreground">
									Times to retry on failure. 0 = no retries
								</p>
							</div>
							<div class="space-y-2">
								<Label for="retryDelay">Retry delay (seconds)</Label>
								<Input id="retryDelay" type="number" bind:value={retryDelay} min={1} />
								<p class="text-xs text-muted-foreground">Wait between retry attempts</p>
							</div>
						</div>

						<label
							class="flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors hover:bg-accent/40"
						>
							<Switch bind:checked={requireOnline} class="mt-0.5" />
							<div class="space-y-0.5">
								<span class="text-sm font-medium">Require server online</span>
								<p class="text-xs text-muted-foreground">
									Skip this task when the server is offline
								</p>
							</div>
						</label>
					{/if}
				{/if}
			</div>

			{#snippet footer()}
				<Button variant="ghost" onclick={closeDialog}>Cancel</Button>
				<Button onclick={saveTask} disabled={!taskName.trim() || creating} class="min-w-28">
					{#if creating}
						<Loader2 class="size-4 animate-spin" />
						{selectedTask ? 'Saving...' : 'Creating...'}
					{:else}
						{selectedTask ? 'Save changes' : 'Create task'}
					{/if}
				</Button>
			{/snippet}
		</SectionedDialogLayout>
	</Dialog.Content>
</Dialog.Root>

<!-- History dialog -->
<Dialog.Root bind:open={showHistoryDialog}>
	<Dialog.Content class="max-h-[80vh] overflow-y-auto sm:max-w-2xl">
		<Dialog.Header>
			<Dialog.Title>
				{selectedTask ? `Task history: ${selectedTask.name}` : 'Recent runs'}
			</Dialog.Title>
			<Dialog.Description>
				{selectedTask ? 'Recent execution history for this task' : 'Latest runs across all tasks'}
			</Dialog.Description>
		</Dialog.Header>

		{#if historyLoading}
			<div class="flex items-center justify-center py-10">
				<Loader2 class="size-6 animate-spin text-muted-foreground" />
			</div>
		{:else if taskHistory.length === 0}
			<EmptyState icon={History} title="No execution history yet" class="py-10" />
		{:else}
			<div class="space-y-2">
				{#each taskHistory as execution (execution.id)}
					{@const badge = executionBadge(execution.status)}
					{@const StatusIcon = badge.icon}
					<div class="rounded-lg border bg-card p-3">
						<div class="flex items-start justify-between gap-2">
							<div class="flex items-center gap-2">
								<Badge variant="outline" class={badge.class}>
									<StatusIcon
										class="size-3 {execution.status === ExecutionStatus.RUNNING
											? 'animate-spin'
											: ''}"
									/>
									{getExecutionStatusLabel(execution.status)}
								</Badge>
								<span class="text-xs text-muted-foreground">
									{enumLabel(TaskTriggerSchema, execution.trigger || TaskTrigger.SCHEDULED)}
								</span>
							</div>
							{#if execution.status === ExecutionStatus.RUNNING}
								<Button
									variant="outline"
									size="sm"
									class="h-6 px-2 text-xs"
									onclick={() => cancelExecution(execution.id)}
								>
									<X class="size-3" />
									Cancel
								</Button>
							{:else if execution.duration}
								<span class="tabular text-xs text-muted-foreground">
									{formatDuration(execution.duration)}
								</span>
							{/if}
						</div>
						<div class="tabular mt-1 text-xs text-muted-foreground">
							{formatDateTime(execution.startedAt)}
						</div>
						{#if execution.output}
							<div
								class="mt-2 max-h-24 overflow-y-auto rounded-md border bg-muted/40 p-2 font-mono text-xs whitespace-pre-wrap"
							>
								{execution.output}
							</div>
						{/if}
						{#if execution.error}
							<div
								class="mt-2 rounded-md border border-status-danger/30 bg-status-danger/10 p-2 text-xs text-status-danger"
							>
								{execution.error}
							</div>
						{/if}
					</div>
				{/each}
			</div>
		{/if}

		<Dialog.Footer>
			<Button variant="outline" onclick={closeHistory}>Close</Button>
		</Dialog.Footer>
	</Dialog.Content>
</Dialog.Root>

<ConfirmDialog
	bind:open={deleteOpen}
	title="Delete {deleteTarget?.name ?? 'task'}?"
	description="The task will no longer run. This cannot be undone."
	confirmLabel="Delete task"
	destructive
	onConfirm={confirmDelete}
/>
