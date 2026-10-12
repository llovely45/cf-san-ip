package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"cf-san/internal/buildinfo"
	"cf-san/internal/config"
)

const (
	// DefaultUpdateLockPath is shared by the manual and automatic update paths.
	DefaultUpdateLockPath = "/run/lock/cf-san-update.lock"

	defaultShutdownTimeout = 2 * time.Minute
	defaultPollInterval    = time.Second
	defaultInstallTimeout  = 5 * time.Minute
)

// Scanner is the process lifecycle needed by the automatic updater.
type Scanner interface {
	Status() (bool, error)
	Stop() error
	WaitStopped(context.Context, time.Duration) error
	Start() error
}

// InstallerFunc installs one exact release version when version is nonempty.
type InstallerFunc func(context.Context, config.Config, string, io.Writer) error

// Logger records private automatic-update outcomes.
type Logger interface {
	Record(UpdateRecord) error
}

// Runner checks for a newer release and coordinates a safe scanner update.
type Runner struct {
	CurrentVersion string
	Repository     string
	// ConfigPath makes Run reload the configuration while it owns the update
	// lock. The caller-supplied cfg remains useful for tests and for callers
	// that already hold a fresh in-memory configuration.
	ConfigPath      string
	Releases        ReleaseClient
	Scanner         Scanner
	Install         InstallerFunc
	Logger          Logger
	LockPath        string
	ResumePath      string
	ShutdownTimeout time.Duration
	PollInterval    time.Duration
	InstallTimeout  time.Duration
}

// Run performs one serialized automatic-update check.
func (r Runner) Run(ctx context.Context, cfg config.Config) error {
	if ctx == nil {
		return errors.New("automatic update context must not be nil")
	}
	lockPath := r.LockPath
	if lockPath == "" {
		lockPath = DefaultUpdateLockPath
	}
	return WithUpdateLock(lockPath, func() error {
		if r.ConfigPath != "" {
			freshConfig, err := config.Load(r.ConfigPath)
			if err != nil {
				return r.finish(UpdateRecord{
					CurrentVersion: r.CurrentVersion,
					Result:         "config-failed",
				}, cfg.Token, err)
			}
			cfg = freshConfig
		}
		return r.checkAndUpdate(ctx, cfg)
	})
}

func (r Runner) checkAndUpdate(ctx context.Context, cfg config.Config) error {
	if err := r.validateIdentity(); err != nil {
		return r.finish(UpdateRecord{CurrentVersion: r.CurrentVersion, Result: "disabled"}, cfg.Token, err)
	}
	if handled, err := r.recoverPendingResume(ctx, cfg); handled {
		return err
	}
	if r.Releases == nil {
		return r.finish(UpdateRecord{CurrentVersion: r.CurrentVersion, Result: "check-failed"}, cfg.Token, errors.New("release client is not configured"))
	}

	release, err := r.Releases.Latest(ctx, r.Repository)
	if err != nil {
		return r.finish(UpdateRecord{
			CurrentVersion: r.CurrentVersion,
			Result:         "check-failed",
		}, cfg.Token, err)
	}
	comparison, err := buildinfo.CompareStableTags(r.CurrentVersion, release.Version)
	if err != nil {
		return r.finish(UpdateRecord{
			CurrentVersion: r.CurrentVersion,
			LatestVersion:  release.Version,
			Result:         "check-failed",
		}, cfg.Token, fmt.Errorf("release version is invalid: %w", err))
	}
	if comparison >= 0 {
		result := "current"
		if comparison > 0 {
			result = "local-newer"
		}
		return r.finish(UpdateRecord{
			CurrentVersion: r.CurrentVersion,
			LatestVersion:  release.Version,
			Result:         result,
		}, cfg.Token, nil)
	}

	if r.Scanner == nil {
		return r.finish(UpdateRecord{
			CurrentVersion: r.CurrentVersion,
			LatestVersion:  release.Version,
			Result:         "check-failed",
		}, cfg.Token, errors.New("scanner is not configured"))
	}
	if r.Install == nil {
		return r.finish(UpdateRecord{
			CurrentVersion: r.CurrentVersion,
			LatestVersion:  release.Version,
			Result:         "check-failed",
		}, cfg.Token, errors.New("installer is not configured"))
	}

	wasRunning, err := r.Scanner.Status()
	if err != nil {
		return r.finish(UpdateRecord{
			CurrentVersion: r.CurrentVersion,
			LatestVersion:  release.Version,
			Result:         "status-failed",
		}, cfg.Token, err)
	}
	baseRecord := UpdateRecord{
		CurrentVersion:  r.CurrentVersion,
		LatestVersion:   release.Version,
		WasRunning:      wasRunning,
		WasRunningKnown: true,
	}
	if !wasRunning {
		if err := r.install(ctx, cfg, release.Version); err != nil {
			return r.finishWithRecord(baseRecord, "install-failed", cfg.Token, err)
		}
		return r.finishWithRecord(baseRecord, "updated", cfg.Token, nil)
	}

	if err := r.Scanner.Stop(); err != nil {
		return r.finishWithRecord(baseRecord, "stop-failed", cfg.Token, err)
	}
	shutdownContext, cancel := context.WithTimeout(ctx, r.shutdownTimeout())
	waitErr := r.Scanner.WaitStopped(shutdownContext, r.pollInterval())
	cancel()
	if waitErr != nil {
		result := "shutdown-failed"
		if errors.Is(waitErr, context.DeadlineExceeded) {
			result = "shutdown-timeout"
		}
		return r.finishWithRecord(baseRecord, result, cfg.Token, waitErr)
	}

	if err := r.install(ctx, cfg, release.Version); err != nil {
		resumeErr := r.resumeAfterFailedInstall()
		if resumeErr != nil {
			err = fmt.Errorf("%w; scanner resume failed: %v", err, resumeErr)
		}
		return r.finishWithRecord(baseRecord, "install-failed", cfg.Token, err)
	}
	if err := r.markResumePending(); err != nil {
		resumeErr := r.resumeScanner()
		if resumeErr != nil {
			err = fmt.Errorf("%w; scanner recovery failed: %v", err, resumeErr)
		}
		return r.finishWithRecord(baseRecord, "restart-failed", cfg.Token, err)
	}
	if err := r.resumeScanner(); err != nil {
		return r.finishWithRecord(baseRecord, "restart-failed", cfg.Token, err)
	}
	return r.finishWithRecord(baseRecord, "updated", cfg.Token, nil)
}

