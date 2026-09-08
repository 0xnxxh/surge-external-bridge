package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ssfun/surge-external-bridge/internal/service"
)

type Health struct {
	OK         bool   `json:"ok"`
	Version    string `json:"version"`
	State      string `json:"state"`
	PID        int    `json:"pid"`
	UpdateID   string `json:"update_id"`
	InstanceID string `json:"instance_id"`
}

func readHealth(j Journal) (Health, error) {
	var health Health
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("health redirect refused") }}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, j.HealthURL, nil)
	if err != nil {
		return health, err
	}
	req.Host = j.HealthHost
	resp, err := client.Do(req)
	if err != nil {
		return health, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return health, fmt.Errorf("health status %d", resp.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&health)
	return health, err
}
func waitReady(j Journal, version string) error {
	deadline := time.Now().Add(60 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		health, err := readHealth(j)
		if err == nil && health.OK && health.State == "running" && normalizedVersion(health.Version) == normalizedVersion(version) && health.PID > 0 && health.InstanceID != "" && health.InstanceID != j.InstanceID && (!activePhase(j.Phase) || health.UpdateID == j.ID) {
			return nil
		}
		if err != nil {
			last = err
		} else {
			last = fmt.Errorf("version=%s state=%s", health.Version, health.State)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("新进程未在 60 秒内以目标版本就绪：%w", last)
}
func waitStopped(dir string) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		f, err := LockInstance(dir)
		if err == nil {
			UnlockInstance(f)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("原实例未释放运行锁，未替换程序")
}
func Worker(dir string, recoverOnly bool) error { return worker(dir, recoverOnly, "") }
func WorkerFor(dir, id string) error {
	if id == "" {
		return errors.New("update worker requires transaction id")
	}
	return worker(dir, false, id)
}
func worker(dir string, recoverOnly bool, expectedID string) (result error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	dir = absolute
	// The dispatching process holds the update lock until the job has been submitted.
	var operation *os.File
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		operation, err = lock(filepath.Join(dir, "update.lock"))
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer unlock(operation)
	if expectedID != "" {
		defer service.FinishUpdateWorker(expectedID)
	}
	j, err := ReadJournal(dir)
	if expectedID != "" && err == nil && j.ID != expectedID {
		return errors.New("更新任务与事务 ID 不匹配")
	}
	if err != nil {
		return err
	}
	if !activePhase(j.Phase) {
		return errors.New("没有待执行或待恢复的更新")
	}
	if filepath.Clean(j.DataDir) != dir || j.ID == "" || !filepath.IsAbs(j.Target) || !filepath.IsAbs(j.Staged) {
		return errors.New("更新记录的路径无效")
	}
	defer func() {
		if result != nil {
			j.Error = result.Error()
			if j.Phase == "prepared" {
				j.Phase = "failed"
			}
			_ = writeJSON(j.path(), j)
		}

	}()
	if j.Service {
		target, err := service.ResolveUpdateTarget(dir)
		if err != nil {
			return err
		}
		if !target.Service || target.Executable != j.Target {
			return errors.New("服务归属或启动路径已改变，停止自动更新")
		}
		definitionDigest, err := fileDigest(target.Definition)
		if err != nil {
			return err
		}
		if definitionDigest != j.DefinitionDigest {
			return errors.New("服务定义在更新期间发生变化，请核对后手动恢复")
		}
	} else if !recoverOnly {
		return errors.New("手动运行更新不应使用后台任务")
	}
	targetLock, err := lock(filepath.Join(filepath.Dir(j.Target), ".SurgeEB-update.lock"))
	if err != nil {
		return err
	}
	defer unlock(targetLock)
	stop := func() error {
		// Stop the named service first. A registered-but-not-running service may
		// coexist with this instance still running from its download directory.
		if j.Service {
			if _, err := service.Stop(); err != nil {
				return err
			}
		}
		if j.PID > 0 {
			health, err := readHealth(j)
			if err == nil && health.PID == j.PID && health.InstanceID == j.InstanceID {
				if err = syscall.Kill(j.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
					return err
				}
			}
		}
		return waitStopped(dir)
	}
	ops := transactionOps{stop: stop, start: func() error {
		if !j.Service {
			return nil
		}
		_, err := service.Start()
		return err
	}, ready: func(version string) error { return waitReady(j, version) }}
	if recoverOnly || j.Phase != "prepared" {
		switch j.Phase {
		case "prepared", "stopping":
			// Replacement has not started. Ensure the original service can resume.
			if err = stop(); err != nil {
				return err
			}
			j.Error = "上次更新在替换前中断，已取消"
			if err = j.save("failed"); err != nil {
				return err
			}
			return ops.start()
		default:
			return rollback(&j, ops, errors.New("上次更新中断，正在恢复旧版本"))
		}
	}
	err = runTransaction(&j, ops)
	if err != nil && !activePhase(j.Phase) {
		j.Error = err.Error()
		_ = writeJSON(j.path(), j)
	}
	return err
}

// StartupAllowed prevents a crashed updater from leaving a new process free to
// mutate data before recovery. Starting and rollback phases are supervised by a worker.
func StartupAllowed(dir string) error {
	j, err := ReadJournal(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取更新记录失败：%w", err)
	}
	switch j.Phase {
	case "prepared", "stopping", "backed_up", "installing", "recovery_failed":
		return errors.New("更新尚未完成；执行 SurgeEB update recover --data-dir PATH 恢复")
	}
	return nil
}
func StartBackground(ctx context.Context, m *Manager) { go m.Run(ctx) }
