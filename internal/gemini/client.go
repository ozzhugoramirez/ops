package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"ops/internal/config"
	"ops/internal/state"
)

const maxPipeChars = 4500

type part struct {
	Text string `json:"text"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type thinkingConfig struct {
	ThinkingBudget int `json:"thinkingBudget"`
}

type genConfig struct {
	Temperature     float64         `json:"temperature"`
	MaxOutputTokens int             `json:"maxOutputTokens"`
	ThinkingConfig  *thinkingConfig `json:"thinkingConfig,omitempty"`
}

type requestPayload struct {
	SystemInstruction *content  `json:"systemInstruction,omitempty"`
	Contents          []content `json:"contents"`
	GenerationConfig  genConfig `json:"generationConfig"`
}

type streamChunk struct {
	Candidates []struct {
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

var httpClient = &http.Client{
	Transport: &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	},
}

func truncateOutput(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	half := maxChars / 2
	return s[:half] + "\n... [salida recortada por longitud] ...\n" + s[len(s)-half:]
}

// BuildPrompt construye el contexto de entorno, variables y comandos de la ventana.
func BuildPrompt(st state.WindowState, entry state.HistoryEntry) string {
	var b strings.Builder

	fmt.Fprintf(&b, "SO: %s/%s | Directorio: %s\n", runtime.GOOS, runtime.GOARCH, entry.CWD)

	if len(st.Variables) > 0 {
		b.WriteString("\nVariables activas:\n")
		for k, v := range st.Variables {
			fmt.Fprintf(&b, "- $%s=%s\n", k, v)
		}
	}

	if len(entry.RecentCmds) > 0 {
		b.WriteString("\nUltimos comandos reales ejecutados en esta ventana:\n")
		start := len(entry.RecentCmds) - 6
		if start < 0 {
			start = 0
		}
		for _, c := range entry.RecentCmds[start:] {
			fmt.Fprintf(&b, "[%s] %s\n", c.Time, c.Cmd)
		}
	}

	if len(st.History) > 1 {
		b.WriteString("\nHistorial reciente de esta ventana:\n")
		start := len(st.History) - 3
		if start < 0 {
			start = 0
		}
		for _, prev := range st.History[start : len(st.History)-1] {
			if prev.Status == "done" && prev.AIResponse != "" {
				fmt.Fprintf(&b, "- Pedido: %q -> Respuesta: %s\n", prev.Instruction, strings.ReplaceAll(prev.AIResponse, "\n", " | "))
			}
		}
	}

	b.WriteString("\n--- CONSULTA ACTUAL ---\n")
	if entry.Instruction != "" {
		fmt.Fprintf(&b, "Pedido: %s\n", entry.Instruction)
	} else {
		b.WriteString("Pedido: Analiza esta salida y aplica el formato configurado.\n")
	}

	if entry.PipeOutput != "" {
		fmt.Fprintf(&b, "Salida de terminal:\n```\n%s\n```\n", truncateOutput(entry.PipeOutput, maxPipeChars))
	}

	return b.String()
}

// Stream ejecuta la consulta SSE contra Gemini y entrega cada fragmento recibido.
func Stream(ctx context.Context, apiKey string, model string, sysPrompt string, cfg config.OpsConfig, prompt string, onChunk func(string)) (string, error) {
	endpoint := "https://generativelanguage.googleapis.com/v1beta/models/" + model + ":streamGenerateContent?alt=sse"

	gen := genConfig{
		Temperature:     cfg.Temperature,
		MaxOutputTokens: cfg.MaxTokens,
	}
	if cfg.ThinkingBudget >= 0 {
		gen.ThinkingConfig = &thinkingConfig{ThinkingBudget: cfg.ThinkingBudget}
	}

	body := requestPayload{
		SystemInstruction: &content{Parts: []part{{Text: sysPrompt}}},
		Contents: []content{
			{Role: "user", Parts: []part{{Text: prompt}}},
		},
		GenerationConfig: gen,
	}

	rawBody, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		errStr := strings.TrimSpace(string(errBytes))
		if resp.StatusCode == http.StatusBadRequest && gen.ThinkingConfig != nil && strings.Contains(strings.ToLower(errStr), "thinking") {
			fallback := cfg
			fallback.ThinkingBudget = -1
			return Stream(ctx, apiKey, model, sysPrompt, fallback, prompt, onChunk)
		}
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, errStr)
	}

	var fullText strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return fullText.String(), ctx.Err()
		default:
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return fullText.String(), fmt.Errorf("API: %s", chunk.Error.Message)
		}
		for _, cand := range chunk.Candidates {
			for _, p := range cand.Content.Parts {
				if p.Text != "" {
					fullText.WriteString(p.Text)
					if onChunk != nil {
						onChunk(p.Text)
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return fullText.String(), err
	}
	return fullText.String(), nil
}
