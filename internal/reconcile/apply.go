package reconcile

import (
	"fmt"
	"time"

	"github.com/makebox/makebox/internal/logx"
)

// Executor performs one planned action. Implementations must be idempotent
// enough to fail safely: the journal snapshots files before Execute runs.
type Executor interface {
	Execute(action Action) error
}

// ApplyOptions controls how a plan is executed.
type ApplyOptions struct {
	Store        *JournalStore
	AutoRollback bool
	Now          func() time.Time
	Out          func(format string, args ...any)
	// Services records which services were active before apply. Nil is
	// treated as "all inactive", which makes rollback skip restarts.
	Services map[string]bool
	// SnapshotPaths overrides the managed-file list used before each action.
	// It exists for tests; production callers should leave it nil.
	SnapshotPaths func(Action) []string
	// Restart is used by rollback to reactivate services that were running
	// before apply. Nil disables service restarts during rollback.
	Restart func([]string) error
}

// ApplyResult summarises one apply transaction.
type ApplyResult struct {
	JournalID  string          `json:"journalId,omitempty"`
	Applied    []string        `json:"applied,omitempty"`
	Failed     string          `json:"failed,omitempty"`
	RolledBack bool            `json:"rolledBack,omitempty"`
	Rollback   *RollbackResult `json:"rollback,omitempty"`
}

func (o *ApplyOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *ApplyOptions) out(format string, args ...any) {
	if o.Out != nil {
		o.Out(format, args...)
		return
	}
	logx.Infof(format, args...)
}

// Apply executes a plan action by action, journaling every file it may touch.
// On failure it either rolls back automatically or leaves the journal for
// `mkbox rollback`.
func Apply(plan Plan, ex Executor, opts ApplyOptions) (*ApplyResult, error) {
	if ex == nil {
		return nil, fmt.Errorf("内部错误: Executor 为空")
	}
	res := &ApplyResult{}
	if plan.Empty() {
		return res, nil
	}
	store := opts.Store
	if store == nil {
		store = NewJournalStore("")
	}
	services := opts.Services
	if services == nil {
		services = map[string]bool{}
	}
	j, err := store.Begin(plan, services, opts.now())
	if err != nil {
		return nil, err
	}
	res.JournalID = j.ID
	opts.out("事务已创建: %s", j.ID)

	fail := func(idx int, cause error) (*ApplyResult, error) {
		if idx >= 0 && idx < len(j.Actions) {
			j.Actions[idx].Status = "failed"
			j.Actions[idx].FinishedAt = opts.now().UTC().Format(time.RFC3339)
			j.Actions[idx].Error = cause.Error()
			res.Failed = j.Actions[idx].ID
		}
		j.Status = JournalFailed
		j.Error = cause.Error()
		_ = store.Save(j)
		if !opts.AutoRollback {
			return res, fmt.Errorf("动作失败: %w（journal %s 已保留，可运行: mkbox rollback %s）", cause, j.ID, j.ID)
		}
		opts.out("动作失败，开始自动回滚: %v", cause)
		_, rb, rbErr := store.Rollback(j.ID, RollbackOptions{
			Force:   true,
			Now:     opts.now(),
			Restart: opts.Restart,
			Out:     opts.Out,
		})
		res.Rollback = rb
		res.RolledBack = rbErr == nil
		if rbErr != nil {
			return res, fmt.Errorf("动作失败且自动回滚失败: %v；回滚错误: %w（journal %s）", cause, rbErr, j.ID)
		}
		return res, fmt.Errorf("动作失败，已自动回滚: %w", cause)
	}

	for i, action := range plan.Actions {
		if i >= len(j.Actions) {
			return fail(-1, fmt.Errorf("journal 动作数量不一致"))
		}
		paths := ManagedFilesForAction(action.ID)
		if opts.SnapshotPaths != nil {
			paths = opts.SnapshotPaths(action)
		}
		if err := store.Snapshot(j, paths, opts.now()); err != nil {
			return fail(i, fmt.Errorf("创建回滚快照失败: %w", err))
		}
		j.Actions[i].Status = "running"
		j.Actions[i].StartedAt = opts.now().UTC().Format(time.RFC3339)
		if err := store.Save(j); err != nil {
			return res, fmt.Errorf("更新 journal: %w", err)
		}
		opts.out("[%d/%d] %s", i+1, len(plan.Actions), action.Title)
		if err := ex.Execute(action); err != nil {
			return fail(i, err)
		}
		j.Actions[i].Status = "applied"
		j.Actions[i].FinishedAt = opts.now().UTC().Format(time.RFC3339)
		if err := store.Save(j); err != nil {
			return res, fmt.Errorf("更新 journal: %w", err)
		}
		res.Applied = append(res.Applied, action.ID)
	}

	j.Status = JournalApplied
	if err := store.Save(j); err != nil {
		return res, fmt.Errorf("写入 journal 完成状态: %w", err)
	}
	opts.out("完成: %d 个动作，journal %s", len(res.Applied), j.ID)
	return res, nil
}
