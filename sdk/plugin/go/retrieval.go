package plugin

import (
	"context"
	"encoding/json"
	"errors"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type RetrieverType string

const (
	RetrieverTypeKeywords RetrieverType = "keywords"
	RetrieverTypeVector   RetrieverType = "vector"
)

type RetrievalIndexDocument struct {
	ID              string
	Content         string
	SourceID        string
	SourceType      int32
	ChunkID         string
	KnowledgeID     string
	KnowledgeBaseID string
	KnowledgeType   string
	TagID           string
	IsEnabled       bool
	IsRecommended   bool
	Embedding       []float32
}

type RetrievalUpsertInput struct {
	Invocation Invocation
	Config     json.RawMessage
	Documents  []RetrievalIndexDocument
}

type RetrievalQueryInput struct {
	Invocation          Invocation
	Config              json.RawMessage
	Query               string
	Embedding           []float32
	KnowledgeBaseIDs    []string
	KnowledgeIDs        []string
	TagIDs              []string
	ExcludeKnowledgeIDs []string
	ExcludeChunkIDs     []string
	TopK                uint32
	Threshold           float64
	KnowledgeType       string
	AdditionalParams    json.RawMessage
	RetrieverType       RetrieverType
}

type RetrievalHit struct {
	ID              string
	Content         string
	SourceID        string
	SourceType      int32
	ChunkID         string
	KnowledgeID     string
	KnowledgeBaseID string
	TagID           string
	Score           float64
	MatchType       int32
	IsEnabled       bool
}

type RetrievalResultSet struct {
	RetrieverType RetrieverType
	Hits          []RetrievalHit
}

type RetrievalEstimateInput struct {
	Invocation Invocation
	Config     json.RawMessage
	Documents  []RetrievalIndexDocument
}

type RetrievalDeleteTarget string

const (
	RetrievalDeleteChunk     RetrievalDeleteTarget = "chunk"
	RetrievalDeleteSource    RetrievalDeleteTarget = "source"
	RetrievalDeleteKnowledge RetrievalDeleteTarget = "knowledge"
)

type RetrievalDeleteInput struct {
	Invocation    Invocation
	Config        json.RawMessage
	Target        RetrievalDeleteTarget
	IDs           []string
	Dimension     int32
	KnowledgeType string
}

type RetrievalCopyInput struct {
	Invocation            Invocation
	Config                json.RawMessage
	SourceKnowledgeBaseID string
	KnowledgeBaseIDMap    map[string]string
	ChunkIDMap            map[string]string
	TargetKnowledgeBaseID string
	Dimension             int32
	KnowledgeType         string
}

type RetrievalUpdateChunksInput struct {
	Invocation    Invocation
	Config        json.RawMessage
	EnabledStatus map[string]bool
	TagIDs        map[string]string
}

// RetrievalEngineHandler is the storage-facing contract for retrieval plugins.
// WeKnora computes embeddings and performs cross-engine fusion before and after
// these calls; implementations only own their external index backend.
type RetrievalEngineHandler interface {
	Upsert(ctx context.Context, input RetrievalUpsertInput) error
	Retrieve(ctx context.Context, input RetrievalQueryInput) ([]RetrievalResultSet, error)
	EstimateStorage(ctx context.Context, input RetrievalEstimateInput) (int64, error)
	Delete(ctx context.Context, input RetrievalDeleteInput) error
	Copy(ctx context.Context, input RetrievalCopyInput) error
	UpdateChunks(ctx context.Context, input RetrievalUpdateChunksInput) error
}

func (s *Server) Upsert(ctx context.Context, request *pluginv1.RetrievalUpsertRequest) (*emptypb.Empty, error) {
	if s.retrievalEngine == nil {
		return nil, status.Error(codes.Unimplemented, "retrieval engine capability is not enabled")
	}
	if err := validateJSONConfig(request.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	documents := make([]RetrievalIndexDocument, 0, len(request.GetDocuments()))
	for _, document := range request.GetDocuments() {
		if document == nil {
			return nil, status.Error(codes.InvalidArgument, "retrieval document is required")
		}
		documents = append(documents, retrievalDocumentFromProto(document))
	}
	err := s.retrievalEngine.Upsert(ctx, RetrievalUpsertInput{
		Invocation: invocationFromProto(request.GetContext()),
		Config:     cloneJSON(request.GetConfigJson()),
		Documents:  documents,
	})
	if err != nil {
		return nil, capabilityError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) Retrieve(ctx context.Context, request *pluginv1.RetrievalRequest) (*pluginv1.RetrievalResponse, error) {
	if s.retrievalEngine == nil {
		return nil, status.Error(codes.Unimplemented, "retrieval engine capability is not enabled")
	}
	if err := validateJSONConfig(request.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	retrieverType, err := retrieverTypeFromProto(request.GetRetrieverType())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	additional := cloneJSON(request.GetAdditionalParamsJson())
	if len(additional) == 0 {
		additional = json.RawMessage("{}")
	} else if !json.Valid(additional) {
		return nil, status.Error(codes.InvalidArgument, "additional_params_json must be valid JSON")
	}
	resultSets, err := s.retrievalEngine.Retrieve(ctx, RetrievalQueryInput{
		Invocation:          invocationFromProto(request.GetContext()),
		Config:              cloneJSON(request.GetConfigJson()),
		Query:               request.GetQuery(),
		Embedding:           append([]float32(nil), request.GetEmbedding()...),
		KnowledgeBaseIDs:    append([]string(nil), request.GetKnowledgeBaseIds()...),
		KnowledgeIDs:        append([]string(nil), request.GetKnowledgeIds()...),
		TagIDs:              append([]string(nil), request.GetTagIds()...),
		ExcludeKnowledgeIDs: append([]string(nil), request.GetExcludeKnowledgeIds()...),
		ExcludeChunkIDs:     append([]string(nil), request.GetExcludeChunkIds()...),
		TopK:                request.GetTopK(),
		Threshold:           request.GetThreshold(),
		KnowledgeType:       request.GetKnowledgeType(),
		AdditionalParams:    additional,
		RetrieverType:       retrieverType,
	})
	if err != nil {
		return nil, capabilityError(err)
	}
	response := &pluginv1.RetrievalResponse{}
	for _, resultSet := range resultSets {
		convertedType, err := retrieverTypeToProto(resultSet.RetrieverType)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		converted := &pluginv1.RetrievalResultSet{RetrieverType: convertedType}
		for _, hit := range resultSet.Hits {
			converted.Hits = append(converted.Hits, &pluginv1.RetrievalHit{
				Id: hit.ID, Content: hit.Content, SourceId: hit.SourceID,
				SourceType: hit.SourceType, ChunkId: hit.ChunkID,
				KnowledgeId: hit.KnowledgeID, KnowledgeBaseId: hit.KnowledgeBaseID,
				TagId: hit.TagID, Score: hit.Score, MatchType: hit.MatchType,
				IsEnabled: hit.IsEnabled,
			})
		}
		response.Results = append(response.Results, converted)
	}
	return response, nil
}

func (s *Server) EstimateStorage(ctx context.Context, request *pluginv1.RetrievalEstimateStorageRequest) (*pluginv1.RetrievalEstimateStorageResponse, error) {
	if s.retrievalEngine == nil {
		return nil, status.Error(codes.Unimplemented, "retrieval engine capability is not enabled")
	}
	if err := validateJSONConfig(request.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	documents := make([]RetrievalIndexDocument, 0, len(request.GetDocuments()))
	for _, document := range request.GetDocuments() {
		if document != nil {
			documents = append(documents, retrievalDocumentFromProto(document))
		}
	}
	bytes, err := s.retrievalEngine.EstimateStorage(ctx, RetrievalEstimateInput{
		Invocation: invocationFromProto(request.GetContext()),
		Config:     cloneJSON(request.GetConfigJson()), Documents: documents,
	})
	if err != nil {
		return nil, capabilityError(err)
	}
	return &pluginv1.RetrievalEstimateStorageResponse{Bytes: bytes}, nil
}

func (s *Server) Delete(ctx context.Context, request *pluginv1.RetrievalDeleteRequest) (*emptypb.Empty, error) {
	if s.retrievalEngine == nil {
		return nil, status.Error(codes.Unimplemented, "retrieval engine capability is not enabled")
	}
	if err := validateJSONConfig(request.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	target, err := retrievalDeleteTargetFromProto(request.GetTarget())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	err = s.retrievalEngine.Delete(ctx, RetrievalDeleteInput{
		Invocation: invocationFromProto(request.GetContext()), Config: cloneJSON(request.GetConfigJson()),
		Target: target, IDs: append([]string(nil), request.GetIds()...),
		Dimension: request.GetDimension(), KnowledgeType: request.GetKnowledgeType(),
	})
	if err != nil {
		return nil, capabilityError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) Copy(ctx context.Context, request *pluginv1.RetrievalCopyRequest) (*emptypb.Empty, error) {
	if s.retrievalEngine == nil {
		return nil, status.Error(codes.Unimplemented, "retrieval engine capability is not enabled")
	}
	if err := validateJSONConfig(request.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	err := s.retrievalEngine.Copy(ctx, RetrievalCopyInput{
		Invocation: invocationFromProto(request.GetContext()), Config: cloneJSON(request.GetConfigJson()),
		SourceKnowledgeBaseID: request.GetSourceKnowledgeBaseId(), KnowledgeBaseIDMap: cloneStringMap(request.GetKnowledgeBaseIdMap()),
		ChunkIDMap: cloneStringMap(request.GetChunkIdMap()), TargetKnowledgeBaseID: request.GetTargetKnowledgeBaseId(),
		Dimension: request.GetDimension(), KnowledgeType: request.GetKnowledgeType(),
	})
	if err != nil {
		return nil, capabilityError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) UpdateChunks(ctx context.Context, request *pluginv1.RetrievalUpdateChunksRequest) (*emptypb.Empty, error) {
	if s.retrievalEngine == nil {
		return nil, status.Error(codes.Unimplemented, "retrieval engine capability is not enabled")
	}
	if err := validateJSONConfig(request.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	err := s.retrievalEngine.UpdateChunks(ctx, RetrievalUpdateChunksInput{
		Invocation: invocationFromProto(request.GetContext()), Config: cloneJSON(request.GetConfigJson()),
		EnabledStatus: cloneBoolMap(request.GetEnabledStatus()), TagIDs: cloneStringMap(request.GetTagIds()),
	})
	if err != nil {
		return nil, capabilityError(err)
	}
	return &emptypb.Empty{}, nil
}

func retrievalDocumentFromProto(document *pluginv1.RetrievalIndexDocument) RetrievalIndexDocument {
	return RetrievalIndexDocument{
		ID: document.GetId(), Content: document.GetContent(), SourceID: document.GetSourceId(),
		SourceType: document.GetSourceType(), ChunkID: document.GetChunkId(),
		KnowledgeID: document.GetKnowledgeId(), KnowledgeBaseID: document.GetKnowledgeBaseId(),
		KnowledgeType: document.GetKnowledgeType(), TagID: document.GetTagId(),
		IsEnabled: document.GetIsEnabled(), IsRecommended: document.GetIsRecommended(),
		Embedding: append([]float32(nil), document.GetEmbedding()...),
	}
}

func retrieverTypeFromProto(value pluginv1.PluginRetrieverType) (RetrieverType, error) {
	switch value {
	case pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_KEYWORDS:
		return RetrieverTypeKeywords, nil
	case pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_VECTOR:
		return RetrieverTypeVector, nil
	default:
		return "", errors.New("retriever_type must be keywords or vector")
	}
}

func retrieverTypeToProto(value RetrieverType) (pluginv1.PluginRetrieverType, error) {
	switch value {
	case RetrieverTypeKeywords:
		return pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_KEYWORDS, nil
	case RetrieverTypeVector:
		return pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_VECTOR, nil
	default:
		return pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_UNSPECIFIED, errors.New("retrieval result has invalid retriever type")
	}
}

func retrievalDeleteTargetFromProto(value pluginv1.RetrievalDeleteTarget) (RetrievalDeleteTarget, error) {
	switch value {
	case pluginv1.RetrievalDeleteTarget_RETRIEVAL_DELETE_TARGET_CHUNK:
		return RetrievalDeleteChunk, nil
	case pluginv1.RetrievalDeleteTarget_RETRIEVAL_DELETE_TARGET_SOURCE:
		return RetrievalDeleteSource, nil
	case pluginv1.RetrievalDeleteTarget_RETRIEVAL_DELETE_TARGET_KNOWLEDGE:
		return RetrievalDeleteKnowledge, nil
	default:
		return "", errors.New("retrieval delete target is invalid")
	}
}

func cloneJSON(value []byte) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func cloneStringMap(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func cloneBoolMap(value map[string]bool) map[string]bool {
	result := make(map[string]bool, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
