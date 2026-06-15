package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"explo/src/config"
	"explo/src/models"
	"explo/src/web"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Option is a value/label pair for select-type fields.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Condition expresses a dependency on another field's value.
// All non-zero properties are ANDed together.
type Condition struct {
	Field    string   `json:"field"`
	Eq       string   `json:"eq,omitempty"`       // field === value
	In       []string `json:"in,omitempty"`       // field is one of values
	Contains string   `json:"contains,omitempty"` // value appears in field's comma-separated list
}

// ConfigResponse is returned by GET /api/config.
type ConfigResponse struct {
	Values  map[string]string `json:"values"`
	Sources map[string]string `json:"sources"` // "db" | "env"  | "file"
}

// runEvent is an SSE event sent to connected browser clients.
type runEvent struct {
	typ  string
	data string
}

// RunStatus is returned by GET /api/run/status.
type RunStatus struct {
	Running  bool `json:"running"`
	ExitCode *int `json:"exit_code,omitempty"`
}

type manualRunState struct {
	mu          sync.Mutex
	running     bool
	cancel      context.CancelFunc
	exitCode    *int
	logs        []string
	subscribers map[chan runEvent]struct{}
}

func newManualRunState() manualRunState {
	return manualRunState{subscribers: make(map[chan runEvent]struct{})}
}

type Server struct {
	cfg            config.ServerConfig
	db             *gorm.DB
	mux            *http.ServeMux
	server         *http.Server
	authStore      *AuthStore
	cronJobs       *Jobs
	sessionManager *SessionManager
	manualRun      manualRunState
}

