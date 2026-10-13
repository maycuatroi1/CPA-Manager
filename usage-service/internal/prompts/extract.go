package prompts

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

// MaxQuestionBytes caps how much of a question is stored per request.
const MaxQuestionBytes = 8 * 1024

const (
	truncatedMarker  = "…[truncated]"
	imagePlaceholder = "[image]"
	bodySectionTitle = "=== REQUEST BODY ==="
)

var sectionHeaderPattern = regexp.MustCompile(`(?m)^=== [A-Z][A-Z ]* ===\s*$`)

// autoTagPatterns match blocks that clients such as Claude Code inject into
// user turns; they are not text the person typed.
var autoTagPatterns = func() []*regexp.Regexp {
	tags := []string{
		"system-reminder",
		"command-name",
		"command-message",
		"command-args",
		"local-command-stdout",
		"local-command-stderr",
		"local-command-caveat",
	}
	patterns := make([]*regexp.Regexp, 0, len(tags))
	for _, tag := range tags {
		patterns = append(patterns, regexp.MustCompile(`(?s)<`+tag+`>.*?</`+tag+`>`))
	}
	return patterns
}()

type turnPiece struct {
	text  string
	image bool
}

// ExtractQuestion returns the latest text the user typed in the request body
// of a CLIProxyAPI request log file.
func ExtractQuestion(logFile []byte) (string, string) {
	body := requestBody(logFile)
	if len(body) == 0 {
		return "", store.PromptStatusUnparsable
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", store.PromptStatusUnparsable
	}
	turns := userTurns(payload)
	for i := len(turns) - 1; i >= 0; i-- {
		if question, ok := renderTurn(turns[i]); ok {
			return truncateQuestion(question), store.PromptStatusOK
		}
	}
	return "", store.PromptStatusUnparsable
}

func requestBody(logFile []byte) []byte {
	start := bytes.Index(logFile, []byte(bodySectionTitle))
	if start < 0 {
		return nil
	}
	rest := logFile[start+len(bodySectionTitle):]
	if next := sectionHeaderPattern.FindIndex(rest); next != nil {
		rest = rest[:next[0]]
	}
	return bytes.TrimSpace(rest)
}

func userTurns(payload map[string]any) [][]turnPiece {
	if messages, ok := payload["messages"].([]any); ok {
		return turnsFromItems(messages, "content")
	}
	if contents, ok := payload["contents"].([]any); ok {
		return turnsFromItems(contents, "parts")
	}
	switch input := payload["input"].(type) {
	case string:
		return [][]turnPiece{{{text: input}}}
	case []any:
		return turnsFromItems(input, "content")
	}
	return nil
}

func turnsFromItems(items []any, contentKey string) [][]turnPiece {
	turns := make([][]turnPiece, 0)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := item["role"].(string); role != "user" {
			continue
		}
		turns = append(turns, piecesFromContent(item[contentKey]))
	}
	return turns
}

func piecesFromContent(content any) []turnPiece {
	switch value := content.(type) {
	case string:
		return []turnPiece{{text: value}}
	case []any:
		pieces := make([]turnPiece, 0, len(value))
		for _, raw := range value {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := block["type"].(string)
			switch blockType {
			case "tool_result", "function_call_output", "tool_use":
				continue
			case "image", "image_url", "input_image":
				pieces = append(pieces, turnPiece{image: true})
				continue
			}
			if block["inline_data"] != nil || block["inlineData"] != nil {
				pieces = append(pieces, turnPiece{image: true})
				continue
			}
			if text, ok := block["text"].(string); ok && (blockType == "" || blockType == "text" || blockType == "input_text") {
				pieces = append(pieces, turnPiece{text: text})
			}
		}
		return pieces
	}
	return nil
}

// renderTurn joins a turn's pieces; ok is false when the turn has no typed text.
func renderTurn(pieces []turnPiece) (string, bool) {
	parts := make([]string, 0, len(pieces))
	hasText := false
	for _, piece := range pieces {
		if piece.image {
			parts = append(parts, imagePlaceholder)
			continue
		}
		cleaned := piece.text
		for _, pattern := range autoTagPatterns {
			cleaned = pattern.ReplaceAllString(cleaned, "")
		}
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			continue
		}
		parts = append(parts, cleaned)
		hasText = true
	}
	return strings.Join(parts, "\n\n"), hasText
}

func truncateQuestion(question string) string {
	if len(question) <= MaxQuestionBytes {
		return question
	}
	cut := question[:MaxQuestionBytes-len(truncatedMarker)]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + truncatedMarker
}
