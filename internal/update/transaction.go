package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Journal struct {
	ID               string `json:"id"`
	Phase            string `json:"phase"`
	Version          string `json:"version"`
	PreviousVersion  string `json:"previous_version"`
	Target           string `json:"target"`
	Staged           string `json:"staged"`
	Digest           string `json:"digest"`
	PreviousDigest   string `json:"previous_digest"`
	DataDir          string `json:"data_dir"`
	Service          bool   `json:"service"`
	DefinitionDigest string `json:"definition_digest,omitempty"`
	PID              int    `json:"pid,omitempty"`
	InstanceID       string `json:"instance_id,omitempty"`
	HealthURL        string `json:"health_url,omitempty"`
	HealthHost       string `json:"health_host,omitempty"`
	Error            string `json:"error,omitempty"`
}

func (j *Journal) path() string            { return filepath.Join(j.DataDir, "update-transaction.json") }
func (j *Journal) save(phase string) error { j.Phase = phase; return writeJSON(j.path(), j) }
func activePhase(phase string) bool {
	switch phase {
	case "prepared", "stopping", "backed_up", "installing", "starting", "rolling_back", "recovery_failed":
		return true
	}
	return false
}

type transactionOps struct {
	stop  func() error
	start func() error
	ready func(string) error
}

func (j *Journal) backupDir() string { return filepath.Join(j.DataDir, "update-backup") }
func runTransaction(j *Journal, ops transactionOps) error {
	if err := regularFile(j.Staged); err != nil {
		return err
	}
	digest, err := fileDigest(j.Staged)
	if err != nil {
		return err
	}
	if digest != j.Digest {
		return errors.New("暂存更新文件已改变，已取消安装")
	}
	if err = regularFile(j.Target); err != nil {
		return err
	}
	if j.PreviousDigest != "" {
		digest, err = fileDigest(j.Target)
		if err != nil {
			return err
		}
		if digest != j.PreviousDigest {
			return errors.New("当前二进制在下载期间发生变化，已取消安装")
		}
	}
	if err = j.save("stopping"); err != nil {
		return err
	}
	if err = ops.stop(); err != nil {
		j.Error = err.Error()
		_ = j.save("failed")
		return err
	}
	executableLock, err := LockExecutable(j.Target, true)
	if err != nil {
		return resumeBeforeInstall(j, ops, err)
	}
	defer func() { unlock(executableLock) }()
	releaseExecutable := func() { unlock(executableLock); executableLock = nil }
	// No process may mutate the config after stop returns. Freeze all management
	// mutations in the candidate until this transaction is committed or rolled back.
	backup := j.backupDir()
	if err = os.MkdirAll(backup, 0o700); err != nil {
		releaseExecutable()
		return resumeBeforeInstall(j, ops, err)
	}
	if err = copyFile(filepath.Join(j.DataDir, "gateway.json"), filepath.Join(backup, "gateway.json"), 0o600); err != nil {
		releaseExecutable()
		return resumeBeforeInstall(j, ops, err)
	}
	if err = copyFile(j.Target, j.Target+".previous", 0o755); err != nil {
		releaseExecutable()
		return resumeBeforeInstall(j, ops, err)
	}
	if err = j.save("backed_up"); err != nil {
		releaseExecutable()
		return resumeBeforeInstall(j, ops, err)
	}
	if err = j.save("installing"); err != nil {
		releaseExecutable()
		return resumeBeforeInstall(j, ops, err)
	}
	if err = os.Rename(j.Staged, j.Target); err != nil {
		releaseExecutable()
		return rollback(j, ops, err)
	}
	if err = syncDir(filepath.Dir(j.Target)); err != nil {
		return rollback(j, ops, err)
	}
	if !j.Service && j.PID == 0 {
		return j.save("installed")
	}
	if err = j.save("starting"); err != nil {
		return rollback(j, ops, err)
	}
	if err = ops.start(); err != nil {
		return rollback(j, ops, err)
	}
	if err = ops.ready(j.Version); err != nil {
		return rollback(j, ops, err)
	}
	if err = j.save("succeeded"); err != nil {
		return err
	}
	return nil
}
func resumeBeforeInstall(j *Journal, ops transactionOps, cause error) error {
	j.Error = cause.Error()
	if err := j.save("failed"); err != nil {
		return errors.Join(cause, err)
	}
	if j.Service {
		if err := ops.start(); err != nil {
			return errors.Join(cause, err)
		}
	}
	return cause
}
func rollback(j *Journal, ops transactionOps, cause error) error {
	j.Error = cause.Error()
	fail := func(err error) error {
		j.Error = errors.Join(cause, fmt.Errorf("自动恢复失败：%w", err)).Error()
		_ = j.save("recovery_failed")
		return errors.New(j.Error)
	}
	if err := j.save("rolling_back"); err != nil {
		return fail(err)
	}
	if err := ops.stop(); err != nil {
		return fail(err)
	}
	if j.PreviousDigest != "" {
		previous, err := fileDigest(j.Target + ".previous")
		if err != nil {
			return fail(err)
		}
		if previous != j.PreviousDigest {
			return fail(errors.New("备份程序已改变"))
		}
		current, err := fileDigest(j.Target)
		if err != nil {
			return fail(err)
		}
		if current != j.Digest && current != j.PreviousDigest {
			return fail(errors.New("目标程序已被其他更新改变，未覆盖"))
		}
	}
	if err := copyFile(j.Target+".previous", j.Target, 0o755); err != nil {
		return fail(err)
	}
	if err := copyFile(filepath.Join(j.backupDir(), "gateway.json"), filepath.Join(j.DataDir, "gateway.json"), 0o600); err != nil {
		return fail(err)
	}
	if j.Service || j.PID != 0 {
		if err := ops.start(); err != nil {
			return fail(err)
		}
		if err := ops.ready(j.PreviousVersion); err != nil {
			return fail(err)
		}
	}
	if err := j.save("rolled_back"); err != nil {
		return fail(err)
	}
	return fmt.Errorf("更新失败，已恢复原版本：%w", cause)
}
