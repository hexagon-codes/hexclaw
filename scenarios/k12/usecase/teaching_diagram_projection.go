package usecase

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
)

var frozenTeachingDiagramImage = regexp.MustCompile(`!\[(?:\\.|[^\]\\])*\]\((asset://[^\r\n()]+/[a-f0-9]{64}\.svg)\)`)

func writeParentGuideStepsMarkdown(out *strings.Builder, label string, guide ParentTeachingGuide, pdf bool) {
	if len(guide.TeachingDiagrams) == 0 {
		writeParentGuideListMarkdown(out, label, guide.FullSolutionSteps)
		return
	}
	out.WriteString(label + "\n\n")
	for i, step := range guide.FullSolutionSteps {
		if i == 0 && pdf {
			out.WriteString("```{=typst}\n#block(breakable: false)[\n```\n\n")
		}
		fmt.Fprintf(out, "%d. %s\n", i+1, strings.TrimSpace(step))
		if i == 0 {
			for _, diagram := range guide.TeachingDiagrams {
				alt := strings.NewReplacer("\\", "\\\\", "]", "\\]", "[", "\\[").Replace(diagram.AltText)
				fmt.Fprintf(out, "\n![%s](%s){width=100%%}\n\n", alt, diagram.AssetID)
			}
			if pdf {
				out.WriteString("```{=typst}\n]\n```\n\n")
			}
		}
	}
	out.WriteString("\n")
}

// 只在首次PDF渲染时解析冻结图示；PreparePrintableArtifact已命中完整PDF时不进入此处。
func (d Deps) inlineFrozenTeachingDiagrams(ctx context.Context, agent, markdown string) (string, error) {
	if !frozenTeachingDiagramImage.MatchString(markdown) {
		return markdown, nil
	}
	var firstErr error
	result := frozenTeachingDiagramImage.ReplaceAllStringFunc(markdown, func(image string) string {
		if firstErr != nil {
			return image
		}
		match := frozenTeachingDiagramImage.FindStringSubmatch(image)
		id := match[1]
		if !strings.HasPrefix(id, "asset://"+agent+"/") || d.Records == nil {
			firstErr = fmt.Errorf("teaching diagram asset scope mismatch")
			return image
		}
		owner, err := d.Records.TeachingDiagramOwnerForAgent(ctx, agent, id)
		if err != nil {
			firstErr = fmt.Errorf("required teaching diagram asset unavailable: %w", err)
			return image
		}
		data, err := d.Records.GetTeachingDiagramAsset(ctx, owner, agent, id)
		if err != nil {
			firstErr = fmt.Errorf("required teaching diagram asset unavailable: %w", err)
			return image
		}
		return strings.Replace(image, id, "data:image/svg+xml;base64,"+base64.StdEncoding.EncodeToString(data), 1)
	})
	if firstErr != nil {
		return "", firstErr
	}
	return result, nil
}
