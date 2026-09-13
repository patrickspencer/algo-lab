// Command algo-lab serves a browser UI for practising coding problems from
// the bundled open problem set.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/patrickspencer/algo-lab-public/internal/server"
	"github.com/patrickspencer/algo-lab-public/internal/store"
)

//go:embed web
var webFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:8484", "address to listen on")
	dbFlag := flag.String("db", "", "SQLite database path (default: <config dir>/algorithms/algorithms.db, or $ALGORITHMS_DB)")
	open := flag.Bool("open", true, "open the browser on start")
	addUser := flag.String("adduser", "", "create or update this account's password, then exit")
	flag.Parse()

	if *addUser != "" {
		if err := setPassword(*dbFlag, *addUser); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if err := run(*addr, *dbFlag, *open); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(addr, dbPath string, openBrowser bool) error {
	st, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	srv := server.New(st, static)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String()
	log.Printf("algo-lab listening on %s (database %s)", url, dbPath)

	if openBrowser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = launch(url)
		}()
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// AI requests can take a while.
		WriteTimeout: 5 * time.Minute,
	}
	return httpSrv.Serve(ln)
}

// openStore resolves the database path and opens the store.
func openStore(dbPath string) (*store.Store, error) {
	if dbPath == "" {
		p, err := store.DefaultPath()
		if err != nil {
			return nil, err
		}
		dbPath = p
	}
	return store.Open(dbPath)
}

// setPassword prompts for a password (twice) on the terminal and sets it for
// the named account, creating the account if it does not exist.
func setPassword(dbPath, name string) error {
	st, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	fmt.Printf("Set a password for %q.\n", name)
	fmt.Print("Password: ")
	p1, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Print("Confirm:  ")
	p2, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return err
	}
	if string(p1) != string(p2) {
		return fmt.Errorf("passwords did not match")
	}
	if len(p1) < 4 {
		return fmt.Errorf("password must be at least 4 characters")
	}
	hash, err := bcrypt.GenerateFromPassword(p1, bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u, err := st.SetPassword(context.Background(), name, string(hash))
	if err != nil {
		return err
	}
	fmt.Printf("Password set for %q (id %d). They can now log in with it.\n", u.Name, u.ID)
	return nil
}

func launch(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
