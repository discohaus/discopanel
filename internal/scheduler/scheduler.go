package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/discohaus/discopanel/internal/command"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/lifecycle"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/webhook"
	appconfig "github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/events"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Manages scheduled tasks for all servers
type Scheduler struct {
	store         *storage.Store
	docker        *docker.Client
	sender        *command.Sender
	lifecycle     *lifecycle.Manager
	appConfig     *appconfig.Config
	metrics       *metrics.Collector
	rec           *metrics.Recorder
	log           *logger.Logger
	checkInterval time.Duration

	// State management
	running  bool
	mu       sync.RWMutex
	stopChan chan struct{}
	wg       sync.WaitGroup

	// Execution tracking
	runningExecutions map[string]context.CancelFunc // Maps execution id to its cancel func
	inFlightTasks     map[string]bool               // Task ids currently executing
	executionMu       sync.RWMutex

	// Cron parser
	cronParser cron.Parser

	// Stats
	lastCheck time.Time
	nextCheck time.Time
}

// Holds scheduler configuration
type Config struct {
	CheckInterval time.Duration // How often to check for due tasks
}

// Returns default scheduler configuration
func DefaultConfig() Config {
	return Config{
		CheckInterval: 10 * time.Second,
	}
}

// Creates a new task scheduler
func NewScheduler(store *storage.Store, docker *docker.Client, sender *command.Sender, lifecycleManager *lifecycle.Manager, appCfg *appconfig.Config, metricsCollector *metrics.Collector, rec *metrics.Recorder, log *logger.Logger, config ...Config) *Scheduler {
	cfg := DefaultConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	return &Scheduler{
		store:             store,
		docker:            docker,
		sender:            sender,
		lifecycle:         lifecycleManager,
		appConfig:         appCfg,
		metrics:           metricsCollector,
		rec:               rec,
		log:               log,
		checkInterval:     cfg.CheckInterval,
		stopChan:          make(chan struct{}),
		runningExecutions: make(map[string]context.CancelFunc),
		inFlightTasks:     make(map[string]bool),
		cronParser:        cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
	}
}

// Begins the scheduler loop
func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("scheduler already running")
	}

	s.running = true
	s.stopChan = make(chan struct{})

	s.wg.Add(1)
	go s.runLoop()

	s.log.Info("Task scheduler started (check interval: %v)", s.checkInterval)
	return nil
}

// Gracefully stops the scheduler
func (s *Scheduler) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	close(s.stopChan)
	s.mu.Unlock()

	// Cancel running executions first so the wait cannot hang
	s.executionMu.Lock()
	for _, cancel := range s.runningExecutions {
		cancel()
	}
	s.executionMu.Unlock()

	// Wait for the loop and task goroutines to finish
	s.wg.Wait()

	s.executionMu.Lock()
	s.runningExecutions = make(map[string]context.CancelFunc)
	s.executionMu.Unlock()

	s.log.Info("Task scheduler stopped")
	return nil
}

// Returns whether the scheduler is running
func (s *Scheduler) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// Returns current scheduler status
func (s *Scheduler) GetStatus() *v1.GetSchedulerStatusResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	s.executionMu.RLock()
	runningCount := len(s.runningExecutions)
	s.executionMu.RUnlock()

	// Count active tasks
	ctx := context.Background()
	tasks, _ := s.store.ListScheduledTasks(ctx)
	activeCount := 0
	for _, task := range tasks {
		if task.Status == v1.TaskStatus_TASK_STATUS_ENABLED {
			activeCount++
		}
	}

	return &v1.GetSchedulerStatusResponse{
		Running:           s.running,
		ActiveTasks:       int32(activeCount),
		RunningExecutions: int32(runningCount),
		LastCheck:         timestamppb.New(s.lastCheck),
		NextCheck:         timestamppb.New(s.nextCheck),
	}
}

