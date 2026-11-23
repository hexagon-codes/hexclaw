package engineadapter

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

const canceledWholeLineMarker = "[crossed out]"

const adjudicatedActiveWorkPrompt = "\nReturn only active working in student_answer. Prefer omitting an unequivocally canceled whole line and transcribing the remaining active lines verbatim. If retaining a visibly canceled line is necessary to distinguish it from active work, put its complete standalone equation on its own line followed by a space and the exact suffix " + canceledWholeLineMarker + ". This suffix means that the entire equation was canceled, never just a digit, fraction reduction, one part of a line or a neighboring line. If cancellation or ownership is uncertain, preserve the risk in ocr_signals and set active_answer_complete=false. Do not infer cancellation from mathematical correctness.\n"

// 只认占整行的一个数学等式；局部数学定界或额外说明不证明整行已划除。
var canceledMathEquation = regexp.MustCompile(`^[0-9A-Za-z.+*/^()×÷·− \t-]+=[0-9A-Za-z.+*/^()×÷·− \t-]+$`)

// 裁决与独立复读所见的有效各行必须依次完全相同，首读还须证实被划掉的原行。
// 求解和评分不参与来源选择；三个模型响应及其原摘要保持冻结。
func projectAdjudicatedActiveAnswer(original json.RawMessage, first, review, adjudicated usecase.RecognizedQuestion, finalAnswer string) json.RawMessage {
	for _, observation := range []usecase.RecognizedQuestion{first, review, adjudicated} {
		if observation.Subject != "数学" || observation.AnswerState != usecase.AnswerStatePresent ||
			len(adjudicated.SourceNumberPath) == 0 || !slices.Equal(observation.SourceNumberPath, adjudicated.SourceNumberPath) {
			return original
		}
	}
	if len(review.OCRSignals) != 0 || len(adjudicated.OCRSignals) != 0 {
		return original
	}
	firstLines := strings.Split(strings.TrimSpace(first.AnswerRawTranscription), "\n")
	reviewLines := strings.Split(strings.TrimSpace(review.AnswerRawTranscription), "\n")
	lines := strings.Split(strings.TrimSpace(adjudicated.AnswerRawTranscription), "\n")
	if len(lines) < 3 || len(firstLines) != len(lines) || len(reviewLines)+1 != len(lines) ||
		!k12.RecognitionSourceQuestionsEqualV1(lines[len(lines)-1], finalAnswer) {
		return original
	}
	activeIndex := 0
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if mathLine, marked := strings.CutSuffix(line, " "+canceledWholeLineMarker); marked {
			expression := strings.TrimSpace(mathLine)
			for _, delimiters := range [][2]string{{`\(`, `\)`}, {`\[`, `\]`}} {
				if content, wrapped := strings.CutPrefix(expression, delimiters[0]); wrapped && strings.HasSuffix(content, delimiters[1]) {
					expression = strings.TrimSuffix(content, delimiters[1])
					break
				}
			}
			if i == 0 || i == len(lines)-1 || i != activeIndex ||
				strings.Contains(expression, `\(`) || strings.Contains(expression, `\)`) || strings.ContainsAny(expression, "[]") ||
				!canceledMathEquation.MatchString(k12.RecognitionSourcePlainTextV1(expression)) ||
				!k12.RecognitionSourceQuestionsEqualV1(firstLines[i], expression) {
				return original
			}
			continue
		}
		if activeIndex >= len(reviewLines) || !k12.RecognitionSourceQuestionsEqualV1(firstLines[i], line) ||
			!k12.RecognitionSourceQuestionsEqualV1(reviewLines[activeIndex], line) {
			return original
		}
		activeIndex++
	}
	if activeIndex != len(reviewLines) {
		return original
	}
	var fields map[string]json.RawMessage
	var observed string
	if json.Unmarshal(original, &fields) != nil || json.Unmarshal(fields["student_answer"], &observed) != nil ||
		observed != adjudicated.AnswerRawTranscription {
		return original
	}
	fields["student_answer"], _ = json.Marshal(review.AnswerRawTranscription)
	projected, err := json.Marshal(fields)
	if err != nil {
		return original
	}
	return projected
}