func envBool(v string) bool {
	return v == "true" || v == "1" || v == "yes"
}
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) bootstrapFromEnvIfNeeded() error {
	var count int64
	if err := s.db.Model(&models.User{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	slog.Info("no users found → bootstrapping DB from stored config")

	env, err := s.getAllConfigValues()
	if err != nil {
		return fmt.Errorf("failed to read config from database: %w", err)
	}

	get := func(key string) string {
		return env[key]
	}

	username := firstNonEmpty(os.Getenv("UI_USERNAME"), get("UI_USERNAME"), "admin")
	password := firstNonEmpty(os.Getenv("UI_PASSWORD"), get("UI_PASSWORD"), "admin")

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	return s.db.Transaction(func(tx *gorm.DB) error {

		// Load the current user from .env
		user := models.User{
			Username:      username,
			Password:      string(passwordHash),
			Role:          models.RoleManager,
			LBUserName:    get("LISTENBRAINZ_USER"),
			DiscoveryMode: firstNonEmpty(get("LISTENBRAINZ_DISCOVERY"), "playlist"),
		}

		if err := tx.Create(&user).Error; err != nil {
			return err
		}

		// Load the system creds from .env

		systemCreds := models.Credential{
			Name:     "default system",
			Type:     models.CredentialUserPass,
			APIKey:   get("API_KEY"),
			Username: get("SYSTEM_USERNAME"),
			Password: get("SYSTEM_PASSWORD"),
		}

		if err := tx.Create(&systemCreds).Error; err != nil {
			return err
		}

		// Load the admin creds from .env

		adminCreds := models.Credential{
			Name:     "default admin",
			Type:     models.CredentialAPIKey,
			APIKey:   get("ADMIN_API_KEY"),
			Username: get("ADMIN_SYSTEM_USERNAME"),
			Password: get("ADMIN_SYSTEM_PASSWORD"),
		}

		if err := tx.Create(&adminCreds).Error; err != nil {
			return err
		}

		// Load the media server settings from .env
		server := models.Server{
			Name:              firstNonEmpty(get("SERVER_NAME"), "default"),
			LibraryName:       get("LIBRARY_NAME"),
			URL:               get("SYSTEM_URL"),
			Type:              models.ServerType(get("EXPLO_SYSTEM")),
			ManagerID:         user.ID,
			AdminCredentialID: &adminCreds.ID,
		}

		if err := tx.Create(&server).Error; err != nil {
			return err
		}

		// Create the Join Table from User Creds and Server
		if err := tx.Create(&models.UserServerCredential{
			UserID:       user.ID,
			ServerID:     server.ID,
			CredentialID: systemCreds.ID,
		}).Error; err != nil {
			return err
		}

		// Downloader config from env
		services := splitServices(firstNonEmpty(get("DOWNLOAD_SERVICES"), "youtube"))
		for idx, service := range services {
			dtype := downloaderType(service)
			dl := models.Downloader{
				UserID:      user.ID,
				Name:        string(dtype),
				Type:        dtype,
				DownloadDir: get("DOWNLOAD_DIR"),
				PlaylistDir: get("PLAYLIST_DIR"),
				RenameTrack: envBool(get("RENAME_TRACK")),
			}
			if dtype == models.DownloaderYoutube || dtype == models.DownloaderYoutubeMusic {
				yt := models.YoutubeDownloader{
					Name:           string(dtype),
					APIKey:         get("YOUTUBE_API_KEY"),
					TrackExtension: get("TRACK_EXTENSION"),
					FilterList:     get("FILTER_LIST"),
				}
				if err := tx.Create(&yt).Error; err != nil {
					return err
				}
				dl.YoutubeDownloaderID = &yt.ID
			}
			if dtype == models.DownloaderSlskd {
				slskd := models.SlskdDownloader{
					Name:       string(dtype),
					URL:        get("SLSKD_URL"),
					APIKey:     get("SLSKD_API_KEY"),
					SlskdDir:   get("SLSKD_DIR"),
					Extensions: get("EXTENSIONS"),
					FilterList: get("FILTER_LIST"),
				}
				if err := tx.Create(&slskd).Error; err != nil {
					return err
				}
				dl.SlskdDownloaderID = &slskd.ID
			}
			if err := tx.Create(&dl).Error; err != nil {
				return err
			}
			if err := tx.Create(&models.UserDownloader{UserID: user.ID, DownloaderID: dl.ID}).Error; err != nil {
				return err
			}
			if err := tx.Create(&models.ServerDownloader{ServerID: server.ID, DownloaderID: dl.ID, Priority: idx}).Error; err != nil {
				return err
			}
		}

		// Load in app settings from .c
		settings := models.DefaultAppSettings()
		settings.SelectedServerID = &server.ID
		settings.ListenBrainzDiscovery = firstNonEmpty(get("LISTENBRAINZ_DISCOVERY"), "playlist")
		settings.DownloadServices = firstNonEmpty(get("DOWNLOAD_SERVICES"), "youtube")
		settings.PathTemplate = get("PATH_TEMPLATE")
		settings.PlaylistNameFormat = firstNonEmpty(get("PLAYLISTNAME_FORMAT"), "week")
		settings.SingleArtist = envBool(firstNonEmpty(get("SINGLE_ARTIST"), "true"))
		settings.KeepPermissions = envBool(firstNonEmpty(get("KEEP_PERMISSIONS"), "true"))
		settings.MigrateDownloads = envBool(get("MIGRATE_DOWNLOADS"))
		settings.EnrichTrackMetadata = envBool(get("ENRICH_TRACK_METADATA"))
		settings.OverwriteMetadata = envBool(get("OVERWRITE_METADATA"))
		settings.UseSubdirectory = envBool(firstNonEmpty(get("USE_SUBDIRECTORY"), "true"))
		if err := tx.Create(&settings).Error; err != nil {
			return err
		}

		slog.Info("bootstrap complete",
			"user", user.Username,
			"server", server.URL,
		)

		return nil
	})
}

func NewServer(cfg config.ServerConfig, db *gorm.DB) *Server {
	sessionManager := NewSessionManager(
		NewInMemorySessionStore(),
		1*time.Hour,
		7*(24*time.Hour),
		"session",
	)

	authStore := NewAuthStore(db, sessionManager)

	cronJobs := NewJobs()

	mux := http.NewServeMux()

	s := &Server{
		cfg: cfg,
		db:  db,
		mux: mux,
		server: &http.Server{
			Addr:    cfg.Port,
			Handler: sessionManager.Handle(mux),
		},
		authStore:      authStore,
		cronJobs:       cronJobs,
		sessionManager: sessionManager,
		manualRun:      newManualRunState(),
	}

	s.registerRoutes()
	return s
}

func (s *Server) Start() error {
	s.initServerLog()
	if err := s.importEnvFileIfConfigEmpty(); err != nil {
		return err
	}
	if err := s.bootstrapFromEnvIfNeeded(); err != nil {
		return err
	}
	if err := s.migrateLegacyCustomPlaylistsToDB(); err != nil {
		slog.Warn("failed to migrate legacy custom playlists", "err", err.Error())
	}
	s.startJobs()
	coversDir := filepath.Join(s.cfg.WebDataDir, "cache", "covers")
	if _, err := os.Stat(coversDir); os.IsNotExist(err) {
		s.PrefetchCovers()
	}
	slog.Info("Explo web UI started", "addr", s.server.Addr)
	go checkForUpdate()
	return s.server.ListenAndServe()
}

func checkForUpdate() {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/LumePart/Explo/releases/latest")
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return
	}
	l := parseVer(release.TagName)
	c := parseVer(config.Version)
	newer := false
	for i := range 3 {
		if l[i] > c[i] {
			newer = true
			break
		}
		if l[i] < c[i] {
			break
		}
	}
	if newer {
		slog.Info("new version available!", "latest", release.TagName, "current", config.Version)
	}
}

func parseVer(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	var out [3]int
	for i, p := range parts {
		if i >= 3 {
			break
		}
		_, _ = fmt.Sscanf(p, "%d", &out[i])
	}
	return out
}

// Jobs to register on startup
func (s *Server) startJobs() {

	coversDir := filepath.Join(s.cfg.WebDataDir, "cache", "covers")
	if err := s.cronJobs.RegisterCoverCleanup(
		"0 3 * * *", coversDir, s.cfg.CacheSizeMB<<20); err != nil {
		slog.Warn("failed to register cover cleanup job", "err", err.Error())
	}

	if err := s.cronJobs.RegisterPlaylistPoller(s); err != nil {
		slog.Warn("failed to register playlist poller", "err", err.Error())
	}

	s.cronJobs.Start()
}

func (s *Server) PrefetchCovers() {

	coversDir := filepath.Join(s.cfg.WebDataDir, "cache", "covers")

	url := randomLocalCoverHiRes(coversDir)
	if url == "" {
		fetchSitewideCovers(coversDir)
	}
}

// spaFS returns the filesystem to serve the frontend from.
// When WEB_DEV=true, serves directly from src/web/dist on disk so that
// running "npm run build" reflects changes without recompiling the binary.
func spaFS() (fs.FS, []byte) {
	if os.Getenv("WEB_DEV") == "true" {
		diskFS := os.DirFS("src/web/dist")
		index, _ := fs.ReadFile(diskFS, "index.html")
		return diskFS, index
	}
	embedded, _ := fs.Sub(web.DistFiles, "dist")
	index, _ := fs.ReadFile(embedded, "index.html")
	return embedded, index
}

func (s *Server) registerRoutes() {
	distFS, indexHTML := spaFS()
	fileServer := http.FileServer(http.FS(distFS))

	// SPA fallback: serve static assets when they exist, otherwise serve index.html.
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if _, err := fs.Stat(distFS, path); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write(indexHTML); err != nil {
			slog.Error("failed writing to http", "msg", err.Error())
		}
	})

	// /api/ui/config — GET = read from DB, POST = save to DB (both require auth)
	s.mux.HandleFunc("/api/ui/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.authStore.RequireAuth(http.HandlerFunc(s.handleGetConfig)).ServeHTTP(w, r)
		case http.MethodPost:
			s.authStore.RequireAuth(http.HandlerFunc(s.handleSaveConfig)).ServeHTTP(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	s.mux.Handle("/api/ui/config/raw", s.authStore.RequireAuth(http.HandlerFunc(s.handleGetConfigRaw)))
	s.mux.Handle("/api/ui/config/reset", s.authStore.RequireAuth(http.HandlerFunc(s.handleResetConfig)))
	s.mux.Handle("/api/ui/config/schedules", s.authStore.RequireAuth(http.HandlerFunc(s.handleSaveSchedule)))
	s.mux.Handle("/api/ui/config/path-template", s.authStore.RequireAuth(http.HandlerFunc(s.handleSavePathTemplate)))
	s.mux.Handle("/api/ui/config/enrich-metadata", s.authStore.RequireAuth(http.HandlerFunc(s.handleSaveEnrichMetadata)))
	s.mux.HandleFunc("/api/ui/path-templates", func(w http.ResponseWriter, r *http.Request) {
		s.authStore.RequireAuth(http.HandlerFunc(s.handlePathTemplates)).ServeHTTP(w, r)
	})
	s.mux.HandleFunc("/api/ui/path-templates/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			s.authStore.RequireAuth(http.HandlerFunc(s.handleDeletePathTemplate)).ServeHTTP(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	// Wizard steps (POST) — require auth
	s.mux.Handle("/api/ui/wizard/step1", s.authStore.RequireAuth(http.HandlerFunc(s.handleWizardStep1)))
	s.mux.Handle("/api/ui/wizard/step2", s.authStore.RequireAuth(http.HandlerFunc(s.handleWizardStep2)))
	s.mux.Handle("/api/ui/wizard/step3", s.authStore.RequireAuth(http.HandlerFunc(s.handleWizardStep3)))

	s.mux.Handle("/api/ui/browse", s.authStore.RequireAuth(http.HandlerFunc(s.handleBrowse)))
	s.mux.Handle("/api/ui/servers", s.authStore.RequireAuth(http.HandlerFunc(s.handleGetServers)))
	s.mux.Handle("/api/ui/run", s.authStore.RequireAuth(http.HandlerFunc(s.handleRun)))
	s.mux.Handle("/api/ui/run/events", s.authStore.RequireAuth(http.HandlerFunc(s.handleRunEvents)))
	s.mux.Handle("/api/ui/run/stop", s.authStore.RequireAuth(http.HandlerFunc(s.handleStopRun)))
	s.mux.Handle("/api/ui/run/status", s.authStore.RequireAuth(http.HandlerFunc(s.handleRunStatus)))

	s.mux.Handle("/api/ui/admin/", s.authStore.RequireManager(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/ui/admin/"), "/")
		if strings.Count(path, "/") == 0 {
			s.handleAdminCollection(w, r)
			return
		}
		s.handleAdminItem(w, r)
	})))

	s.mux.Handle("/api/ui/logs", s.authStore.RequireAuth(http.HandlerFunc(s.handleGetLog)))
	s.mux.Handle("/api/ui/playlists", s.authStore.RequireAuth(http.HandlerFunc(s.handleGetPlaylist)))
	s.mux.Handle("/api/ui/playlists/prefetch", s.authStore.RequireAuth(http.HandlerFunc(s.handlePrefetchCovers)))

	// TODO: Uncomment when jeffs branch is in
	// custom playlists: GET list, POST import (same path); per-ID actions under prefix
	s.mux.HandleFunc("/api/ui/custom-playlists", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.authStore.RequireAuth(http.HandlerFunc(s.handleGetCustomPlaylists)).ServeHTTP(w, r)
		case http.MethodPost:
			s.authStore.RequireAuth(http.HandlerFunc(s.handleImportCustomPlaylist)).ServeHTTP(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	// ID-specific routes: DELETE /api/ui/custom-playlists/{id} and POST .../{id}/refresh
	s.mux.HandleFunc("/api/ui/custom-playlists/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.authStore.RequireAuth(http.HandlerFunc(s.handleRefreshCustomPlaylist)).ServeHTTP(w, r)
	})
	s.mux.HandleFunc("/api/ui/custom-playlists/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.authStore.RequireAuth(http.HandlerFunc(s.handleDeleteCustomPlaylist)).ServeHTTP(w, r)
	})

	s.mux.Handle("/api/ui/logout", s.authStore.RequireAuth(http.HandlerFunc(s.handleLogout)))

	// public/special routes
	s.mux.HandleFunc("/api/ui/csrf", s.csrfHandler)
	s.mux.HandleFunc("/api/ui/login", s.handleLogin)
	s.mux.HandleFunc("/api/ui/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("/api/ui/background-art", s.handleBackgroundArt)
	s.mux.HandleFunc("/api/ui/setup-status", s.handleSetupStatus)

	coversDir := filepath.Join(s.cfg.WebDataDir, "cache", "covers")
	s.mux.Handle("/api/covers/", http.StripPrefix("/api/covers/", http.FileServer(http.Dir(coversDir))))
}

// ── Logging ────────────────────────────────────────────────────────────────

// logPath returns the path to the single rolling log file.
func (s *Server) logPath() string {
	return filepath.Join(s.cfg.WebDataDir, "logs", "explo.log")
}

// initServerLog redirects the default slog handler so all server log output
// goes to both stderr and the rolling log file.
func (s *Server) initServerLog() {
	lf, err := s.openRunLog()
	if err != nil {
		return
	}
	w := io.MultiWriter(os.Stderr, lf)
	slog.SetDefault(slog.New(slog.NewTextHandler(w, nil)))
}

// openRunLog opens the single rolling log file in append mode.
func (s *Server) openRunLog() (*os.File, error) {
	p := s.logPath()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return nil, err
	}
	return os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
}

