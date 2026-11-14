// Command hop runs HopDrop: peer-to-peer file and text transfer on the home LAN.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"hopdrop/internal/hop"
)

const usage = `HopDrop — send files and text between your devices on the home LAN.

usage:
  hop [-name N] [-port P] [-dir D]   run in this terminal (Ctrl-C stops it)
  hop open                           start in the background if needed, then open the page
  hop off                            stop HopDrop
  hop version

Config: %s (HOPDROP_HOME overrides the folder)
`

func main() {
	flag.Usage = func() { fmt.Fprintf(os.Stderr, usage, filepath.Join(hop.ConfigDir(), "config.json")) }
	cmd := ""
	if len(os.Args) > 1 && os.Args[1] != "" && os.Args[1][0] != '-' {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "":
		err = run()
	case "open":
		err = openPage()
	case "off":
		err = off()
	case "version":
		fmt.Println("hop", hop.Version)
	case "help":
		flag.Usage()
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hop:", err)
		os.Exit(1)
	}
}

func run() error {
	name := flag.String("name", "", "device name for this run")
	port := flag.Int("port", 0, "HTTP port for this run (default 47100)")
	dir := flag.String("dir", "", "download folder for this run")
	flag.Parse()
	cfg, err := hop.LoadConfig()
	if err != nil {
		return err
	}
	if *name != "" {
		cfg.Name = *name
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *dir != "" {
		cfg.DownloadDir = *dir
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return hop.NewNode(cfg).Run(ctx)
}

func localURL() (string, error) {
	cfg, err := hop.LoadConfig()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://localhost:%d/", cfg.Port), nil
}

func running(base string) bool {
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get(base + "api/info")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// openPage starts HopDrop in the background (if it is not running) and opens its page.
func openPage() error {
	base, err := localURL()
	if err != nil {
		return err
	}
	if !running(base) {
		if err := startBackground(); err != nil {
			return err
		}
		for i := 0; !running(base); i++ {
			if i == 50 {
				return fmt.Errorf("HopDrop did not start; see %s", logPath())
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Printf("HopDrop is on (log: %s). `hop off` turns it off.\n", logPath())
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	if err := exec.Command(opener, base).Run(); err != nil {
		fmt.Println("Open", base)
	}
	return nil
}

func logPath() string { return filepath.Join(hop.ConfigDir(), "hop.log") }

func startBackground() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(hop.ConfigDir(), 0o755); err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func off() error {
	base, err := localURL()
	if err != nil {
		return err
	}
	resp, err := http.Post(base+"api/quit", "", nil)
	if err != nil {
		fmt.Println("HopDrop is not running")
		return nil
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("could not stop it: %s", resp.Status)
	}
	for i := 0; running(base) && i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("HopDrop is off")
	return nil
}

func init() { log.SetFlags(log.Ldate | log.Ltime) }
