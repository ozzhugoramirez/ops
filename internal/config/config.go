package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type PromptProfile struct {
	Name   string `json:"name"`
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt"`
}

type OpsConfig struct {
	DefaultModel   string                   `json:"default_model"`
	ThinkingBudget int                      `json:"thinking_budget"`
	Temperature    float64                  `json:"temperature"`
	MaxTokens      int                      `json:"max_tokens"`
	DefaultPrompt  string                   `json:"default_prompt"`
	Prompts        map[string]PromptProfile `json:"prompts"`
}

func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".config/ops"
	}
	return filepath.Join(home, ".config", "ops")
}

func FilePath() string {
	return filepath.Join(Dir(), "config.json")
}

func Default() OpsConfig {
	return OpsConfig{
		DefaultModel:   "gemini-3.8-flash",
		ThinkingBudget: 0,
		Temperature:    0.3,
		MaxTokens:      700,
		DefaultPrompt: `Sos OPS, un asistente tecnico de terminal UNIX/macOS/Linux en tmux.
Reglas:
1. Responde en espanol claro, directo y sin saludos ni relleno.
2. Nunca uses emojis.
3. Usa Markdown: **negrita** para puntos clave, backticks para comandos/rutas en linea y bloques de codigo para ejemplos o comandos.
4. Se breve (4 a 12 lineas) para evitar scroll en panel dividido.
5. Reutiliza variables ($VAR) y comandos previos de la ventana cuando aplique.`,
		Prompts: map[string]PromptProfile{
			"register": {
				Name: "Registro y resumen de logs",
				Prompt: `Analiza registros y salidas de terminal sin usar emojis.
Extrae unicamente lo relevante: errores, advertencias, estado final y datos clave en vinetas breves.
Usa **negrita** para estados criticos y bloques de codigo para comandos de verificacion.`,
			},
			"cmd": {
				Name: "Solo comando",
				Prompt: `Devolve unicamente el comando o one-liner exacto de bash/zsh listo para ejecutar.
Sin explicaciones, sin emojis y sin bloques markdown.`,
			},
			"debug": {
				Name:   "Diagnostico rapido",
				Prompt: `Identifica la causa exacta del fallo en maximo 2 lineas usando **negrita** en el motivo principal y entrega el comando preciso en un bloque de codigo para resolverlo. Sin emojis.`,
			},
		},
	}
}

// Load lee ~/.config/ops/config.json o lo crea con valores por defecto.
func Load() OpsConfig {
	cfgPath := FilePath()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		cfg := Default()
		_ = os.MkdirAll(Dir(), 0o700)
		if raw, mErr := json.MarshalIndent(cfg, "", "  "); mErr == nil {
			_ = os.WriteFile(cfgPath, raw, 0o600)
		}
		return cfg
	}

	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default()
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = "gemini-3.8-flash"
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 700
	}
	if cfg.Prompts == nil {
		cfg.Prompts = make(map[string]PromptProfile)
	}
	return cfg
}

// Resolve selecciona el prompt y el modelo segun el perfil indicado o variable de entorno.
func Resolve(cfg OpsConfig, profileKey string) (string, string, error) {
	model := cfg.DefaultModel
	if envModel := strings.TrimSpace(os.Getenv("OPS_MODEL")); envModel != "" {
		model = envModel
	}

	if profileKey == "" {
		return cfg.DefaultPrompt, model, nil
	}

	prof, ok := cfg.Prompts[profileKey]
	if !ok {
		return "", "", fmt.Errorf("el perfil %q no existe en %s (usa `ops -l` para listar)", profileKey, FilePath())
	}
	if strings.TrimSpace(prof.Model) != "" {
		model = strings.TrimSpace(prof.Model)
	}
	return prof.Prompt, model, nil
}

// LoadAPIKey obtiene la clave desde ~/.config/ops/credentials o GEMINI_API_KEY.
func LoadAPIKey() string {
	credPath := filepath.Join(Dir(), "credentials")
	if data, err := os.ReadFile(credPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			if strings.HasPrefix(line, "GEMINI_API_KEY=") {
				val := strings.TrimPrefix(line, "GEMINI_API_KEY=")
				return strings.Trim(val, `"' `)
			}
			if !strings.Contains(line, "=") {
				return strings.Trim(line, `"' `)
			}
		}
	}
	return strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
}
