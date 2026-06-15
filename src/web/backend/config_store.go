package backend

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"explo/src/config"
	"explo/src/models"
	"explo/src/web"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func (s *Server) getConfigValues(keys []string) (map[string]string, error) {
	all, err := s.getAllConfigValues()
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := all[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (s *Server) getAllConfigValues() (map[string]string, error) {
	return config.DatabaseValues(s.db)
}

func (s *Server) setConfigValues(updates map[string]string) error {
	return s.applyConfigValues(updates)
}

func (s *Server) setConfigValuesForUser(userID uint, updates map[string]string) error {
	return s.applyConfigValuesForUser(userID, updates)
}

func (s *Server) replaceConfigValues(values map[string]string) error {
	updates := make(map[string]string, len(allConfigKeys)+len(values))
	for _, key := range allConfigKeys {
		updates[key] = ""
	}
	for key, value := range values {
		updates[key] = value
	}
	return s.applyConfigValues(updates)
}

func (s *Server) resetConfigValues() error {
	return s.replaceConfigValues(parseEnvText(string(web.SampleEnv)))
}

func configValuesToEnvText(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		v := values[k]
		if v == "" {
			continue
		}
		_, _ = fmt.Fprintf(&b, "%s=%s\n", k, formatEnvValue(v))
	}
	return b.String()
}

// formatEnvValue quotes values when rendering database-backed settings in the
// legacy .env text format used by the raw settings editor.
func formatEnvValue(v string) string {
	// preserve already quoted values
	if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
		return v
	}

	if strings.ContainsAny(v, `"$#?' `) {
		// escape single quotes inside value
		v = strings.ReplaceAll(v, `'`, `'\''`)
		return fmt.Sprintf(`'%s'`, v)
	}

	return v
}

func (s *Server) importEnvFileIfConfigEmpty() error {
	empty, err := s.configTablesEmpty()
	if err != nil {
		return err
	}
	if !empty {
		return nil
	}

	data, err := readConfigFileOrSample(s.cfg.WebEnvPath)
	if err != nil {
		return err
	}
	values := parseEnvText(string(data))
	if len(values) == 0 {
		return nil
	}
	return s.setConfigValues(values)
}

func (s *Server) configTablesEmpty() (bool, error) {
	modelsToCheck := []any{
		&models.User{},
		&models.Server{},
		&models.Downloader{},
		&models.AppSettings{},
		&models.Playlist{},
	}
	for _, model := range modelsToCheck {
		var count int64
		if err := s.db.Model(model).Count(&count).Error; err != nil {
			return false, err
		}
		if count > 0 {
			return false, nil
		}
	}
	return true, nil
}

func readConfigFileOrSample(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if os.IsNotExist(err) {
		return web.SampleEnv, nil
	}
	return nil, err
}

func (s *Server) applyConfigValues(values map[string]string) error {
	return s.applyConfigValuesForUser(0, values)
}

func (s *Server) applyConfigValuesForUser(userID uint, values map[string]string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		return s.applyConfigValuesTxForUser(tx, userID, values)
	})
}

func (s *Server) applyConfigValuesTx(tx *gorm.DB, values map[string]string) error {
	return s.applyConfigValuesTxForUser(tx, 0, values)
}

func (s *Server) applyConfigValuesTxForUser(tx *gorm.DB, userID uint, values map[string]string) error {
	user, err := ensureConfigUser(tx, userID, values)
	if err != nil {
		return err
	}
	if err := applyUserConfig(tx, user, values); err != nil {
		return err
	}
	if err := applyScheduleConfig(tx, user, values); err != nil {
		return err
	}
	server, err := applyServerConfig(tx, user, values)
	if err != nil {
		return err
	}
	if err := applyDownloaderConfig(tx, user, values); err != nil {
		return err
	}
	if err := applySettingsConfig(tx, values); err != nil {
		return err
	}
	return applyCredentialConfig(tx, user, server, values)
}

