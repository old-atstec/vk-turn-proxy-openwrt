//go:build windows

package main

// The service: SYSTEM, started by Windows (automatic), serving the window on
// 127.0.0.1 and running the tunnel. Why a service and not one elevated app:
// wintun, routes and the DNS policy need the administrator, and an elevated
// app would ask UAC at every start and could not connect at boot; with the
// service, UAC is asked once — at the install — as Clash Verge's service
// mode does.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const (
	serviceName = "VKTurn"
	displayName = "VK TURN"
)

// dataDir: %ProgramData%\VK TURN.
func dataDir() (string, error) {
	pd, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(pd, displayName), nil
}

func tokenPath(dir string) string { return filepath.Join(dir, "ui-token") }
func usersPath(dir string) string { return filepath.Join(dir, "ui-users") }

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

type service struct{}

func runService() error {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isSvc {
		return errors.New("vkturn.exe service: запускает Windows, а не человек")
	}
	return svc.Run(serviceName, service{})
}

func (service) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	lg := &logRing{}
	log.SetFlags(0)
	log.SetOutput(lg)
	stop, err := serve(lg)
	if err != nil {
		lg.add("service: " + err.Error())
		return true, 1
	}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range req {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			stop()
			return false, 0
		}
	}
	return false, 0
}

// serve starts everything and returns its stop.
func serve(lg *logRing) (func(), error) {
	dir, err := dataDir()
	if err != nil {
		return nil, err
	}
	if err := protectDir(dir); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	tok, err := os.ReadFile(tokenPath(dir))
	if err != nil || len(strings.TrimSpace(string(tok))) < 32 {
		return nil, fmt.Errorf("the window's token %s: %v — reinstall", tokenPath(dir), err)
	}
	plat := newWinPlatform()
	daemon := filepath.Join(exeDir(), "vk-turn-proxyd.exe")
	t := newTunnel(plat, Dirs{Root: dir}, daemon, lg)
	t.cleanStale()
	t.logf("VK TURN %s %s: the service is up", version, commit)
	u := newUI(t, strings.TrimSpace(string(tok)), daemon, uiPort)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", uiPort))
	if err != nil {
		return nil, fmt.Errorf("127.0.0.1:%d: %w", uiPort, err)
	}
	srv := &http.Server{Handler: u.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	if s := t.Settings(); s.AutoConnect && s.Validate() == nil {
		go func() {
			// At boot the network may come a minute after the service.
			for i := 0; i < 60; i++ {
				if _, ok := plat.physical(); ok {
					break
				}
				time.Sleep(2 * time.Second)
			}
			if err := t.Start(); err != nil {
				t.logf("autoconnect: %v", err)
			}
		}()
	}
	return func() {
		t.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}
