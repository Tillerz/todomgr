package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	Version = "0.10.0"
	AppName = "Tillerz Todo Mgr"
)

func main() {
	cmd, args := splitCommand(os.Args[1:])
	if cmd == "" {
		cmd = "serve"
		if len(args) == 0 {
			args = []string{"--open"} // double-click / plain "todo": start and open the browser
		}
	}
	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "mcp":
		err = runMCPStdio(args)
	case "version", "--version", "-v":
		fmt.Printf("%s v%s (data schema %d)\n", AppName, Version, schemaVersion)
	case "help", "--help", "-h":
		fmt.Print(usageText)
	default:
		err = runCLI(cmd, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// splitCommand finds the command word even if options come first
// ("todo --data D list"); everything else is passed on in order.
func splitCommand(args []string) (string, []string) {
	valued := map[string]bool{"--data": true, "--url": true, "--token": true, "--port": true, "--ui": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--help", "-h":
			return "help", nil
		case "--version", "-v":
			return "version", nil
		}
		if strings.HasPrefix(a, "-") {
			if valued[a] && i+1 < len(args) {
				i++
			}
			continue
		}
		rest := append(append([]string{}, args[:i]...), args[i+1:]...)
		return a, rest
	}
	return "", args
}

// resolveDataDir: --data, then $TODOMGR_DATA, then "data" next to the program
// (so the whole folder stays portable). When started with "go run" the program
// lives in a temp folder, so "./data" is used instead.
func resolveDataDir(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("TODOMGR_DATA"); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		if !strings.Contains(exe, "go-build") && !strings.HasPrefix(exe, os.TempDir()) {
			return filepath.Join(filepath.Dir(exe), "data")
		}
	}
	return "data"
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dataFlag := fs.String("data", "", "data folder (default: ./data next to the program)")
	port := fs.Int("port", 0, fmt.Sprintf("port (default: from config.json, %d)", DefaultPort))
	ui := fs.String("ui", "", "serve the UI from this folder instead of the embedded copy (for development)")
	open := fs.Bool("open", false, "open the app in the default browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := resolveDataDir(*dataFlag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		return err
	}
	p := cfg.Get().Port
	if *port > 0 {
		p = *port
	}
	// loopback only, so nothing outside this computer can connect; all interfaces when LAN access is on
	lan := cfg.Get().LANEnabled
	addr := fmt.Sprintf("127.0.0.1:%d", p)
	if lan {
		addr = fmt.Sprintf(":%d", p)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s (already running?): %w", addr, err)
	}
	st, err := OpenStore(dir, cfg)
	if err != nil {
		ln.Close()
		return err
	}
	srv, err := NewServer(st, cfg, p, *ui)
	if err != nil {
		ln.Close()
		st.Close()
		return err
	}
	srv.lan = lan
	url := fmt.Sprintf("http://localhost:%d/", p)
	sj, _ := json.Marshal(map[string]any{"port": p, "pid": os.Getpid(), "url": url, "startedAt": time.Now().Format(time.RFC3339)})
	_ = os.WriteFile(filepath.Join(dir, "server.json"), sj, 0o644)

	log.SetFlags(log.Ltime)
	fmt.Printf("%s v%s\n  data:  %s\n  open:  %s\n  MCP:   %s\n  stop:  Ctrl+C\n", AppName, Version, dir, url,
		map[bool]string{true: "ON (127.0.0.1 only)", false: "off (enable in Settings)"}[cfg.Get().MCPEnabled])
	if lan {
		for _, u := range lanURLs(p) {
			fmt.Printf("  LAN:   %s  (log in with the token: Settings → LAN access, or %s)\n", u, filepath.Join(dir, "config.json"))
		}
	}
	if st.Recovered != "" {
		fmt.Println("  NOTE:  " + st.Recovered)
	}
	if *open {
		go func() { time.Sleep(300 * time.Millisecond); openBrowser(url) }()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-sig:
		fmt.Println("\nshutting down…")
	case err = <-errc:
	}
	srv.Shutdown()
	st.Close()
	_ = os.Remove(filepath.Join(dir, "server.json"))
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}
