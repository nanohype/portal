package tfparse

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxFileSize  = 1 << 20 // 1 MB
	maxVariables = 500
)

// DiscoveredVariable represents a variable block parsed from a .tf file.
type DiscoveredVariable struct {
	Name        string  `json:"name"`
	Type        string  `json:"type,omitempty"`
	Description string  `json:"description,omitempty"`
	Default     *string `json:"default,omitempty"`
	Required    bool    `json:"required"`
}

var variableBlockRe = regexp.MustCompile(`(?m)^variable\s+"([^"]+)"\s*\{`)

// ParseVariables extracts variable blocks from HCL content using regex and brace counting.
func ParseVariables(content string) []DiscoveredVariable {
	matches := variableBlockRe.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return nil
	}

	var vars []DiscoveredVariable
	for _, m := range matches {
		if len(vars) >= maxVariables {
			break
		}

		name := content[m[2]:m[3]]
		// Find the opening brace
		braceStart := strings.Index(content[m[0]:], "{")
		if braceStart < 0 {
			continue
		}
		blockStart := m[0] + braceStart

		// Count braces to find block end
		depth := 0
		blockEnd := -1
		for i := blockStart; i < len(content); i++ {
			switch content[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					blockEnd = i
					goto found
				}
			case '"':
				// Skip quoted strings to avoid counting braces inside them
				for i++; i < len(content) && content[i] != '"'; i++ {
					if content[i] == '\\' {
						i++
					}
				}
			case '#':
				// Skip single-line comments
				for i++; i < len(content) && content[i] != '\n'; i++ {
				}
			}
		}
	found:
		if blockEnd < 0 {
			continue
		}

		body := content[blockStart+1 : blockEnd]
		v := DiscoveredVariable{
			Name:        name,
			Type:        extractAttribute(body, "type"),
			Description: extractStringAttribute(body, "description"),
		}

		if def, ok := extractDefault(body); ok {
			v.Default = &def
			v.Required = false
		} else {
			v.Required = true
		}

		vars = append(vars, v)
	}

	return vars
}

var (
	stringAttrRe  = regexp.MustCompile(`(?m)^\s*(\w+)\s*=\s*"([^"]*)"`)
	typeAttrRe    = regexp.MustCompile(`(?m)^\s*type\s*=\s*(.+)`)
	defaultAttrRe = regexp.MustCompile(`(?m)^\s*default\s*=\s*(.+)`)
)

func extractStringAttribute(body, name string) string {
	for _, m := range stringAttrRe.FindAllStringSubmatch(body, -1) {
		if m[1] == name {
			return m[2]
		}
	}
	return ""
}

func extractAttribute(body, name string) string {
	if name == "type" {
		m := typeAttrRe.FindStringSubmatch(body)
		if m == nil {
			return ""
		}
		val := strings.TrimSpace(m[1])
		// Handle multiline type expressions like list(object({...}))
		if needsBalancing(val) {
			val = balanceValue(body, typeAttrRe, val)
		}
		return strings.TrimSpace(val)
	}
	return extractStringAttribute(body, name)
}

func extractDefault(body string) (string, bool) {
	m := defaultAttrRe.FindStringSubmatchIndex(body)
	if m == nil {
		return "", false
	}
	// Get the value starting position
	valStart := m[2]
	val := strings.TrimSpace(body[valStart:m[3]])

	// If it's a simple quoted string
	if strings.HasPrefix(val, "\"") {
		end := strings.Index(val[1:], "\"")
		if end >= 0 {
			return val[1 : end+1], true
		}
	}

	// If it needs balancing (maps, lists, objects)
	if needsBalancing(val) {
		val = balanceValue(body, defaultAttrRe, val)
	}

	return strings.TrimSpace(val), true
}

func needsBalancing(val string) bool {
	opens := strings.Count(val, "{") + strings.Count(val, "[") + strings.Count(val, "(")
	closes := strings.Count(val, "}") + strings.Count(val, "]") + strings.Count(val, ")")
	return opens > closes
}

func balanceValue(body string, re *regexp.Regexp, initial string) string {
	loc := re.FindStringIndex(body)
	if loc == nil {
		return initial
	}
	// Start scanning from where the value begins
	eqIdx := strings.Index(body[loc[0]:], "=")
	if eqIdx < 0 {
		return initial
	}
	start := loc[0] + eqIdx + 1

	depth := 0
	var end int
	for i := start; i < len(body); i++ {
		switch body[i] {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
			if depth <= 0 {
				end = i + 1
				return strings.TrimSpace(body[start:end])
			}
		case '"':
			for i++; i < len(body) && body[i] != '"'; i++ {
				if body[i] == '\\' {
					i++
				}
			}
		}
	}
	return initial
}