// handleSetupStatus returns {"wizard_complete": bool} for first time setups. Public — no auth required.
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	values, err := s.getConfigValues([]string{"WIZARD_COMPLETE"})
	wizardComplete := err == nil && values["WIZARD_COMPLETE"] == "true"
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]bool{"wizard_complete": wizardComplete}); err != nil {
		slog.Error("failed encoding setup status", "err", err.Error())
	}
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionManager.GetSession(r)
	auth, _ := sess.Get("authenticated").(bool)
	if !auth {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	var user models.User
	err := s.db.Where("username = ?", username).First(&user).Error
	if err != nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)) != nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	sess := s.sessionManager.GetSession(r)
	sess.Put("authenticated", true)
	sess.Put("user_id", user.ID)
	sess.Put("role", user.Role)
	sess.Put("username", user.Username)

	slog.Info("successful login", "user", username)

	w.WriteHeader(http.StatusOK)
}
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionManager.GetSession(r)
	sess.Delete("authenticated")
	sess.Delete("username")
	w.WriteHeader(http.StatusOK)
}

// handleGetLog returns the contents of the rolling log file.
func (s *Server) handleGetLog(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.logPath())
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "failed to read log", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := w.Write(data); err != nil {
		slog.Error("failed writing http response", "msg", err.Error())
	}
}

