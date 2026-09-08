package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// UpdateTarget distinguishes installed service ownership from the running executable.
type UpdateTarget struct {
	Executable string `json:"executable"`
	Service    bool   `json:"service"`
	Scope      string `json:"scope"`
	Definition string `json:"definition,omitempty"`
}

func ResolveUpdateTarget(dataDir string) (UpdateTarget, error) {
	if err := validateServiceContext(runtime.GOOS, os.Geteuid()); err != nil {
		return UpdateTarget{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return UpdateTarget{}, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return UpdateTarget{}, err
	}
	target := UpdateTarget{Executable: executable, Scope: "manual"}
	path, _, err := servicePath()
	if err != nil {
		return target, err
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return target, nil
	}
	if err != nil {
		return target, err
	}
	installed, matched, err := updateDefinition(runtime.GOOS, os.Geteuid(), content, dataDir)
	if err != nil {
		return target, err
	}
	if !matched {
		return target, nil
	}
	if runtime.GOOS == "darwin" && filepath.Clean(installed) != filepath.Clean(installedExecutablePath()) {
		return target, errors.New("自启服务使用旧路径，请先在设置页修复自动启动")
	}
	target = UpdateTarget{Executable: installed, Service: true, Scope: "user", Definition: path}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		target.Scope = "system"
	}
	if runtime.GOOS == "darwin" {
		output, runErr := exec.Command("launchctl", "print", launchdTarget(os.Getuid(), label)).CombinedOutput()
		if runErr == nil {
			if err := validateLoadedLaunchAgent(output, installed, dataDir); err != nil {
				return target, err
			}
		} else if !launchAgentTargetMissing(output) {
			return target, serviceCommandError("launchctl print", runErr, output)
		}
	}
	if runtime.GOOS == "linux" {
		// Reject overrides: a unit on disk alone is not authoritative when drop-ins change ExecStart.
		output, err := updateCommand("systemctl", systemctlArguments(os.Geteuid(), "show", systemdUnit, "--property=FragmentPath", "--property=DropInPaths", "--property=NeedDaemonReload")...)
		if err != nil {
			return target, err
		}
		fields := strings.Split(string(output), "\n")
		fragment := ""
		for _, line := range fields {
			if line == "NeedDaemonReload=yes" {
				return target, errors.New("systemd 服务定义已修改但尚未重新加载，请先重新安装自动启动")
			}
			if strings.HasPrefix(line, "FragmentPath=") {
				fragment = strings.TrimPrefix(line, "FragmentPath=")
			}
			if strings.HasPrefix(line, "DropInPaths=") && line != "DropInPaths=" {
				return target, errors.New("systemd 服务含覆盖配置，请手动下载 Release 替换程序并核对启动路径")
			}
		}
		if filepath.Clean(fragment) != filepath.Clean(path) {
			return target, errors.New("systemd 实际加载的服务文件与安装定义不一致")
		}
	}
	return target, nil
}

