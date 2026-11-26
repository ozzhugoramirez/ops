package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"ops/internal/config"
	"ops/internal/gemini"
	"ops/internal/state"
	"ops/internal/tmux"
	"ops/internal/ui"
)

const directTimeout = 45 * time.Second

func writeOutputFile(path string, appendMode bool, content string) error {
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	_, err = f.WriteString(content)
	return err
}

func runSender(instruction, pipeOutput, profileKey, outFile string, appendMode, stdoutRedirected bool) error {
	state.EnsureDir()
	cfg := config.Load()
	sysPrompt, model, err := config.Resolve(cfg, profileKey)
	if err != nil {
		return err
	}

	win := tmux.CurrentWindow(true)
	st := state.Load(win.ID)

	for _, c := range state.ConsumeCommands(win.ID) {
		state.ExtractVariables(c.Cmd, st.Variables)
		st.RecentCmds = append(st.RecentCmds, c)
	}
	state.ExtractVariables(instruction+"\n"+pipeOutput, st.Variables)

	needDirectOutput := stdoutRedirected || outFile != ""
	status := "pending"
	if needDirectOutput {
		status = "generating"
	}

	entryID := time.Now().UnixNano()
	entry := state.HistoryEntry{
		ID:          entryID,
		Time:        time.Now().Format("15:04:05"),
		CWD:         win.CWD,
		ProfileKey:  profileKey,
		ModelUsed:   model,
		Instruction: strings.TrimSpace(instruction),
		PipeOutput:  strings.TrimRight(pipeOutput, "\r\n"),
		RecentCmds:  append([]state.CommandRecord(nil), st.RecentCmds...),
		Status:      status,
	}

	st.History = append(st.History, entry)
	st.LastEntryID = entryID
	state.Save(win.ID, st)
	ui.Notify(win.ID)

	if !needDirectOutput {
		return nil
	}

	// Garantiza que el estado final siempre se persista y desbloquee el panel derecho
	finalStatus := "error"
	finalText := "Proceso interrumpido antes de completar la respuesta."
	defer func() {
		state.UpdateEntry(win.ID, entryID, finalStatus, finalText)
		ui.Notify(win.ID)
	}()

	apiKey := config.LoadAPIKey()
	if apiKey == "" {
		finalText = "Falta GEMINI_API_KEY en ~/.config/ops/credentials"
		return fmt.Errorf("%s", finalText)
	}

	ctx, cancel := context.WithTimeout(context.Background(), directTimeout)
	defer cancel()

	prompt := gemini.BuildPrompt(st, entry)
	fullResp, err := gemini.Stream(ctx, apiKey, model, sysPrompt, cfg, prompt, nil)
	if err != nil {
		finalText = err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			finalText = "Tiempo de espera agotado al consultar el modelo."
		}
		return fmt.Errorf("%s", finalText)
	}

	cleanResp := strings.TrimSpace(fullResp)

	if outFile != "" {
		if wErr := writeOutputFile(outFile, appendMode, cleanResp); wErr != nil {
			finalText = fmt.Sprintf("No se pudo escribir en %s: %v", outFile, wErr)
			return fmt.Errorf("%s", finalText)
		}
	}
	if stdoutRedirected {
		fmt.Println(cleanResp)
	}

	finalStatus = "done"
	finalText = cleanResp
	return nil
}

func listPrompts() {
	cfg := config.Load()
	fmt.Printf("Configuracion: %s\n", config.FilePath())
	fmt.Printf("Modelo base:   %s (thinking_budget: %d)\n\n", cfg.DefaultModel, cfg.ThinkingBudget)
	fmt.Println("Perfiles disponibles:")
	fmt.Println("  (sin -p)     -> Prompt principal por defecto")

	keys := make([]string, 0, len(cfg.Prompts))
	for k := range cfg.Prompts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		p := cfg.Prompts[k]
		tag := ""
		if p.Model != "" {
			tag = fmt.Sprintf(" [%s]", p.Model)
		}
		fmt.Printf("  -p %-10s -> %s%s\n", k, p.Name, tag)
	}
}

func main() {
	args := os.Args[1:]

	quiet := false
	profileKey := ""
	outFile := ""
	appendMode := false
	var cleanArgs []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--hook":
			fmt.Println(tmux.ZshHook())
			return
		case a == "-c" || a == "--clear":
			state.EnsureDir()
			win := tmux.CurrentWindow(true)
			state.Clear(win.ID)
			ui.Notify(win.ID)
			fmt.Printf("Contexto de ventana [%s:%s] limpiado.\n", win.Index, win.Name)
			return
		case a == "-l" || a == "--prompts":
			listPrompts()
			return
		case a == "-q" || a == "--quiet":
			quiet = true
		case (a == "-p" || a == "--prompt") && i+1 < len(args):
			i++
			profileKey = args[i]
		case strings.HasPrefix(a, "--prompt="):
			profileKey = strings.TrimPrefix(a, "--prompt=")
		case (a == "-o" || a == "--out") && i+1 < len(args):
			i++
			outFile = args[i]
			appendMode = false
		case (a == "-a" || a == "--append") && i+1 < len(args):
			i++
			outFile = args[i]
			appendMode = true
		default:
			cleanArgs = append(cleanArgs, a)
		}
	}

	inStat, _ := os.Stdin.Stat()
	hasPipe := (inStat.Mode() & os.ModeCharDevice) == 0

	outStat, _ := os.Stdout.Stat()
	stdoutRedirected := (outStat.Mode() & os.ModeCharDevice) == 0

	instruction := strings.Join(cleanArgs, " ")

	if hasPipe {
		var buf bytes.Buffer
		var dst io.Writer = &buf
		if !quiet && !stdoutRedirected {
			dst = io.MultiWriter(os.Stdout, &buf)
		}
		_, _ = io.Copy(dst, os.Stdin)
		if err := runSender(instruction, buf.String(), profileKey, outFile, appendMode, stdoutRedirected); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else if instruction != "" || profileKey != "" {
		if err := runSender(instruction, "", profileKey, outFile, appendMode, stdoutRedirected); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else {
		ui.RunReceiver()
	}
}