func (s *Server) csrfHandler(w http.ResponseWriter, r *http.Request) {
	session := s.sessionManager.GetSession(r)

	token, _ := session.Get("csrf_token").(string)

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]string{
		"csrf_token": token,
	}); err != nil {
		slog.Error("failed encoding token to http", "msg", err.Error())
	}
}

// ── Config ─────────────────────────────────────────────────────────────────

// parseEnvText parses key=value lines, ignoring comments, blanks and unquotes variables
// .
func parseEnvText(text string) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		if k = strings.TrimSpace(k); k != "" {
			v = strings.TrimSpace(v)

			// unquote if quoted
			if len(v) >= 2 {
				if (v[0] == '\'' && v[len(v)-1] == '\'') ||
					(v[0] == '"' && v[len(v)-1] == '"') {
					v = v[1 : len(v)-1]
				}
			}
			out[k] = v
		}
	}
	return out
}

// handleGetConfig returns resolved config as JSON: { values, sources }.
// Database values take precedence; process environment variables are the fallback.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	dbValues, err := s.getConfigValues(allConfigKeys)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	values := make(map[string]string, len(allConfigKeys))
	sources := make(map[string]string, len(allConfigKeys))
	for _, key := range allConfigKeys {
		if v, ok := dbValues[key]; ok && v != "" {
			values[key] = v
			sources[key] = "db"
		} else if v, ok := os.LookupEnv(key); ok && v != "" {
			values[key] = v
			sources[key] = "env"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(ConfigResponse{Values: values, Sources: sources}); err != nil {
		slog.Error("failed encoding config to http", "msg", err.Error())
	}
}

// handleGetConfigRaw returns database config rendered in .env format for the raw editor.
// Might need to remove this as it kind of adds more confusion.
func (s *Server) handleGetConfigRaw(w http.ResponseWriter, r *http.Request) {
	values, err := s.getAllConfigValues()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := w.Write([]byte(configValuesToEnvText(values))); err != nil {
		slog.Error("failed writing http response", "msg", err.Error())
	}
}

// handleSaveConfig replaces database config with the posted .env-format body.
// Might need to remove this as it kind of adds more confusion.
func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.replaceConfigValues(parseEnvText(string(data))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleSavePathTemplate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Template string `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.setConfigValues(map[string]string{"PATH_TEMPLATE": body.Template}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleSaveEnrichMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	val := "false"
	if body.Enabled {
		val = "true"
	}
	if err := s.setConfigValues(map[string]string{"ENRICH_TRACK_METADATA": val}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}


// handleResetConfig resets all settings and restarts the container.
func (s *Server) handleResetConfig(w http.ResponseWriter, r *http.Request) {
	if err := s.resetConfigValues(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := syscall.Kill(1, syscall.SIGTERM); err != nil {
			slog.Warn("failed to kill process", "msg", err.Error())
		}

	}()
}

// handleSaveSchedule updates a single playlist's schedule in the database.
func (s *Server) handleSaveSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Day     int    `json:"day"` // 0=Sun…6=Sat, -1=every day
		Hour    int    `json:"hour"`
		Minute  int    `json:"minute"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var envPrefix string
	var defaultFlags string

	if def, ok := playlistDefs[body.Name]; ok {
		envPrefix = def.EnvPrefix
		defaultFlags = def.DefaultFlags
	} else if customIDRe.MatchString(body.Name) {
		envPrefix = customEnvPrefix(body.Name)
		defaultFlags = "--playlist " + body.Name
	} else {
		http.Error(w, "unknown playlist name", http.StatusBadRequest)
		return
	}

	updates := map[string]string{}
	if !body.Enabled {
		// Toggle off — truly disable, regardless of day value carried over from state
		updates[envPrefix+"_SCHEDULE"] = ""
		updates[envPrefix+"_FLAGS"] = ""
	} else if body.Day == -2 {
		// "Never" — keep playlist active for manual runs but remove auto-schedule
		updates[envPrefix+"_SCHEDULE"] = ""
		updates[envPrefix+"_FLAGS"] = defaultFlags
	} else {
		dom := "*"
		dow := "*"
		if body.Day == 100 {
			dom = "1"
		} else if body.Day >= 0 {
			dow = fmt.Sprintf("%d", body.Day)
		}
		updates[envPrefix+"_SCHEDULE"] = fmt.Sprintf("%d %d %s * %s", body.Minute, body.Hour, dom, dow)
		updates[envPrefix+"_FLAGS"] = defaultFlags
	}

	userID, err := resolveUserID(w, r)
	if err != nil {
		return
	}
	if err := s.setConfigValuesForUser(userID, updates); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func resolveUserID(w http.ResponseWriter, r *http.Request) (uint, error) {
	sess, ok := r.Context().Value(sessionContextKey{}).(*Session)
	if !ok || sess == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return 0, errors.New("session missing from request context")
	}

	uid := sess.Get("user_id")
	var userID uint
	switch v := uid.(type) {
	case uint:
		userID = v
	case int:
		userID = uint(v)
	case int64:
		userID = uint(v)
	case float64:
		userID = uint(v)
	default:
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return 0, errors.New("invalid user_id type in session")
	}
	if userID == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return 0, errors.New("user_id missing from session")
	}
	return userID, nil
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username      string   `json:"username"`
		Password      string   `json:"password"`
		LBUser        string   `json:"user"`
		Playlists     []string `json:"playlists"`
		DiscoveryMode string   `json:"discovery_mode"`
		Servers       []string `json:"servers"`
		Downloaders   []string `json:"downloaders"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.LBUser == "" {
		http.Error(w, "user is required", http.StatusBadRequest)
		return
	}

	enabled := make(map[string]bool, len(body.Playlists))
	for _, p := range body.Playlists {
		enabled[p] = true
	}

	updates := map[string]string{
		"LISTENBRAINZ_USER":      body.LBUser,
		"LISTENBRAINZ_DISCOVERY": body.DiscoveryMode,
	}
	for name, def := range playlistDefs {
		if enabled[name] {
			updates[def.EnvPrefix+"_SCHEDULE"] = def.DefaultSchedule
			updates[def.EnvPrefix+"_FLAGS"] = def.DefaultFlags
		} else {
			updates[def.EnvPrefix+"_SCHEDULE"] = ""
			updates[def.EnvPrefix+"_FLAGS"] = ""
		}
	}

	userID, err := resolveUserID(w, r)
	if err != nil {
		return
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.First(&user, userID).Error; err != nil {
			return err
		}
		if body.LBUser != "" {
			user.LBUserName = body.LBUser
		}
		if body.DiscoveryMode != "" {
			user.DiscoveryMode = body.DiscoveryMode
		}
		if err := tx.Save(&user).Error; err != nil {
			return err
		}
		return s.applyConfigValuesTxForUser(tx, user.ID, updates)
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ── Wizard ─────────────────────────────────────────────────────────────────

// handleWizardStep1 saves discovery settings (username + enabled playlists with default schedules).
func (s *Server) handleWizardStep1(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username      string   `json:"username"`
		Password      string   `json:"password"`
		LBUser        string   `json:"user"`
		Playlists     []string `json:"playlists"`
		DiscoveryMode string   `json:"discovery_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.LBUser == "" {
		http.Error(w, "user is required", http.StatusBadRequest)
		return
	}

	// Ensure username
	username := body.Username
	if username == "" {
		username = fmt.Sprintf("user_%d", time.Now().Unix())
	}

	// Hash password (empty password allowed)
	pw := body.Password
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "failed hashing password: "+err.Error(), http.StatusInternalServerError)
		return
	}

	discovery := body.DiscoveryMode
	if discovery == "" {
		discovery = "weekly"
	}

	user := models.User{
		Username:      username,
		Password:      string(hash),
		Role:          models.RoleBasic,
		LBUserName:    body.LBUser,
		DiscoveryMode: discovery,
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		enabled := make(map[string]bool, len(body.Playlists))
		for _, p := range body.Playlists {
			enabled[p] = true
		}
		updates := map[string]string{
			"LISTENBRAINZ_USER":      body.LBUser,
			"LISTENBRAINZ_DISCOVERY": discovery,
		}
		for name, def := range playlistDefs {
			if enabled[name] {
				updates[def.EnvPrefix+"_SCHEDULE"] = def.DefaultSchedule
				updates[def.EnvPrefix+"_FLAGS"] = def.DefaultFlags
			}
		}
		return s.applyConfigValuesTxForUser(tx, user.ID, updates)
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// handleWizardStep2 saves media system configuration.
func (s *Server) handleWizardStep2(w http.ResponseWriter, r *http.Request) {
	var body struct {
		System              string `json:"system"`
		URL                 string `json:"url"`
		APIKey              string `json:"api_key"`
		LibraryName         string `json:"library_name"`
		Username            string `json:"username"`
		Password            string `json:"password"`
		PlaylistDir         string `json:"playlist_dir"`
		Sleep               string `json:"sleep"`
		AdminAPIKey         string `json:"admin_api_key"`
		AdminSystemUsername string `json:"admin_system_username"`
		AdminSystemPassword string `json:"admin_system_password"`
		ServerID            uint   `json:"server_id"`

		PublicPlaylist bool `json:"public_playlist"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if body.ServerID != 0 && body.System == "" {
		if err := s.applySelectedServer(body.ServerID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if body.System == "" {
		http.Error(w, "system is required", http.StatusBadRequest)
		return
	}

	publicPlaylist := ""
	if body.PublicPlaylist {
		publicPlaylist = "true"
	}

	// require authenticated user (wizard routes are protected, but assert user_id)
	if _, err := resolveUserID(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	updates := map[string]string{
		"EXPLO_SYSTEM":    body.System,
		"SYSTEM_URL":      body.URL,
		"LIBRARY_NAME":    body.LibraryName,
		"PLAYLIST_DIR":    body.PlaylistDir,
		"SLEEP":           body.Sleep,
		"PUBLIC_PLAYLIST": publicPlaylist,
	}
	if body.ServerID != 0 {
		updates["SELECTED_SERVER_ID"] = fmt.Sprint(body.ServerID)
	}
	if body.ServerID == 0 || strings.TrimSpace(body.APIKey) != "" || strings.TrimSpace(body.Username) != "" || strings.TrimSpace(body.Password) != "" {
		updates["API_KEY"] = body.APIKey
		updates["SYSTEM_USERNAME"] = body.Username
		updates["SYSTEM_PASSWORD"] = body.Password
	}
	if body.ServerID == 0 || strings.TrimSpace(body.AdminAPIKey) != "" || strings.TrimSpace(body.AdminSystemUsername) != "" || strings.TrimSpace(body.AdminSystemPassword) != "" {
		updates["ADMIN_API_KEY"] = body.AdminAPIKey
		updates["ADMIN_SYSTEM_USERNAME"] = body.AdminSystemUsername
		updates["ADMIN_SYSTEM_PASSWORD"] = body.AdminSystemPassword
	}
	if err := s.setConfigValues(updates); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleWizardStep3 saves downloader configuration.
func (s *Server) handleWizardStep3(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DownloadDir      string   `json:"download_dir"`
		UseSubdirectory  bool     `json:"use_subdirectory"`
		MigrateDownloads bool     `json:"migrate_downloads"`
		DownloadServices []string `json:"download_services"`
		YoutubeAPIKey    string   `json:"youtube_api_key"`
		TrackExtension   string   `json:"track_extension"`
		FilterList       string   `json:"filter_list"`
		SlskdURL         string   `json:"slskd_url"`
		SlskdAPIKey      string   `json:"slskd_api_key"`
		Extensions       string   `json:"extensions"`
		PathTemplate     string   `json:"path_template"`
		PlaylistFormat   string   `json:"playlist_name_format"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.DownloadServices) == 0 {
		http.Error(w, "at least one download service is required", http.StatusBadRequest)
		return
	}

	if _, err := resolveUserID(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	services := strings.Join(body.DownloadServices, ",")
	appUpdates := map[string]string{
		"DOWNLOAD_DIR":        body.DownloadDir,
		"USE_SUBDIRECTORY":    "false",
		"MIGRATE_DOWNLOADS":   "false",
		"DOWNLOAD_SERVICES":   services,
		"PATH_TEMPLATE":       body.PathTemplate,
		"PLAYLISTNAME_FORMAT": firstNonEmpty(body.PlaylistFormat, "week"),
		"YOUTUBE_API_KEY":     body.YoutubeAPIKey,
		"TRACK_EXTENSION":     body.TrackExtension,
		"FILTER_LIST":         body.FilterList,
		"SLSKD_URL":           body.SlskdURL,
		"SLSKD_API_KEY":       body.SlskdAPIKey,
		"EXTENSIONS":          body.Extensions,
		"WIZARD_COMPLETE":     "true",
	}
	if body.UseSubdirectory {
		appUpdates["USE_SUBDIRECTORY"] = "true"
	}
	if body.MigrateDownloads {
		appUpdates["MIGRATE_DOWNLOADS"] = "true"
	}
	if err := s.setConfigValues(appUpdates); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, err := resolveUserID(w, r)
	if err != nil {
		return
	}
	query := s.db.Order("name, id")
	if !requestIsManager(r) {
		query = query.Where("manager_id = ?", userID)
	}
	var rows []models.Server
	if err := query.Find(&rows).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

func requestIsManager(r *http.Request) bool {
	if session, ok := r.Context().Value(sessionContextKey{}).(*Session); ok && session != nil {
		role, _ := session.Get("role").(models.UserRole)
		if role == "" {
			if roleString, ok := session.Get("role").(string); ok {
				role = models.UserRole(roleString)
			}
		}
		return role == models.RoleManager
	}
	return false
}

func (s *Server) applySelectedServer(serverID uint) error {
	if serverID == 0 {
		return nil
	}
	return s.setConfigValues(map[string]string{"SELECTED_SERVER_ID": fmt.Sprint(serverID)})
}

// handleBrowse returns subdirectories of the requested path for filesystem autocomplete.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(r.URL.Query().Get("path"))
	if path == "" || path == "." {
		path = "/"
	}
	if !filepath.IsAbs(path) {
		http.Error(w, "path must be absolute", http.StatusBadRequest)
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode([]string{}); err != nil {
			slog.Error("failed to encode empty slice", "msg", err.Error())
		}
		return
	}

	dirs := make([]string, 0)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, filepath.Join(path, e.Name()))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(dirs); err != nil {
		slog.Warn("failed to encode directories to response", "err", err.Error())
	}
}

// ── Manual run ─────────────────────────────────────────────────────────────

var errRunAlreadyStarted = errors.New("run already in progress")

// handleRun starts an explo run in the background. Clients follow output via /api/ui/run/events.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(1 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	userID, err := resolveUserID(w, r)
	if err != nil {
		return
	}
	playlist := s.resolveRunnablePlaylist(userID, r.FormValue("playlist"))
	args := buildArgs(playlist, r.FormValue("download_mode"),
		r.FormValue("persist") == "false", r.FormValue("exclude_local") == "true",
		s.cfg.WebEnvPath)

	if err := s.startRunWithEnv(args, s.runEnvForUser(userID)); err != nil {
		if errors.Is(err, errRunAlreadyStarted) {
			http.Error(w, "a run is already in progress", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(s.currentRunStatus()); err != nil {
		slog.Warn("failed to encode current run status", "msg", err.Error())
	}
}

// triggerLibraryRefresh spawns the CLI with --refresh-only in the background to
// nudge the configured media server's library scan. Fire-and-forget: errors are
// logged but do not block the caller.
func (s *Server) triggerLibraryRefresh() {
	go func() {
		cmd := exec.Command(s.cfg.ExploPath, "--refresh-only", "--config", s.cfg.WebEnvPath)
		env := make([]string, 0, len(os.Environ()))
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "WEB_UI=") {
				env = append(env, e)
			}
		}
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			slog.Warn("library refresh failed", "err", err.Error(), "output", string(out))
			return
		}
		slog.Info("library refresh complete")
	}()
}

