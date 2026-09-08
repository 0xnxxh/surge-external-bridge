package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func directory(path string) string { return filepath.Dir(path) }
func binaryVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}
	words := strings.Fields(string(data))
	if len(words) < 2 || words[0] != "SurgeEB" {
		return "", errors.New("unexpected binary version response")
	}
	return words[1], nil
}
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".update-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0o600)
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func copyFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temp, err := os.CreateTemp(filepath.Dir(target), ".update-copy-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err = temp.Chmod(mode); err != nil {
		return err
	}
	if _, err = io.Copy(temp, input); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp.Name(), target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func regularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("目标必须是普通文件：%s", path)
	}
	return nil
}

// File locks are released by the kernel after a crash; lock files are never unlinked.
var errLocked = errors.New("实例或更新任务正在运行")

func lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}
func unlock(f *os.File) {
	if f != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}
func LockInstance(dataDir string) (*os.File, error) {
	return lock(filepath.Join(dataDir, "instance.lock"))
}
func UnlockInstance(f *os.File) { unlock(f) }

// Serve holds a shared lock on the executable inode. An update takes an
// exclusive lock after stopping its instance, protecting other data directories
// still using the same executable without requiring a writable install directory.
func LockExecutable(path string, exclusive bool) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	operation := syscall.LOCK_SH | syscall.LOCK_NB
	if exclusive {
		operation = syscall.LOCK_EX | syscall.LOCK_NB
	}
	if err = syscall.Flock(int(f.Fd()), operation); err != nil {
		f.Close()
		return nil, errors.New("同一程序仍被其他实例使用，请先停止这些实例")
	}
	return f, nil
}

func checkProtocol(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, path, "__update-protocol").Output()
	if err != nil || strings.TrimSpace(string(data)) != "1" {
		return errors.New("该程序尚不支持更新事务；请先使用含自动更新功能的版本重新安装自动启动")
	}
	return nil
}
