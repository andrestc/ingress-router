// Copyright 2014 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package saml

import (
	"crypto"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/globalsign/mgo"
	"github.com/globalsign/mgo/bson"
	"github.com/pkg/errors"
	"github.com/tsuru/config"
	"github.com/tsuru/tsuru/auth"
	"github.com/tsuru/tsuru/db"
	"github.com/tsuru/tsuru/permission"
	authTypes "github.com/tsuru/tsuru/types/auth"
)

const (
	keySize           = 32
	defaultExpiration = 7 * 24 * time.Hour
)

var tokenExpire time.Duration

type Token struct {
	Token     string        `json:"token"`
	Creation  time.Time     `json:"creation"`
	Expires   time.Duration `json:"expires"`
	UserEmail string        `json:"email"`
	AppName   string        `json:"app"`
}

func (t *Token) GetValue() string {
	return t.Token
}

func (t *Token) User() (*authTypes.User, error) {
	return auth.ConvertOldUser(auth.GetUserByEmail(t.UserEmail))
}

func (t *Token) IsAppToken() bool {
	return t.AppName != ""
}

func (t *Token) GetUserName() string {
	return t.UserEmail
}

func (t *Token) GetAppName() string {
	return t.AppName
}

func (t *Token) Permissions() ([]permission.Permission, error) {
	return auth.BaseTokenPermission(t)
}

func loadConfig() error {
	if tokenExpire == 0 {
		var err error
		var days int
		if days, err = config.GetInt("auth:token-expire-days"); err == nil {
			tokenExpire = time.Duration(int64(days) * 24 * int64(time.Hour))
		} else {
			tokenExpire = defaultExpiration
		}
	}
	return nil
}

func token(data string, hash crypto.Hash) string {
	var tokenKey [keySize]byte
	n, err := rand.Read(tokenKey[:])
	for n < keySize || err != nil {
		n, err = rand.Read(tokenKey[:])
	}
	h := hash.New()
	h.Write([]byte(data))
	h.Write(tokenKey[:])
	h.Write([]byte(time.Now().Format(time.RFC3339Nano)))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func newUserToken(u *auth.User) (*Token, error) {
	if u == nil {
		return nil, errors.New("User is nil")
	}
	if u.Email == "" {
		return nil, errors.New("Impossible to generate tokens for users without email")
	}
	if err := loadConfig(); err != nil {
		return nil, err
	}
	t := Token{}
	t.Creation = time.Now()
	t.Expires = tokenExpire
	t.Token = token(u.Email, crypto.SHA1)
	t.UserEmail = u.Email
	return &t, nil
}

func removeOldTokens(userEmail string) error {
	conn, err := db.Conn()
	if err != nil {
		return err
	}
	defer conn.Close()
	var limit int
	if limit, err = config.GetInt("auth:max-simultaneous-sessions"); err != nil {
		return err
	}
	count, err := conn.Tokens().Find(bson.M{"useremail": userEmail}).Count()
	if err != nil {
		return err
	}
	diff := count - limit
	if diff < 1 {
		return nil
	}
	var tokens []map[string]interface{}
	err = conn.Tokens().Find(bson.M{"useremail": userEmail}).
		Select(bson.M{"_id": 1}).Sort("creation").Limit(diff).All(&tokens)
	if err != nil {
		return nil
	}
	ids := make([]interface{}, 0, len(tokens))
	for _, token := range tokens {
		ids = append(ids, token["_id"])
	}
	_, err = conn.Tokens().RemoveAll(bson.M{"_id": bson.M{"$in": ids}})
	return err
}

func createToken(u *auth.User) (*Token, error) {
	if u.Email == "" {
		return nil, errors.New("User does not have an email")
	}
	conn, err := db.Conn()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	token, err := newUserToken(u)
	if err != nil {
		return nil, err
	}
	err = conn.Tokens().Insert(token)
	go removeOldTokens(u.Email)
	return token, err
}

func getToken(header string) (*Token, error) {
	conn, err := db.Conn()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var t Token
	token, err := auth.ParseToken(header)
	if err != nil {
		return nil, err
	}
	err = conn.Tokens().Find(bson.M{"token": token}).One(&t)
	if err != nil {
		if err == mgo.ErrNotFound {
			return nil, auth.ErrInvalidToken
		}
		return nil, err
	}
	if t.Expires > 0 && time.Until(t.Creation.Add(t.Expires)) < 1 {
		return nil, auth.ErrInvalidToken
	}
	return &t, nil
}

func deleteToken(token string) error {
	conn, err := db.Conn()
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Tokens().Remove(bson.M{"token": token})
}

func deleteAllTokens(email string) error {
	conn, err := db.Conn()
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Tokens().RemoveAll(bson.M{"useremail": email})
	return err
}