func (s *Server) resolveRunnablePlaylist(userID uint, selected string) string {
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return selected
	}
	if _, ok := playlistDefs[selected]; ok || strings.HasPrefix(selected, "custom-") {
		return selected
	}
	var playlist models.Playlist
	if err := s.db.Where("user_id = ? AND (name = ? OR display_name = ? OR env_prefix = ?)", userID, selected, selected, selected).First(&playlist).Error; err != nil {
		return selected
	}
	fields := strings.Fields(playlist.Flags)
	for i, field := range fields {
		if field == "--playlist" && i+1 < len(fields) {
			return fields[i+1]
		}
		if strings.HasPrefix(field, "--playlist=") {
			return strings.TrimPrefix(field, "--playlist=")
		}
	}
	if playlist.Kind == models.PlaylistKindDefault && playlist.DisplayName != "" {
		return playlist.DisplayName
	}
	return selected
}

func (s *Server) runEnvForUser(userID uint) map[string]string {
	env := map[string]string{"EXPLO_RUN_USER_ID": fmt.Sprint(userID)}
	var user models.User
	if err := s.db.First(&user, userID).Error; err == nil && strings.TrimSpace(user.LBUserName) != "" {
		env["EXPLO_RUN_LB_USER"] = user.LBUserName
	}
	return env
}

