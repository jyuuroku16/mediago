package engine

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"mediago/internal/model"
	"mediago/internal/storage"
)

// TaskUpdateListener 任务变更监听回调
type TaskUpdateListener func(task *model.Task)

// Scheduler 两级并发下载调度器
type Scheduler struct {
	storage         *storage.Storage
	httpEngine      *HTTPRangeEngine
	hlsEngine       *HLSEngine
	extractorEngine *ExtractorWorkerEngine

	maxActiveTasks int
	activeTasks    map[string]context.CancelFunc
	deletedTasks   map[string]struct{}

	hostConns map[string]int
	mu        sync.Mutex

	listeners []TaskUpdateListener
	queueChan chan *model.Task
	stopCh    chan struct{}
}

// NewScheduler 创建下载调度器
func NewScheduler(s *storage.Storage, maxActive int) *Scheduler {
	if maxActive <= 0 {
		maxActive = 3
	}
	return &Scheduler{
		storage:         s,
		httpEngine:      NewHTTPRangeEngine(),
		hlsEngine:       NewHLSEngine(),
		extractorEngine: NewExtractorWorkerEngine(),
		maxActiveTasks:  maxActive,
		activeTasks:     make(map[string]context.CancelFunc),
		deletedTasks:    make(map[string]struct{}),
		hostConns:       make(map[string]int),
		queueChan:       make(chan *model.Task, 100),
		stopCh:          make(chan struct{}),
	}
}

// Start 启动调度器后台工作协程
func (sc *Scheduler) Start() {
	go sc.scheduleLoop()
}

// Stop 停止调度器
func (sc *Scheduler) Stop() {
	close(sc.stopCh)
	sc.mu.Lock()
	for _, cancel := range sc.activeTasks {
		cancel()
	}
	sc.mu.Unlock()
}

// MaxActiveTasks 获取当前最大并发数
func (sc *Scheduler) MaxActiveTasks() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.maxActiveTasks
}

// SetMaxActiveTasks 动态调整全局最大下载任务并发数 (1~8)
func (sc *Scheduler) SetMaxActiveTasks(n int) {
	if n <= 0 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	sc.mu.Lock()
	sc.maxActiveTasks = n
	sc.mu.Unlock()
}

// OnUpdate 注册任务更新监听器
func (sc *Scheduler) OnUpdate(listener TaskUpdateListener) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.listeners = append(sc.listeners, listener)
}

// isDeleted 判断任务是否已被用户在运行期间删除 (防止引擎协程回写"复活"已删除的记录)
func (sc *Scheduler) isDeleted(id string) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	_, ok := sc.deletedTasks[id]
	return ok
}

func (sc *Scheduler) notifyUpdate(task *model.Task) {
	if sc.isDeleted(task.ID) {
		return
	}
	if err := sc.storage.SaveTask(task); err != nil {
		log.Printf("[scheduler] 保存任务状态失败 (%s): %v", task.ID, err)
	}
	sc.mu.Lock()
	listeners := make([]TaskUpdateListener, len(sc.listeners))
	copy(listeners, sc.listeners)
	sc.mu.Unlock()

	for _, l := range listeners {
		l(task)
	}
}

// Submit 提交新任务或恢复排队
func (sc *Scheduler) Submit(task *model.Task) error {
	task.Status = model.StatusQueued
	task.UpdatedAt = time.Now()
	if err := sc.storage.SaveTask(task); err != nil {
		return err
	}
	sc.notifyUpdate(task)
	sc.queueChan <- task
	return nil
}

// SubmitIdle 添加任务但保持暂停，等待用户手动点击继续 (对应设置中关闭"自动开始")
func (sc *Scheduler) SubmitIdle(task *model.Task) error {
	task.Status = model.StatusPaused
	task.Speed = 0
	task.ErrorMsg = ""
	task.UpdatedAt = time.Now()
	if err := sc.storage.SaveTask(task); err != nil {
		return err
	}
	sc.notifyUpdate(task)
	return nil
}

