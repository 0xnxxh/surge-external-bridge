package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type instanceIdentity struct {
	PID int    `json:"pid"`
	ID  string `json:"instance_id"`
}

// RegisterInstance is called while serve holds instance.lock. A stale identity
// is harmless: clients first check that the lock is held, then verify health.
func RegisterInstance(dir string, pid int, id string) error {
	if pid <= 0 || id == "" {
		return errors.New("invalid running instance identity")
	}
	return writeJSON(filepath.Join(dir, "update-instance.json"), instanceIdentity{PID: pid, ID: id})
}

// InstallRunning delegates CLI installation to the live instance so CLI and UI
// share the same write freeze, runtime identity, and service takeover path.
// An error never falls back to offline installation while an instance is live.
func InstallRunning(ctx context.Context, dir string) (json.RawMessage, bool, error) {
	return runningCommand(ctx, dir, true)
}

// StatusRunning reads the live manager's progress and download errors. Local
// transaction records are a fallback only when no instance is running.
func StatusRunning(ctx context.Context, dir string) (json.RawMessage, bool, error) {
	return runningCommand(ctx, dir, false)
}

func runningCommand(ctx context.Context, dir string, install bool) (json.RawMessage, bool, error) {
	instance, err := LockInstance(dir)
	if err == nil {
		UnlockInstance(instance)
		return nil, false, nil
	}
	if !install && errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if !errors.Is(err, errLocked) {
		return nil, false, err
	}
	var identity instanceIdentity
	data, err := os.ReadFile(filepath.Join(dir, "update-instance.json"))
	if err != nil {
		return nil, true, fmt.Errorf("无法确认运行实例，请通过设置页更新或停止实例后重试：%w", err)
	}
	if err = json.Unmarshal(data, &identity); err != nil || identity.PID <= 0 || identity.ID == "" {
		return nil, true, errors.New("运行实例记录无效，请通过设置页更新或停止实例后重试")
	}
	var config struct {
		HTTPBind        string `json:"http_bind"`
		PolicyHost      string `json:"policy_host"`
		ManagementToken string `json:"management_token"`
	}
	data, err = os.ReadFile(filepath.Join(dir, "gateway.json"))
	if err != nil {
		return nil, true, err
	}
	if err = json.Unmarshal(data, &config); err != nil {
		return nil, true, err
	}
	j := Journal{HealthURL: healthURL(config.HTTPBind), HealthHost: net.JoinHostPort(config.PolicyHost, portOf(config.HTTPBind))}
	health, err := readHealth(j)
	if err != nil {
		return nil, true, fmt.Errorf("无法连接运行实例，请确认监听地址后重试：%w", err)
	}
	if health.PID != identity.PID || health.InstanceID != identity.ID {
		return nil, true, errors.New("管理地址与数据目录中的运行实例不匹配，未执行更新")
	}
	client := &http.Client{Timeout: 35 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("management redirect refused") }}
	defer client.CloseIdleConnections()
	request := func(method, path string, body []byte) (json.RawMessage, error) {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(j.HealthURL, "/health")+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Host = j.HealthHost
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-SurgeEB-Instance", identity.ID)
		if config.ManagementToken != "" {
			req.Header.Set("Authorization", "Bearer "+config.ManagementToken)
		}
		if path == "/api/update/install" {
			req.Header.Set("X-SurgeEB-Confirm", "install-update")
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		if err != nil {
			return nil, err
		}
		if len(data) > 4<<20 || !json.Valid(data) {
			return nil, errors.New("invalid management update response")
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
			var failure struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(data, &failure)
			return nil, fmt.Errorf("运行实例更新请求失败 (HTTP %d)：%s", resp.StatusCode, failure.Error)
		}
		return data, nil
	}
	if !install {
		data, err = request(http.MethodGet, "/api/update", nil)
		return data, true, err
	}
	data, err = request(http.MethodPost, "/api/update/check", nil)
	if err != nil {
		return nil, true, err
	}
	var status Status
	if err = json.Unmarshal(data, &status); err != nil {
		return nil, true, err
	}
	if status.Latest == nil {
		return data, true, nil
	}
	body, _ := json.Marshal(map[string]string{"version": status.Latest.Tag})
	data, err = request(http.MethodPost, "/api/update/install", body)
	if err != nil {
		return nil, true, fmt.Errorf("%w；请先检查 update status 或设置页确认结果，不要重复提交", err)
	}
	return data, true, nil
}