func (s *Server) startRun(args []string) error {
	return s.startRunWithEnv(args, nil)
}

func (s *Server) startRunWithEnv(args []string, extraEnv map[string]string) error {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, s.cfg.ExploPath, args...)
	// Strip WEB_UI from env so the child process runs normally, not as web server.
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "WEB_UI=") {
			env = append(env, e)
		}
	}
	for key, value := range extraEnv {
		env = append(env, key+"="+value)
	}
	cmd.Env = env

	pr, pw, err := os.Pipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	lf, err := s.openRunLog()
	if err != nil {
		slog.Warn("failed to open run log", "err", err.Error())
	}

	s.manualRun.mu.Lock()
	if s.manualRun.running {
		s.manualRun.mu.Unlock()
		cancel()
		if err := pr.Close(); err != nil {
			slog.Warn("failed to close file reader", "err", err.Error())
		}

		if err := pw.Close(); err != nil {
			slog.Warn("failed to close file writer", "err", err.Error())
		}
		if lf != nil {
			if err := pw.Close(); err != nil {
				slog.Warn("failed to close file writer", "err", err.Error())
			}
		}
		return errRunAlreadyStarted
	}
	s.manualRun.running = true
	s.manualRun.cancel = cancel
	s.manualRun.exitCode = nil
	s.manualRun.logs = nil
	s.manualRun.mu.Unlock()

	if err := cmd.Start(); err != nil {
		s.finishRun(1)
		cancel()
		if err := pr.Close(); err != nil {
			slog.Warn("failed to close file reader", "err", err.Error())
		}

		if err := pw.Close(); err != nil {
			slog.Warn("failed to close file writer", "err", err.Error())
		}
		if lf != nil {
			if err := lf.Close(); err != nil {
				slog.Warn("failed to close run log", "err", err.Error())
			}
		}
		return fmt.Errorf("failed to start explo: %w", err)
	}

	// Close write end in parent so reader gets EOF when child exits.
	if err := pw.Close(); err != nil {
		slog.Warn("failed to close file writer", "err", err.Error())
	}

	go s.collectRunOutput(cmd, pr, lf)
	return nil
}