// ParseDirectory reads all .tf files in a directory (non-recursive) and returns deduplicated variables.
func ParseDirectory(dir string) ([]DiscoveredVariable, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var result []DiscoveredVariable

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".tf") {
			continue
		}
		if filepath.Ext(entry.Name()) != ".tf" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Size() > maxFileSize {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}

		vars := ParseVariables(string(data))
		for _, v := range vars {
			if seen[v.Name] {
				continue
			}
			seen[v.Name] = true
			result = append(result, v)
			if len(result) >= maxVariables {
				return result, nil
			}
		}
	}

	return result, nil
}

// ParseTerragruntInputs reads the terragrunt.hcl at dir and extracts the
// `inputs = { ... }` block into DiscoveredVariable entries. Only the leaf's
// own inputs are surfaced — values inherited via include/_envcommon are not
// followed (full resolution would require shelling out to `terragrunt
// render-json`).
//
// Returned entries have Default set to the literal RHS text (e.g. "3",
// `"us-west-2"`, `{ Env = "prod" }`) and Required=false. The handler is
// expected to mark them Configured=true since terragrunt already owns the
// value at run time.
func ParseTerragruntInputs(dir string) ([]DiscoveredVariable, error) {
	path := filepath.Join(dir, "terragrunt.hcl")
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFileSize {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseTerragruntInputs(string(data)), nil
}

var inputsBlockRe = regexp.MustCompile(`(?m)^\s*inputs\s*=\s*\{`)

func parseTerragruntInputs(content string) []DiscoveredVariable {
	loc := inputsBlockRe.FindStringIndex(content)
	if loc == nil {
		return nil
	}
	openIdx := strings.Index(content[loc[0]:], "{")
	if openIdx < 0 {
		return nil
	}
	blockStart := loc[0] + openIdx

	// Balance-count to find the matching close brace.
	depth := 0
	blockEnd := -1
	for i := blockStart; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				blockEnd = i
				goto found
			}
		case '"':
			for i++; i < len(content) && content[i] != '"'; i++ {
				if content[i] == '\\' {
					i++
				}
			}
		case '#':
			for i++; i < len(content) && content[i] != '\n'; i++ {
			}
		}
	}
found:
	if blockEnd < 0 {
		return nil
	}

	body := content[blockStart+1 : blockEnd]
	return parseInputAssignments(body)
}

// parseInputAssignments walks the body of an inputs block and pulls each
// top-level `key = value` pair. Values may span multiple lines (maps,
// lists, function calls); they're captured as literal text via brace and
// bracket balance counting.
func parseInputAssignments(body string) []DiscoveredVariable {
	var out []DiscoveredVariable
	i := 0
	for i < len(body) {
		// Skip whitespace, commas, and comments at depth 0.
		switch {
		case body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r' || body[i] == ',':
			i++
			continue
		case body[i] == '#':
			for i < len(body) && body[i] != '\n' {
				i++
			}
			continue
		case strings.HasPrefix(body[i:], "//"):
			for i < len(body) && body[i] != '\n' {
				i++
			}
			continue
		}

		// Read an identifier.
		start := i
		for i < len(body) && (body[i] == '_' || body[i] == '-' || (body[i] >= 'a' && body[i] <= 'z') || (body[i] >= 'A' && body[i] <= 'Z') || (body[i] >= '0' && body[i] <= '9')) {
			i++
		}
		if i == start {
			i++
			continue
		}
		name := body[start:i]

		// Expect optional whitespace then '='.
		for i < len(body) && (body[i] == ' ' || body[i] == '\t') {
			i++
		}
		if i >= len(body) || body[i] != '=' {
			continue
		}
		i++ // consume '='
		for i < len(body) && (body[i] == ' ' || body[i] == '\t') {
			i++
		}

		// Capture the value, respecting nested braces/brackets/parens and strings.
		valStart := i
		depth := 0
		for i < len(body) {
			c := body[i]
			if depth == 0 && (c == '\n' || c == ',') {
				break
			}
			switch c {
			case '{', '[', '(':
				depth++
			case '}', ']', ')':
				if depth == 0 {
					break
				}
				depth--
			case '"':
				i++
				for i < len(body) && body[i] != '"' {
					if body[i] == '\\' {
						i++
					}
					i++
				}
			case '#':
				if depth == 0 {
					goto afterValue
				}
				for i < len(body) && body[i] != '\n' {
					i++
				}
				continue
			}
			i++
		}
	afterValue:
		val := strings.TrimSpace(body[valStart:i])
		if val == "" {
			continue
		}
		def := val
		out = append(out, DiscoveredVariable{
			Name:     name,
			Default:  &def,
			Required: false,
		})
		if len(out) >= maxVariables {
			break
		}
	}
	return out
}
