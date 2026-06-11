package models

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"time"

	"gorm.io/gorm"
)

//
// SECRET_KEY
// Must be 32 bytes (AES-256)
//

var encryptionKey []byte

func SetEncryptionKey(key []byte) {
	encryptionKey = key
}


type UserRole string

const (
	RoleBasic   UserRole = "basic"
	RoleManager UserRole = "manager"
)

type ServerType string

const (
	ServerPlex      ServerType = "plex"
	ServerSubsonic  ServerType = "subsonic"
	ServerNavidrome ServerType = "navidrome"
	ServerJellyfin  ServerType = "jellyfin"
	ServerCustom    ServerType = "custom"
)

type CredentialType string

const (
	CredentialAPIKey   CredentialType = "api_key"
	CredentialUserPass CredentialType = "userpass"
)

type DownloaderType string

const (
	DownloaderYoutube      DownloaderType = "youtube"
	DownloaderYoutubeMusic DownloaderType = "youtube_music"
	DownloaderSlskd        DownloaderType = "slskd"
)


type User struct {
	ID       uint     `gorm:"primaryKey"`
	Username string   `gorm:"uniqueIndex;not null"`
	Password string   `gorm:"not null"` // bcrypt hash
	Role     UserRole `gorm:"not null;default:'basic'"`

	Servers     []Server     `gorm:"foreignKey:ManagerID"`
	Downloaders []Downloader `gorm:"many2many:user_downloaders;"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}


type Server struct {
	ID uint `gorm:"primaryKey"`

	Name        string     `gorm:"not null"`
	LibraryName string     `gorm:"not null"`
	URL         string     `gorm:"not null"`
	Type        ServerType `gorm:"not null"`

	ManagerID uint
	Manager   User

	AdminCredentialID uint
	AdminCredential   Credential
	System     string
	Downloaders []Downloader `gorm:"many2many:server_downloaders;"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}


type Credential struct {
	ID uint `gorm:"primaryKey"`

	Type CredentialType `gorm:"not null"`

	APIKey   string
	Username string
	Password string

	apiKeyPlain   string `gorm:"-"`
	usernamePlain string `gorm:"-"`
	passwordPlain string `gorm:"-"`

	CreatedAt time.Time
	UpdatedAt time.Time
}


func encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if len(encryptionKey) != 32 {
		return "", errors.New("invalid encryption key length (must be 64 bytes)")
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

func (c *Credential) AfterFind(tx *gorm.DB) error {
	var err error

	if c.APIKey != "" {
		c.apiKeyPlain, err = decrypt(c.APIKey)
		if err != nil {
			return err
		}
	}

	if c.Username != "" {
		c.usernamePlain, err = decrypt(c.Username)
		if err != nil {
			return err
		}
	}

	if c.Password != "" {
		c.passwordPlain, err = decrypt(c.Password)
		if err != nil {
			return err
		}
	}

	return nil
}


func (c *Credential) GetAPIKey() string {
	if c.apiKeyPlain != "" {
		return c.apiKeyPlain
	}
	return c.APIKey
}

func (c *Credential) GetUsername() string {
	if c.usernamePlain != "" {
		return c.usernamePlain
	}
	return c.Username
}

func (c *Credential) GetPassword() string {
	if c.passwordPlain != "" {
		return c.passwordPlain
	}
	return c.Password
}


//
// USER <-> SERVER CREDENTIALS
//
// Stores the manager's credentials for a server.
//
// eg:
//
// Server:
//   Plex
//
// Admin Credential:
//   ADMIN_API_KEY
//
// Manager Credential:
//   SYSTEM_USERNAME + SYSTEM_PASSWORD
//
// Keeps both credential sets separate.
//

type UserServerCredential struct {
	ID uint `gorm:"primaryKey"`

	UserID uint `gorm:"not null"`
	User   User

	ServerID uint `gorm:"not null"`
	Server   Server

	CredentialID uint `gorm:"not null"`
	Credential   Credential

	CreatedAt time.Time
	UpdatedAt time.Time
}

type Downloader struct {
	ID     uint `gorm:"primaryKey"`
	UserID uint

	// core
	DownloadDir     string
	PlaylistDir     string
	UseSubdirectory bool
	KeepPermissions bool
	Services        string

	// youtube
	YouTubeAPIKey  string
	TrackExtension string

	// slskd
	SlskdURL         string
	SlskdAPIKey      string
	MigrateDownloads bool
	RenameTrack      bool
	SlskdDir         string
	SlskdRetry       int
	SlskdDLAttempts  int
	Extensions       string
	FilterList       string
}


type UserDownloader struct {
	UserID uint `gorm:"primaryKey"`
	User   User

	DownloaderID uint `gorm:"primaryKey"`
	Downloader   Downloader
}

type ServerDownloader struct {
	ServerID uint `gorm:"primaryKey"`
	Server   Server

	DownloaderID uint `gorm:"primaryKey"`
	Downloader   Downloader
}


func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&User{},
		&Server{},
		&Credential{},
		&UserServerCredential{},
		&Downloader{},
		&UserDownloader{},
		&ServerDownloader{},
	)
}
