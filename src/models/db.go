package models

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

//
// SECRET_KEY
// Must be 32 bytes (AES-256)
//
//

var encryptionKey []byte

func SetEncryptionKey(key []byte) {
	encryptionKey = key
}

func encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if len(encryptionKey) != 32 {
		return "", errors.New("invalid encryption key length (must be 32 bytes)")
	}

	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	if len(encryptionKey) != 32 {
		return "", errors.New("invalid encryption key length")
	}

	data, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("invalid ciphertext")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plain), nil
}

func (c *Credential) BeforeSave(tx *gorm.DB) error {
	var err error

	if c.APIKey != "" {
		c.APIKey, err = encrypt(c.APIKey)
		if err != nil {
			return err
		}
	}

	if c.Username != "" {
		c.Username, err = encrypt(c.Username)
		if err != nil {
			return err
		}
	}

	if c.Password != "" {
		c.Password, err = encrypt(c.Password)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Credential) APIKeyPlain() string {
	v, _ := decrypt(c.APIKey)
	return v
}

func (c *Credential) UsernamePlain() string {
	v, _ := decrypt(c.Username)
	return v
}

func (c *Credential) PasswordPlain() string {
	v, _ := decrypt(c.Password)
	return v
}

type AppSettings struct {
	ID                    uint `gorm:"primaryKey"`
	WizardComplete        bool
	ListenBrainzDiscovery string `gorm:"not null;default:'playlist'"`
	DownloadServices      string `gorm:"not null;default:'youtube'"`
	SelectedServerID      *uint
	SelectedServer        *Server `gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`
	PathTemplate          string
	PlaylistNameFormat    string `gorm:"not null;default:'week'"`
	SingleArtist          bool   `gorm:"not null;default:true"`
	KeepPermissions       bool   `gorm:"not null;default:true"`
	MigrateDownloads      bool   `gorm:"not null;default:false"`
	EnrichTrackMetadata   bool   `gorm:"not null;default:false"`
	OverwriteMetadata     bool   `gorm:"not null;default:false"`
	UseSubdirectory       bool   `gorm:"not null;default:true"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type PathTemplatePreset struct {
	ID uint `gorm:"primaryKey"`

	Name     string `gorm:"not null;uniqueIndex"`
	Template string `gorm:"not null"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

func DefaultAppSettings() AppSettings {
	return AppSettings{
		ListenBrainzDiscovery: "playlist",
		DownloadServices:      "youtube",
		PlaylistNameFormat:    "week",
		SingleArtist:          true,
		KeepPermissions:       true,
		UseSubdirectory:       true,
	}
}

type PlaylistSchedule struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"not null;index:idx_playlist_schedule_user_name,unique"`
	EnvPrefix string `gorm:"not null"`
	Schedule  string
	Flags     string
	UserID    *uint `gorm:"index:idx_playlist_schedule_user_name,unique"`
	User      *User `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	LastRunAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type UserRole string

const (
	RoleBasic   UserRole = "basic"
	RoleManager UserRole = "manager"
)

type User struct {
	ID            uint         `gorm:"primaryKey"`
	Username      string       `gorm:"uniqueIndex;not null"`
	Password      string       `gorm:"not null"` // bcrypt hash
	Role          UserRole     `gorm:"not null;default:'basic'"`
	LBUserName    string       `gorm:"not null"`
	Servers       []Server     `gorm:"foreignKey:ManagerID"`
	Downloaders   []Downloader `gorm:"many2many:user_downloaders"`
	Playlists     []Playlist   `gorm:"foreignKey:UserID"`
	DiscoveryMode string       `gorm:"not null;default:'weekly'"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     gorm.DeletedAt `gorm:"index"`
}
type ServerType string

const (
	ServerPlex      ServerType = "plex"
	ServerSubsonic  ServerType = "subsonic"
	ServerNavidrome ServerType = "navidrome"
	ServerJellyfin  ServerType = "jellyfin"
	ServerCustom    ServerType = "custom"
)

type Server struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"not null"`

	Type           ServerType `gorm:"not null"`
	URL            string     `gorm:"not null"`
	LibraryName    string     `gorm:"not null"`
	Sleep          int
	PublicPlaylist bool

	ManagerID uint
	Manager   User

	AdminCredentialID *uint       `gorm:"default:null"`
	AdminCredential   *Credential `gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`

	Downloaders []Downloader `gorm:"many2many:server_downloaders"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

type PlaylistKind string

const (
	PlaylistKindDefault PlaylistKind = "default"
	PlaylistKindCustom  PlaylistKind = "custom"
)

type Playlist struct {
	ID          uint   `gorm:"primaryKey"`
	Name        string `gorm:"not null;index:idx_playlist_user_name,unique"`
	DisplayName string

	UserID uint `gorm:"not null;index:idx_playlist_user_name,unique"`
	User   *User

	Kind      PlaylistKind `gorm:"not null;default:'default'"`
	Source    string
	SourceURL string
	LBMBID    string
	EnvPrefix string
	Schedule  string
	Flags     string
	Enabled   bool `gorm:"not null;default:true"`

	RefreshDays     int
	ColorIndex      int
	ArtworkURL      string
	ArtworkUploaded bool
	LastFetched     *time.Time
	LastRunAt       *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

type CredentialType string

const (
	CredentialAPIKey   CredentialType = "api_key"
	CredentialUserPass CredentialType = "userpass"
)

type Credential struct {
	ID   uint `gorm:"primaryKey"`
	Name string
	Type CredentialType `gorm:"not null"`

	APIKey   string
	Username string
	Password string

	CreatedAt time.Time
	UpdatedAt time.Time
}

type DownloaderType string

const (
	DownloaderYoutube      DownloaderType = "youtube"
	DownloaderYoutubeMusic DownloaderType = "youtube_music"
	DownloaderSlskd        DownloaderType = "slskd"
)

type PlaylistNameFormat string

const (
	Week PlaylistNameFormat = "week"
	Date PlaylistNameFormat = "date"
)

type Downloader struct {
	ID uint `gorm:"primaryKey"`

	Name string `gorm:"not null"`

	Type DownloaderType `gorm:"not null"`

	UserID uint
	User   *User

	// Core downloader routing/path settings shared by every backend.
	DownloadDir string `default:"/data/"`
	PlaylistDir string
	RenameTrack bool

	YoutubeDownloaderID *uint
	YoutubeDownloader   *YoutubeDownloader `gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`
	SlskdDownloaderID   *uint
	SlskdDownloader     *SlskdDownloader `gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

type YoutubeDownloader struct {
	ID uint `gorm:"primaryKey"`

	Name string `gorm:"not null"`

	APIKey         string
	TrackExtension string
	FilterList     string

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

type SlskdDownloader struct {
	ID uint `gorm:"primaryKey"`

	Name string `gorm:"not null"`

	URL              string
	APIKey           string
	SlskdDir         string
	Retry            int
	DownloadAttempts int
	Extensions       string
	FilterList       string

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

type UserDownloader struct {
	UserID       uint `gorm:"primaryKey"`
	DownloaderID uint `gorm:"primaryKey"`
}
type ServerDownloader struct {
	ID           uint `gorm:"primaryKey"`
	ServerID     uint `gorm:"not null;uniqueIndex:idx_server_downloader"`
	DownloaderID uint `gorm:"not null;uniqueIndex:idx_server_downloader"`
	Priority     int  `gorm:"not null;default:0"`
}

type UserServerCredential struct {
	ID uint `gorm:"primaryKey"`

	UserID   uint
	ServerID uint

	CredentialID uint

	CreatedAt time.Time
	UpdatedAt time.Time
}

func AutoMigrate(db *gorm.DB) error {
	hadAppDownloaderOptions := db.Migrator().HasColumn(&AppSettings{}, "single_artist")
	hadAbstractDownloaderTables := db.Migrator().HasTable(&YoutubeDownloader{}) || db.Migrator().HasTable(&SlskdDownloader{})

	if err := migrateServerDownloadersPrimaryKey(db); err != nil {
		return err
	}

	if db.Migrator().HasIndex(&PlaylistSchedule{}, "idx_playlist_schedules_name") {
		if err := db.Migrator().DropIndex(&PlaylistSchedule{}, "idx_playlist_schedules_name"); err != nil {
			return err
		}
	}

	if err := db.AutoMigrate(
		&User{},
		&Server{},
		&Credential{},
		&UserServerCredential{},
		&YoutubeDownloader{},
		&SlskdDownloader{},
		&Downloader{},
		&UserDownloader{},
		&ServerDownloader{},
		&AppSettings{},
		&PathTemplatePreset{},
		&Playlist{},
		&PlaylistSchedule{},
	); err != nil {
		return err
	}

	if err := migratePlaylistSchedulesToPlaylists(db); err != nil {
		return err
	}
	if !hadAppDownloaderOptions {
		if err := migrateDownloaderOptionsToAppSettings(db); err != nil {
			return err
		}
	}
	if !hadAbstractDownloaderTables {
		if err := migrateDownloaderServiceTables(db); err != nil {
			return err
		}
	}

	needsMigration, err := needsNullableAdminCredentialMigration(db)
	if err != nil {
		return err
	}
	if needsMigration {
		if err := db.Migrator().AlterColumn(&Server{}, "AdminCredentialID"); err != nil {
			return err
		}
	}

	return db.Model(&Server{}).Where("admin_credential_id = ?", 0).Update("admin_credential_id", nil).Error
}

func migrateServerDownloadersPrimaryKey(db *gorm.DB) error {
	if !db.Migrator().HasTable("server_downloaders") || hasTableColumn(db, "server_downloaders", "id") {
		return nil
	}
	priorityExpr := "0"
	if hasTableColumn(db, "server_downloaders", "priority") {
		priorityExpr = "COALESCE(priority, 0)"
	}
	statements := []string{
		"CREATE TABLE server_downloaders_new (id integer PRIMARY KEY AUTOINCREMENT, server_id integer NOT NULL, downloader_id integer NOT NULL, priority integer NOT NULL DEFAULT 0)",
		"INSERT INTO server_downloaders_new (server_id, downloader_id, priority) SELECT server_id, downloader_id, " + priorityExpr + " FROM server_downloaders",
		"DROP TABLE server_downloaders",
		"ALTER TABLE server_downloaders_new RENAME TO server_downloaders",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_server_downloader ON server_downloaders(server_id, downloader_id)",
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateDownloaderOptionsToAppSettings(db *gorm.DB) error {
	if !hasTableColumn(db, "downloaders", "single_artist") {
		return nil
	}

	var dl struct {
		SingleArtist      bool
		KeepPermissions   bool
		MigrateDownloads  bool
		OverwriteMetadata bool
		UseSubdirectory   bool
		PathTemplating    string
		PlaylistName      string
	}
	selects := []string{"single_artist", "keep_permissions", "migrate_downloads", "overwrite_metadata", "use_subdirectory"}
	if hasTableColumn(db, "downloaders", "path_templating") {
		selects = append(selects, "path_templating")
	}
	if hasTableColumn(db, "downloaders", "playlist_name") {
		selects = append(selects, "playlist_name")
	}
	if err := db.Table("downloaders").Select(strings.Join(selects, ", ")).Order("id").First(&dl).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}

	settings := DefaultAppSettings()
	if err := db.First(&settings).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := db.Create(&settings).Error; err != nil {
			return err
		}
	}

	settings.SingleArtist = dl.SingleArtist
	settings.KeepPermissions = dl.KeepPermissions
	settings.MigrateDownloads = dl.MigrateDownloads
	settings.OverwriteMetadata = dl.OverwriteMetadata
	settings.UseSubdirectory = dl.UseSubdirectory
	settings.PathTemplate = dl.PathTemplating
	if strings.TrimSpace(dl.PlaylistName) != "" {
		settings.PlaylistNameFormat = dl.PlaylistName
	}
	return db.Save(&settings).Error
}

func migrateDownloaderServiceTables(db *gorm.DB) error {
	if !hasTableColumn(db, "downloaders", "you_tube_api_key") && !hasTableColumn(db, "downloaders", "slskd_url") {
		return nil
	}

	type legacyDownloader struct {
		ID              uint
		Name            string
		Type            DownloaderType
		YouTubeAPIKey   string
		TrackExtension  string
		SlskdURL        string
		SlskdAPIKey     string
		SlskdDir        string
		SlskdRetry      int
		SlskdDLAttempts int
		Extensions      string
		FilterList      string
	}
	var rows []legacyDownloader
	if err := db.Table("downloaders").Select("id, name, type, you_tube_api_key, track_extension, slskd_url, slskd_api_key, slskd_dir, slskd_retry, slskd_dl_attempts, extensions, filter_list").Order("id").Scan(&rows).Error; err != nil {
		return err
	}
	for _, legacy := range rows {
		updates := map[string]any{}
		if legacy.Type == DownloaderYoutube || legacy.Type == DownloaderYoutubeMusic {
			yt := YoutubeDownloader{Name: firstNonEmptyString(legacy.Name, string(legacy.Type)), APIKey: legacy.YouTubeAPIKey, TrackExtension: legacy.TrackExtension, FilterList: legacy.FilterList}
			if err := db.Create(&yt).Error; err != nil {
				return err
			}
			updates["youtube_downloader_id"] = yt.ID
		}
		if legacy.Type == DownloaderSlskd {
			slskd := SlskdDownloader{Name: firstNonEmptyString(legacy.Name, string(legacy.Type)), URL: legacy.SlskdURL, APIKey: legacy.SlskdAPIKey, SlskdDir: legacy.SlskdDir, Retry: legacy.SlskdRetry, DownloadAttempts: legacy.SlskdDLAttempts, Extensions: legacy.Extensions, FilterList: legacy.FilterList}
			if err := db.Create(&slskd).Error; err != nil {
				return err
			}
			updates["slskd_downloader_id"] = slskd.ID
		}
		if len(updates) > 0 {
			if err := db.Model(&Downloader{}).Where("id = ?", legacy.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func hasTableColumn(db *gorm.DB, table, column string) bool {
	columns, err := db.Migrator().ColumnTypes(table)
	if err != nil {
		return false
	}
	for _, col := range columns {
		if col.Name() == column {
			return true
		}
	}
	return false
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func needsNullableAdminCredentialMigration(db *gorm.DB) (bool, error) {
	columns, err := db.Migrator().ColumnTypes(&Server{})
	if err != nil {
		return false, err
	}
	for _, column := range columns {
		if column.Name() != "admin_credential_id" {
			continue
		}
		if nullable, ok := column.Nullable(); ok && !nullable {
			return true, nil
		}
		if defaultValue, ok := column.DefaultValue(); ok && defaultValue == "0" {
			return true, nil
		}
		return false, nil
	}
	return false, nil
}

func migratePlaylistSchedulesToPlaylists(db *gorm.DB) error {
	var schedules []PlaylistSchedule
	if err := db.Find(&schedules).Error; err != nil {
		return err
	}
	for _, schedule := range schedules {
		if schedule.UserID == nil || schedule.Name == "" {
			continue
		}

		var user User
		_ = db.First(&user, *schedule.UserID).Error
		dbName := schedule.Name
		if !strings.HasPrefix(schedule.Name, "custom-") && strings.TrimSpace(user.LBUserName) != "" {
			dbName = schedule.Name + "-" + playlistNameSlug(user.LBUserName)
		}

		var playlist Playlist
		err := db.Unscoped().Where(
			"user_id = ? AND (name IN ? OR env_prefix = ?)",
			*schedule.UserID,
			[]string{schedule.Name, dbName},
			schedule.EnvPrefix,
		).First(&playlist).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			playlist = Playlist{UserID: *schedule.UserID, Name: dbName, DisplayName: schedule.Name}
		}

		playlist.Name = dbName
		if playlist.DisplayName == "" {
			playlist.DisplayName = schedule.Name
		}
		playlist.Kind = PlaylistKindDefault
		if strings.HasPrefix(schedule.Name, "custom-") {
			playlist.Kind = PlaylistKindCustom
		}
		playlist.EnvPrefix = schedule.EnvPrefix
		playlist.Schedule = schedule.Schedule
		playlist.Flags = schedule.Flags
		playlist.Enabled = schedule.Flags != ""
		playlist.LastRunAt = schedule.LastRunAt
		playlist.DeletedAt = gorm.DeletedAt{}
		if playlist.ID == 0 {
			if err := db.Create(&playlist).Error; err != nil {
				return err
			}
		} else if err := db.Unscoped().Save(&playlist).Error; err != nil {
			return err
		}
	}
	return nil
}

var playlistSlugUnsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func playlistNameSlug(raw string) string {
	return strings.Trim(playlistSlugUnsafeChars.ReplaceAllString(strings.TrimSpace(raw), "-"), "-._")
}
