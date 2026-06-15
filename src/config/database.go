package config

import (
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"explo/src/models"

	"github.com/ilyakaznacheev/cleanenv"
	"gorm.io/gorm"
)

// ReadDatabase overlays settings stored in structured database tables on top of
// the config that was loaded from the environment/.env fallback. Values are
// exported to the process environment before asking cleanenv to re-read the
// struct so existing env tags and defaults stay the source of truth.
func (cfg *Config) ReadDatabase(db *gorm.DB) {
	values, err := DatabaseValues(db)
	if err != nil {
		slog.Warn("failed to load config from database", "err", err.Error())
		return
	}
	if len(values) == 0 {
		return
	}

	for key, value := range values {
		if key == "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			slog.Warn("failed to export database config", "key", key, "err", err.Error())
		}
	}

	if err := cleanenv.ReadEnv(cfg); err != nil {
		slog.Warn("failed to apply database config", "err", err.Error())
		return
	}
	cfg.CommonFixes()
}

// DatabaseValues flattens the structured configuration tables into the legacy
// env-key shape consumed by cleanenv and by the web UI API.
func DatabaseValues(db *gorm.DB) (map[string]string, error) {
	values := map[string]string{}
	if db == nil {
		return values, nil
	}

	userID, hasUserID, runLBUser := databaseUserContextFromEnv()

	settings := models.DefaultAppSettings()
	if err := db.First(&settings).Error; err != nil && !isRecordNotFound(err) {
		return nil, err
	}
	values["WIZARD_COMPLETE"] = strconv.FormatBool(settings.WizardComplete)
	if settings.SelectedServerID != nil && *settings.SelectedServerID != 0 {
		values["SELECTED_SERVER_ID"] = strconv.FormatUint(uint64(*settings.SelectedServerID), 10)
	}
	put(values, "LISTENBRAINZ_DISCOVERY", settings.ListenBrainzDiscovery)
	put(values, "DOWNLOAD_SERVICES", settings.DownloadServices)
	put(values, "PATH_TEMPLATE", settings.PathTemplate)
	put(values, "PLAYLISTNAME_FORMAT", firstNonEmpty(settings.PlaylistNameFormat, "week"))
	values["SINGLE_ARTIST"] = strconv.FormatBool(settings.SingleArtist)
	values["KEEP_PERMISSIONS"] = strconv.FormatBool(settings.KeepPermissions)
	values["MIGRATE_DOWNLOADS"] = strconv.FormatBool(settings.MigrateDownloads)
	values["ENRICH_TRACK_METADATA"] = strconv.FormatBool(settings.EnrichTrackMetadata)
	values["OVERWRITE_METADATA"] = strconv.FormatBool(settings.OverwriteMetadata)
	values["USE_SUBDIRECTORY"] = strconv.FormatBool(settings.UseSubdirectory)

	var user models.User
	userQuery := db.Order("id")
	if hasUserID {
		userQuery = userQuery.Where("id = ?", userID)
	}
	if runLBUser != "" {
		userQuery = userQuery.Where("lb_user_name = ?", runLBUser)
	}
	if err := userQuery.First(&user).Error; err == nil {
		put(values, "LISTENBRAINZ_USER", user.LBUserName)
		userID = user.ID
		hasUserID = true
	} else if err != nil && !isRecordNotFound(err) {
		return nil, err
	}

	var server models.Server
	serverQuery := db.Preload("AdminCredential").Order("id")
	if settings.SelectedServerID != nil && *settings.SelectedServerID != 0 {
		serverQuery = serverQuery.Where("id = ?", *settings.SelectedServerID)
	} else if hasUserID {
		serverQuery = serverQuery.Where("manager_id = ?", userID)
	}
	serverErr := serverQuery.First(&server).Error
	if isRecordNotFound(serverErr) && settings.SelectedServerID != nil && *settings.SelectedServerID != 0 && hasUserID {
		serverErr = db.Preload("AdminCredential").Where("manager_id = ?", userID).Order("id").First(&server).Error
	}
	if serverErr == nil {
		put(values, "EXPLO_SYSTEM", string(server.Type))
		put(values, "SYSTEM_URL", server.URL)
		put(values, "LIBRARY_NAME", server.LibraryName)
		if server.Sleep > 0 {
			values["SLEEP"] = strconv.Itoa(server.Sleep)
		}
		values["PUBLIC_PLAYLIST"] = strconv.FormatBool(server.PublicPlaylist)
		if server.AdminCredential != nil {
			put(values, "ADMIN_API_KEY", server.AdminCredential.APIKeyPlain())
			put(values, "ADMIN_SYSTEM_USERNAME", server.AdminCredential.UsernamePlain())
			put(values, "ADMIN_SYSTEM_PASSWORD", server.AdminCredential.PasswordPlain())
		}

		var link models.UserServerCredential
		linkQuery := db.Where("server_id = ?", server.ID).Order("id")
		if hasUserID {
			linkQuery = linkQuery.Where("user_id = ?", userID)
		}
		if err := linkQuery.First(&link).Error; err == nil {
			var credential models.Credential
			if err := db.First(&credential, link.CredentialID).Error; err == nil {
				put(values, "API_KEY", credential.APIKeyPlain())
				put(values, "SYSTEM_USERNAME", credential.UsernamePlain())
				put(values, "SYSTEM_PASSWORD", credential.PasswordPlain())
			} else if err != nil && !isRecordNotFound(err) {
				return nil, err
			}
		} else if err != nil && !isRecordNotFound(err) {
			return nil, err
		}
	} else if serverErr != nil && !isRecordNotFound(serverErr) {
		return nil, serverErr
	}

	downloaders, err := orderedDownloaders(db, userID, hasUserID, splitCSV(settings.DownloadServices))
	if err != nil {
		return nil, err
	}
	if len(downloaders) > 0 {
		for _, dl := range downloaders {
			putMissing(values, "DOWNLOAD_DIR", dl.DownloadDir)
			putMissing(values, "PLAYLIST_DIR", dl.PlaylistDir)
			if dl.YoutubeDownloader != nil {
				putMissing(values, "YOUTUBE_API_KEY", dl.YoutubeDownloader.APIKey)
				putMissing(values, "TRACK_EXTENSION", dl.YoutubeDownloader.TrackExtension)
				putMissing(values, "FILTER_LIST", dl.YoutubeDownloader.FilterList)
			}
			if dl.SlskdDownloader != nil {
				putMissing(values, "SLSKD_URL", dl.SlskdDownloader.URL)
				putMissing(values, "SLSKD_API_KEY", dl.SlskdDownloader.APIKey)
				putMissing(values, "SLSKD_DIR", dl.SlskdDownloader.SlskdDir)
				putMissing(values, "EXTENSIONS", dl.SlskdDownloader.Extensions)
				putMissing(values, "FILTER_LIST", dl.SlskdDownloader.FilterList)
			}
		}
	}

	var playlists []models.Playlist
	playlistQuery := db.Order("id")
	if hasUserID {
		playlistQuery = playlistQuery.Where("user_id = ?", userID)
	}
	if err := playlistQuery.Find(&playlists).Error; err != nil {
		return nil, err
	}
	for _, playlist := range playlists {
		if playlist.EnvPrefix == "" {
			continue
		}
		if playlist.Enabled {
			values[playlist.EnvPrefix+"_SCHEDULE"] = playlist.Schedule
			values[playlist.EnvPrefix+"_FLAGS"] = playlist.Flags
		} else {
			values[playlist.EnvPrefix+"_SCHEDULE"] = ""
			values[playlist.EnvPrefix+"_FLAGS"] = ""
		}
	}

	return values, nil
}

