package tmux

import (
	"os"
	"os/exec"
	"strings"
)

type WindowInfo struct {
	ID    string `json:"id"`
	Index string `json:"index"`
	Name  string `json:"name"`
	CWD   string `json:"cwd"`
}

// CurrentWindow consulta la ventana activa o la asociada al panel emisor.
func CurrentWindow(useSenderPane bool) WindowInfo {
	if os.Getenv("TMUX") == "" {
		cwd, _ := os.Getwd()
		return WindowInfo{ID: "@local", Index: "0", Name: "local", CWD: cwd}
	}

	args := []string{"display-message", "-p"}
	if useSenderPane {
		if pane := os.Getenv("TMUX_PANE"); pane != "" {
			args = append(args, "-t", pane)
		}
	}
	args = append(args, "#{window_id}|#{window_index}|#{window_name}|#{pane_current_path}")

	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		cwd, _ := os.Getwd()
		return WindowInfo{ID: "@local", Index: "0", Name: "local", CWD: cwd}
	}

	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 4)
	if len(parts) < 4 {
		return WindowInfo{ID: "@local", Index: "0", Name: "local"}
	}
	return WindowInfo{
		ID:    parts[0],
		Index: parts[1],
		Name:  parts[2],
		CWD:   parts[3],
	}
}

// WindowPanes devuelve los identificadores de paneles de una ventana de tmux.
func WindowPanes(winID string) []string {
	if os.Getenv("TMUX") == "" || winID == "@local" {
		if pane := os.Getenv("TMUX_PANE"); pane != "" {
			return []string{pane}
		}
		return nil
	}
	out, err := exec.Command("tmux", "list-panes", "-t", winID, "-F", "#{pane_id}").Output()
	if err != nil {
		return nil
	}
	var panes []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			panes = append(panes, line)
		}
	}
	return panes
}

// ZshHook genera el hook preexec nativo para registrar comandos reales por panel.
func ZshHook() string {
	return `zmodload zsh/datetime
_ops_preexec() {
  [[ -n "$TMUX_PANE" ]] || return
  [[ -d /tmp/ops_tmux ]] || mkdir -p /tmp/ops_tmux
  local ts
  strftime -s ts '%H:%M:%S' "$EPOCHSECONDS"
  print -r -- "${ts}|$1" >> "/tmp/ops_tmux/pane_${TMUX_PANE//\%/}.log"
}
autoload -Uz add-zsh-hook
add-zsh-hook preexec _ops_preexec`
}
