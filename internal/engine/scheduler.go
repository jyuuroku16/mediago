package engine

import (
	"context"
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

func (sc *Scheduler) notifyUpdate(task *model.Task) {
	_ = sc.storage.SaveTask(task)
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
	return sc.Submit(task)
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
			task.Status = model.StatusFailed
			task.ErrorMsg = err.Error()
			task.Speed = 0
			task.UpdatedAt = time.Now()
			sc.notifyUpdate(task)
			return
		}

		// 下载完成
		task.Status = model.StatusCompleted
		task.Progress = 1.0
		task.DownloadedBytes = task.TotalBytes
		task.Speed = 0
		task.UpdatedAt = time.Now()
		sc.notifyUpdate(task)

		// 归档到 Assets
		asset := &model.Asset{
			ID:        "ast-" + task.ID,
			TaskID:    task.ID,
			Title:     task.Title,
			SourceURL: task.SourceURL,
			FilePath:  task.FinalPath,
			FileSize:  task.TotalBytes,
			CreatedAt: time.Now(),
		}
		_ = sc.storage.SaveAsset(asset)
	}()
}
