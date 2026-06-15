package backend

// Jobs running on a schedule go here i.e cache cleanups (and playlist imports in the future)

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"explo/src/config"
	"explo/src/models"

	"github.com/go-co-op/gocron/v2"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

type Jobs struct {
	scheduler gocron.Scheduler
}

type fileInfo struct {
	path    string
	size    int64
	modTime time.Time
}

func NewJobs() *Jobs {
	scheduler, err := gocron.NewScheduler()
	if err != nil {
		slog.Error("failed creating cron scheduler")
	}

	return &Jobs{scheduler: scheduler}
}

func (j *Jobs) Start() {
	j.scheduler.Start()
}

func (j *Jobs) RegisterCoverCleanup(schedule, coversDir string, maxBytes int64) error {
	_, err := j.scheduler.NewJob(
		gocron.CronJob(schedule, false),
		gocron.NewTask(func() {
			slog.Info("running cache cleanup")

			trimCacheDir(coversDir, maxBytes)
		}),
	)

	return err
}

// RegisterCustomPlaylistRefresh registers a cache-refresh job for each custom playlist
// using its stored schedule. Falls back to daily at 4 AM if no schedule is set.
func (j *Jobs) RegisterCustomPlaylistRefresh(cfgDir string, db *gorm.DB) error {
	playlists := loadCustomPlaylists(cfgDir)
	if len(playlists) == 0 {
		return nil
	}

	envValues, err := config.DatabaseValues(db)
	if err != nil {
		return err
	}

	for _, p := range playlists {
		p := p
		prefix := customEnvPrefix(p.ID)
		flags := envValues[prefix+"_FLAGS"]
		if flags == "" {
			continue // disabled
		}
		schedule := envValues[prefix+"_SCHEDULE"]
		if p.RefreshDays <= 0 && schedule == "" {
			continue
		}
		if schedule == "" {
			schedule = "0 4 * * *"
		}
		_, err := j.scheduler.NewJob(
			gocron.CronJob(schedule, false),
			gocron.NewTask(func() {
				if time.Since(p.LastFetched) < time.Duration(p.RefreshDays)*24*time.Hour {
					return
				}
				slog.Info("custom-playlists: refreshing", "id", p.ID, "name", p.Name, "source", p.Source)
				result, err := fetchCustomPlaylistTracks(p)
				if err != nil {
					slog.Warn("custom-playlists: refresh fetch failed", "id", p.ID, "err", err)
					return
				}
				writePrefetchCache(cfgDir, p.ID, result.Tracks)
				playlists := loadCustomPlaylists(cfgDir)
				for i, pl := range playlists {
					if pl.ID == p.ID {
						playlists[i].LastFetched = time.Now().UTC()
						break
					}
				}
				if err := saveCustomPlaylists(cfgDir, playlists); err != nil {
					slog.Error("custom-playlists: failed to save after refresh", "err", err)
				}
			}),
		)
		if err != nil {
			slog.Warn("custom-playlists: failed to register refresh job", "id", p.ID, "err", err)
		}
	}
	return nil
}

// RegisterPlaylistPoller checks once per hour for enabled user-scoped playlists
// that are due and starts the normal Explo CLI for that specific user. This
// replaces container-level start.sh cron loops and works for built-in and custom
// playlists alike because Playlist rows carry schedule, enabled, and source state.
func (j *Jobs) RegisterPlaylistPoller(s *Server) error {
	_, err := j.scheduler.NewJob(
		gocron.DurationJob(time.Hour),
		gocron.NewTask(func() { s.pollDuePlaylistRuns() }),
	)
	if err != nil {
		return err
	}
	// Also check shortly after startup so a container restart does not miss work
	// that became due while it was down.
	go func() {
		time.Sleep(5 * time.Second)
		s.pollDuePlaylistRuns()
	}()
	return nil
}

