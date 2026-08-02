package services

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/scheduler"
	"github.com/discohaus/discopanel/internal/webhook"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/protometa"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Compile-time check that TaskService implements the interface
var _ discopanelv1connect.TaskServiceHandler = (*TaskService)(nil)

// TaskService implements the Task service
type TaskService struct {
	store     *storage.Store
	scheduler *scheduler.Scheduler
	rec       *metrics.Recorder
	log       *logger.Logger
}

// NewTaskService creates a new task service
func NewTaskService(store *storage.Store, sched *scheduler.Scheduler, rec *metrics.Recorder, log *logger.Logger) *TaskService {
	return &TaskService{
		store:     store,
		scheduler: sched,
		rec:       rec,
		log:       log,
	}
}

// ListTasks lists all tasks for a server
func (s *TaskService) ListTasks(ctx context.Context, req *connect.Request[v1.ListTasksRequest]) (*connect.Response[v1.ListTasksResponse], error) {
	var tasks []*v1.ScheduledTask
	var err error
	if req.Msg.ServerId != "" {
		tasks, err = s.store.ListServerScheduledTasks(ctx, req.Msg.ServerId)
	} else {
		tasks, err = s.store.ListScheduledTasks(ctx)
	}
	if err != nil {
		s.log.Error("Failed to list tasks: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list tasks"))
	}

	protoTasks := make([]*v1.ScheduledTask, len(tasks))
	for i, task := range tasks {
		task.Server = nil
		protoTasks[i] = task
	}

	return connect.NewResponse(&v1.ListTasksResponse{
		Tasks: protoTasks,
	}), nil
}

// GetTask gets a specific task
func (s *TaskService) GetTask(ctx context.Context, req *connect.Request[v1.GetTaskRequest]) (*connect.Response[v1.GetTaskResponse], error) {
	task, err := s.store.GetScheduledTask(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task not found"))
	}

	task.Server = nil
	return connect.NewResponse(&v1.GetTaskResponse{
		Task: task,
	}), nil
}

// CreateTask creates a new scheduled task
func (s *TaskService) CreateTask(ctx context.Context, req *connect.Request[v1.CreateTaskRequest]) (*connect.Response[v1.CreateTaskResponse], error) {
	msg := req.Msg

	// Validate server exists
	_, err := getServer(ctx, s.store, msg.ServerId)
	if err != nil {
		return nil, err
	}

	// Validate required fields
	if msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}

	// Validate cron expression if using cron schedule
	scheduleType := msg.Schedule
	if scheduleType == v1.ScheduleType_SCHEDULE_TYPE_CRON && msg.CronExpr != "" {
		if err := s.scheduler.ValidateCronExpr(msg.CronExpr); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid cron expression: %v", err))
		}
	}

	// Validate event-triggered tasks
	taskType := msg.TaskType
	eventTriggers := msg.EventTriggers
	if scheduleType == v1.ScheduleType_SCHEDULE_TYPE_EVENT && len(eventTriggers) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one event_trigger is required for event-scheduled tasks"))
	}

	// Validate webhook task config
	if taskType == v1.TaskType_TASK_TYPE_WEBHOOK {
		if err := validateWebhookConfig(msg.WebhookConfig); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}

	// Create task
	task := &v1.ScheduledTask{
		Id:            uuid.New().String(),
		ServerId:      msg.ServerId,
		Name:          msg.Name,
		Description:   msg.Description,
		TaskType:      taskType,
		Status:        v1.TaskStatus_TASK_STATUS_ENABLED,
		Schedule:      scheduleType,
		CronExpr:      msg.CronExpr,
		IntervalSecs:  msg.IntervalSecs,
		Timezone:      msg.Timezone,
		CommandConfig: msg.CommandConfig,
		BackupConfig:  msg.BackupConfig,
		ScriptConfig:  msg.ScriptConfig,
		WebhookConfig: msg.WebhookConfig,
		Timeout:       msg.Timeout,
		RetryCount:    msg.RetryCount,
		RetryDelay:    msg.RetryDelay,
		RequireOnline: msg.RequireOnline,
		EventTriggers: eventTriggers,
	}

	// Set defaults
	if task.Timezone == "" {
		task.Timezone = "UTC"
	}
	if task.Timeout == 0 {
		task.Timeout = 300 // 5 minutes default
	}

	// Set run_at for once schedule
	if msg.RunAt != nil {
		task.RunAt = msg.RunAt
	}

	// Bad schedules reject instead of never running
	nextRun, err := s.scheduler.CalculateNextRun(task)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid schedule: %w", err))
	}
	task.NextRun = nextRunPb(nextRun)

	// Save to database
	if err := s.store.CreateScheduledTask(ctx, task); err != nil {
		s.log.Error("Failed to create task: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create task"))
	}

	s.log.Info("Created scheduled task: %s for server %s", task.Name, task.ServerId)
	s.rec.Record(ctx, task.ServerId, v1.ServerActionKind_SERVER_ACTION_KIND_TASK_CREATE, metrics.Attrs{"task": task.Name, "type": protometa.Name(task.TaskType)}, "created task %q", task.Name)

	task.Server = nil
	return connect.NewResponse(&v1.CreateTaskResponse{
		Task: task,
	}), nil
}

