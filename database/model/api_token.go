package model

// ApiToken is a long-lived Bearer credential for programmatic access
// (sales bots such as mirzabot, scripts). The Token column stores a SHA-256
// hex digest of the plaintext; the plaintext is shown once at creation and
// never again.
type ApiToken struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Name      string `json:"name" gorm:"uniqueIndex;size:64"`
	Token     string `json:"-" gorm:"size:64;not null"` // SHA-256 hex of plaintext
	Enabled   bool   `json:"enabled" gorm:"default:1"`
	CreatedAt int64  `json:"createdAt" gorm:"autoCreateTime:milli"`
}
