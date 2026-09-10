package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/types"
)

const maxRetrievalResults = 10000

type repository struct {
	store        types.VectorStore
	registration registration
}

func (r *repository) EngineType() types.RetrieverEngineType { return r.store.EngineType }

func (r *repository) Support() []types.RetrieverType {
	result := make([]types.RetrieverType, 0, len(r.registration.capabilities))
	for _, candidate := range []types.RetrieverType{types.KeywordsRetrieverType, types.VectorRetrieverType} {
		if r.registration.capabilities[candidate] {
			result = append(result, candidate)
		}
	}
	return result
}

func (r *repository) Save(ctx context.Context, info *types.IndexInfo, params map[string]any) error {
	if info == nil {
		return errors.New("index info is required")
	}
	return r.upsert(ctx, []*types.IndexInfo{info}, params)
}

func (r *repository) BatchSave(ctx context.Context, infos []*types.IndexInfo, params map[string]any) error {
	return r.upsert(ctx, infos, params)
}

func (r *repository) upsert(ctx context.Context, infos []*types.IndexInfo, params map[string]any) error {
	client, err := r.registration.runtime.Retrieval()
	if err != nil {
		return err
	}
	documents, err := r.documents(infos, params)
	if err != nil {
		return err
	}
	_, err = client.Upsert(ctx, &pluginv1.RetrievalUpsertRequest{
		Context: r.invocation(ctx), ConfigJson: r.configJSON(), Documents: documents,
	})
	return err
}

func (r *repository) EstimateStorageSize(
	ctx context.Context,
	infos []*types.IndexInfo,
	params map[string]any,
) int64 {
	client, err := r.registration.runtime.Retrieval()
	if err != nil {
		return 0
	}
	documents, err := r.documents(infos, params)
	if err != nil {
		return 0
	}
	response, err := client.EstimateStorage(ctx, &pluginv1.RetrievalEstimateStorageRequest{
		Context: r.invocation(ctx), ConfigJson: r.configJSON(), Documents: documents,
	})
	if err != nil || response.GetBytes() < 0 {
		return 0
	}
	return response.GetBytes()
}

func (r *repository) Retrieve(ctx context.Context, params types.RetrieveParams) ([]*types.RetrieveResult, error) {
	if !r.registration.capabilities[params.RetrieverType] {
		return nil, fmt.Errorf("retrieval engine %s does not support %s", r.store.EngineType, params.RetrieverType)
	}
	if !finiteVector(params.Embedding) {
		return nil, errors.New("retrieval query contains a non-finite embedding")
	}
	client, err := r.registration.runtime.Retrieval()
	if err != nil {
		return nil, err
	}
	additional, err := json.Marshal(params.AdditionalParams)
	if err != nil {
		return nil, fmt.Errorf("encode retrieval parameters: %w", err)
	}
	response, err := client.Retrieve(ctx, &pluginv1.RetrievalRequest{
		Context: r.invocation(ctx), ConfigJson: r.configJSON(), Query: params.Query,
		Embedding:        append([]float32(nil), params.Embedding...),
		KnowledgeBaseIds: append([]string(nil), params.KnowledgeBaseIDs...),
		KnowledgeIds:     append([]string(nil), params.KnowledgeIDs...), TagIds: append([]string(nil), params.TagIDs...),
		ExcludeKnowledgeIds: append([]string(nil), params.ExcludeKnowledgeIDs...),
		ExcludeChunkIds:     append([]string(nil), params.ExcludeChunkIDs...),
		TopK:                safeUint32(params.TopK), Threshold: params.Threshold, KnowledgeType: params.KnowledgeType,
		AdditionalParamsJson: additional, RetrieverType: retrieverTypeToProto(params.RetrieverType),
	})
	if err != nil {
		return nil, err
	}
	if len(response.GetResults()) > 2 {
		return nil, errors.New("retrieval plugin returned too many result sets")
	}
	results := make([]*types.RetrieveResult, 0, len(response.GetResults()))
	for _, resultSet := range response.GetResults() {
		result, err := r.convertResultSet(resultSet)
		if err != nil {
			return nil, err
		}
		if result.RetrieverType != params.RetrieverType {
			return nil, errors.New("retrieval plugin returned a mismatched retriever type")
		}
		results = append(results, result)
	}
	return results, nil
}

