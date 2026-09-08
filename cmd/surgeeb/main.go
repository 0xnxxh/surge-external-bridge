package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ssfun/surge-external-bridge/internal/gateway"
	"github.com/ssfun/surge-external-bridge/internal/management"
	core "github.com/ssfun/surge-external-bridge/internal/mihomo"
	serviceManager "github.com/ssfun/surge-external-bridge/internal/service"
	"github.com/ssfun/surge-external-bridge/internal/update"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "SurgeEB:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return serve(nil)
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("SurgeEB %s (Embedded Mihomo %s)\n", gateway.Version, core.CoreVersion)
		return nil
	case "__update-protocol":
		fmt.Println("1")
		return nil
	case "update":
		return updateCommand(args[1:])
	case "__update-worker":
		flags := flag.NewFlagSet("update-worker", flag.ContinueOnError)
		dir := flags.String("data-dir", defaultDataDir(), "data directory")
		id := flags.String("id", "", "update transaction identifier")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return update.WorkerFor(*dir, *id)
	case "service":
		return serviceCommand(args[1:])
	case "help", "--help", "-h":
		printHelp()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	dataDir := flags.String("data-dir", defaultDataDir(), "configuration and state directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	// Provider caches contain upstream node credentials. Install a process-wide
	// private umask before any product or Mihomo state can be created, including
	// interactive runs outside the service definitions that already use 0077.
	syscall.Umask(0o077)
	absolute, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	*dataDir = absolute
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	if err := update.StartupAllowed(*dataDir); err != nil {
		return err
	}
	instanceLock, err := update.LockInstance(*dataDir)
	if err != nil {
		return err
	}
	defer update.UnlockInstance(instanceLock)
	if err := update.StartupAllowed(*dataDir); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executableLock, err := update.LockExecutable(executable, false)
	if err != nil {
		return err
	}
	defer update.UnlockInstance(executableLock)
	application, err := gateway.New(*dataDir)
	if err != nil {
		return err
	}
	defer application.Close()
	server, err := management.New(application)
	if err != nil {
		return err
	}
	instanceBytes := make([]byte, 16)
	if _, err := rand.Read(instanceBytes); err != nil {
		return err
	}
	instanceID := hex.EncodeToString(instanceBytes)
	config := application.Config()
	updater, err := update.NewManager(*dataDir, gateway.Version, update.Runtime{PID: os.Getpid(), InstanceID: instanceID, HTTPBind: config.HTTPBind, PolicyHost: config.PolicyHost})
	if err != nil {
		return err
	}
	updater.RuntimeInfo = func() update.Runtime {
		c := application.Config()
		return update.Runtime{PID: os.Getpid(), InstanceID: instanceID, HTTPBind: c.HTTPBind, PolicyHost: c.PolicyHost}
	}
	server.SetUpdater(updater, instanceID)
	if err := update.RegisterInstance(*dataDir, os.Getpid(), instanceID); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	update.StartBackground(ctx, updater)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	fmt.Printf("Surge External Bridge %s · Embedded Mihomo %s\n", gateway.Version, core.CoreVersion)
	fmt.Printf("configuration console: http://%s\n", config.HTTPBind)
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func serviceCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("service requires install, uninstall, status, start, stop, or restart")
	}
	switch args[0] {
	case "status":
		info, err := serviceManager.Status()
		if err != nil {
			return err
		}
		fmt.Printf("platform=%s scope=%s installed=%t active=%t repair_needed=%t path=%s\n", info.Platform, info.Scope, info.Installed, info.Active, info.RepairNeeded, info.Path)
		return nil
	case "install":
		flags := flag.NewFlagSet("service install", flag.ContinueOnError)
		dataDir := flags.String("data-dir", defaultDataDir(), "configuration and state directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		info, err := serviceManager.Install(*dataDir)
		if err != nil {
			return err
		}
		fmt.Printf("installed %s: %s\n", info.Scope, info.Path)
		return nil
	case "uninstall":
		info, err := serviceManager.Uninstall()
		if err != nil {
			return err
		}
		fmt.Printf("removed service definition: %s\n", info.Path)
		return nil
	case "start", "stop", "restart":
		var info serviceManager.Info
		var err error
		switch args[0] {
		case "start":
			info, err = serviceManager.Start()
		case "stop":
			info, err = serviceManager.Stop()
		case "restart":
			info, err = serviceManager.Restart()
		}
		if err != nil {
			return err
		}
		fmt.Printf("%s %s: active=%t\n", args[0], info.Scope, info.Active)
		return nil
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func defaultDataDir() string {
	if override := os.Getenv("SURGEEB_DATA_DIR"); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".surge-external-bridge"
	}
	return filepath.Join(home, ".surge-external-bridge")
}

func printHelp() {
	fmt.Println(`SurgeEB - Surge External Bridge powered by embedded Mihomo

Usage:
  SurgeEB serve [--data-dir PATH]
  SurgeEB version
  SurgeEB service status
  SurgeEB service install [--data-dir PATH]
  SurgeEB service start|stop|restart
  SurgeEB service uninstall
  SurgeEB update check|install|status|recover [--data-dir PATH]`)
}

func updateCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("update requires check, install, status, or recover")
	}
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	dir := flags.String("data-dir", defaultDataDir(), "configuration and state directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "recover" {
		return update.Worker(*dir, true)
	}
	if args[0] == "install" || args[0] == "status" {
		command := update.InstallRunning
		if args[0] == "status" {
			command = update.StatusRunning
		}
		result, running, err := command(context.Background(), *dir)
		if err != nil {
			return err
		}
		if running {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
	}
	manager, err := update.NewManager(*dir, gateway.Version, update.Runtime{})
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
	case "check", "install":
		if err = manager.Check(context.Background()); err != nil {
			return err
		}
		if args[0] == "install" {
			status := manager.Status()
			if status.Latest == nil {
				fmt.Println("已是最新稳定版本")
				return nil
			}
			if err = manager.StartInstall(context.Background(), status.Latest.Tag, false); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown update command %q", args[0])
	}
	return json.NewEncoder(os.Stdout).Encode(manager.Status())
}
