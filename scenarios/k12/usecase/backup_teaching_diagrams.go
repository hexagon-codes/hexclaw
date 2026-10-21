package usecase

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

var teachingDiagramAssetIDPattern = regexp.MustCompile(`^asset://[^/\\\r\n]+/[a-f0-9]{64}\.svg$`)
var teachingDiagramArchiveReference = regexp.MustCompile(`asset://[^\r\n()"\\]+?/[a-f0-9]{64}\.svg`)

func validateHexbakTeachingDiagrams(bak *Hexbak) error {
	if bak.Version < 8 && len(bak.TeachingDiagrams) > 0 {
		return fmt.Errorf("teaching diagram archive is not covered by this version")
	}
	if err := k12storage.ValidateTeachingDiagramArchive(bak.AgentName, bak.TeachingDiagrams); err != nil {
		return err
	}
	assets := map[string]bool{}
	for _, asset := range bak.TeachingDiagrams {
		assets[asset.AssetID] = true
	}
	// SVG引用也可能位于冻结Markdown或JSON信封内，不能只检查顶层字符串。
	raw, err := json.Marshal(bak)
	if err != nil {
		return err
	}
	for _, id := range teachingDiagramArchiveReference.FindAllString(string(raw), -1) {
		if !assets[id] {
			return fmt.Errorf("required archived teaching diagram is missing")
		}
	}
	return nil
}

func migratedTeachingDiagramID(source k12storage.TeachingDiagramArchive, target string) string {
	return "asset://" + target + "/" + source.AssetDigest + ".svg"
}

func migrateTeachingDiagramArchive(source *Hexbak, target string, migrated *Hexbak) map[string]string {
	mapping := map[string]string{}
	for i, asset := range source.TeachingDiagrams {
		id := migratedTeachingDiagramID(asset, target)
		mapping[asset.AssetID] = id
		migrated.TeachingDiagrams[i].AgentName = target
		migrated.TeachingDiagrams[i].AssetID = id
	}
	for _, record := range migrated.Records {
		if record == nil {
			continue
		}
		for from, to := range mapping {
			record.Fields = strings.ReplaceAll(record.Fields, from, to)
		}
	}
	return mapping
}
