package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name        string
	Description string
	Content     string
	Dir         string
	Commands    map[string]string
}

type frontmatter struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Commands    map[string]string `yaml:"commands"`
}

// LoadAll 扫描 skills 目录下每个子目录中的 SKILL.md
func LoadAll(dir string) ([]*Skill, error) {
	if dir == "" {
		return nil, nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, 0o755)
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var skills []*Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mdPath := filepath.Join(dir, e.Name(), "SKILL.md")
		if _, err := os.Stat(mdPath); os.IsNotExist(err) {
			continue
		}
		sk, err := loadOne(mdPath)
		if err != nil {
			return nil, err
		}
		skills = append(skills, sk)
	}
	return skills, nil
}

func loadOne(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(data)
	// 解析 frontmatter (--- ... ---)
	fm := &frontmatter{}
	rest := content
	if strings.HasPrefix(strings.TrimSpace(content), "---") {
		parts := strings.SplitN(strings.TrimSpace(content)[3:], "---", 2)
		if len(parts) == 2 {
			if err := yaml.Unmarshal([]byte(parts[0]), fm); err != nil {
				return nil, fmt.Errorf("解析 %s frontmatter: %w", path, err)
			}
			rest = parts[1]
		}
	}
	name := fm.Name
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	return &Skill{
		Name:        name,
		Description: fm.Description,
		Content:     strings.TrimSpace(rest),
		Dir:         filepath.Dir(path),
		Commands:    fm.Commands,
	}, nil
}

// ToSystemContext 生成注入 system prompt 的 skill 描述
func (s *Skill) ToSystemContext() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[Skill: %s]\n", s.Name))
	if s.Description != "" {
		b.WriteString("用途: " + s.Description + "\n")
	}
	b.WriteString(s.Content)
	b.WriteString("\n")
	if len(s.Commands) > 0 {
		b.WriteString("可执行命令: \n")
		for k, v := range s.Commands {
			b.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
		}
	}
	return b.String()
}

// RenderAllSkills 把所有 skill 汇总为一段文本
func RenderAllSkills(skills []*Skill) string {
	var b strings.Builder
	for _, s := range skills {
		b.WriteString(s.ToSystemContext())
		b.WriteString("\n")
	}
	return b.String()
}