func put(values map[string]string, key, value string) {
	if value != "" {
		values[key] = value
	}
}

func putMissing(values map[string]string, key, value string) {
	if values[key] == "" {
		put(values, key, value)
	}
}

func orderedDownloaders(db *gorm.DB, userID uint, hasUserID bool, services []string) ([]models.Downloader, error) {
	if len(services) == 0 {
		services = []string{"youtube"}
	}
	var candidates []models.Downloader
	if hasUserID {
		if err := db.Preload("YoutubeDownloader").Preload("SlskdDownloader").
			Joins("LEFT JOIN server_downloaders ON server_downloaders.downloader_id = downloaders.id").
			Joins("LEFT JOIN servers ON servers.id = server_downloaders.server_id AND servers.deleted_at IS NULL").
			Joins("JOIN user_downloaders ON user_downloaders.downloader_id = downloaders.id").
			Where("user_downloaders.user_id = ?", userID).
			Where("servers.manager_id = ? OR server_downloaders.server_id IS NULL", userID).
			Order("server_downloaders.server_id IS NULL, server_downloaders.priority, user_downloaders.downloader_id").Find(&candidates).Error; err != nil {
			return nil, err
		}
	}
	var fallback []models.Downloader
	query := db.Preload("YoutubeDownloader").Preload("SlskdDownloader").Order("id")
	if hasUserID {
		query = query.Where("user_id = ? OR user_id = 0", userID)
	}
	if err := query.Find(&fallback).Error; err != nil {
		return nil, err
	}
	candidates = append(candidates, fallback...)

	byType := map[string][]models.Downloader{}
	for _, dl := range candidates {
		byType[string(dl.Type)] = append(byType[string(dl.Type)], dl)
	}
	ordered := make([]models.Downloader, 0, len(services))
	seen := map[uint]bool{}
	for _, service := range services {
		for _, dl := range byType[service] {
			if hasUserID && dl.UserID != 0 && dl.UserID != userID {
				continue
			}
			if !seen[dl.ID] {
				ordered = append(ordered, dl)
				seen[dl.ID] = true
			}
			break
		}
	}
	return ordered, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func splitCSV(raw string) []string {
	items := []string{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

func isRecordNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func databaseUserContextFromEnv() (uint, bool, string) {
	raw := strings.TrimSpace(os.Getenv("EXPLO_RUN_USER_ID"))
	lbUser := strings.TrimSpace(os.Getenv("EXPLO_RUN_LB_USER"))
	if raw == "" {
		return 0, false, lbUser
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, false, lbUser
	}
	return uint(id), true, lbUser
}