// UpdateTask updates an existing task
func (s *TaskService) UpdateTask(ctx context.Context, req *connect.Request[v1.UpdateTaskRequest]) (*connect.Response[v1.UpdateTaskResponse], error) {
	msg := req.Msg

	task, err := s.store.GetScheduledTask(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task not found"))
	}

	// Update fields if provided
	if msg.Name != nil {
		task.Name = *msg.Name
	}
	if msg.Description != nil {
		task.Description = *msg.Description
	}
	if msg.TaskType != nil {
		task.TaskType = *msg.TaskType
	}
	if msg.Schedule != nil {
		task.Schedule = *msg.Schedule
	}
	if msg.CronExpr != nil {
		// Validate cron expression
		if task.Schedule == v1.ScheduleType_SCHEDULE_TYPE_CRON && *msg.CronExpr != "" {
			if err := s.scheduler.ValidateCronExpr(*msg.CronExpr); err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid cron expression: %v", err))
			}
		}
		task.CronExpr = *msg.CronExpr
	}
	if msg.IntervalSecs != nil {
		task.IntervalSecs = *msg.IntervalSecs
	}
	if msg.RunAt != nil {
		task.RunAt = msg.RunAt
	}
	if msg.Timezone != nil {
		task.Timezone = *msg.Timezone
	}
	if msg.CommandConfig != nil {
		task.CommandConfig = msg.CommandConfig
	}
	if msg.BackupConfig != nil {
		task.BackupConfig = msg.BackupConfig
	}
	if msg.ScriptConfig != nil {
		task.ScriptConfig = msg.ScriptConfig
	}
	if msg.WebhookConfig != nil {
		task.WebhookConfig = msg.WebhookConfig
	}
	if msg.Timeout != nil {
		task.Timeout = *msg.Timeout
	}
	if msg.RetryCount != nil {
		task.RetryCount = *msg.RetryCount
	}
	if msg.RetryDelay != nil {
		task.RetryDelay = *msg.RetryDelay
	}
	if msg.RequireOnline != nil {
		task.RequireOnline = *msg.RequireOnline
	}
	if msg.ClearEventTriggers {
		task.EventTriggers = nil
	}
	if len(msg.EventTriggers) > 0 {
		task.EventTriggers = msg.EventTriggers
	}

	// Validate event-triggered tasks
	if task.Schedule == v1.ScheduleType_SCHEDULE_TYPE_EVENT && len(task.EventTriggers) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one event_trigger is required for event-scheduled tasks"))
	}

	// Validate webhook task config
	if task.TaskType == v1.TaskType_TASK_TYPE_WEBHOOK {
		if err := validateWebhookConfig(task.WebhookConfig); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}

	// Bad schedules reject instead of never running
	nextRun, err := s.scheduler.CalculateNextRun(task)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid schedule: %w", err))
	}
	task.NextRun = nextRunPb(nextRun)

	// Save changes
	if err := s.store.UpdateScheduledTask(ctx, task); err != nil {
		s.log.Error("Failed to update task: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update task"))
	}

	s.log.Info("Updated scheduled task: %s", task.Name)
	s.rec.Record(ctx, task.ServerId, v1.ServerActionKind_SERVER_ACTION_KIND_TASK_UPDATE, metrics.Attrs{"task": task.Name}, "updated task %q", task.Name)

	task.Server = nil
	return connect.NewResponse(&v1.UpdateTaskResponse{
		Task: task,
	}), nil
}

// DeleteTask deletes a task
func (s *TaskService) DeleteTask(ctx context.Context, req *connect.Request[v1.DeleteTaskRequest]) (*connect.Response[v1.DeleteTaskResponse], error) {
	task, err := s.store.GetScheduledTask(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task not found"))
	}

	if err := s.store.DeleteScheduledTask(ctx, req.Msg.Id); err != nil {
		s.log.Error("Failed to delete task: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete task"))
	}

	s.log.Info("Deleted scheduled task: %s", task.Name)
	s.rec.Record(ctx, task.ServerId, v1.ServerActionKind_SERVER_ACTION_KIND_TASK_DELETE, metrics.Attrs{"task": task.Name}, "deleted task %q", task.Name)

	return connect.NewResponse(&v1.DeleteTaskResponse{}), nil
}