func (s *Server) collectRunOutput(cmd *exec.Cmd, pr *os.File, lf *os.File) {
	defer func() {
		if cerr := pr.Close(); cerr != nil {
			slog.Error("failed to close source file", "err", cerr.Error())
		}
	}()

	if lf != nil {
		defer func() {
			if cerr := lf.Close(); cerr != nil {
				slog.Error("failed to close source file", "err", cerr.Error())
			}
		}()
	}

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		line := scanner.Text()
		if lf != nil {
			if _, err := fmt.Fprintln(lf, line); err != nil {
				s.appendRunLog("failed to write run output: " + err.Error())
			}
		}
		s.appendRunLog(line)
	}
	if err := scanner.Err(); err != nil {
		s.appendRunLog("failed to read run output: " + err.Error())
	}

	code := 0
	if err := cmd.Wait(); err != nil && cmd.ProcessState == nil {
		code = 1
	}
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	s.finishRun(code)
}

func (s *Server) handleStopRun(w http.ResponseWriter, r *http.Request) {
	s.manualRun.mu.Lock()
	cancel := s.manualRun.cancel
	running := s.manualRun.running
	s.manualRun.mu.Unlock()

	if !running || cancel == nil {
		http.Error(w, "no run is currently in progress", http.StatusConflict)
		return
	}

	cancel()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) currentRunStatus() RunStatus {
	s.manualRun.mu.Lock()
	defer s.manualRun.mu.Unlock()

	var exitCode *int
	if s.manualRun.exitCode != nil {
		code := *s.manualRun.exitCode
		exitCode = &code
	}
	return RunStatus{Running: s.manualRun.running, ExitCode: exitCode}
}