func ensureConfigUser(tx *gorm.DB, userID uint, values map[string]string) (*models.User, error) {
	var user models.User
	query := tx.Order("id")
	if userID != 0 {
		query = query.Where("id = ?", userID)
	}
	if err := query.First(&user).Error; err == nil {
		return &user, nil
	} else if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}

	username := firstNonEmpty(os.Getenv("UI_USERNAME"), values["UI_USERNAME"], "admin")
	password := firstNonEmpty(os.Getenv("UI_PASSWORD"), values["UI_PASSWORD"], "admin")
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	user = models.User{
		Username:      username,
		Password:      string(passwordHash),
		Role:          models.RoleManager,
		LBUserName:    values["LISTENBRAINZ_USER"],
		DiscoveryMode: firstNonEmpty(values["LISTENBRAINZ_DISCOVERY"], "playlist"),
	}
	if err := tx.Create(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func applyUserConfig(tx *gorm.DB, user *models.User, values map[string]string) error {
	if user == nil {
		return nil
	}
	changed := false
	if value, ok := values["LISTENBRAINZ_USER"]; ok {
		user.LBUserName = value
		changed = true
	}
	if changed {
		return tx.Save(user).Error
	}
	return nil
}

func applyScheduleConfig(tx *gorm.DB, user *models.User, values map[string]string) error {
	if user == nil {
		return nil
	}
	prefixToName := map[string]string{}
	for name, def := range playlistDefs {
		prefixToName[def.EnvPrefix] = name
	}
	for key := range values {
		if strings.HasSuffix(key, "_SCHEDULE") {
			prefix := strings.TrimSuffix(key, "_SCHEDULE")
			if _, ok := prefixToName[prefix]; !ok && strings.HasPrefix(prefix, "CUSTOM_") {
				prefixToName[prefix] = strings.ToLower(strings.ReplaceAll(prefix, "_", "-"))
			}
		}
		if strings.HasSuffix(key, "_FLAGS") {
			prefix := strings.TrimSuffix(key, "_FLAGS")
			if _, ok := prefixToName[prefix]; !ok && strings.HasPrefix(prefix, "CUSTOM_") {
				prefixToName[prefix] = strings.ToLower(strings.ReplaceAll(prefix, "_", "-"))
			}
		}
	}

	for prefix, name := range prefixToName {
		schedule, hasSchedule := values[prefix+"_SCHEDULE"]
		flags, hasFlags := values[prefix+"_FLAGS"]
		if !hasSchedule && !hasFlags {
			continue
		}
		dbName := playlistDBName(name, user.LBUserName)
		var row models.Playlist
		if err := tx.Unscoped().Where("user_id = ? AND (env_prefix = ? OR name IN ?)", user.ID, prefix, []string{name, dbName}).First(&row).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			row = models.Playlist{UserID: user.ID, Name: dbName, DisplayName: name, EnvPrefix: prefix, Source: "listenbrainz", Enabled: true}
			if strings.HasPrefix(name, "custom-") {
				row.Kind = models.PlaylistKindCustom
			} else {
				row.Kind = models.PlaylistKindDefault
			}
		}
		if row.Name == "" || (row.Kind == models.PlaylistKindDefault && row.Name == name) {
			row.Name = dbName
		}
		row.DeletedAt = gorm.DeletedAt{}
		if row.DisplayName == "" {
			row.DisplayName = name
		}
		if row.EnvPrefix == "" {
			row.EnvPrefix = prefix
		}
		if row.Kind == "" {
			row.Kind = models.PlaylistKindDefault
		}
		if hasSchedule {
			row.Schedule = schedule
		}
		if hasFlags {
			row.Flags = flags
		}
		row.Enabled = row.Flags != ""
		if row.ID == 0 {
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		} else if err := tx.Unscoped().Save(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func playlistDBName(logicalName, lbUser string) string {
	if strings.HasPrefix(logicalName, "custom-") {
		return logicalName
	}
	owner := config.ListenBrainzUserSlug(lbUser)
	if owner == "" {
		return logicalName
	}
	return logicalName + "-" + owner
}

func applyServerConfig(tx *gorm.DB, user *models.User, values map[string]string) (*models.Server, error) {
	serverKeys := []string{"EXPLO_SYSTEM", "SYSTEM_URL", "LIBRARY_NAME", "SLEEP", "PUBLIC_PLAYLIST", "ADMIN_API_KEY", "ADMIN_SYSTEM_USERNAME", "ADMIN_SYSTEM_PASSWORD"}
	selectedServerID := uint(0)
	if value, ok := values["SELECTED_SERVER_ID"]; ok && strings.TrimSpace(value) != "" {
		serverID, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil || serverID == 0 {
			return nil, fmt.Errorf("invalid selected server id")
		}
		selectedServerID = uint(serverID)
	}

	if selectedServerID != 0 && !hasAny(values, serverKeys...) {
		if err := selectAppServer(tx, selectedServerID); err != nil {
			return nil, err
		}
		var server models.Server
		if err := tx.First(&server, selectedServerID).Error; err != nil {
			return nil, err
		}
		return &server, nil
	}
	if !hasAny(values, serverKeys...) {
		var existing models.Server
		if err := tx.Order("id").First(&existing).Error; err == nil {
			return &existing, nil
		} else if err == gorm.ErrRecordNotFound {
			return nil, nil
		} else {
			return nil, err
		}
	}

	if user == nil {
		return nil, nil
	}
	var server models.Server
	if selectedServerID != 0 {
		if err := tx.First(&server, selectedServerID).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
			server = models.Server{ManagerID: user.ID, Name: "default", Type: models.ServerCustom}
		}
	} else if err := tx.Where("manager_id = ?", user.ID).Order("id").First(&server).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		server = models.Server{ManagerID: user.ID, Name: "default", Type: models.ServerCustom}
	}
	if server.ManagerID == 0 {
		server.ManagerID = user.ID
	}
	if value, ok := values["EXPLO_SYSTEM"]; ok {
		server.Type = models.ServerType(value)
	}
	if value, ok := values["SYSTEM_URL"]; ok {
		server.URL = value
	}
	if value, ok := values["LIBRARY_NAME"]; ok {
		server.LibraryName = value
		if server.Name == "" && value != "" {
			server.Name = value
		}
	}
	if value, ok := values["SLEEP"]; ok {
		server.Sleep, _ = strconv.Atoi(value)
	}
	if value, ok := values["PUBLIC_PLAYLIST"]; ok {
		server.PublicPlaylist = envBool(value)
	}
	if server.Name == "" {
		server.Name = firstNonEmpty(server.LibraryName, "default")
	}
	if server.AdminCredentialID != nil && *server.AdminCredentialID == 0 {
		server.AdminCredentialID = nil
	}
	if server.AdminCredentialID == nil {
		server.AdminCredential = nil
	}
	if server.ID == 0 {
		if err := tx.Omit("AdminCredential").Create(&server).Error; err != nil {
			return nil, err
		}
	} else if err := tx.Save(&server).Error; err != nil {
		return nil, err
	}
	if err := selectAppServer(tx, server.ID); err != nil {
		return nil, err
	}
	return &server, nil
}

func selectAppServer(tx *gorm.DB, serverID uint) error {
	settings := models.DefaultAppSettings()
	if err := tx.First(&settings).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	settings.SelectedServerID = &serverID
	if settings.ID == 0 {
		return tx.Create(&settings).Error
	}
	return tx.Save(&settings).Error
}

func applyDownloaderConfig(tx *gorm.DB, user *models.User, values map[string]string) error {
	if user == nil || !hasAny(values, "DOWNLOAD_DIR", "PLAYLIST_DIR", "YOUTUBE_API_KEY", "TRACK_EXTENSION", "FILTER_LIST", "SLSKD_URL", "SLSKD_API_KEY", "SLSKD_DIR", "EXTENSIONS") {
		return nil
	}
	services := splitServices(firstNonEmpty(values["DOWNLOAD_SERVICES"], "youtube"))
	for idx, service := range services {
		dtype := downloaderType(service)
		var dl models.Downloader
		if err := tx.Where("user_id = ? AND type = ?", user.ID, dtype).First(&dl).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			dl = models.Downloader{UserID: user.ID, Name: string(dtype), Type: dtype}
		}
		if err := applyDownloaderValues(tx, &dl, values, services); err != nil {
			return err
		}
		if dl.ID == 0 {
			if err := tx.Create(&dl).Error; err != nil {
				return err
			}
		} else if err := tx.Save(&dl).Error; err != nil {
			return err
		}
		link := models.UserDownloader{UserID: user.ID, DownloaderID: dl.ID}
		if err := tx.FirstOrCreate(&link, link).Error; err != nil {
			return err
		}
		var server models.Server
		if err := tx.Where("manager_id = ?", user.ID).Order("id").First(&server).Error; err == nil {
			serverLink := models.ServerDownloader{ServerID: server.ID, DownloaderID: dl.ID}
			if err := tx.FirstOrCreate(&serverLink, serverLink).Error; err != nil {
				return err
			}
			if err := tx.Model(&serverLink).Update("priority", idx).Error; err != nil {
				return err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	return nil
}

func applyDownloaderValues(tx *gorm.DB, dl *models.Downloader, values map[string]string, services []string) error {
	if value, ok := values["DOWNLOAD_DIR"]; ok {
		dl.DownloadDir = value
	}
	if value, ok := values["PLAYLIST_DIR"]; ok {
		dl.PlaylistDir = value
	}
	if dl.Type == models.DownloaderYoutube || dl.Type == models.DownloaderYoutubeMusic {
		yt, err := upsertYoutubeDownloader(tx, dl.YoutubeDownloaderID, dl.Name, values)
		if err != nil {
			return err
		}
		dl.YoutubeDownloaderID = &yt.ID
	}
	if dl.Type == models.DownloaderSlskd {
		slskd, err := upsertSlskdDownloader(tx, dl.SlskdDownloaderID, dl.Name, values)
		if err != nil {
			return err
		}
		dl.SlskdDownloaderID = &slskd.ID
	}
	return nil
}

func upsertYoutubeDownloader(tx *gorm.DB, id *uint, name string, values map[string]string) (*models.YoutubeDownloader, error) {
	var row models.YoutubeDownloader
	if id != nil && *id != 0 {
		if err := tx.First(&row, *id).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if row.ID == 0 {
		row = models.YoutubeDownloader{Name: name}
	}
	if value, ok := values["YOUTUBE_API_KEY"]; ok {
		row.APIKey = value
	}
	if value, ok := values["TRACK_EXTENSION"]; ok {
		row.TrackExtension = value
	}
	if value, ok := values["FILTER_LIST"]; ok {
		row.FilterList = value
	}
	if row.ID == 0 {
		return &row, tx.Create(&row).Error
	}
	return &row, tx.Save(&row).Error
}

func upsertSlskdDownloader(tx *gorm.DB, id *uint, name string, values map[string]string) (*models.SlskdDownloader, error) {
	var row models.SlskdDownloader
	if id != nil && *id != 0 {
		if err := tx.First(&row, *id).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if row.ID == 0 {
		row = models.SlskdDownloader{Name: name}
	}
	if value, ok := values["SLSKD_URL"]; ok {
		row.URL = value
	}
	if value, ok := values["SLSKD_API_KEY"]; ok {
		row.APIKey = value
	}
	if value, ok := values["SLSKD_DIR"]; ok {
		row.SlskdDir = value
	}
	if value, ok := values["EXTENSIONS"]; ok {
		row.Extensions = value
	}
	if value, ok := values["FILTER_LIST"]; ok {
		row.FilterList = value
	}
	if row.ID == 0 {
		return &row, tx.Create(&row).Error
	}
	return &row, tx.Save(&row).Error
}

func applySettingsConfig(tx *gorm.DB, values map[string]string) error {
	if !hasAny(values, "WIZARD_COMPLETE", "SELECTED_SERVER_ID", "LISTENBRAINZ_DISCOVERY", "DOWNLOAD_SERVICES", "PATH_TEMPLATE", "PLAYLISTNAME_FORMAT", "SINGLE_ARTIST", "KEEP_PERMISSIONS", "MIGRATE_DOWNLOADS", "ENRICH_TRACK_METADATA", "OVERWRITE_METADATA", "USE_SUBDIRECTORY") {
		return nil
	}
	settings := models.DefaultAppSettings()
	if err := tx.First(&settings).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	if value, ok := values["WIZARD_COMPLETE"]; ok {
		settings.WizardComplete = envBool(value)
	}
	if value, ok := values["SELECTED_SERVER_ID"]; ok {
		if strings.TrimSpace(value) == "" {
			settings.SelectedServerID = nil
		} else if id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64); err == nil && id != 0 {
			selectedID := uint(id)
			settings.SelectedServerID = &selectedID
		}
	}
	if value, ok := values["LISTENBRAINZ_DISCOVERY"]; ok {
		settings.ListenBrainzDiscovery = firstNonEmpty(value, "playlist")
	}
	if value, ok := values["DOWNLOAD_SERVICES"]; ok {
		settings.DownloadServices = strings.Join(splitServices(value), ",")
	}
	if value, ok := values["PATH_TEMPLATE"]; ok {
		settings.PathTemplate = value
	}
	if value, ok := values["PLAYLISTNAME_FORMAT"]; ok {
		settings.PlaylistNameFormat = firstNonEmpty(value, "week")
	}
	if value, ok := values["SINGLE_ARTIST"]; ok {
		settings.SingleArtist = envBool(firstNonEmpty(value, "true"))
	}
	if value, ok := values["KEEP_PERMISSIONS"]; ok {
		settings.KeepPermissions = envBool(firstNonEmpty(value, "true"))
	}
	if value, ok := values["MIGRATE_DOWNLOADS"]; ok {
		settings.MigrateDownloads = envBool(value)
	}
	if value, ok := values["ENRICH_TRACK_METADATA"]; ok {
		settings.EnrichTrackMetadata = envBool(value)
	}
	if value, ok := values["OVERWRITE_METADATA"]; ok {
		settings.OverwriteMetadata = envBool(value)
	}
	if value, ok := values["USE_SUBDIRECTORY"]; ok {
		settings.UseSubdirectory = envBool(firstNonEmpty(value, "true"))
	}
	if settings.ID == 0 {
		return tx.Create(&settings).Error
	}
	return tx.Save(&settings).Error
}

func applyCredentialConfig(tx *gorm.DB, user *models.User, server *models.Server, values map[string]string) error {
	if user == nil || server == nil {
		return nil
	}
	if hasAny(values, "ADMIN_API_KEY", "ADMIN_SYSTEM_USERNAME", "ADMIN_SYSTEM_PASSWORD") {
		var adminCredentialID uint
		if server.AdminCredentialID != nil {
			adminCredentialID = *server.AdminCredentialID
		}
		cred, err := upsertCredential(tx, adminCredentialID, models.CredentialAPIKey, values["ADMIN_API_KEY"], values["ADMIN_SYSTEM_USERNAME"], values["ADMIN_SYSTEM_PASSWORD"])
		if err != nil {
			return err
		}
		server.AdminCredentialID = &cred.ID
		if err := tx.Save(server).Error; err != nil {
			return err
		}
	}
	if hasAny(values, "API_KEY", "SYSTEM_USERNAME", "SYSTEM_PASSWORD") {
		var link models.UserServerCredential
		if err := tx.Where("user_id = ? AND server_id = ?", user.ID, server.ID).First(&link).Error; err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		cred, err := upsertCredential(tx, link.CredentialID, models.CredentialUserPass, values["API_KEY"], values["SYSTEM_USERNAME"], values["SYSTEM_PASSWORD"])
		if err != nil {
			return err
		}
		if link.ID == 0 {
			link = models.UserServerCredential{UserID: user.ID, ServerID: server.ID, CredentialID: cred.ID}
			return tx.Create(&link).Error
		}
		link.CredentialID = cred.ID
		return tx.Save(&link).Error
	}
	return nil
}

func upsertCredential(tx *gorm.DB, id uint, typ models.CredentialType, apiKey, username, password string) (*models.Credential, error) {
	cred := models.Credential{Type: typ}
	if id != 0 {
		if err := tx.First(&cred, id).Error; err != nil && err != gorm.ErrRecordNotFound {
			return nil, err
		}
		cred.Type = typ
	}
	cred.APIKey = apiKey
	cred.Username = username
	cred.Password = password
	if cred.ID == 0 {
		return &cred, tx.Create(&cred).Error
	}
	return &cred, tx.Save(&cred).Error
}

func hasAny(values map[string]string, keys ...string) bool {
	for _, key := range keys {
		if _, ok := values[key]; ok {
			return true
		}
	}
	return false
}

func splitServices(raw string) []string {
	parts := strings.Split(raw, ",")
	services := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		service := strings.TrimSpace(part)
		if service == "" || seen[service] {
			continue
		}
		seen[service] = true
		services = append(services, service)
	}
	if len(services) == 0 {
		return []string{"youtube"}
	}
	return services
}

func downloaderType(service string) models.DownloaderType {
	switch strings.ToLower(strings.TrimSpace(service)) {
	case "youtube_music", "youtube-music", "ytmusic":
		return models.DownloaderYoutubeMusic
	case "slskd", "slsk":
		return models.DownloaderSlskd
	default:
		return models.DownloaderYoutube
	}
}
