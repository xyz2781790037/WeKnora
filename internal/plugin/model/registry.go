package model

import (
	"errors"
	"strings"
	"sync"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/models/rerank"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	"github.com/Tencent/WeKnora/internal/types"
)

type registration struct {
	runtime      runtimeclient.Gateway
	capabilities map[string]bool
	configSchema map[string]any
}

var externalProviders = struct {
	sync.RWMutex
	items map[string]registration
}{items: make(map[string]registration)}

func Register(pluginID string, runtime runtimeclient.Gateway, capabilities []string, configSchema map[string]any) error {
	if strings.TrimSpace(pluginID) == "" || runtime == nil {
		return errors.New("model plugin id and runtime are required")
	}
	values := make(map[string]bool)
	for _, capability := range capabilities {
		value := strings.ToLower(strings.TrimSpace(capability))
		switch value {
		case "chat":
			values["chat"] = true
		case "embed", "embedding":
			values["embedding"] = true
		case "rerank":
			values["rerank"] = true
		}
	}
	if len(values) == 0 {
		return errors.New("model provider plugin must declare chat, embedding or rerank capability")
	}
	externalProviders.Lock()
	externalProviders.items[pluginID] = registration{runtime: runtime, capabilities: values, configSchema: configSchema}
	externalProviders.Unlock()
	return nil
}

func Unregister(pluginID string) {
	externalProviders.Lock()
	delete(externalProviders.items, pluginID)
	externalProviders.Unlock()
}

func lookup(provider, capability string) (registration, bool) {
	externalProviders.RLock()
	item, ok := externalProviders.items[provider]
	externalProviders.RUnlock()
	return item, ok && item.capabilities[capability]
}

func NewChat(model *types.Model) (chat.Chat, bool, error) {
	if model == nil {
		return nil, false, errors.New("model is required")
	}
	item, ok := lookup(model.Parameters.Provider, "chat")
	if !ok {
		return nil, false, nil
	}
	return &chatAdapter{pluginID: model.Parameters.Provider, runtime: item.runtime, model: model, configSchema: item.configSchema}, true, nil
}

func NewEmbedder(model *types.Model) (embedding.Embedder, bool, error) {
	if model == nil {
		return nil, false, errors.New("model is required")
	}
	item, ok := lookup(model.Parameters.Provider, "embedding")
	if !ok {
		return nil, false, nil
	}
	return &embeddingAdapter{pluginID: model.Parameters.Provider, runtime: item.runtime, model: model, configSchema: item.configSchema}, true, nil
}

func NewReranker(model *types.Model) (rerank.Reranker, bool, error) {
	if model == nil {
		return nil, false, errors.New("model is required")
	}
	item, ok := lookup(model.Parameters.Provider, "rerank")
	if !ok {
		return nil, false, nil
	}
	return &rerankAdapter{pluginID: model.Parameters.Provider, runtime: item.runtime, model: model, configSchema: item.configSchema}, true, nil
}