func (s *Server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.currentRunStatus()); err != nil {
		slog.Warn("failed encoding current run status to response")
	}
}

// ── SSE event stream ───────────────────────────────────────────────────────

func (s *Server) appendRunLog(line string) {
	event := runEvent{data: line}

	s.manualRun.mu.Lock()
	s.manualRun.logs = append(s.manualRun.logs, line)
	subscribers := make([]chan runEvent, 0, len(s.manualRun.subscribers))
	for ch := range s.manualRun.subscribers {
		subscribers = append(subscribers, ch)
	}
	s.manualRun.mu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *Server) finishRun(code int) {
	done := runEvent{typ: "done", data: fmt.Sprintf("%d", code)}

	s.manualRun.mu.Lock()
	s.manualRun.running = false
	s.manualRun.cancel = nil
	s.manualRun.exitCode = &code
	subscribers := make([]chan runEvent, 0, len(s.manualRun.subscribers))
	for ch := range s.manualRun.subscribers {
		subscribers = append(subscribers, ch)
		delete(s.manualRun.subscribers, ch)
	}
	s.manualRun.mu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- done:
		default:
		}
		close(ch)
	}
}

// handleRunEvents streams the current in-memory run log, then follows new lines
// until the active run exits. Safe to reconnect after a browser refresh.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sendEvent := func(typ, data string) {
		if typ != "" {
			if _, err := fmt.Fprintf(w, "event: %s\n", typ); err != nil {
				slog.Warn("failed handling run event", "err", err.Error())
			}
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			slog.Warn("failed handling run event", "err", err.Error())
		}
		flusher.Flush()
	}

	ch := make(chan runEvent, 256)
	s.manualRun.mu.Lock()
	lines := append([]string(nil), s.manualRun.logs...)
	running := s.manualRun.running
	var exitCode *int
	if s.manualRun.exitCode != nil {
		code := *s.manualRun.exitCode
		exitCode = &code
	}
	if running {
		s.manualRun.subscribers[ch] = struct{}{}
	}
	s.manualRun.mu.Unlock()

	for _, line := range lines {
		sendEvent("", line)
	}
	if !running {
		if exitCode != nil {
			sendEvent("done", fmt.Sprintf("%d", *exitCode))
		}
		return
	}

	defer s.unsubscribeRun(ch)
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			sendEvent(ev.typ, ev.data)
			if ev.typ == "done" {
				return
			}
		}
	}
}

func (s *Server) unsubscribeRun(ch chan runEvent) {
	s.manualRun.mu.Lock()
	delete(s.manualRun.subscribers, ch)
	s.manualRun.mu.Unlock()
}

// ── Helpers ────────────────────────────────────────────────────────────────

func buildArgs(playlist, downloadMode string, noPersist, excludeLocal bool, WebEnvPath string) []string {
	args := []string{"--config", WebEnvPath}
	if playlist != "" {
		args = append(args, "--playlist", playlist)
	}
	if downloadMode != "" {
		args = append(args, "--download-mode", downloadMode)
	}
	if noPersist {
		args = append(args, "--persist=false")
	}
	if excludeLocal {
		args = append(args, "--exclude-local")
	}
	return args
}
