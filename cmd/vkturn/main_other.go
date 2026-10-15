//go:build !windows

// vkturn is the Windows app (windows/README.md). Elsewhere it serves its
// window over a fake network — for working on the page:
//
//	go run ./cmd/vkturn -demo      # then http://127.0.0.1:47447/login?t=demo
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

var (
	version = "dev"
	commit  = ""
)

func main() {
	demo := flag.Bool("demo", false, "serve the window over a fake network")
	daemon := flag.String("daemon", "", "vk-turn-proxyd for genkey/pubkey (go build ./cmd/vk-turn-proxyd)")
	flag.Parse()
	if !*demo {
		fmt.Fprintln(os.Stderr, "vkturn is the Windows app; -demo serves its window here")
		os.Exit(2)
	}
	dir, err := os.MkdirTemp("", "vkturn-demo-")
	if err != nil {
		log.Fatal(err)
	}
	lg := &logRing{}
	log.SetOutput(lg)
	t := newTunnel(newFakePlatform(), Dirs{Root: dir}, *daemon, lg)
	t.logf("demo: data in %s", dir)
	u := newUI(t, "demo", *daemon, uiPort)
	addr := fmt.Sprintf("127.0.0.1:%d", uiPort)
	fmt.Printf("http://%s/login?t=demo\n", addr)
	log.Fatal(http.ListenAndServe(addr, u.handler()))
}