// ToggleTask toggles task enabled/disabled status
func (s *TaskService) ToggleTask(ctx context.Context, req *connect.Request[v1.ToggleTaskRequest]) (*connect.Response[v1.ToggleTaskResponse], error) {
	task, err := s.store.GetScheduledTask(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task not found"))
	}

	task.Status = req.Msg.Status

	// Recalculate next run if enabling
	if task.Status == v1.TaskStatus_TASK_STATUS_ENABLED {
		nextRun, err := s.scheduler.CalculateNextRun(task)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid schedule: %w", err))
		}
		task.NextRun = nextRunPb(nextRun)
	}

	if err := s.store.UpdateScheduledTask(ctx, task); err != nil {
		s.log.Error("Failed to toggle task: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to toggle task"))
	}

	s.log.Info("Toggled task %s to status %s", task.Name, task.Status)
	s.rec.Record(ctx, task.ServerId, v1.ServerActionKind_SERVER_ACTION_KIND_TASK_TOGGLE, metrics.Attrs{"task": task.Name, "status": protometa.Name(task.Status)}, "set task %q to %s", task.Name, protometa.Name(task.Status))

	task.Server = nil
	return connect.NewResponse(&v1.ToggleTaskResponse{
		Task: task,
	}), nil
}

// TriggerTask manually triggers a task execution
func (s *TaskService) TriggerTask(ctx context.Context, req *connect.Request[v1.TriggerTaskRequest]) (*connect.Response[v1.TriggerTaskResponse], error) {
	execution, err := s.scheduler.TriggerTask(ctx, req.Msg.Id)
	if err != nil {
		s.log.Error("Failed to trigger task: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to trigger task: %v", err))
	}

	execution.Task = nil
	execution.Server = nil
	return connect.NewResponse(&v1.TriggerTaskResponse{
		Execution: execution,
	}), nil
}

// ListTaskExecutions gets execution history for a task
func (s *TaskService) ListTaskExecutions(ctx context.Context, req *connect.Request[v1.ListTaskExecutionsRequest]) (*connect.Response[v1.ListTaskExecutionsResponse], error) {
	limit := int(req.Msg.Limit)
	if limit == 0 {
		limit = 50 // Default limit
	}

	executions, err := s.store.ListTaskExecutions(ctx, req.Msg.TaskId, limit)
	if err != nil {
		s.log.Error("Failed to list task executions: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list executions"))
	}

	protoExecs := make([]*v1.TaskExecution, len(executions))
	for i, exec := range executions {
		exec.Task = nil
		exec.Server = nil
		protoExecs[i] = exec
	}

	return connect.NewResponse(&v1.ListTaskExecutionsResponse{
		Executions: protoExecs,
	}), nil
}

// ListServerExecutions gets execution history for a server
func (s *TaskService) ListServerExecutions(ctx context.Context, req *connect.Request[v1.ListServerExecutionsRequest]) (*connect.Response[v1.ListServerExecutionsResponse], error) {
	limit := int(req.Msg.Limit)
	if limit == 0 {
		limit = 50 // Default limit
	}

	executions, err := s.store.ListServerTaskExecutions(ctx, req.Msg.ServerId, limit)
	if err != nil {
		s.log.Error("Failed to list server executions: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list executions"))
	}

	protoExecs := make([]*v1.TaskExecution, len(executions))
	for i, exec := range executions {
		exec.Task = nil
		exec.Server = nil
		protoExecs[i] = exec
	}

	return connect.NewResponse(&v1.ListServerExecutionsResponse{
		Executions: protoExecs,
	}), nil
}

// GetTaskExecution gets a specific execution
func (s *TaskService) GetTaskExecution(ctx context.Context, req *connect.Request[v1.GetTaskExecutionRequest]) (*connect.Response[v1.GetTaskExecutionResponse], error) {
	execution, err := s.store.GetTaskExecution(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("execution not found"))
	}

	execution.Task = nil
	execution.Server = nil
	return connect.NewResponse(&v1.GetTaskExecutionResponse{
		Execution: execution,
	}), nil
}

// CancelExecution cancels a running execution
func (s *TaskService) CancelExecution(ctx context.Context, req *connect.Request[v1.CancelExecutionRequest]) (*connect.Response[v1.CancelExecutionResponse], error) {
	if err := s.scheduler.CancelExecution(req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("execution not found or already finished"))
	}

	// Wait briefly for cancellation to be recorded
	execution, err := s.store.GetTaskExecution(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("execution not found"))
	}

	execution.Task = nil
	execution.Server = nil
	return connect.NewResponse(&v1.CancelExecutionResponse{
		Execution: execution,
	}), nil
}

// Parses webhook task config and validates required fields
func validateWebhookConfig(wcfg *v1.WebhookTaskConfig) error {
	if wcfg.GetUrl() == "" {
		return fmt.Errorf("webhook URL is required")
	}
	if wcfg.PayloadTemplate != "" {
		if err := webhook.ValidateTemplate(wcfg.PayloadTemplate); err != nil {
			return fmt.Errorf("invalid payload template: %v", err)
		}
	}
	return nil
}

// GetSchedulerStatus gets the scheduler status
func (s *TaskService) GetSchedulerStatus(ctx context.Context, req *connect.Request[v1.GetSchedulerStatusRequest]) (*connect.Response[v1.GetSchedulerStatusResponse], error) {
	return connect.NewResponse(s.scheduler.GetStatus()), nil
}

// Optional next run as a proto timestamp
func nextRunPb(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}
