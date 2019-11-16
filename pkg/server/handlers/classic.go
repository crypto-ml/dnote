/* Copyright (C) 2019 Monomax Software Pty Ltd
 *
 * This file is part of Dnote.
 *
 * Dnote is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * Dnote is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with Dnote.  If not, see <https://www.gnu.org/licenses/>.
 */

package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/dnote/dnote/pkg/server/crypt"
	"github.com/dnote/dnote/pkg/server/database"
	"github.com/dnote/dnote/pkg/server/helpers"
	"github.com/dnote/dnote/pkg/server/log"
	"github.com/dnote/dnote/pkg/server/operations"
	"github.com/dnote/dnote/pkg/server/presenters"
	"github.com/pkg/errors"
	"golang.org/x/crypto/bcrypt"
)

func (a *App) classicMigrate(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(helpers.KeyUser).(database.User)
	if !ok {
		HandleError(w, "No authenticated user found", nil, http.StatusInternalServerError)
		return
	}

	var account database.Account
	if err := a.DB.Where("user_id = ?", user.ID).First(&account).Error; err != nil {
		HandleError(w, "finding account", err, http.StatusInternalServerError)
		return
	}

	if err := a.DB.Model(&account).
		Update(map[string]interface{}{
			"salt":                 "",
			"auth_key_hash":        "",
			"cipher_key_enc":       "",
			"client_kdf_iteration": 0,
			"server_kdf_iteration": 0,
		}).Error; err != nil {
		HandleError(w, "updating account", err, http.StatusInternalServerError)
		return
	}
}

// PresigninResponse is a response for presignin
type PresigninResponse struct {
	Iteration int `json:"iteration"`
}

func (a *App) classicPresignin(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	email := q.Get("email")
	if email == "" {
		http.Error(w, "email is required", http.StatusBadRequest)
		return
	}

	var account database.Account
	conn := a.DB.Where("email = ?", email).First(&account)
	if !conn.RecordNotFound() && conn.Error != nil {
		HandleError(w, "getting user", conn.Error, http.StatusInternalServerError)
		return
	}

	var response PresigninResponse
	if conn.RecordNotFound() {
		response = PresigninResponse{
			Iteration: 100000,
		}
	} else {
		response = PresigninResponse{
			Iteration: account.ClientKDFIteration,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		HandleError(w, "encoding response", nil, http.StatusInternalServerError)
		return
	}
}

type classicSigninPayload struct {
	Email   string `json:"email"`
	AuthKey string `json:"auth_key"`
}

func (a *App) classicSignin(w http.ResponseWriter, r *http.Request) {
	var params classicSigninPayload
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		HandleError(w, "decoding payload", err, http.StatusInternalServerError)
		return
	}

	if params.Email == "" || params.AuthKey == "" {
		http.Error(w, ErrLoginFailure.Error(), http.StatusUnauthorized)
		return
	}

	var account database.Account
	conn := a.DB.Where("email = ?", params.Email).First(&account)
	if conn.RecordNotFound() {
		http.Error(w, ErrLoginFailure.Error(), http.StatusUnauthorized)
		return
	} else if err := conn.Error; err != nil {
		HandleError(w, "getting user", err, http.StatusInternalServerError)
		return
	}

	authKeyHash := crypt.HashAuthKey(params.AuthKey, account.Salt, account.ServerKDFIteration)
	if account.AuthKeyHash != authKeyHash {
		log.WithFields(log.Fields{
			"account_id": account.ID,
		}).Error("Sign in password mismatch")
		http.Error(w, ErrLoginFailure.Error(), http.StatusUnauthorized)
		return
	}

	session, err := operations.CreateSession(a.DB, account.UserID)
	if err != nil {
		HandleError(w, "creating session", nil, http.StatusBadRequest)
		return
	}

	setSessionCookie(w, session.Key, session.ExpiresAt)

	response := struct {
		Key          string `json:"key"`
		ExpiresAt    int64  `json:"expires_at"`
		CipherKeyEnc string `json:"cipher_key_enc"`
	}{
		Key:          session.Key,
		ExpiresAt:    session.ExpiresAt.Unix(),
		CipherKeyEnc: account.CipherKeyEnc,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		HandleError(w, "encoding response", err, http.StatusInternalServerError)
		return
	}
}

func (a *App) classicGetMe(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(helpers.KeyUser).(database.User)
	if !ok {
		HandleError(w, "No authenticated user found", nil, http.StatusInternalServerError)
		return
	}

	var account database.Account
	if err := a.DB.Where("user_id = ?", user.ID).First(&account).Error; err != nil {
		HandleError(w, "finding account", err, http.StatusInternalServerError)
		return
	}

	legacy := account.AuthKeyHash == ""

	type classicSession struct {
		ID              int    `json:"id"`
		GithubName      string `json:"github_name"`
		GithubAccountID string `json:"github_account_id"`
		APIKey          string `json:"api_key"`
		Name            string `json:"name"`
		Email           string `json:"email"`
		EmailVerified   bool   `json:"email_verified"`
		Provider        string `json:"provider"`
		Cloud           bool   `json:"cloud"`
		Legacy          bool   `json:"legacy"`
		Encrypted       bool   `json:"encrypted"`
		CipherKeyEnc    string `json:"cipher_key_enc"`
	}

	session := classicSession{
		ID:              user.ID,
		GithubName:      account.Nickname,
		GithubAccountID: account.AccountID,
		APIKey:          user.APIKey,
		Cloud:           user.Cloud,
		Email:           account.Email.String,
		EmailVerified:   account.EmailVerified,
		Name:            user.Name,
		Provider:        account.Provider,
		Legacy:          legacy,
		Encrypted:       user.Encrypted,
		CipherKeyEnc:    account.CipherKeyEnc,
	}

	response := struct {
		User classicSession `json:"user"`
	}{
		User: session,
	}

	respondJSON(w, http.StatusOK, response)
}

type classicSetPasswordPayload struct {
	Password string
}

func (a *App) classicSetPassword(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(helpers.KeyUser).(database.User)
	if !ok {
		HandleError(w, "No authenticated user found", nil, http.StatusInternalServerError)
		return
	}

	var params classicSetPasswordPayload
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		HandleError(w, "decoding payload", err, http.StatusInternalServerError)
		return
	}

	var account database.Account
	if err := a.DB.Where("user_id = ?", user.ID).First(&account).Error; err != nil {
		HandleError(w, "getting user", nil, http.StatusInternalServerError)
		return
	}

	hashedNewPassword, err := bcrypt.GenerateFromPassword([]byte(params.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, errors.Wrap(err, "hashing password").Error(), http.StatusInternalServerError)
		return
	}

	if err := a.DB.Model(&account).Update("password", string(hashedNewPassword)).Error; err != nil {
		http.Error(w, errors.Wrap(err, "updating password").Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (a *App) classicGetNotes(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(helpers.KeyUser).(database.User)
	if !ok {
		HandleError(w, "No authenticated user found", nil, http.StatusInternalServerError)
		return
	}

	var notes []database.Note
	if err := a.DB.Where("user_id = ? AND encrypted = true", user.ID).Find(&notes).Error; err != nil {
		HandleError(w, "finding notes", err, http.StatusInternalServerError)
		return
	}

	presented := presenters.PresentNotes(notes)
	respondJSON(w, http.StatusOK, presented)
}