func (r *repository) convertResultSet(value *pluginv1.RetrievalResultSet) (*types.RetrieveResult, error) {
	if value == nil || len(value.GetHits()) > maxRetrievalResults {
		return nil, errors.New("retrieval plugin returned an invalid result set")
	}
	retrieverType, err := retrieverTypeFromProto(value.GetRetrieverType())
	if err != nil || !r.registration.capabilities[retrieverType] {
		return nil, errors.New("retrieval plugin returned an unsupported retriever type")
	}
	result := &types.RetrieveResult{RetrieverEngineType: r.store.EngineType, RetrieverType: retrieverType}
	for _, hit := range value.GetHits() {
		if hit == nil || hit.GetChunkId() == "" ||
			!validSourceType(hit.GetSourceType()) || !validMatchType(hit.GetMatchType()) ||
			math.IsNaN(hit.GetScore()) || math.IsInf(hit.GetScore(), 0) {
			return nil, errors.New("retrieval plugin returned an invalid hit")
		}
		result.Results = append(result.Results, &types.IndexWithScore{
			ID: hit.GetId(), Content: hit.GetContent(), SourceID: hit.GetSourceId(), SourceType: types.SourceType(hit.GetSourceType()),
			ChunkID: hit.GetChunkId(), KnowledgeID: hit.GetKnowledgeId(), KnowledgeBaseID: hit.GetKnowledgeBaseId(),
			TagID: hit.GetTagId(), Score: hit.GetScore(), MatchType: types.MatchType(hit.GetMatchType()), IsEnabled: hit.GetIsEnabled(),
		})
	}
	return result, nil
}

func validSourceType(value int32) bool {
	switch types.SourceType(value) {
	case types.ChunkSourceType, types.PassageSourceType, types.SummarySourceType:
		return true
	default:
		return false
	}
}

func validMatchType(value int32) bool {
	switch types.MatchType(value) {
	case types.MatchTypeEmbedding,
		types.MatchTypeKeywords,
		types.MatchTypeNearByChunk,
		types.MatchTypeHistory,
		types.MatchTypeParentChunk,
		types.MatchTypeRelationChunk,
		types.MatchTypeGraph,
		types.MatchTypeWebSearch,
		types.MatchTypeDirectLoad,
		types.MatchTypeDataAnalysis:
		return true
	default:
		return false
	}
}

func (r *repository) DeleteByChunkIDList(ctx context.Context, ids []string, dimension int, knowledgeType string) error {
	return r.delete(ctx, pluginv1.RetrievalDeleteTarget_RETRIEVAL_DELETE_TARGET_CHUNK, ids, dimension, knowledgeType)
}

func (r *repository) DeleteBySourceIDList(ctx context.Context, ids []string, dimension int, knowledgeType string) error {
	return r.delete(ctx, pluginv1.RetrievalDeleteTarget_RETRIEVAL_DELETE_TARGET_SOURCE, ids, dimension, knowledgeType)
}

func (r *repository) DeleteByKnowledgeIDList(ctx context.Context, ids []string, dimension int, knowledgeType string) error {
	return r.delete(ctx, pluginv1.RetrievalDeleteTarget_RETRIEVAL_DELETE_TARGET_KNOWLEDGE, ids, dimension, knowledgeType)
}

func (r *repository) delete(ctx context.Context, target pluginv1.RetrievalDeleteTarget, ids []string, dimension int, knowledgeType string) error {
	client, err := r.registration.runtime.Retrieval()
	if err != nil {
		return err
	}
	_, err = client.Delete(ctx, &pluginv1.RetrievalDeleteRequest{
		Context: r.invocation(ctx), ConfigJson: r.configJSON(), Target: target,
		Ids: append([]string(nil), ids...), Dimension: safeInt32(dimension), KnowledgeType: knowledgeType,
	})
	return err
}

