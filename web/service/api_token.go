package service

import (
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/util/common"
	"github.com/mhsanaei/3x-ui/v2/util/crypto"
	"github.com/mhsanaei/3x-ui/v2/util/random"
)

// ApiTokenService manages Bearer tokens for /panel/api/* (mirzabot and similar).
type ApiTokenService struct{}

const apiTokenLength = 48

// ApiTokenView is the JSON shape returned to the UI / create response.
type ApiTokenView struct {
	Id        int    `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token,omitempty"` // plaintext only on create
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"createdAt"`
}

func apiTokenToView(t *model.ApiToken) *ApiTokenView {
	created := t.CreatedAt
	if created >= 1_000_000_000_000 { // ms → s for display consistency
		created = created / 1000
	}
	return &ApiTokenView{
		Id:        t.Id,
		Name:      t.Name,
		Enabled:   t.Enabled,
		CreatedAt: created,
	}
}

// List returns all tokens without plaintext values.
func (s *ApiTokenService) List() ([]*ApiTokenView, error) {
	db := database.GetDB()
	var rows []*model.ApiToken
	if err := db.Model(&model.ApiToken{}).Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*ApiTokenView, 0, len(rows))
	for _, r := range rows {
		out = append(out, apiTokenToView(r))
	}
	return out, nil
}

// Create mints a new token. Plaintext is returned once in view.Token.
func (s *ApiTokenService) Create(name string) (*ApiTokenView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, common.NewError("token name is required")
	}
	if len(name) > 64 {
		return nil, common.NewError("token name must be 64 characters or fewer")
	}
	db := database.GetDB()
	var count int64
	if err := db.Model(&model.ApiToken{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, common.NewError("a token with that name already exists")
	}
	plaintext := random.Seq(apiTokenLength)
	row := &model.ApiToken{
		Name:    name,
		Token:   crypto.HashTokenSHA256(plaintext),
		Enabled: true,
	}
	if err := db.Create(row).Error; err != nil {
		return nil, err
	}
	view := apiTokenToView(row)
	view.Token = plaintext
	return view, nil
}

// Delete permanently removes a token by id.
func (s *ApiTokenService) Delete(id int) error {
	if id <= 0 {
		return common.NewError("invalid token id")
	}
	return database.GetDB().Where("id = ?", id).Delete(&model.ApiToken{}).Error
}

// SetEnabled enables or disables a token without deleting it.
func (s *ApiTokenService) SetEnabled(id int, enabled bool) error {
	if id <= 0 {
		return common.NewError("invalid token id")
	}
	res := database.GetDB().Model(&model.ApiToken{}).Where("id = ?", id).Update("enabled", enabled)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("token not found")
	}
	return nil
}

// Match reports whether presented plaintext matches any enabled stored hash.
func (s *ApiTokenService) Match(presented string) bool {
	if presented == "" {
		return false
	}
	var rows []*model.ApiToken
	if err := database.GetDB().Model(&model.ApiToken{}).Where("enabled = ?", true).Find(&rows).Error; err != nil {
		return false
	}
	presentedHash := []byte(crypto.HashTokenSHA256(presented))
	matched := false
	for _, r := range rows {
		if subtle.ConstantTimeCompare([]byte(r.Token), presentedHash) == 1 {
			matched = true
		}
	}
	return matched
}
