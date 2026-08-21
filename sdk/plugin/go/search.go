package plugin

import (
	"context"
	"encoding/json"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type WebSearchInput struct {
	Invocation Invocation
	Config     json.RawMessage
	Query      string
	Language   string
	Limit      uint32
	PageToken  string
}

type WebSearchResult struct {
	Title       string
	URL         string
	Snippet     string
	Source      string
	PublishedAt time.Time
	Metadata    map[string]string
}

type WebSearchOutput struct {
	Results       []WebSearchResult
	NextPageToken string
}

type WebSearchHandler interface {
	Search(ctx context.Context, input WebSearchInput) (WebSearchOutput, error)
}

func (s *Server) Search(ctx context.Context, request *pluginv1.WebSearchRequest) (*pluginv1.WebSearchResponse, error) {
	if s.webSearch == nil {
		return nil, status.Error(codes.Unimplemented, "web search capability is not enabled")
	}
	if err := validateJSONConfig(request.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	output, err := s.webSearch.Search(ctx, WebSearchInput{
		Invocation: invocationFromProto(request.GetContext()),
		Config:     append(json.RawMessage(nil), request.GetConfigJson()...),
		Query:      request.GetQuery(), Language: request.GetLanguage(),
		Limit: request.GetLimit(), PageToken: request.GetPageToken(),
	})
	if err != nil {
		return nil, capabilityError(err)
	}
	response := &pluginv1.WebSearchResponse{NextPageToken: output.NextPageToken}
	for _, item := range output.Results {
		converted := &pluginv1.WebSearchResult{
			Title: item.Title, Url: item.URL, Snippet: item.Snippet,
			Source: item.Source, Metadata: item.Metadata,
		}
		if !item.PublishedAt.IsZero() {
			converted.PublishedAt = timestamppb.New(item.PublishedAt)
		}
		response.Results = append(response.Results, converted)
	}
	return response, nil
}
