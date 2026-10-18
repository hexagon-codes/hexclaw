package engineadapter

import (
	"fmt"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 新传输合同省去应用可确定的编号和重复来源文本，原始观察仍完整保留。
const recognitionLayoutInitialReadCompactPromptV1 = `Read this original worksheet page once and return its complete layout manifest AND the first transcription for every target in the SAME JSON response. Do not solve, grade, correct, or infer any answer.
Return exactly {"format":"initial_read_compact_v1","sections":[{"path":["一"],"label":"一、直接写得数"}],"conditions":[],"targets":[{"number":["1"],"label":"1.","section":0,"shared":[],"region":[0,0,1,1],"kind":"question","question":"complete target-specific printed question","subject":"数学","answer_state":"present","student_answer":"all active handwritten working and final answer","confidence":0.99,"risks":[],"answer_bbox":[0,0,1,1],"ownership":"visible source evidence distinguishing this target's active handwriting from printed examples, cancelled work and neighboring handwriting"}]}.
List targets from top to bottom and left to right within each row. The application generates manifest references and order from array position; never output these fields. Split subquestions only when each has its own visibly printed stable subquestion label. When one printed question number owns multiple unlabelled question sentences or requested quantities, keep its COMPLETE printed question and ALL active handwritten answers together as ONE target. Never split those unlabelled sentences or quantities, duplicate their shared printed number/region, or invent child numbering. Preserve the complete printed hierarchy in number and its verbatim label. Without visible numbering use number=[] and label="". Section headings, headers and decoration are not targets. Never omit, duplicate, merge or invent targets.
sections stores each owning printed section once, with its visibly printed path and verbatim label. section is its ZERO-BASED array index, or null when no owning section is visible. Do not copy the section heading into every target. conditions stores each verbatim shared condition once; shared is the ordered list of ZERO-BASED condition indices that actually apply to this target, or []. Do not mix instructions/conditions from different sections. question contains ALL target-specific printed text, preserving every sentence and requested quantity; the application prepends the referenced conditions verbatim, so do not repeat those conditions in question. Do not replace any question text with a summary or reference.
region and answer_bbox are [x,y,width,height] in ABSOLUTE integer pixels of this original page. region contains the complete target and its answer area. answer_bbox tightly encloses this target's active final handwriting (or working when no final value), never printed text or the whole question rectangle; use null for blank, unclear or uncertain location.
Subject is 数学/语文/英语/物理/化学 or empty. Preserve every printed fraction numerator/bar/denominator, decimal, operator and unit. Keep TeX enclosed in \\( ... \\) or \\[ ... \\] and escape JSON only once. Do not infer clipped glyphs from arithmetic or handwriting. If a genuine title/instruction/decoration was included as a target, set kind=non_question and question, subject, answer_state, student_answer, confidence, risks and answer_bbox all to null; keep its source metadata and ownership.
student_answer transcribes ALL active handwritten working lines in order, including incorrect steps and final answer, separated by newlines. Printed worked examples, printed answers, printed cancellation/simplification marks and nearby handwriting are not this target's student answer. Clearly crossed-out abandoned work is not the active answer. If ownership or cancellation is uncertain, preserve it in risks instead of guessing. answer_state is present only for legible active handwriting; blank/unclear require student_answer="". Empty answer space does not lower confidence in clear printed text. Confidence and risks describe unresolved source/active-answer risks, not routine descriptions.
Distinguish abandoned work from fraction-reduction notation by the visible extent of the strokes. Omit a written fraction or equality step only when it is clearly crossed out as a whole; also omit an equals sign belonging solely to that abandoned step. Slashes through individual numerators or denominators with replacement numerals are reduction notation in a still-active expression, not abandonment of the whole step. Do not invent an extra equality step from those annotations. Retain all readable active handwritten work, including incorrect steps or reductions. Never decide that a step is cancelled because its mathematics is wrong. If cancellation remains genuinely ambiguous, preserve that uncertainty in risks rather than choosing a reading or declaring the active answer complete.
Keep ownership concise and factual. Do not add fields outside this contract. Do not output a separate answer list or an explanation.`

func buildRecognitionInitialReadPromptForPolicy(executionPolicy string) (string, error) {
	switch executionPolicy {
	case "":
		return recognitionLayoutInitialReadPromptV1, nil
	case k12.RecognitionExecutionPolicyCompactDAGV1, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2:
		return recognitionLayoutInitialReadCompactPromptV1, nil
	default:
		return "", fmt.Errorf("%w: unsupported initial read execution policy", k12.ErrRecognitionProtocolInvalid)
	}
}