func (r Runner) install(ctx context.Context, cfg config.Config, version string) error {
	installContext, cancel := context.WithTimeout(ctx, r.installTimeout())
	defer cancel()
	return r.Install(installContext, cfg, version, io.Discard)
}

func (r Runner) resumeAfterFailedInstall() error {
	if err := r.markResumePending(); err != nil {
		return err
	}
	return r.resumeScanner()
}

func (r Runner) recoverPendingResume(ctx context.Context, cfg config.Config) (bool, error) {
	pending, err := HasPendingResume(r.resumePath())
	if err != nil {
		return true, r.finish(UpdateRecord{CurrentVersion: r.CurrentVersion, Result: "resume-failed"}, cfg.Token, err)
	}
	if !pending {
		return false, nil
	}
	if r.Scanner == nil {
		return true, r.finish(UpdateRecord{CurrentVersion: r.CurrentVersion, Result: "resume-failed"}, cfg.Token, errors.New("scanner is not configured"))
	}
	if err := ctx.Err(); err != nil {
		return true, r.finish(UpdateRecord{CurrentVersion: r.CurrentVersion, Result: "resume-failed"}, cfg.Token, err)
	}
	if err := r.resumeScanner(); err != nil {
		return true, r.finish(UpdateRecord{
			CurrentVersion:  r.CurrentVersion,
			WasRunningKnown: true,
			Result:          "resume-failed",
		}, cfg.Token, err)
	}
	return true, r.finish(UpdateRecord{
		CurrentVersion:  r.CurrentVersion,
		WasRunning:      true,
		WasRunningKnown: true,
		Result:          "resume-recovered",
	}, cfg.Token, nil)
}

func (r Runner) markResumePending() error {
	return SetPendingResume(r.resumePath())
}

func (r Runner) resumeScanner() error {
	running, err := r.Scanner.Status()
	if err != nil {
		return err
	}
	if !running {
		startErr := r.Scanner.Start()
		confirmed, statusErr := r.Scanner.Status()
		if statusErr != nil {
			if startErr != nil {
				return fmt.Errorf("%w; scanner status after start failed: %v", startErr, statusErr)
			}
			return fmt.Errorf("scanner status after start failed: %w", statusErr)
		}
		if !confirmed {
			if startErr != nil {
				return fmt.Errorf("%w; scanner is not running after start", startErr)
			}
			return errors.New("scanner is not running after start")
		}
	}
	if err := ClearPendingResume(r.resumePath()); err != nil {
		return err
	}
	return nil
}

func (r Runner) validateIdentity() error {
	if _, err := buildinfo.CompareStableTags(r.CurrentVersion, r.CurrentVersion); err != nil {
		return fmt.Errorf("automatic updates require a stable local version: %w", err)
	}
	if err := validateRepository(r.Repository); err != nil {
		return fmt.Errorf("automatic updates require a release repository: %w", err)
	}
	return nil
}

func (r Runner) shutdownTimeout() time.Duration {
	if r.ShutdownTimeout <= 0 {
		return defaultShutdownTimeout
	}
	return r.ShutdownTimeout
}

func (r Runner) pollInterval() time.Duration {
	if r.PollInterval <= 0 {
		return defaultPollInterval
	}
	return r.PollInterval
}

func (r Runner) installTimeout() time.Duration {
	if r.InstallTimeout <= 0 {
		return defaultInstallTimeout
	}
	return r.InstallTimeout
}

func (r Runner) resumePath() string {
	if r.ResumePath == "" {
		if provider, ok := r.Scanner.(interface{ PendingResumePath() string }); ok {
			if path := provider.PendingResumePath(); path != "" {
				return path
			}
		}
		return DefaultResumeStatePath
	}
	return r.ResumePath
}

func (r Runner) finish(record UpdateRecord, token string, err error) error {
	return r.finishWithRecord(record, record.Result, token, err)
}

func (r Runner) finishWithRecord(record UpdateRecord, result, token string, err error) error {
	record.Result = result
	var safeErr error
	if err != nil {
		safeErr = redactError(err, token)
		record.Error = safeErr.Error()
	}
	if r.Logger != nil {
		if logErr := r.Logger.Record(record); logErr != nil {
			safeLogErr := redactError(logErr, token)
			if safeErr == nil {
				return fmt.Errorf("record automatic update result: %s", safeLogErr)
			}
			return fmt.Errorf("%w; record automatic update result: %s", safeErr, safeLogErr)
		}
	}
	return safeErr
}

type redactedError struct {
	cause   error
	message string
}

func (err redactedError) Error() string {
	return err.message
}

func (err redactedError) Unwrap() error {
	return err.cause
}

func redactError(err error, token string) error {
	if err == nil {
		return nil
	}
	message := sanitizeErrorTextWithToken(err.Error(), token)
	if message == err.Error() {
		return err
	}
	return redactedError{cause: err, message: message}
}
