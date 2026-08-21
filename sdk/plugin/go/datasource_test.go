package plugin

import (
	"context"
	"io"
	"testing"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestSyncEmitterChunksDocumentAndCheckpoints(t *testing.T) {
	stream := &captureSyncStream{ctx: t.Context()}
	emitter := newSyncEmitter(stream)
	emitter.chunkSize = 4

	err := emitter.EmitDocument(Document{
		ExternalID:  "docs/readme.md",
		Title:       "README",
		FileName:    "README.md",
		ContentType: "text/markdown",
		Content:     []byte("abcdefghij"),
	})
	require.NoError(t, err)
	require.NoError(t, emitter.Checkpoint([]byte(`{"commit":"abc"}`)))

	require.Len(t, stream.events, 6)
	assert.Equal(t, int64(10), stream.events[0].GetDocumentBegin().GetContentSize())
	assert.Equal(t, []byte("abcd"), stream.events[1].GetDocumentChunk().GetData())
	assert.Equal(t, []byte("efgh"), stream.events[2].GetDocumentChunk().GetData())
	assert.Equal(t, []byte("ij"), stream.events[3].GetDocumentChunk().GetData())
	assert.Equal(t, uint32(2), stream.events[3].GetDocumentChunk().GetSequence())
	assert.Equal(t,
		"72399361da6a7754fec986dca5b7cbaf1c810a28ded4abaf56b2106d06cb78b0",
		stream.events[4].GetDocumentEnd().GetSha256(),
	)
	assert.JSONEq(t, `{"commit":"abc"}`, string(stream.events[5].GetCheckpoint().GetCursorJson()))
}

func TestSyncEmitterRejectsInvalidDocument(t *testing.T) {
	emitter := newSyncEmitter(&captureSyncStream{ctx: t.Context()})
	err := emitter.EmitDocument(Document{ExternalID: "one", FileName: "one.md"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestCapabilityErrorPreservesCancellationAndDeadline(t *testing.T) {
	assert.Equal(t, codes.Canceled, status.Code(capabilityError(context.Canceled)))
	assert.Equal(t, codes.DeadlineExceeded, status.Code(capabilityError(context.DeadlineExceeded)))
}

type captureSyncStream struct {
	ctx    context.Context
	events []*pluginv1.DataSourceSyncEvent
}

func (s *captureSyncStream) Send(event *pluginv1.DataSourceSyncEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *captureSyncStream) SetHeader(metadata.MD) error  { return nil }
func (s *captureSyncStream) SendHeader(metadata.MD) error { return nil }
func (s *captureSyncStream) SetTrailer(metadata.MD)       {}
func (s *captureSyncStream) Context() context.Context     { return s.ctx }
func (s *captureSyncStream) SendMsg(any) error            { return nil }
func (s *captureSyncStream) RecvMsg(any) error            { return io.EOF }
