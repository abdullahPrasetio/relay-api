package entity

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Session is one logged-in device. Only the SHA-256 hex hash of the current
// opaque refresh token is stored, never the token itself.
//
// During the rotation grace window the previous token hash lives in PrevHash
// and the replacement token pair is kept AES-GCM-encrypted in ReplacementCT so
// a retried refresh returns exactly the pair the first call produced.
type Session struct {
	ID            uuid.UUID  `gorm:"type:uuid;primaryKey;not null" json:"id"`
	UserID        uuid.UUID  `gorm:"type:uuid;not null;index"     json:"user_id"`
	DeviceLabel   string     `gorm:"type:text;not null;default:''" json:"device_label"`
	RefreshHash   string     `gorm:"type:text;not null;uniqueIndex" json:"-"`
	PrevHash      *string    `gorm:"type:text"                    json:"-"`
	PrevExpiresAt *time.Time `gorm:"type:timestamptz"             json:"-"`
	ReplacementCT []byte     `gorm:"type:bytea"                   json:"-"`
	ExpiresAt     time.Time  `gorm:"type:timestamptz;not null"    json:"expires_at"`
	RevokedAt     *time.Time `gorm:"type:timestamptz"             json:"revoked_at,omitempty"`
	CreatedAt     time.Time  `gorm:"type:timestamptz;not null"    json:"created_at"`
	LastUsedAt    *time.Time `gorm:"type:timestamptz"             json:"last_used_at,omitempty"`
}

func (s *Session) BeforeCreate(_ *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	return nil
}

// Active reports whether the session can still authenticate requests.
func (s *Session) Active(now time.Time) bool {
	return s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

func (Session) TableName() string { return "sessions" }
