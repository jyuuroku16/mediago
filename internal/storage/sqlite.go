package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"mediago/internal/model"

	_ "modernc.org/sqlite"
)

// Storage 负责 SQLite 数据的持久化管理
type Storage struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewStorage 初始化或打开 SQLite 数据库，并配置 WAL 模式
func NewStorage(dbPath string) (*Storage, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}

	dsn := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 失败: %w", err)
	}

	// 保证单连接写入并发安全
	db.SetMaxOpenConns(1)

	s := &Storage{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("执行数据库迁移失败: %w", err)
	}

	return s, nil
}

// Close 关闭数据库
func (s *Storage) Close() error {
	return s.db.Close()
}

func (s *Storage) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		source_url TEXT NOT NULL,
		download_url TEXT NOT NULL,
		cover_url TEXT,
		engine TEXT NOT NULL,
		status TEXT NOT NULL,
		total_bytes INTEGER NOT NULL,
		downloaded_bytes INTEGER NOT NULL,
		speed INTEGER NOT NULL,
		progress REAL NOT NULL,
		output_dir TEXT NOT NULL,
		output_filename TEXT NOT NULL,
		final_path TEXT,
		error_msg TEXT,
		retry_count INTEGER NOT NULL,
		priority INTEGER NOT NULL,
		chunks_json TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS assets (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL,
		title TEXT NOT NULL,
		source_url TEXT NOT NULL,
		file_path TEXT NOT NULL,
		file_size INTEGER NOT NULL,
		duration REAL NOT NULL,
		resolution TEXT,
		file_hash_sha256 TEXT,
		tags_json TEXT,
		created_at DATETIME NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
	CREATE INDEX IF NOT EXISTS idx_assets_created ON assets(created_at DESC);
	`
	_, err := s.db.Exec(schema)
	return err
}

// SaveTask 保存或更新任务
func (s *Storage) SaveTask(task *model.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	chunksJSON, err := json.Marshal(task.Chunks)
	if err != nil {
		chunksJSON = []byte("[]")
	}

	query := `
	INSERT INTO tasks (
		id, title, source_url, download_url, cover_url, engine, status,
		total_bytes, downloaded_bytes, speed, progress, output_dir, output_filename,
		final_path, error_msg, retry_count, priority, chunks_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		title=excluded.title,
		status=excluded.status,
		total_bytes=excluded.total_bytes,
		downloaded_bytes=excluded.downloaded_bytes,
		speed=excluded.speed,
		progress=excluded.progress,
		final_path=excluded.final_path,
		error_msg=excluded.error_msg,
		retry_count=excluded.retry_count,
		chunks_json=excluded.chunks_json,
		updated_at=excluded.updated_at
	`

	_, err = s.db.Exec(query,
		task.ID, task.Title, task.SourceURL, task.DownloadURL, task.CoverURL, string(task.Engine), string(task.Status),
		task.TotalBytes, task.DownloadedBytes, task.Speed, task.Progress, task.OutputDir, task.OutputFilename,
		task.FinalPath, task.ErrorMsg, task.RetryCount, task.Priority, string(chunksJSON), task.CreatedAt, task.UpdatedAt,
	)
	return err
}

// GetTask 获取单个任务
func (s *Storage) GetTask(id string) (*model.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `SELECT 
		id, title, source_url, download_url, cover_url, engine, status,
		total_bytes, downloaded_bytes, speed, progress, output_dir, output_filename,
		final_path, error_msg, retry_count, priority, chunks_json, created_at, updated_at
	FROM tasks WHERE id = ?`

	row := s.db.QueryRow(query, id)

	var t model.Task
	var engineStr, statusStr, chunksJSON string
	err := row.Scan(
		&t.ID, &t.Title, &t.SourceURL, &t.DownloadURL, &t.CoverURL, &engineStr, &statusStr,
		&t.TotalBytes, &t.DownloadedBytes, &t.Speed, &t.Progress, &t.OutputDir, &t.OutputFilename,
		&t.FinalPath, &t.ErrorMsg, &t.RetryCount, &t.Priority, &chunksJSON, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	t.Engine = model.EngineType(engineStr)
	t.Status = model.TaskStatus(statusStr)
	if chunksJSON != "" {
		_ = json.Unmarshal([]byte(chunksJSON), &t.Chunks)
	}

	return &t, nil
}

// ListTasks 获取所有任务列表（按创建时间倒序）
func (s *Storage) ListTasks(statusFilter model.TaskStatus) ([]*model.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `SELECT 
		id, title, source_url, download_url, cover_url, engine, status,
		total_bytes, downloaded_bytes, speed, progress, output_dir, output_filename,
		final_path, error_msg, retry_count, priority, chunks_json, created_at, updated_at
	FROM tasks`

	var args []any
	if statusFilter != "" {
		query += " WHERE status = ?"
		args = append(args, string(statusFilter))
	}
	query += " ORDER BY created_at DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*model.Task
	for rows.Next() {
		var t model.Task
		var engineStr, statusStr, chunksJSON string
		if err := rows.Scan(
			&t.ID, &t.Title, &t.SourceURL, &t.DownloadURL, &t.CoverURL, &engineStr, &statusStr,
			&t.TotalBytes, &t.DownloadedBytes, &t.Speed, &t.Progress, &t.OutputDir, &t.OutputFilename,
			&t.FinalPath, &t.ErrorMsg, &t.RetryCount, &t.Priority, &chunksJSON, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		t.Engine = model.EngineType(engineStr)
		t.Status = model.TaskStatus(statusStr)
		if chunksJSON != "" {
			_ = json.Unmarshal([]byte(chunksJSON), &t.Chunks)
		}
		tasks = append(tasks, &t)
	}

	return tasks, nil
}

// DeleteTask 删除任务记录
func (s *Storage) DeleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM tasks WHERE id = ?", id)
	return err
}

// SaveAsset 保存已归档资产
func (s *Storage) SaveAsset(asset *model.Asset) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tagsJSON, err := json.Marshal(asset.Tags)
	if err != nil {
		tagsJSON = []byte("[]")
	}

	query := `
	INSERT INTO assets (
		id, task_id, title, source_url, file_path, file_size, duration,
		resolution, file_hash_sha256, tags_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		title=excluded.title,
		file_path=excluded.file_path,
		file_size=excluded.file_size,
		tags_json=excluded.tags_json
	`
	_, err = s.db.Exec(query,
		asset.ID, asset.TaskID, asset.Title, asset.SourceURL, asset.FilePath, asset.FileSize,
		asset.Duration, asset.Resolution, asset.FileHashSHA256, string(tagsJSON), asset.CreatedAt,
	)
	return err
}

// ListAssets 获取所有资产列表
func (s *Storage) ListAssets() ([]*model.Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `SELECT 
		id, task_id, title, source_url, file_path, file_size, duration,
		resolution, file_hash_sha256, tags_json, created_at
	FROM assets ORDER BY created_at DESC`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var assets []*model.Asset
	for rows.Next() {
		var a model.Asset
		var tagsJSON string
		if err := rows.Scan(
			&a.ID, &a.TaskID, &a.Title, &a.SourceURL, &a.FilePath, &a.FileSize,
			&a.Duration, &a.Resolution, &a.FileHashSHA256, &tagsJSON, &a.CreatedAt,
		); err != nil {
			return nil, err
		}
		if tagsJSON != "" {
			_ = json.Unmarshal([]byte(tagsJSON), &a.Tags)
		}
		assets = append(assets, &a)
	}

	return assets, nil
}

// DeleteAsset 删除资产记录
func (s *Storage) DeleteAsset(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM assets WHERE id = ?", id)
	return err
}
