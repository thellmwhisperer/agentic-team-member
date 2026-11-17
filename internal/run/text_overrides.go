package run

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

var textNames = []string{"system-md", "brief-template", "ponytail-brief-template", "ponytail-skill",
	"ponytail-review-skill", "rejection-message"}

type textFile struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

func (a *agent) textFlags(fs *flag.FlagSet) {
	for _, name := range textNames {
		fs.Func(name, "a file replacing "+name+" for this run; an empty file turns the text off", func(path string) error {
			if a.textFiles == nil {
				a.textFiles = map[string]string{}
			}
			a.textFiles[name] = path
			return nil
		})
	}
}

func (a *agent) readTexts() error {
	for _, name := range textNames {
		file, ok := a.textFiles[name]
		if !ok {
			continue
		}
		path, err := filepath.Abs(file)
		var b []byte
		if err == nil {
			b, err = os.ReadFile(path)
		}
		if err != nil {
			return fmt.Errorf("--%s: %w", name, err)
		}
		if a.texts == nil {
			a.texts = map[string]*runSystemPrompt{}
		}
		a.texts[name] = &runSystemPrompt{File: path, SHA256: fmt.Sprintf("%x", sha256.Sum256(b)), Text: string(b)}
	}
	return nil
}

func (a agent) text(name, fallback string) string {
	if replacement := a.texts[name]; replacement != nil {
		return replacement.Text
	}
	return fallback
}

func (a agent) textMetadata() map[string]textFile {
	files := make(map[string]textFile, len(a.texts))
	for name, replacement := range a.texts {
		files[name] = textFile{File: replacement.File, SHA256: replacement.SHA256}
	}
	return files
}

// Workers have no default skill; an explicit replacement opts into ponytail.
func (a agent) skillName(skill string) string {
	if skill == "" && a.texts["ponytail-skill"] != nil {
		skill = "ponytail"
	}
	if replacement := a.texts[skill+"-skill"]; replacement != nil && replacement.Text == "" {
		return ""
	}
	return skill
}

func (a agent) placeSkill(clone, skill string) (string, error) {
	skill = a.skillName(skill)
	return placeSkill(clone, a.Harness, skill, a.texts[skill+"-skill"])
}
