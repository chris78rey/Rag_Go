package task

import (
	"sync"
	"time"
)

// Status represents the processing state of a document ingestion task.
type Status string

const (
	StatusPending    Status = "pending"
	StatusExtracting Status = "extracting"
	StatusEmbedding  Status = "embedding"
	StatusStoring    Status = "storing"
	StatusCompleted  Status = "completed"
	StatusError      Status = "error"
)

// Info holds metadata about an ingestion task.
type Info struct {
	ID        string    `json:"id"`
	Filename  string    `json:"filename"`
	Status    Status    `json:"status"`
	Chunks    int       `json:"chunks"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Manager tracks ingestion tasks in memory.
type Manager struct {
	mu    sync.RWMutex
	tasks map[string]*Info
}

// NewManager creates a new task manager.
func NewManager() *Manager {
	return &Manager{
		tasks: make(map[string]*Info),
	}
}

// Create registers a new task.
func (m *Manager) Create(id, filename string) *Info {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	info := &Info{
		ID:        id,
		Filename:  filename,
		Status:    StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	m.tasks[id] = info
	return info
}

// Get retrieves a task by ID.
func (m *Manager) Get(id string) (*Info, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	info, ok := m.tasks[id]
	return info, ok
}

// UpdateStatus updates the status of a task.
func (m *Manager) UpdateStatus(id string, status Status, chunks int, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if info, ok := m.tasks[id]; ok {
		info.Status = status
		info.UpdatedAt = time.Now()
		if chunks > 0 {
			info.Chunks = chunks
		}
		if errMsg != "" {
			info.Error = errMsg
		}
	}
}