// Main scheduler loop
func (s *Scheduler) runLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	// Execution rows would otherwise grow forever
	pruneTicker := time.NewTicker(24 * time.Hour)
	defer pruneTicker.Stop()
	pruneExecutions := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.store.PruneTaskExecutions(ctx, time.Now().AddDate(0, 0, -30)); err != nil {
			s.log.Error("Failed to prune task executions: %v", err)
		}
	}

	// Run initial check
	s.checkAndRunDueTasks()
	pruneExecutions()

	for {
		select {
		case <-ticker.C:
			s.checkAndRunDueTasks()
		case <-pruneTicker.C:
			pruneExecutions()
		case <-s.stopChan:
			return
		}
	}
}

// Checks for due tasks and executes them
func (s *Scheduler) checkAndRunDueTasks() {
	s.mu.Lock()
	s.lastCheck = time.Now()
	s.nextCheck = s.lastCheck.Add(s.checkInterval)
	s.mu.Unlock()

	ctx := context.Background()

	// Get all due tasks
	tasks, err := s.store.ListDueScheduledTasks(ctx, v1.TaskStatus_TASK_STATUS_ENABLED, time.Now())
	if err != nil {
		s.log.Error("Failed to list due tasks: %v", err)
		return
	}

	for _, task := range tasks {
		// Execute task asynchronously
		s.wg.Add(1)
		go func(t *v1.ScheduledTask) {
			defer s.wg.Done()
			s.executeTask(t, v1.TaskTrigger_TASK_TRIGGER_SCHEDULED, v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_UNSPECIFIED, nil)
		}(task)
	}
}

// Manually triggers a task execution
func (s *Scheduler) TriggerTask(ctx context.Context, taskID string) (*v1.TaskExecution, error) {
	task, err := s.store.GetScheduledTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	execution, err := s.executeTask(task, v1.TaskTrigger_TASK_TRIGGER_MANUAL, v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_UNSPECIFIED, nil)
	return execution, err
}

// Schedulers subscription to the central event bus
func (s *Scheduler) HandleServerEvent(ctx context.Context, event events.Event) {
	tasks, err := s.store.ListEventTriggeredTasks(ctx, event.ServerId, event.Type)
	if err != nil {
		s.log.Error("Failed to list event-triggered tasks for %s: %v", event.Type, err)
		return
	}
	for _, task := range tasks {
		s.wg.Add(1)
		go func(t *v1.ScheduledTask) {
			defer s.wg.Done()
			s.executeTaskForEvent(t, event.Type, event.Data)
		}(task)
	}
}

// Runs a task from an event, threads type to webhooks
func (s *Scheduler) executeTaskForEvent(task *v1.ScheduledTask, eventType v1.TriggeredEventType, eventData map[string]string) {
	s.executeTask(task, v1.TaskTrigger_TASK_TRIGGER_EVENT, eventType, eventData)
}

// Marks a task in flight unless it already is
func (s *Scheduler) tryBeginTask(taskID string) bool {
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	if s.inFlightTasks[taskID] {
		return false
	}
	s.inFlightTasks[taskID] = true
	return true
}

// Clears the task's in-flight mark
func (s *Scheduler) endTask(taskID string) {
	s.executionMu.Lock()
	delete(s.inFlightTasks, taskID)
	s.executionMu.Unlock()
}

