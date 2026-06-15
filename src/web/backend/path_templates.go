package backend

import (
	"encoding/json"
	"errors"
	"explo/src/models"
	"net/http"
	"net/url"
	"strings"

	"gorm.io/gorm"
)

// PathTemplatePreset is a named folder-structure template saved by the user.
type PathTemplatePreset struct {
	Name     string `json:"name"`
	Template string `json:"template"`
}

type pathTemplateResponse struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	BuiltIn  bool   `json:"built_in"`
}

var builtinPathTemplates = []pathTemplateResponse{
	{Name: "Artist / Album", Template: "{{artist}}/{{album}}/{{title}}", BuiltIn: true},
	{Name: "Artist - Title", Template: "{{artist}} - {{title}}", BuiltIn: true},
	{Name: "Album / Track", Template: "{{album}}/{{track}} - {{title}}", BuiltIn: true},
}


func (s *Server) handlePathTemplates(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var rows []models.PathTemplatePreset
		if err := s.db.Order("name").Find(&rows).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items := append([]pathTemplateResponse{}, builtinPathTemplates...)
		seen := map[string]bool{}
		for _, item := range items {
			seen[item.Name] = true
		}
		for _, row := range rows {
			if row.Name == "" || row.Template == "" || seen[row.Name] {
				continue
			}
			items = append(items, pathTemplateResponse{Name: row.Name, Template: row.Template})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	case http.MethodPost:
		var body struct {
			Name     string `json:"name"`
			Template string `json:"template"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		body.Template = strings.TrimSpace(body.Template)
		if body.Name == "" || body.Template == "" {
			http.Error(w, "name and template are required", http.StatusBadRequest)
			return
		}
		var row models.PathTemplatePreset
		if err := s.db.Where("name = ?", body.Name).First(&row).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			row = models.PathTemplatePreset{Name: body.Name}
		}
		row.Template = body.Template
		if row.ID == 0 {
			if err := s.db.Create(&row).Error; err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if err := s.db.Save(&row).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(pathTemplateResponse{Name: row.Name, Template: row.Template})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDeletePathTemplate(w http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/ui/path-templates/"))
	if err != nil || strings.TrimSpace(name) == "" {
		http.Error(w, "invalid template name", http.StatusBadRequest)
		return
	}
	for _, item := range builtinPathTemplates {
		if item.Name == name {
			http.Error(w, "built-in templates cannot be deleted", http.StatusBadRequest)
			return
		}
	}
	if err := s.db.Where("name = ?", name).Delete(&models.PathTemplatePreset{}).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
