package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ops/internal/tmux"
)

const (
	BaseDir          = "/tmp/ops_tmux"
	SockPath         = "/tmp/ops_tmux/ops.sock"
	maxWindowHistory = 12
	maxRecentCmds    = 20
)

type CommandRecord struct {
	Time string `json:"time"`
	Cmd  string `json:"cmd"`
}

type HistoryEntry struct {
	ID          int64           `json:"id"`
	Time        string          `json:"time"`
	CWD         string          `json:"cwd"`
	ProfileKey  string          `json:"profile_key,omitempty"`
	ModelUsed   string          `json:"model_used"`
	Instruction string          `json:"instruction"`
	PipeOutput  string          `json:"pipe_output"`
	RecentCmds  []CommandRecord `json:"recent_cmds"`
	Status      string          `json:"status"` // "pending", "generating", "done", "error"
	AIResponse  string          `json:"ai_response"`
}

type WindowState struct {
	Variables   map[string]string `json:"variables"`
	RecentCmds  []CommandRecord   `json:"recent_cmds"`
	History     []HistoryEntry    `json:"history"`
	LastEntryID int64             `json:"last_entry_id"`
}

var varAssignRegex = regexp.MustCompile(`(?:^|[;&|]\s*|\s+)(?:export\s+|typeset\s+|local\s+)?([A-Za-z_][A-Za-z0-9_]*)=("[^"]*"|'[^']*'|[^\s;|&]+)`)

func EnsureDir() {
	_ = os.MkdirAll(BaseDir, 0o755)
}

func stateFilePath(winID string) string {
	clean := strings.ReplaceAll(winID, "@", "win_")
	return filepath.Join(BaseDir, fmt.Sprintf("state_%s.json", clean))
}

func paneLogPath(paneID string) string {
	clean := strings.TrimPrefix(paneID, "%")
	return filepath.Join(BaseDir, fmt.Sprintf("pane_%s.log", clean))
}

func Load(winID string) WindowState {
	data, err := os.ReadFile(stateFilePath(winID))
	if err != nil {
		return WindowState{Variables: make(map[string]string)}
	}
	var st WindowState
	if err := json.Unmarshal(data, &st); err != nil {
		return WindowState{Variables: make(map[string]string)}
	}
	if st.Variables == nil {
		st.Variables = make(map[string]string)
	}
	return st
}

// Save guarda el estado de la ventana de forma atomica.
func Save(winID string, st WindowState) {
	if len(st.History) > maxWindowHistory {
		st.History = st.History[len(st.History)-maxWindowHistory:]
	}
	if len(st.RecentCmds) > maxRecentCmds {
		st.RecentCmds = st.RecentCmds[len(st.RecentCmds)-maxRecentCmds:]
	}

	path := stateFilePath(winID)
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(tmp, data, 0o644); err == nil {
		_ = os.Rename(tmp, path)
	}
}

// UpdateEntry actualiza el estado y resultado de una consulta especifica.
func UpdateEntry(winID string, entryID int64, status string, response string) {
	st := Load(winID)
	for i := len(st.History) - 1; i >= 0; i-- {
		if st.History[i].ID == entryID {
			st.History[i].Status = status
			st.History[i].AIResponse = response
			break
		}
	}
	Save(winID, st)
}

// ConsumeCommands lee y limpia los comandos capturados por preexec en la ventana.
func ConsumeCommands(winID string) []CommandRecord {
	var records []CommandRecord
	for _, pane := range tmux.WindowPanes(winID) {
		logFile := paneLogPath(pane)
		data, err := os.ReadFile(logFile)
		if err != nil || len(data) == 0 {
			continue
		}
		_ = os.WriteFile(logFile, nil, 0o644)

		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			parts := strings.SplitN(line, "|", 2)
			if len(parts) != 2 {
				continue
			}
			cmdText := strings.TrimSpace(parts[1])
			if cmdText == "" || strings.HasPrefix(cmdText, "_ops_") {
				continue
			}
			records = append(records, CommandRecord{
				Time: parts[0],
				Cmd:  cmdText,
			})
		}
	}
	return records
}

// ExtractVariables detecta asignaciones VAR=valor en comandos o salidas.
func ExtractVariables(text string, vars map[string]string) {
	matches := varAssignRegex.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		name := m[1]
		val := strings.Trim(m[2], `"'`)
		if len(name) < 2 || strings.HasPrefix(name, "_") || name == "PS1" || name == "PROMPT" || strings.Contains(name, "-") {
			continue
		}
		vars[name] = val
	}
}

func Clear(winID string) {
	_ = os.Remove(stateFilePath(winID))
	for _, pane := range tmux.WindowPanes(winID) {
		_ = os.Remove(paneLogPath(pane))
	}
}