// Runs a single task, trigger names what drove it
func (s *Scheduler) executeTask(task *v1.ScheduledTask, trigger v1.TaskTrigger, eventType v1.TriggeredEventType, eventData map[string]string) (*v1.TaskExecution, error) {
	ctx := context.Background()

	if !s.tryBeginTask(task.Id) {
		s.log.Debug("Task %s: skipped, previous run still in flight", task.Name)
		return nil, fmt.Errorf("task %q is already running", task.Name)
	}
	defer s.endTask(task.Id)

	// Advance schedule before running so re-listing never doubles
	if trigger == v1.TaskTrigger_TASK_TRIGGER_SCHEDULED {
		s.updateNextRun(task)
	}

	// Check if server exists
	server, err := s.store.GetServer(ctx, task.ServerId)
	if err != nil {
		s.log.Error("Task %s: server not found: %v", task.Name, err)
		// Orphan tasks disable instead of erroring forever
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if derr := s.store.UpdateScheduledTaskFields(ctx, task.Id, map[string]any{"status": v1.TaskStatus_TASK_STATUS_DISABLED}); derr != nil {
				s.log.Error("Task %s: failed to disable orphan task: %v", task.Name, derr)
			}
		}
		return nil, err
	}
	s.store.HydrateProxyPorts(ctx, server)

	// Checks if server is online, webhook tasks always fire
	if task.RequireOnline && task.TaskType != v1.TaskType_TASK_TYPE_WEBHOOK && server.Status != v1.ServerStatus_SERVER_STATUS_RUNNING {
		s.log.Debug("Task %s: skipped (server offline)", task.Name)

		// Create skipped execution record
		execution := &v1.TaskExecution{
			Id:        uuid.New().String(),
			TaskId:    task.Id,
			ServerId:  task.ServerId,
			Status:    v1.ExecutionStatus_EXECUTION_STATUS_SKIPPED,
			StartedAt: timestamppb.Now(),
			Trigger:   trigger,
			Error:     "server offline",
		}
		execution.EndedAt = timestamppb.Now()
		s.store.CreateTaskExecution(ctx, execution)
		return execution, nil
	}

	// Create execution record
	execution := &v1.TaskExecution{
		Id:        uuid.New().String(),
		TaskId:    task.Id,
		ServerId:  task.ServerId,
		Status:    v1.ExecutionStatus_EXECUTION_STATUS_RUNNING,
		StartedAt: timestamppb.Now(),
		Trigger:   trigger,
	}
	if err := s.store.CreateTaskExecution(ctx, execution); err != nil {
		s.log.Error("Task %s: failed to create execution record: %v", task.Name, err)
		return nil, err
	}

	// Create cancellable context with timeout
	timeout := time.Duration(task.Timeout) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Minute // Default timeout
	}
	execCtx, cancel := context.WithTimeout(metrics.WithTrace(metrics.WithSource(ctx, "scheduler")), timeout)

	// Track running execution
	s.executionMu.Lock()
	s.runningExecutions[execution.Id] = cancel
	s.executionMu.Unlock()

	defer func() {
		cancel()
		s.executionMu.Lock()
		delete(s.runningExecutions, execution.Id)
		s.executionMu.Unlock()
	}()

	s.log.Info("Task %s: executing on server %s (trigger: %s)", task.Name, server.Name, trigger)

	// Executes the task, retrying on failure if configured
	var output string
	var execErr error

	for attempt := 0; ; attempt++ {
		output, execErr = s.runTaskType(execCtx, server, task, eventType, eventData)
		if execErr == nil || attempt >= int(task.RetryCount) || execCtx.Err() != nil {
			break
		}

		retryDelay := time.Duration(task.RetryDelay) * time.Second
		if retryDelay <= 0 {
			retryDelay = time.Minute
		}
		s.log.Warn("Task %s: attempt %d failed, retrying in %v: %v", task.Name, attempt+1, retryDelay, execErr)

		select {
		case <-execCtx.Done():
		case <-time.After(retryDelay):
		}
		if execCtx.Err() != nil {
			break
		}
		execution.RetryNum = int32(attempt + 1)
	}

	// Update execution record
	endTime := time.Now()
	execution.EndedAt = timestamppb.New(endTime)
	execution.Duration = endTime.Sub(execution.StartedAt.AsTime()).Milliseconds()
	execution.Output = output

	if execErr != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			execution.Status = v1.ExecutionStatus_EXECUTION_STATUS_TIMEOUT
			execution.Error = "execution timed out"
		} else if execCtx.Err() == context.Canceled {
			execution.Status = v1.ExecutionStatus_EXECUTION_STATUS_CANCELLED
			execution.Error = "execution cancelled"
		} else {
			execution.Status = v1.ExecutionStatus_EXECUTION_STATUS_FAILED
			execution.Error = execErr.Error()
		}
		s.log.Error("Task %s: failed: %v", task.Name, execErr)
	} else {
		execution.Status = v1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
		s.log.Info("Task %s: completed successfully", task.Name)
	}

	s.store.UpdateTaskExecution(ctx, execution)

	return execution, execErr
}

