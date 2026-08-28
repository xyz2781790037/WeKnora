package parser

import (
	"strings"
	"testing"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/stretchr/testify/require"
)

func TestParserOutputBudgetRejectsOversizedMetadata(t *testing.T) {
	var budget parserOutputBudget
	err := budget.addMetadata(&pluginv1.ParseMetadata{Values: map[string]string{"key": strings.Repeat("x", maxParsedMetadataBytes)}})
	require.ErrorContains(t, err, "metadata output exceeds")
}

func TestParserOutputBudgetRejectsTooManyAttachments(t *testing.T) {
	var budget parserOutputBudget
	for index := 0; index < maxParsedAttachments; index++ {
		require.NoError(t, budget.addAttachment(&pluginv1.ParsedAttachment{FileName: "image.png"}))
	}
	require.ErrorContains(t, budget.addAttachment(&pluginv1.ParsedAttachment{FileName: "extra.png"}), "attachment output exceeds")
}

func TestParserOutputBudgetRejectsUnnamedAttachment(t *testing.T) {
	var budget parserOutputBudget
	require.ErrorContains(t, budget.addAttachment(&pluginv1.ParsedAttachment{}), "file name")
}
