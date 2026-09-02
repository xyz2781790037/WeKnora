package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

const pluginOperationRetention = time.Hour

var ErrPluginOperationNotFound = errors.New("plugin operation not found")

type PluginOperationStep struct {
	Key         string     `json:"key"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// PluginOperation is a short-lived, secret-free view of one asynchronous
// install or upgrade. It lets clients poll real backend stages without
// introducing a second source of truth for installed plugins.
type PluginOperation struct {
	ID        string                `json:"id"`
	Kind      string                `json:"kind"`
	PluginID  string                `json:"plugin_id,omitempty"`
	Status    string                `json:"status"`
	Steps     []PluginOperationStep `json:"steps"`
	Result    *types.Plugin         `json:"result,omitempty"`
	Error     string                `json:"error,omitempty"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}

type pluginOperationStore struct {
	mu         sync.RWMutex
	operations map[string]*PluginOperation
}

func newPluginOperationStore() *pluginOperationStore {
	return &pluginOperationStore{operations: make(map[string]*PluginOperation)}
}

func pluginOperationSteps(kind string) []PluginOperationStep {
	runtimeDescription := "拉取镜像、启动隔离容器并完成协议握手"
	persistDescription := "保存插件安装记录"
	if kind == "upgrade" {
		runtimeDescription = "替换运行时镜像、验证新版本并在失败时回滚"
		persistDescription = "保存新版本和运行状态"
	}
	return []PluginOperationStep{
		{Key: "manifest", Title: "获取插件清单", Description: "读取 YAML 或安全下载清单地址", Status: "pending"},
		{Key: "validation", Title: "校验插件清单", Description: "验证协议、配置、权限和 WeKnora 兼容范围", Status: "pending"},
		{Key: "conflicts", Title: "检查安装约束", Description: "检查插件 ID、连接器类型和升级版本", Status: "pending"},
		{Key: "runtime", Title: "验证插件运行时", Description: runtimeDescription, Status: "pending"},
		{Key: "persistence", Title: "保存插件信息", Description: persistDescription, Status: "pending"},
	}
}

func (s *pluginOperationStore) create(kind, pluginID string) *PluginOperation {
	now := time.Now().UTC()
	operation := &PluginOperation{
		ID:        uuid.NewString(),
		Kind:      kind,
		PluginID:  pluginID,
		Status:    "running",
		Steps:     pluginOperationSteps(kind),
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Lock()
	s.pruneLocked(now)
	s.operations[operation.ID] = operation
	s.mu.Unlock()
	return clonePluginOperation(operation)
}

func (s *pluginOperationStore) advance(id, key, pluginID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operation := s.operations[id]
	if operation == nil || operation.Status != "running" {
		return
	}
	now := time.Now().UTC()
	if pluginID != "" {
		operation.PluginID = pluginID
	}
	for index := range operation.Steps {
		step := &operation.Steps[index]
		if step.Key == key {
			if step.Status == "pending" {
				step.Status = "running"
				step.StartedAt = &now
			}
			break
		}
		if step.Status == "running" {
			step.Status = "success"
			step.FinishedAt = &now
		}
	}
	operation.UpdatedAt = now
}

func (s *pluginOperationStore) complete(id string, result *types.Plugin) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operation := s.operations[id]
	if operation == nil {
		return
	}
	now := time.Now().UTC()
	for index := range operation.Steps {
		step := &operation.Steps[index]
		if step.Status == "running" {
			step.Status = "success"
			step.FinishedAt = &now
		}
	}
	operation.Status = "success"
	operation.Result = result
	operation.UpdatedAt = now
}

func (s *pluginOperationStore) fail(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operation := s.operations[id]
	if operation == nil {
		return
	}
	now := time.Now().UTC()
	for index := range operation.Steps {
		step := &operation.Steps[index]
		if step.Status == "running" {
			step.Status = "error"
			step.FinishedAt = &now
			break
		}
	}
	operation.Status = "error"
	operation.Error = err.Error()
	operation.UpdatedAt = now
}

func (s *pluginOperationStore) get(id string) (*PluginOperation, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	s.pruneLocked(now)
	operation := s.operations[id]
	copy := clonePluginOperation(operation)
	s.mu.Unlock()
	if copy == nil {
		return nil, ErrPluginOperationNotFound
	}
	return copy, nil
}

func (s *pluginOperationStore) pruneLocked(now time.Time) {
	for id, operation := range s.operations {
		if operation.Status != "running" && now.Sub(operation.UpdatedAt) > pluginOperationRetention {
			delete(s.operations, id)
		}
	}
}

func clonePluginOperation(operation *PluginOperation) *PluginOperation {
	if operation == nil {
		return nil
	}
	copy := *operation
	copy.Steps = append([]PluginOperationStep(nil), operation.Steps...)
	if operation.Result != nil {
		result := *operation.Result
		copy.Result = &result
	}
	return &copy
}

func (s *PluginService) StartInstallOperation(input PluginInstallInput) *PluginOperation {
	operation := s.operations.create("install", "")
	go s.runPluginOperation(operation.ID, "", input)
	return operation
}

func (s *PluginService) StartUpgradeOperation(id string, input PluginInstallInput) *PluginOperation {
	operation := s.operations.create("upgrade", id)
	go s.runPluginOperation(operation.ID, id, input)
	return operation
}

func (s *PluginService) GetOperation(id string) (*PluginOperation, error) {
	return s.operations.get(id)
}

func (s *PluginService) runPluginOperation(operationID, pluginID string, input PluginInstallInput) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginRuntimeOperationTimeout+time.Minute)
	defer cancel()
	progress := func(stage, resolvedPluginID string) {
		s.operations.advance(operationID, stage, resolvedPluginID)
	}
	var (
		result *types.Plugin
		err    error
	)
	if pluginID == "" {
		result, err = s.install(ctx, input, progress)
	} else {
		result, err = s.upgrade(ctx, pluginID, input, progress)
	}
	if err != nil {
		s.operations.fail(operationID, err)
		return
	}
	s.operations.complete(operationID, result)
}