// Dispatches a single execution attempt to its executor
func (s *Scheduler) runTaskType(ctx context.Context, server *v1.Server, task *v1.ScheduledTask, eventType v1.TriggeredEventType, eventData map[string]string) (string, error) {
	switch task.TaskType {
	case v1.TaskType_TASK_TYPE_COMMAND:
		return s.executeCommandTask(ctx, server, task)
	case v1.TaskType_TASK_TYPE_RESTART:
		return s.executeRestartTask(ctx, server, task)
	case v1.TaskType_TASK_TYPE_START:
		return s.executeStartTask(ctx, server, task)
	case v1.TaskType_TASK_TYPE_STOP:
		return s.executeStopTask(ctx, server, task)
	case v1.TaskType_TASK_TYPE_BACKUP:
		return s.executeBackupTask(ctx, server, task)
	case v1.TaskType_TASK_TYPE_SCRIPT:
		return s.executeScriptTask(ctx, server, task)
	case v1.TaskType_TASK_TYPE_WEBHOOK:
		return s.executeWebhookTask(ctx, server, task, eventType, eventData)
	default:
		return "", fmt.Errorf("unknown task type: %s", task.TaskType)
	}
}

// Cancels a running execution
func (s *Scheduler) CancelExecution(executionID string) error {
	s.executionMu.RLock()
	cancel, exists := s.runningExecutions[executionID]
	s.executionMu.RUnlock()

	if !exists {
		return fmt.Errorf("execution not found or already finished")
	}

	cancel()
	return nil
}

// Calculates and persists the next run time
func (s *Scheduler) updateNextRun(task *v1.ScheduledTask) {
	ctx := context.Background()
	now := time.Now()
	var nextRun *time.Time
	var status v1.TaskStatus

	switch task.Schedule {
	case v1.ScheduleType_SCHEDULE_TYPE_CRON:
		if task.CronExpr != "" {
			schedule, err := s.cronParser.Parse(task.CronExpr)
			if err == nil {
				next := schedule.Next(now)
				nextRun = &next
			}
		}
	case v1.ScheduleType_SCHEDULE_TYPE_INTERVAL:
		if task.IntervalSecs > 0 {
			next := now.Add(time.Duration(task.IntervalSecs) * time.Second)
			nextRun = &next
		}
	case v1.ScheduleType_SCHEDULE_TYPE_ONCE:
		// Once tasks never repeat, disable on first fire
		task.Status = v1.TaskStatus_TASK_STATUS_DISABLED
		status = v1.TaskStatus_TASK_STATUS_DISABLED
		nextRun = nil
	case v1.ScheduleType_SCHEDULE_TYPE_EVENT:
		// Event-triggered tasks have no time-based next run
		nextRun = nil
	}

	fields := map[string]any{"next_run": nextRun, "last_run": now}
	if status != v1.TaskStatus_TASK_STATUS_UNSPECIFIED {
		fields["status"] = status
	}
	if err := s.store.UpdateScheduledTaskFields(ctx, task.Id, fields); err != nil {
		s.log.Error("Task %s: failed to persist next run: %v", task.Name, err)
	}
}

// Task type executors

func (s *Scheduler) executeCommandTask(ctx context.Context, server *v1.Server, task *v1.ScheduledTask) (string, error) {
	config := task.GetCommandConfig()

	if config.GetCommand() == "" {
		return "", fmt.Errorf("no command specified")
	}

	if server.ContainerId == "" {
		return "", fmt.Errorf("server has no container")
	}

	output, err := s.sender.SendCommand(ctx, server.Id, config.Command)
	if err == nil {
		s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_TASK_COMMAND, metrics.Attrs{"command": config.Command, "task": task.Name}, "ran command %q (task %q)", config.Command, task.Name)
	}
	return output, err
}

func (s *Scheduler) executeRestartTask(ctx context.Context, server *v1.Server, _ *v1.ScheduledTask) (string, error) {
	if err := s.lifecycle.Restart(ctx, server.Id); err != nil {
		return "", err
	}
	return "server restarted successfully", nil
}

func (s *Scheduler) executeStartTask(ctx context.Context, server *v1.Server, _ *v1.ScheduledTask) (string, error) {
	if err := s.lifecycle.Start(ctx, server.Id); err != nil {
		return "", err
	}
	return "server started successfully", nil
}

