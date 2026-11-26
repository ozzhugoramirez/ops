package ui

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"ops/internal/config"
	"ops/internal/gemini"
	"ops/internal/state"
	"ops/internal/tmux"
)

const apiTimeout = 45 * time.Second

// Notify avisa al panel derecho mediante el socket Unix.
func Notify(winID string) {
	conn, err := net.DialTimeout("unix", state.SockPath, 60*time.Millisecond)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(winID + "\n"))
}

// RunReceiver mantiene el panel derecho escuchando consultas y cambios de ventana.
func RunReceiver() {
	if os.Getenv("TMUX") == "" {
		fmt.Println("Error: ejecuta `ops` dentro de una sesion de tmux.")
		os.Exit(1)
	}

	state.EnsureDir()
	_ = config.Load()
	_ = os.Remove(state.SockPath)

	listener, err := net.Listen("unix", state.SockPath)
	if err != nil {
		fmt.Printf("Error iniciando socket: %v\n", err)
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		_ = listener.Close()
		_ = os.Remove(state.SockPath)
		fmt.Println("\nOPS finalizado.")
		os.Exit(0)
	}()

	var (
		mu             sync.Mutex
		cancelCurrent  context.CancelFunc
		currentDone    chan struct{}
		currentWinID   string
		renderedEntry  int64
		renderedStatus string
	)

	triggerRender := func() {
		win := tmux.CurrentWindow(false)
		st := state.Load(win.ID)

		var lastStatus string
		if len(st.History) > 0 {
			lastStatus = st.History[len(st.History)-1].Status
		}

		mu.Lock()
		prevCancel := cancelCurrent
		prevDone := currentDone

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		cancelCurrent = cancel
		currentDone = done
		currentWinID = win.ID
		renderedEntry = st.LastEntryID
		renderedStatus = lastStatus
		mu.Unlock()

		go func() {
			defer close(done)
			if prevCancel != nil {
				prevCancel()
			}
			if prevDone != nil {
				select {
				case <-prevDone:
				case <-time.After(250 * time.Millisecond):
				}
			}
			renderView(ctx, win, st)
		}()
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
			triggerRender()
		}
	}()

	triggerRender()

	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		win := tmux.CurrentWindow(false)
		st := state.Load(win.ID)

		var lastStatus string
		if len(st.History) > 0 {
			lastStatus = st.History[len(st.History)-1].Status
		}

		mu.Lock()
		changed := win.ID != currentWinID || st.LastEntryID != renderedEntry || lastStatus != renderedStatus
		mu.Unlock()

		if changed {
			triggerRender()
		}
	}
}

func startSpinner(ctx context.Context, label string) func() {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	done := make(chan struct{})
	var once sync.Once

	go func() {
		start := time.Now()
		i := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				elapsed := time.Since(start).Seconds()
				fmt.Printf("\r\033[K\033[36m%s\033[0m %s \033[90m(%.1fs)\033[0m", frames[i%len(frames)], label, elapsed)
				i++
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(done)
			fmt.Print("\r\033[K")
		})
	}
}

func renderView(ctx context.Context, win tmux.WindowInfo, st state.WindowState) {
	fmt.Print("\033[2J\033[H")

	if len(st.History) == 0 {
		fmt.Println("\033[90mListo. Esperando consulta desde el panel de trabajo...\033[0m")
		return
	}

	last := st.History[len(st.History)-1]

	switch last.Status {
	case "generating":
		stopSpin := startSpinner(ctx, "Procesando y guardando respuesta...")
		<-ctx.Done()
		stopSpin()
		return

	case "done":
		fmt.Println(RenderMarkdown(last.AIResponse))
		return

	case "error":
		fmt.Printf("\033[31m[ERROR]\033[0m %s\n", last.AIResponse)
		return
	}

	apiKey := config.LoadAPIKey()
	if apiKey == "" {
		fmt.Println("\033[31m[ERROR]\033[0m Falta GEMINI_API_KEY en ~/.config/ops/credentials")
		return
	}

	cfg := config.Load()
	sysPrompt, model, err := config.Resolve(cfg, last.ProfileKey)
	if err != nil {
		fmt.Printf("\033[31m[ERROR]\033[0m %v\n", err)
		return
	}

	reqCtx, cancelTimeout := context.WithTimeout(ctx, apiTimeout)
	defer cancelTimeout()

	prompt := gemini.BuildPrompt(st, last)
	stopSpin := startSpinner(reqCtx, "Consultando...")
	firstChunk := true
	mdStream := NewStreamRenderer(os.Stdout)

	fullResponse, err := gemini.Stream(reqCtx, apiKey, model, sysPrompt, cfg, prompt, func(chunk string) {
		if firstChunk {
			stopSpin()
			firstChunk = false
		}
		mdStream.Write(chunk)
	})
	stopSpin()

	if ctx.Err() != nil {
		return
	}

	if err != nil {
		errMsg := err.Error()
		if reqCtx.Err() == context.DeadlineExceeded {
			errMsg = "Tiempo de espera agotado al consultar el modelo."
		}
		fmt.Printf("\033[31m[ERROR]\033[0m %s\n", errMsg)
		state.UpdateEntry(win.ID, last.ID, "error", errMsg)
		return
	}

	mdStream.Flush()
	state.UpdateEntry(win.ID, last.ID, "done", strings.TrimSpace(fullResponse))
}