func (r *repository) CopyIndices(
	ctx context.Context,
	sourceKnowledgeBaseID string,
	knowledgeBaseIDMap map[string]string,
	chunkIDMap map[string]string,
	targetKnowledgeBaseID string,
	dimension int,
	knowledgeType string,
) error {
	client, err := r.registration.runtime.Retrieval()
	if err != nil {
		return err
	}
	_, err = client.Copy(ctx, &pluginv1.RetrievalCopyRequest{
		Context: r.invocation(ctx), ConfigJson: r.configJSON(), SourceKnowledgeBaseId: sourceKnowledgeBaseID,
		KnowledgeBaseIdMap: cloneStringMap(knowledgeBaseIDMap), ChunkIdMap: cloneStringMap(chunkIDMap),
		TargetKnowledgeBaseId: targetKnowledgeBaseID, Dimension: safeInt32(dimension), KnowledgeType: knowledgeType,
	})
	return err
}

func (r *repository) BatchUpdateChunkEnabledStatus(ctx context.Context, values map[string]bool) error {
	return r.updateChunks(ctx, values, nil)
}

func (r *repository) BatchUpdateChunkTagID(ctx context.Context, values map[string]string) error {
	return r.updateChunks(ctx, nil, values)
}

func (r *repository) updateChunks(ctx context.Context, enabled map[string]bool, tags map[string]string) error {
	client, err := r.registration.runtime.Retrieval()
	if err != nil {
		return err
	}
	_, err = client.UpdateChunks(ctx, &pluginv1.RetrievalUpdateChunksRequest{
		Context: r.invocation(ctx), ConfigJson: r.configJSON(), EnabledStatus: cloneBoolMap(enabled), TagIds: cloneStringMap(tags),
	})
	return err
}

func (r *repository) documents(infos []*types.IndexInfo, params map[string]any) ([]*pluginv1.RetrievalIndexDocument, error) {
	embeddings, _ := params["embedding"].(map[string][]float32)
	documents := make([]*pluginv1.RetrievalIndexDocument, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			return nil, errors.New("index info is required")
		}
		embedding := embeddings[info.SourceID]
		if len(embedding) == 0 {
			embedding = embeddings[info.ChunkID]
		}
		if len(embedding) > 0 && !r.registration.capabilities[types.VectorRetrieverType] {
			return nil, errors.New("retrieval plugin does not support vector documents")
		}
		if !finiteVector(embedding) {
			return nil, errors.New("retrieval document contains a non-finite embedding")
		}
		documents = append(documents, &pluginv1.RetrievalIndexDocument{
			Id: info.ID, Content: info.Content, SourceId: info.SourceID, SourceType: int32(info.SourceType),
			ChunkId: info.ChunkID, KnowledgeId: info.KnowledgeID, KnowledgeBaseId: info.KnowledgeBaseID,
			KnowledgeType: info.KnowledgeType, TagId: info.TagID, IsEnabled: info.IsEnabled,
			IsRecommended: info.IsRecommended, Embedding: append([]float32(nil), embedding...),
		})
	}
	return documents, nil
}

func finiteVector(values []float32) bool {
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

func (r *repository) invocation(_ context.Context) *pluginv1.InvocationContext {
	return &pluginv1.InvocationContext{
		PluginId:   r.registration.pluginID,
		TenantId:   r.store.TenantID,
		InstanceId: r.store.ID,
	}
}

func (r *repository) configJSON() []byte {
	encoded, _ := json.Marshal(mergedConfig(r.store.ConnectionConfig))
	return encoded
}

func retrieverTypeToProto(value types.RetrieverType) pluginv1.PluginRetrieverType {
	if value == types.VectorRetrieverType {
		return pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_VECTOR
	}
	return pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_KEYWORDS
}

func retrieverTypeFromProto(value pluginv1.PluginRetrieverType) (types.RetrieverType, error) {
	switch value {
	case pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_KEYWORDS:
		return types.KeywordsRetrieverType, nil
	case pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_VECTOR:
		return types.VectorRetrieverType, nil
	default:
		return "", errors.New("unknown retriever type")
	}
}

func safeUint32(value int) uint32 {
	if value <= 0 {
		return 0
	}
	if uint64(value) > uint64(math.MaxUint32) {
		return math.MaxUint32
	}
	return uint32(value)
}

func safeInt32(value int) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < math.MinInt32 {
		return math.MinInt32
	}
	return int32(value)
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneBoolMap(input map[string]bool) map[string]bool {
	if input == nil {
		return nil
	}
	result := make(map[string]bool, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
