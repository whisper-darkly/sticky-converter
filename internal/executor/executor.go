package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

// FileVars holds the path components available in command/target templates.
type FileVars struct {
	Dir      string // directory containing the file
	Name     string // full filename (base + ext)
	Basename string // filename without extension
	Ext      string // file extension including leading dot
}

// NewFileVars populates FileVars from an absolute path.
func NewFileVars(absPath string) FileVars {
	name := filepath.Base(absPath)
	ext := filepath.Ext(name)
	return FileVars{
		Dir:      filepath.Dir(absPath),
		Name:     name,
		Basename: strings.TrimSuffix(name, ext),
		Ext:      ext,
	}
}

// TemplateData is the data available inside command and target templates.
type TemplateData struct {
	Input  string
	Output string
	Extra  string // JSON-encoded merged extra map
	File   FileVars
}

// RenderTargetPath derives the output path for inputPath using the pipeline's
// target regex (optional named groups) and format template.
func RenderTargetPath(inputPath, regexStr, formatTmpl string) (string, error) {
	fv := NewFileVars(inputPath)
	data := map[string]any{
		"File": fv,
	}

	if regexStr != "" {
		re, err := regexp.Compile(regexStr)
		if err != nil {
			return "", fmt.Errorf("target regex: %w", err)
		}
		m := re.FindStringSubmatch(filepath.Base(inputPath))
		if m != nil {
			for i, name := range re.SubexpNames() {
				if name != "" && i < len(m) {
					data[name] = m[i]
				}
			}
		}
	}

	tmpl, err := template.New("target").Parse(formatTmpl)
	if err != nil {
		return "", fmt.Errorf("parse target template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render target template: %w", err)
	}
	return buf.String(), nil
}

// MergeExtra merges base (from YAML) with override (from DB). Override wins.
func MergeExtra(base map[string]any, overrideJSON string) (string, error) {
	merged := make(map[string]any)
	for k, v := range base {
		merged[k] = v
	}
	if overrideJSON != "" && overrideJSON != "{}" {
		var override map[string]any
		if err := json.Unmarshal([]byte(overrideJSON), &override); err != nil {
			return "", fmt.Errorf("parse override extra: %w", err)
		}
		for k, v := range override {
			merged[k] = v
		}
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// RenderCommand renders the command template and splits it into argv.
func RenderCommand(cmdTmpl, inputPath, outputPath, extraJSON string) ([]string, error) {
	fv := NewFileVars(inputPath)
	data := TemplateData{
		Input:  inputPath,
		Output: outputPath,
		Extra:  extraJSON,
		File:   fv,
	}
	tmpl, err := template.New("cmd").Parse(cmdTmpl)
	if err != nil {
		return nil, fmt.Errorf("parse command template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render command template: %w", err)
	}
	return parseArgs(buf.String()), nil
}

// parseArgs splits a command string into argv respecting quoted strings.
// Ported from chaturbate-dvr/server/converter.go.
func parseArgs(s string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case (r == '"' || r == '\'') && !inQuote:
			inQuote = true
			quoteChar = r
		case r == quoteChar && inQuote:
			inQuote = false
			quoteChar = 0
		case r == ' ' && !inQuote:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		case r == '\\' && i+1 < len(runes):
			next := runes[i+1]
			if next == '"' || next == '\'' || next == '\\' {
				current.WriteRune(next)
				i++
				continue
			}
			current.WriteRune(r)
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}
