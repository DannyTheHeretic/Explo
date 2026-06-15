package backend

import (
	"net/http"

	"explo/src/models"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AuthStore struct {
	db             *gorm.DB
	sessionManager *SessionManager
}

func NewAuthStore(db *gorm.DB, sessionManager *SessionManager) *AuthStore {
	return &AuthStore{
		db:             db,
		sessionManager: sessionManager,
	}
}

func (a *AuthStore) CompareCreds(formUser, formPass string) bool {
	var user models.User

	err := a.db.Where("username = ?", formUser).First(&user).Error
	if err != nil {
		return false
	}

	return bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(formPass)) == nil
}

func (a *AuthStore) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := a.sessionManager.GetSession(r)

		auth, _ := sess.Get("authenticated").(bool)
		if !auth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		userID, ok := sess.Get("user_id").(uint)
		if !ok || userID == 0 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (a *AuthStore) RequireManager(next http.Handler) http.Handler {
	return a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := a.sessionManager.GetSession(r)
		role, _ := sess.Get("role").(models.UserRole)
		if role == "" {
			if roleString, ok := sess.Get("role").(string); ok {
				role = models.UserRole(roleString)
			}
		}
		if role != models.RoleManager {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}