func updateDefinition(goos string, euid int, content []byte, dataDir string) (string, bool, error) {
	var executable, configuredDir string
	if goos == "darwin" {
		decoder := xml.NewDecoder(bytes.NewReader(content))
		key := ""
		inArgs := false
		var args []string
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", false, err
			}
			switch v := token.(type) {
			case xml.StartElement:
				if v.Name.Local == "key" {
					if err := decoder.DecodeElement(&key, &v); err != nil {
						return "", false, err
					}
				}
				if v.Name.Local == "array" {
					inArgs = key == "ProgramArguments"
				}
				if v.Name.Local == "string" && inArgs {
					var arg string
					if err := decoder.DecodeElement(&arg, &v); err != nil {
						return "", false, err
					}
					args = append(args, arg)
				}
			case xml.EndElement:
				if v.Name.Local == "array" {
					inArgs = false
				}
			}
		}
		if len(args) != 4 || args[1] != "serve" || args[2] != "--data-dir" {
			return "", false, errors.New("无法识别 LaunchAgent 的启动参数，请修复自动启动")
		}
		executable, configuredDir = args[0], args[3]
	} else if goos == "linux" {
		var line string
		for _, s := range strings.Split(string(content), "\n") {
			if strings.HasPrefix(s, "ExecStart=") {
				if line != "" {
					return "", false, errors.New("multiple ExecStart directives")
				}
				line = strings.TrimPrefix(s, "ExecStart=")
			}
		}
		// Only accept the exact quoted syntax emitted by this product, never execute shell text.
		args, err := parseSystemdArgs(line)
		if err != nil || len(args) != 4 || args[1] != "serve" || args[2] != "--data-dir" {
			return "", false, errors.New("无法识别 systemd 的启动参数，请重新安装自动启动")
		}
		executable, configuredDir = args[0], args[3]
	} else {
		return "", false, errors.New("unsupported service platform")
	}
	if !filepath.IsAbs(executable) || !filepath.IsAbs(configuredDir) {
		return "", false, errors.New("service paths must be absolute")
	}
	expected, err := filepath.Abs(dataDir)
	if err != nil {
		return "", false, err
	}
	if real, err := filepath.EvalSymlinks(expected); err == nil {
		expected = real
	}
	if real, err := filepath.EvalSymlinks(configuredDir); err == nil {
		configuredDir = real
	}
	return executable, filepath.Clean(expected) == filepath.Clean(configuredDir), nil
}
func parseSystemdArgs(s string) ([]string, error) {
	var args []string
	for s = strings.TrimSpace(s); s != ""; s = strings.TrimSpace(s) {
		if s[0] != '"' {
			i := strings.IndexByte(s, ' ')
			if i < 0 {
				i = len(s)
			}
			args = append(args, s[:i])
			s = s[i:]
			continue
		}
		end := 1
		escaped := false
		for ; end < len(s); end++ {
			if s[end] == '"' && !escaped {
				break
			}
			if s[end] == '\\' && !escaped {
				escaped = true
			} else {
				escaped = false
			}
		}
		if end == len(s) {
			return nil, errors.New("unterminated systemd argument")
		}
		word, err := strconv.Unquote(s[:end+1])
		if err != nil {
			return nil, err
		}
		word = strings.ReplaceAll(word, "%%", "%")
		args = append(args, word)
		s = s[end+1:]
	}
	return args, nil
}
func updateCommand(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return nil, serviceCommandError(name, err, out)
	}
	return out, nil
}

// LaunchUpdateWorker uses a separate service job, outside the gateway's process group/cgroup.
func LaunchUpdateWorker(executable, dataDir, id string) error {
	log := filepath.Join(dataDir, "update.log")
	f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	switch runtime.GOOS {
	case "darwin":
		_, err = updateCommand("launchctl", "submit", "-l", "com.sfun.surgeeb.update."+id, "-o", log, "-e", log, "--", executable, "__update-worker", "--data-dir", dataDir, "--id", id)
	case "linux":
		args := []string{"--collect", "--quiet", "--unit=surgeeb-update-" + id, "--property=Type=exec", "--property=UMask=0077"}
		if os.Geteuid() != 0 {
			args = append(args, "--user")
		}
		args = append(args, "--", executable, "__update-worker", "--data-dir", dataDir, "--id", id)
		_, err = updateCommand("systemd-run", args...)
	default:
		err = fmt.Errorf("unsupported update platform %s", runtime.GOOS)
	}
	return err
}
func FinishUpdateWorker(id string) {
	if runtime.GOOS == "darwin" {
		_, _ = updateCommand("launchctl", "remove", "com.sfun.surgeeb.update."+id)
	}
}

func validateLoadedLaunchAgent(output []byte, executable, dataDir string) error {
	var args []string
	inArgs := false
	for _, line := range strings.Split(string(output), "\n") {
		field := strings.TrimSpace(line)
		if field == "arguments = {" {
			inArgs = true
			continue
		}
		if inArgs {
			if field == "}" {
				break
			}
			args = append(args, field)
		}
	}
	expected, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	if len(args) != 4 || args[0] != executable || args[1] != "serve" || args[2] != "--data-dir" {
		return errors.New("已加载的 LaunchAgent 启动参数与安装定义不一致，请先修复自动启动")
	}
	actual := args[3]
	if real, err := filepath.EvalSymlinks(expected); err == nil {
		expected = real
	}
	if real, err := filepath.EvalSymlinks(actual); err == nil {
		actual = real
	}
	if filepath.Clean(expected) != filepath.Clean(actual) {
		return errors.New("已加载的 LaunchAgent 属于其他数据目录，未执行更新")
	}
	return nil
}
