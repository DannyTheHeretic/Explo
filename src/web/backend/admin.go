package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"explo/src/models"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type adminUserRequest struct {
	Username      string          `json:"username"`
	Password      string          `json:"password"`
	Role          models.UserRole `json:"role"`
	LBUserName    string          `json:"lb_user_name"`
	DiscoveryMode string          `json:"discovery_mode"`
}

type adminUserResponse struct {
	ID            uint            `json:"id"`
	Username      string          `json:"username"`
	Role          models.UserRole `json:"role"`
	LBUserName    string          `json:"lb_user_name"`
	DiscoveryMode string          `json:"discovery_mode"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

func (s *Server) handleAdminCollection(w http.ResponseWriter, r *http.Request) {
	resource := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/ui/admin/"), "/")
	if strings.Contains(resource, "/") || resource == "" {
		http.NotFound(w, r)
		return
	}

	if resource == "schema" {
		s.handleAdminSchema(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleAdminList(w, r, resource)
	case http.MethodPost:
		s.handleAdminCreate(w, r, resource)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAdminItem(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/ui/admin/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || id == 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleAdminGet(w, r, parts[0], uint(id))
	case http.MethodPut:
		s.handleAdminUpdate(w, r, parts[0], uint(id))
	case http.MethodDelete:
		s.handleAdminDelete(w, r, parts[0], uint(id))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAdminList(w http.ResponseWriter, r *http.Request, resource string) {
	writeJSON := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	switch resource {
	case "users":
		var rows []models.User
		if err := s.db.Order("id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]adminUserResponse, 0, len(rows))
		for _, row := range rows {
			out = append(out, userResponse(row))
		}
		writeJSON(out)
	case "servers":
		var rows []models.Server
		if err := s.db.Order("id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rows)
	case "downloaders":
		var rows []models.Downloader
		if err := s.db.Preload("YoutubeDownloader").Preload("SlskdDownloader").Order("id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rows)
	case "playlists":
		var rows []models.Playlist
		if err := s.db.Order("user_id, id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rows)
	case "youtube-downloaders":
		var rows []models.YoutubeDownloader
		if err := s.db.Order("id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rows)
	case "slskd-downloaders":
		var rows []models.SlskdDownloader
		if err := s.db.Order("id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rows)
	case "credentials":
		var rows []models.Credential
		if err := s.db.Order("id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rows)
	case "server-downloaders":
		var rows []models.ServerDownloader
		if err := s.db.Order("server_id, priority, id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rows)
	case "user-server-credentials":
		var rows []models.UserServerCredential
		if err := s.db.Order("user_id, server_id, id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rows)
	case "playlist-schedules":
		var rows []models.PlaylistSchedule
		if err := s.db.Order("id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rows)
	case "app-settings":
		var rows []models.AppSettings
		if err := s.db.Order("id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rows)
	case "path-templates":
		var rows []models.PathTemplatePreset
		if err := s.db.Order("name, id").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(rows)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleAdminGet(w http.ResponseWriter, r *http.Request, resource string, id uint) {
	writeJSON := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	switch resource {
	case "users":
		var row models.User
		if !findAdminRow(w, s.db.First(&row, id).Error) {
			return
		}
		writeJSON(userResponse(row))
	case "servers":
		var row models.Server
		if !findAdminRow(w, s.db.First(&row, id).Error) {
			return
		}
		writeJSON(row)
	case "downloaders":
		var row models.Downloader
		if !findAdminRow(w, s.db.Preload("YoutubeDownloader").Preload("SlskdDownloader").First(&row, id).Error) {
			return
		}
		writeJSON(row)
	case "playlists":
		var row models.Playlist
		if !findAdminRow(w, s.db.First(&row, id).Error) {
			return
		}
		writeJSON(row)
	case "youtube-downloaders":
		var row models.YoutubeDownloader
		if !findAdminRow(w, s.db.First(&row, id).Error) {
			return
		}
		writeJSON(row)
	case "slskd-downloaders":
		var row models.SlskdDownloader
		if !findAdminRow(w, s.db.First(&row, id).Error) {
			return
		}
		writeJSON(row)
	case "credentials":
		var row models.Credential
		if !findAdminRow(w, s.db.First(&row, id).Error) {
			return
		}
		writeJSON(row)
	case "server-downloaders":
		var row models.ServerDownloader
		if !findAdminRow(w, s.db.First(&row, id).Error) {
			return
		}
		writeJSON(row)
	case "user-server-credentials":
		var row models.UserServerCredential
		if !findAdminRow(w, s.db.First(&row, id).Error) {
			return
		}
		writeJSON(row)
	case "playlist-schedules":
		var row models.PlaylistSchedule
		if !findAdminRow(w, s.db.First(&row, id).Error) {
			return
		}
		writeJSON(row)
	case "app-settings":
		var row models.AppSettings
		if !findAdminRow(w, s.db.First(&row, id).Error) {
			return
		}
		writeJSON(row)
	case "path-templates":
		var row models.PathTemplatePreset
		if !findAdminRow(w, s.db.First(&row, id).Error) {
			return
		}
		writeJSON(row)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleAdminCreate(w http.ResponseWriter, r *http.Request, resource string) {
	switch resource {
	case "users":
		s.createAdminUser(w, r)
	case "servers":
		createAdminModel[models.Server](w, r, s.db)
	case "downloaders":
		createAdminModel[models.Downloader](w, r, s.db)
	case "youtube-downloaders":
		createAdminModel[models.YoutubeDownloader](w, r, s.db)
	case "slskd-downloaders":
		createAdminModel[models.SlskdDownloader](w, r, s.db)
	case "server-downloaders":
		createAdminModel[models.ServerDownloader](w, r, s.db)
	case "playlists":
		createAdminModel[models.Playlist](w, r, s.db)
	case "credentials":
		createAdminModel[models.Credential](w, r, s.db)
	case "user-server-credentials":
		createAdminModel[models.UserServerCredential](w, r, s.db)
	case "playlist-schedules":
		createAdminModel[models.PlaylistSchedule](w, r, s.db)
	case "app-settings":
		createAdminModel[models.AppSettings](w, r, s.db)
	case "path-templates":
		createAdminModel[models.PathTemplatePreset](w, r, s.db)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleAdminUpdate(w http.ResponseWriter, r *http.Request, resource string, id uint) {
	switch resource {
	case "users":
		s.updateAdminUser(w, r, id)
	case "servers":
		updateAdminModel[models.Server](w, r, s.db, id)
	case "downloaders":
		updateAdminModel[models.Downloader](w, r, s.db, id)
	case "youtube-downloaders":
		updateAdminModel[models.YoutubeDownloader](w, r, s.db, id)
	case "slskd-downloaders":
		updateAdminModel[models.SlskdDownloader](w, r, s.db, id)
	case "server-downloaders":
		updateAdminModel[models.ServerDownloader](w, r, s.db, id)
	case "playlists":
		updateAdminModel[models.Playlist](w, r, s.db, id)
	case "credentials":
		updateAdminModel[models.Credential](w, r, s.db, id)
	case "user-server-credentials":
		updateAdminModel[models.UserServerCredential](w, r, s.db, id)
	case "playlist-schedules":
		updateAdminModel[models.PlaylistSchedule](w, r, s.db, id)
	case "app-settings":
		updateAdminModel[models.AppSettings](w, r, s.db, id)
	case "path-templates":
		updateAdminModel[models.PathTemplatePreset](w, r, s.db, id)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request, resource string, id uint) {
	switch resource {
	case "users":
		deleteAdminModel[models.User](w, s.db, id)
	case "servers":
		deleteAdminModel[models.Server](w, s.db, id)
	case "downloaders":
		deleteAdminModel[models.Downloader](w, s.db, id)
	case "youtube-downloaders":
		deleteAdminModel[models.YoutubeDownloader](w, s.db, id)
	case "slskd-downloaders":
		deleteAdminModel[models.SlskdDownloader](w, s.db, id)
	case "server-downloaders":
		deleteAdminModel[models.ServerDownloader](w, s.db, id)
	case "playlists":
		deleteAdminModel[models.Playlist](w, s.db, id)
	case "credentials":
		deleteAdminModel[models.Credential](w, s.db, id)
	case "user-server-credentials":
		deleteAdminModel[models.UserServerCredential](w, s.db, id)
	case "playlist-schedules":
		deleteAdminModel[models.PlaylistSchedule](w, s.db, id)
	case "app-settings":
		deleteAdminModel[models.AppSettings](w, s.db, id)
	case "path-templates":
		deleteAdminModel[models.PathTemplatePreset](w, s.db, id)
	default:
		http.NotFound(w, r)
	}
}
type adminSchemaResource struct {
	Key      string             `json:"key"`
	Label    string             `json:"label"`
	IDField  string             `json:"idField"`
	Template map[string]any     `json:"template"`
	Fields   []adminSchemaField `json:"fields"`
}

type adminSchemaField struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	Type       string   `json:"type,omitempty"`
	Options    []string `json:"options,omitempty"`
	Resource   string   `json:"resource,omitempty"`
	Nullable   bool     `json:"nullable,omitempty"`
	CreateOnly bool     `json:"createOnly,omitempty"`
}

type adminSchemaModel struct {
	Key     string
	Label   string
	IDField string
	Model   any
}

var adminSchemaModels = []adminSchemaModel{
	{Key: "servers", Label: "Servers", IDField: "ID", Model: models.Server{}},
	{Key: "downloaders", Label: "Downloaders", IDField: "ID", Model: models.Downloader{}},
	{Key: "youtube-downloaders", Label: "YouTube Configs", IDField: "ID", Model: models.YoutubeDownloader{}},
	{Key: "slskd-downloaders", Label: "Slskd Configs", IDField: "ID", Model: models.SlskdDownloader{}},
	{Key: "server-downloaders", Label: "Server Downloaders", IDField: "ID", Model: models.ServerDownloader{}},
	{Key: "users", Label: "Users", IDField: "id", Model: models.User{}},
	{Key: "playlists", Label: "Playlists", IDField: "ID", Model: models.Playlist{}},
	{Key: "credentials", Label: "Credentials", IDField: "ID", Model: models.Credential{}},
	{Key: "user-server-credentials", Label: "User Credentials", IDField: "ID", Model: models.UserServerCredential{}},
	{Key: "playlist-schedules", Label: "Playlist Schedules", IDField: "ID", Model: models.PlaylistSchedule{}},
	{Key: "app-settings", Label: "App Settings", IDField: "ID", Model: models.AppSettings{}},
	{Key: "path-templates", Label: "Path Templates", IDField: "ID", Model: models.PathTemplatePreset{}},
}

var adminEnumOptions = map[string][]string{
	"Type:servers":                       {"plex", "subsonic", "navidrome", "jellyfin", "custom"},
	"Type:downloaders":                   {"youtube", "youtube_music", "slskd"},
	"Type:credentials":                   {"api_key", "userpass"},
	"Role:users":                         {"basic", "manager"},
	"DiscoveryMode:users":                {"playlist", "api"},
	"Kind:playlists":                     {"default", "custom"},
	"Source:playlists":                   {"listenbrainz", "apple_music", "spotify"},
	"ListenBrainzDiscovery:app-settings": {"playlist", "api"},
	"PlaylistNameFormat:app-settings":    {"week", "date"},
}

var adminForeignResources = map[string]string{
	"UserID":              "users",
	"ManagerID":           "users",
	"ServerID":            "servers",
	"SelectedServerID":    "servers",
	"CredentialID":        "credentials",
	"AdminCredentialID":   "credentials",
	"DownloaderID":        "downloaders",
	"YoutubeDownloaderID": "youtube-downloaders",
	"SlskdDownloaderID":   "slskd-downloaders",
}

func (s *Server) handleAdminSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resources := make([]adminSchemaResource, 0, len(adminSchemaModels))
	for _, def := range adminSchemaModels {
		resource, err := s.adminSchemaResource(def)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resources = append(resources, resource)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resources)
}

func (s *Server) adminSchemaResource(def adminSchemaModel) (adminSchemaResource, error) {
	if def.Key == "users" {
		return adminUserSchema(), nil
	}
	parsed, err := schema.Parse(def.Model, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		return adminSchemaResource{}, err
	}
	columns, err := s.db.Migrator().ColumnTypes(def.Model)
	if err != nil {
		return adminSchemaResource{}, err
	}
	columnInfo := map[string]gorm.ColumnType{}
	for _, column := range columns {
		columnInfo[column.Name()] = column
	}

	fields := []adminSchemaField{}
	template := adminTemplateFor(def.Key)
	for _, field := range parsed.Fields {
		if !adminEditableField(field) {
			continue
		}
		meta := adminFieldMeta(def.Key, field, columnInfo[field.DBName])
		fields = append(fields, meta)
		if _, ok := template[meta.Key]; !ok {
			template[meta.Key] = adminZeroValue(field.FieldType, meta.Nullable)
		}
	}
	return adminSchemaResource{Key: def.Key, Label: def.Label, IDField: def.IDField, Template: template, Fields: fields}, nil
}

func adminEditableField(field *schema.Field) bool {
	if field.DBName == "" || field.PrimaryKey {
		return false
	}
	switch field.Name {
	case "CreatedAt", "UpdatedAt", "DeletedAt":
		return false
	}
	return true
}

func adminFieldMeta(resource string, field *schema.Field, column gorm.ColumnType) adminSchemaField {
	meta := adminSchemaField{Key: field.Name, Label: adminLabel(field.Name)}
	if column != nil {
		if nullable, ok := column.Nullable(); ok {
			meta.Nullable = nullable
		}
	}
	kind := field.FieldType.Kind()
	if kind == reflect.Pointer {
		meta.Nullable = true
		kind = field.FieldType.Elem().Kind()
	}
	if fk, ok := adminForeignResources[field.Name]; ok {
		meta.Type = "fk"
		meta.Resource = fk
		return meta
	}
	if options, ok := adminEnumOptions[field.Name+":"+resource]; ok {
		meta.Type = "select"
		meta.Options = options
		return meta
	}
	if field.Name == "PathTemplate" || (resource == "path-templates" && field.Name == "Template") {
		meta.Type = "path-template"
		return meta
	}
	if strings.Contains(strings.ToLower(field.Name), "password") {
		meta.Type = "password"
		return meta
	}
	if strings.Contains(field.Name, "Dir") {
		meta.Type = "dir"
		return meta
	}
	switch kind {
	case reflect.Bool:
		meta.Type = "checkbox"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		meta.Type = "number"
	}
	return meta
}

func adminTemplateFor(resource string) map[string]any {
	if resource == "app-settings" {
		settings := models.DefaultAppSettings()
		return map[string]any{
			"WizardComplete":        settings.WizardComplete,
			"ListenBrainzDiscovery": settings.ListenBrainzDiscovery,
			"DownloadServices":      settings.DownloadServices,
			"SelectedServerID":      nil,
			"PathTemplate":          settings.PathTemplate,
			"PlaylistNameFormat":    settings.PlaylistNameFormat,
			"SingleArtist":          settings.SingleArtist,
			"KeepPermissions":       settings.KeepPermissions,
			"MigrateDownloads":      settings.MigrateDownloads,
			"EnrichTrackMetadata":   settings.EnrichTrackMetadata,
			"OverwriteMetadata":     settings.OverwriteMetadata,
			"UseSubdirectory":       settings.UseSubdirectory,
		}
	}
	return map[string]any{}
}

func adminZeroValue(fieldType reflect.Type, nullable bool) any {
	if nullable {
		return nil
	}
	if fieldType.Kind() == reflect.Pointer {
		fieldType = fieldType.Elem()
	}
	switch fieldType.Kind() {
	case reflect.Bool:
		return false
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return 0
	default:
		return ""
	}
}

func adminUserSchema() adminSchemaResource {
	return adminSchemaResource{
		Key:     "users",
		Label:   "Users",
		IDField: "id",
		Template: map[string]any{
			"username":       "",
			"password":       "",
			"role":           "basic",
			"lb_user_name":   "",
			"discovery_mode": "playlist",
		},
		Fields: []adminSchemaField{
			{Key: "username", Label: "Username"},
			{Key: "password", Label: "New password", Type: "password", CreateOnly: true},
			{Key: "role", Label: "Role", Type: "select", Options: []string{"basic", "manager"}},
			{Key: "lb_user_name", Label: "ListenBrainz user"},
			{Key: "discovery_mode", Label: "Discovery mode", Type: "select", Options: []string{"playlist", "api"}},
		},
	}
}

func adminLabel(name string) string {
	if name == "ID" {
		return "ID"
	}
	var out strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out.WriteRune(' ')
		}
		out.WriteRune(r)
	}
	label := out.String()
	label = strings.ReplaceAll(label, "ID", "ID")
	return label
}

func createAdminModel[T any](w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	var row T
	if err := json.NewDecoder(r.Body).Decode(&row); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := db.Create(&row).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(row)
}

func updateAdminModel[T any](w http.ResponseWriter, r *http.Request, db *gorm.DB, id uint) {
	var row T
	if !findAdminRow(w, db.First(&row, id).Error) {
		return
	}
	var attrs map[string]any
	if err := json.NewDecoder(r.Body).Decode(&attrs); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	deleteReadOnlyAdminFields(attrs)
	if len(attrs) > 0 {
		if err := db.Model(&row).Updates(attrs).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := db.First(&row, id).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(row)
}

func deleteReadOnlyAdminFields(attrs map[string]any) {
	for _, key := range []string{"ID", "id", "CreatedAt", "UpdatedAt", "DeletedAt", "Manager", "AdminCredential", "User", "YoutubeDownloader", "SlskdDownloader"} {
		delete(attrs, key)
	}
}

func deleteAdminModel[T any](w http.ResponseWriter, db *gorm.DB, id uint) {
	var row T
	if !findAdminRow(w, db.First(&row, id).Error) {
		return
	}
	if err := db.Delete(&row).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createAdminUser(w http.ResponseWriter, r *http.Request) {
	var body adminUserRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Username == "" {
		http.Error(w, "username is required", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	user := models.User{
		Username:      body.Username,
		Password:      string(hash),
		Role:          firstRole(body.Role),
		LBUserName:    body.LBUserName,
		DiscoveryMode: firstNonEmpty(body.DiscoveryMode, "playlist"),
	}
	if err := s.db.Create(&user).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(userResponse(user))
}

func (s *Server) updateAdminUser(w http.ResponseWriter, r *http.Request, id uint) {
	var user models.User
	if !findAdminRow(w, s.db.First(&user, id).Error) {
		return
	}
	var body adminUserRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Username != "" {
		user.Username = body.Username
	}
	if body.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		user.Password = string(hash)
	}
	if body.Role != "" {
		user.Role = firstRole(body.Role)
	}
	user.LBUserName = body.LBUserName
	user.DiscoveryMode = firstNonEmpty(body.DiscoveryMode, "playlist")
	if err := s.db.Save(&user).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(userResponse(user))
}

func findAdminRow(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return false
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
	return false
}

func firstRole(role models.UserRole) models.UserRole {
	if role == "" {
		return models.RoleBasic
	}
	return role
}

func userResponse(user models.User) adminUserResponse {
	return adminUserResponse{
		ID:            user.ID,
		Username:      user.Username,
		Role:          user.Role,
		LBUserName:    user.LBUserName,
		DiscoveryMode: user.DiscoveryMode,
		CreatedAt:     user.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:     user.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}
