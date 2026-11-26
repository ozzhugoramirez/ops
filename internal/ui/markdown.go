package ui

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var (
	boldRe    = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	bulletRe  = regexp.MustCompile(`^(\s*)[-*]\s+(.*)$`)
	numListRe = regexp.MustCompile(`^(\s*)(\d+\.)\s+(.*)$`)
	headingRe = regexp.MustCompile(`^#{1,4}\s+(.*)$`)
)

// StreamRenderer formatea Markdown con secuencias ANSI linea por linea durante el streaming.
type StreamRenderer struct {
	out         io.Writer
	buf         strings.Builder
	inCodeBlock bool
}

func NewStreamRenderer(w io.Writer) *StreamRenderer {
	return &StreamRenderer{out: w}
}

func (r *StreamRenderer) Write(chunk string) {
	r.buf.WriteString(chunk)
	for {
		content := r.buf.String()
		idx := strings.IndexByte(content, '\n')
		if idx == -1 {
			break
		}
		line := content[:idx]
		r.buf.Reset()
		r.buf.WriteString(content[idx+1:])

		formatted, skip := formatLine(line, &r.inCodeBlock)
		if !skip {
			fmt.Fprintln(r.out, formatted)
		}
	}
}

func (r *StreamRenderer) Flush() {
	rem := r.buf.String()
	r.buf.Reset()
	if rem == "" {
		return
	}
	formatted, skip := formatLine(rem, &r.inCodeBlock)
	if !skip {
		fmt.Fprintln(r.out, formatted)
	}
}

// RenderMarkdown convierte un bloque Markdown completo a salida ANSI limpia.
func RenderMarkdown(text string) string {
	var out bytes.Buffer
	sr := NewStreamRenderer(&out)
	sr.Write(text)
	sr.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func formatLine(raw string, inCodeBlock *bool) (string, bool) {
	line := strings.TrimRight(raw, "\r")
	trimmed := strings.TrimSpace(line)

	if strings.HasPrefix(trimmed, "```") {
		*inCodeBlock = !*inCodeBlock
		if *inCodeBlock {
			lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			if lang != "" {
				return "\033[36m┌─ " + lang + "\033[0m", false
			}
			return "\033[36m┌──\033[0m", false
		}
		return "\033[36m└──\033[0m", false
	}

	if *inCodeBlock {
		if strings.HasPrefix(trimmed, "#") {
			return "\033[36m│\033[0m \033[32m" + line + "\033[0m", false
		}
		return "\033[36m│\033[0m \033[96m" + line + "\033[0m", false
	}

	if m := headingRe.FindStringSubmatch(trimmed); m != nil {
		return "\033[1;36m" + formatInline(m[1]) + "\033[0m", false
	}

	line = bulletRe.ReplaceAllString(line, "$1\033[36m•\033[0m $2")
	line = numListRe.ReplaceAllString(line, "$1\033[36m$2\033[0m $3")
	return formatInline(line), false
}

// formatInline resalta codigo entre backticks y negritas sin interferir entre si.
func formatInline(s string) string {
	parts := strings.Split(s, "`")
	for i := 0; i < len(parts); i++ {
		if i%2 == 1 {
			parts[i] = "\033[1;96m" + parts[i] + "\033[0m"
		} else {
			parts[i] = boldRe.ReplaceAllString(parts[i], "\033[1;97m$1\033[0m")
		}
	}
	return strings.Join(parts, "")
}