// CancelTask 彻底取消并删除任务记录
// 先取消运行中的引擎协程并登记删除墓碑，确保其后续进度回写不会"复活"已删除的任务
func (sc *Scheduler) CancelTask(id string) error {
	sc.mu.Lock()
	if cancel, active := sc.activeTasks[id]; active {
		cancel()
		delete(sc.activeTasks, id)
	}
	sc.deletedTasks[id] = struct{}{}
	sc.mu.Unlock()

	return sc.storage.DeleteTask(id)
}

// ReconcileOnStartup 应用启动时修复上次异常退出遗留的任务状态
// autoResume 为 true 时自动断点续传中断的任务，否则统一置为暂停等待用户手动继续
func (sc *Scheduler) ReconcileOnStartup(autoResume bool) {
	tasks, err := sc.storage.ListTasks("")
	if err != nil {
		log.Printf("[scheduler] 启动状态修复读取任务失败: %v", err)
		return
	}
	for _, t := range tasks {
		switch t.Status {
		case model.StatusDownloading, model.StatusQueued, model.StatusResolving,
			model.StatusVerifying, model.StatusProcessing, model.StatusRetryWait, model.StatusWaitingForLogin:
		default:
			continue
		}

		if autoResume {
			t.ErrorMsg = ""
			if err := sc.Submit(t); err != nil {
				log.Printf("[scheduler] 自动恢复中断任务失败 (%s): %v", t.ID, err)
			}
			continue
		}

		t.Status = model.StatusPaused
		t.Speed = 0
		t.ErrorMsg = "应用上次退出导致下载中断，点击继续可断点续传"
		t.UpdatedAt = time.Now()
		sc.notifyUpdate(t)
	}
}

// Pause 暂停任务
func (sc *Scheduler) Pause(id string) error {
	sc.mu.Lock()
	cancel, active := sc.activeTasks[id]
	if active {
		cancel()
		delete(sc.activeTasks, id)
	}
	sc.mu.Unlock()

	task, err := sc.storage.GetTask(id)
	if err != nil {
		return err
	}
	task.Status = model.StatusPaused
	task.Speed = 0
	task.UpdatedAt = time.Now()
	sc.notifyUpdate(task)
	return nil
}

// Resume 恢复暂停的任务
func (sc *Scheduler) Resume(id string) error {
	task, err := sc.storage.GetTask(id)
	if err != nil {
		return err
	}
	if task.Status != model.StatusPaused && task.Status != model.StatusFailed {
		return nil
	}
	task.ErrorMsg = ""
	task.RetryCount = 0 // 手动"继续"视为重新开始，重置自动重试计数
	return sc.Submit(task)
}

// 自动重试策略: 瞬时失败最多自动重试 3 次，退避间隔 5s / 15s / 45s
const maxAutoRetries = 3

// autoRetryDelay 返回第 attempt 次自动重试前的等待时长 (超出重试上限返回 0)
func autoRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 5 * time.Second
	case 2:
		return 15 * time.Second
	case 3:
		return 45 * time.Second
	default:
		return 0
	}
}

// scheduleAutoRetry 延时后自动重新入队
// 期间若用户手动暂停/删除任务，或任务状态已被改写，则放弃本次重试
func (sc *Scheduler) scheduleAutoRetry(id string, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-sc.stopCh:
		return
	case <-timer.C:
	}

	if sc.isDeleted(id) {
		return
	}
	latest, err := sc.storage.GetTask(id)
	if err != nil || latest.Status != model.StatusRetryWait {
		return
	}
	if err := sc.Submit(latest); err != nil {
		log.Printf("[scheduler] 自动重试重新入队失败 (%s): %v", id, err)
	}
}

func (sc *Scheduler) scheduleLoop() {
	for {
		select {
		case <-sc.stopCh:
			return
		case task := <-sc.queueChan:
			sc.waitForSlot()
			sc.startTask(task)
		}
	}
}

