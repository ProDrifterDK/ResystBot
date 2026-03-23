package agent

import (
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"

	tke "github.com/pkoukk/tiktoken-go"
)

var _encoder *tke.Tiktoken

func init() {
	enc, err := tke.GetEncoding("cl100k_base")
	if err != nil {
		logger.WarnCF("tokencount", "Failed to load tiktoken encoder, using fallback", map[string]any{
			"error": err.Error(),
		})
	} else {
		_encoder = enc
	}
}

func countTokens(text string) int {
	if text == "" {
		return 0
	}
	if _encoder != nil {
		return len(_encoder.Encode(text, nil, nil))
	}
	return utf8.RuneCountInString(text) / 3
}

func countMessageTokens(messages []protocoltypes.Message) int {
	total := 0
	for _, m := range messages {
		total += countTokens(m.Content)
		if m.ReasoningContent != "" {
			total += countTokens(m.ReasoningContent)
		}
		for _, tc := range m.ToolCalls {
			if tc.Function != nil {
				total += countTokens(tc.Function.Arguments)
			}
		}
	}
	return total * 110 / 100 // 10% safety margin
}