func (s *Server) pollDuePlaylistRuns() {
	var playlists []models.Playlist
	if err := s.db.Where("enabled = ? AND flags <> '' AND schedule <> ''", true).Find(&playlists).Error; err != nil {
		slog.Warn("playlist poller: failed to load playlists", "err", err.Error())
		return
	}

	now := time.Now()
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	for _, playlist := range playlists {
		if !playlistScheduleDue(parser, playlist.Name, playlist.Schedule, playlist.LastRunAt, now) {
			continue
		}
		if playlist.Kind == models.PlaylistKindCustom {
			if err := s.refreshCustomPlaylistCache(playlist, now); err != nil {
				slog.Warn("playlist poller: custom refresh failed", "playlist", playlist.Name, "user_id", playlist.UserID, "err", err.Error())
				continue
			}
		}
		args := fieldsWithConfig(playlist.Flags, s.cfg.WebEnvPath)
		slog.Info("playlist poller: starting scheduled run", "user_id", playlist.UserID, "playlist", playlist.Name)
		if err := s.startRunWithEnv(args, s.runEnvForUser(playlist.UserID)); err != nil {
			if errors.Is(err, errRunAlreadyStarted) {
				slog.Info("playlist poller: run already active, will retry next hour", "playlist", playlist.Name, "user_id", playlist.UserID)
				continue
			}
			slog.Warn("playlist poller: failed to start run", "playlist", playlist.Name, "user_id", playlist.UserID, "err", err.Error())
			continue
		}
		nowCopy := now.UTC()
		if err := s.db.Model(&models.Playlist{}).Where("id = ?", playlist.ID).Update("last_run_at", &nowCopy).Error; err != nil {
			slog.Warn("playlist poller: failed to mark run time", "playlist", playlist.Name, "err", err.Error())
		}
	}
}

func (s *Server) refreshCustomPlaylistCache(row models.Playlist, now time.Time) error {
	cp := customPlaylistFromModel(row)
	if row.RefreshDays > 0 && row.LastFetched != nil && now.Sub(*row.LastFetched) < time.Duration(row.RefreshDays)*24*time.Hour {
		return nil
	}
	result, err := fetchCustomPlaylistTracks(cp)
	if err != nil {
		return err
	}
	writePrefetchCache(s.cfg.WebDataDir, row.Name, result.Tracks)
	nowCopy := now.UTC()
	return s.db.Model(&models.Playlist{}).Where("id = ?", row.ID).Update("last_fetched", &nowCopy).Error
}

func playlistScheduleDue(parser cron.Parser, name, schedule string, lastRunAt *time.Time, now time.Time) bool {
	if lastRunAt != nil && now.Sub(*lastRunAt) < 50*time.Minute {
		return false
	}
	sched, err := parser.Parse(schedule)
	if err != nil {
		slog.Warn("playlist poller: invalid cron", "playlist", name, "schedule", schedule, "err", err.Error())
		return false
	}
	windowStart := now.Add(-1 * time.Hour)
	if lastRunAt != nil {
		windowStart = *lastRunAt
	} else {
		windowStart = now.Add(-24 * time.Hour)
	}
	next := sched.Next(windowStart)
	return !next.After(now)
}

func fieldsWithConfig(flags, cfgPath string) []string {
	args := strings.Fields(flags)
	if cfgPath != "" {
		args = append(args, "--config", cfgPath)
	}
	return args
}

func trimCacheDir(dataDir string, maxBytes int64) {

	var files []fileInfo
	var total int64

	err := filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		files = append(files, fileInfo{
			path:    path,
			size:    info.Size(),
			modTime: info.ModTime(),
		})

		total += info.Size()
		return nil
	})

	if err != nil || total <= maxBytes {
		return
	}

	slices.SortFunc(files, func(a, b fileInfo) int {
		return a.modTime.Compare(b.modTime)
	})

	for _, f := range files {
		if total <= maxBytes {
			break
		}

		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
	}
}