func (sc *Scheduler) waitForSlot() {
	for {
		sc.mu.Lock()
		activeCount := len(sc.activeTasks)
		sc.mu.Unlock()

		if activeCount < sc.maxActiveTasks {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (sc *Scheduler) startTask(task *model.Task) {
	sc.mu.Lock()
	_, alreadyActive := sc.activeTasks[task.ID]
	_, deleted := sc.deletedTasks[task.ID]
	sc.mu.Unlock()
	if alreadyActive || deleted {
		return
	}

	// 任务可能在排队期间已被暂停或删除，以数据库中的最新状态为准
	latest, err := sc.storage.GetTask(task.ID)
	if err != nil || latest.Status != model.StatusQueued {
		return
	}
	task = latest

	ctx, cancel := context.WithCancel(context.Background())

	sc.mu.Lock()
	sc.activeTasks[task.ID] = cancel
	sc.mu.Unlock()

	task.Status = model.StatusDownloading
	task.UpdatedAt = time.Now()
	sc.notifyUpdate(task)

	go func() {
		defer func() {
			sc.mu.Lock()
			delete(sc.activeTasks, task.ID)
			sc.mu.Unlock()
		}()

		opts := DownloadOptions{
			NumChunks: 4,
			OnProgress: func(downloaded, total, speed int64) {
				task.DownloadedBytes = downloaded
				task.TotalBytes = total
				task.Speed = speed
				if total > 0 {
					task.Progress = float64(downloaded) / float64(total)
				}
				sc.notifyUpdate(task)
			},
		}

		var err error
		if task.Engine == model.EngineExtractor {
			err = sc.extractorEngine.Download(ctx, task, opts)
		} else if task.Engine == model.EngineHLS {
			err = sc.hlsEngine.Download(ctx, task, opts)
		} else {
			err = sc.httpEngine.Download(ctx, task, opts)
		}

		if err != nil {
			if ctx.Err() != nil {
				// 用户主动取消或暂停
				return
			}
			// 网络抖动、站点限流等瞬时故障自动退避重试，无需用户手动点"继续"
			if task.RetryCount < maxAutoRetries {
				task.RetryCount++
				delay := autoRetryDelay(task.RetryCount)
				task.Status = model.StatusRetryWait
				task.ErrorMsg = fmt.Sprintf("下载中断，%d 秒后自动重试 (%d/%d): %v", int(delay.Seconds()), task.RetryCount, maxAutoRetries, err)
				task.Speed = 0
				task.UpdatedAt = time.Now()
				sc.notifyUpdate(task)
				go sc.scheduleAutoRetry(task.ID, delay)
				return
			}
			task.Status = model.StatusFailed
			task.ErrorMsg = fmt.Sprintf("%s (已自动重试 %d 次)", err.Error(), task.RetryCount)
			task.Speed = 0
			task.UpdatedAt = time.Now()
			sc.notifyUpdate(task)
			return
		}

		// 下载完成
		task.Status = model.StatusCompleted
		task.Progress = 1.0
		task.Speed = 0
		// 以磁盘上的真实文件大小为准修正字节数
		if task.FinalPath != "" {
			if fi, statErr := os.Stat(task.FinalPath); statErr == nil {
				task.TotalBytes = fi.Size()
			}
		}
		task.DownloadedBytes = task.TotalBytes
		task.UpdatedAt = time.Now()

		// 先归档到媒体资源库，再广播最终状态
		// (归档必须早于最后一次 notifyUpdate，否则 UI 刷新读库时还看不到这条新资产)
		if !sc.isDeleted(task.ID) {
			asset := &model.Asset{
				ID:        "ast-" + task.ID,
				TaskID:    task.ID,
				Title:     task.Title,
				SourceURL: task.SourceURL,
				FilePath:  task.FinalPath,
				FileSize:  task.TotalBytes,
				CreatedAt: time.Now(),
			}
			if err := sc.storage.SaveAsset(asset); err != nil {
				log.Printf("[scheduler] 归档媒体资源失败 (%s): %v", task.ID, err)
			}
		}
		sc.notifyUpdate(task)
	}()
}