func (s *Scheduler) executeStopTask(ctx context.Context, server *v1.Server, _ *v1.ScheduledTask) (string, error) {
	if err := s.lifecycle.Stop(ctx, server.Id); err != nil {
		return "", err
	}
	return "server stopped successfully", nil
}

func (s *Scheduler) executeScriptTask(ctx context.Context, server *v1.Server, task *v1.ScheduledTask) (string, error) {
	// Script tasks execute inside the container
	config := task.GetScriptConfig()

	if config.GetScriptPath() == "" {
		return "", fmt.Errorf("no script/executable specified")
	}

	if server.ContainerId == "" {
		return "", fmt.Errorf("server has no container")
	}

	execCmd := []string{config.ScriptPath}
	stdout, stderr, err := s.docker.Exec(ctx, server.ContainerId, append(execCmd, config.Args...))
	if err != nil {
		return "", err
	}
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_TASK_SCRIPT, metrics.Attrs{"script": config.ScriptPath, "task": task.Name}, "ran script %s (task %q)", config.ScriptPath, task.Name)
	if strings.TrimSpace(stderr) != "" {
		return stdout + "\n[stderr]\n" + stderr, nil
	}
	return stdout, nil
}

// Calculates the next run time based on schedule
func (s *Scheduler) CalculateNextRun(task *v1.ScheduledTask) (*time.Time, error) {
	now := time.Now()

	switch task.Schedule {
	case v1.ScheduleType_SCHEDULE_TYPE_CRON:
		if task.CronExpr == "" {
			return nil, fmt.Errorf("cron expression required")
		}
		schedule, err := s.cronParser.Parse(task.CronExpr)
		if err != nil {
			return nil, fmt.Errorf("invalid cron expression: %w", err)
		}
		next := schedule.Next(now)
		return &next, nil

	case v1.ScheduleType_SCHEDULE_TYPE_INTERVAL:
		if task.IntervalSecs <= 0 {
			return nil, fmt.Errorf("interval must be positive")
		}
		next := now.Add(time.Duration(task.IntervalSecs) * time.Second)
		return &next, nil

	case v1.ScheduleType_SCHEDULE_TYPE_ONCE:
		if task.RunAt == nil {
			return nil, fmt.Errorf("run_at time required for once schedule")
		}
		if task.RunAt.AsTime().Before(now) {
			return nil, fmt.Errorf("run_at time is in the past")
		}
		runAt := task.RunAt.AsTime()
		return &runAt, nil

	case v1.ScheduleType_SCHEDULE_TYPE_EVENT:
		// No scheduled time, execution is triggered via OnEvent
		return nil, nil

	default:
		return nil, fmt.Errorf("unknown schedule type: %s", task.Schedule)
	}
}

// Validates a cron expression
func (s *Scheduler) ValidateCronExpr(expr string) error {
	_, err := s.cronParser.Parse(expr)
	return err
}

func (s *Scheduler) executeWebhookTask(ctx context.Context, server *v1.Server, task *v1.ScheduledTask, eventType v1.TriggeredEventType, eventData map[string]string) (string, error) {
	cfg := task.GetWebhookConfig()
	if cfg.GetUrl() == "" {
		return "", fmt.Errorf("webhook URL is required")
	}

	// Event runs pass the firing event, schedules fall back
	event := eventType
	if event == v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_UNSPECIFIED && len(task.EventTriggers) > 0 {
		event = task.EventTriggers[0]
	}

	// Pull live count from metrics so payloads report players accurately
	if s.metrics != nil {
		if m := s.metrics.GetMetrics(server.Id); m != nil {
			server.PlayersOnline = int32(m.PlayersOnline)
		}
	}

	payload := webhook.BuildPayload(event, server, eventData)

	result := webhook.Deliver(ctx, cfg, payload)
	output := fmt.Sprintf("HTTP %d in %dms (attempt %d)", result.ResponseCode, result.DurationMs, result.Attempts)
	if result.ResponseBody != "" {
		output += "\n" + result.ResponseBody
	}
	if result.Success {
		return output, nil
	}
	return output, fmt.Errorf("%s", result.ErrorMessage)
}
