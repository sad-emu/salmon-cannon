package api

import (
	"context"
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const passwordSaltSize = 32

type authResult struct {
	username string
	ok       bool
}

type authenticator interface {
	authenticate(*http.Request) authResult
}

type authenticatedUserKey struct{}

func withAuthenticatedUser(r *http.Request, username string) *http.Request {
	ctx := context.WithValue(r.Context(), authenticatedUserKey{}, username)
	return r.WithContext(ctx)
}

type passwordFile struct {
	Users []passwordFileUser `json:"users"`
}

type passwordFileUser struct {
	Username       string `json:"username"`
	Password       string `json:"password,omitempty"`
	PasswordSHA512 string `json:"password_sha512,omitempty"`
	Salt           string `json:"salt,omitempty"`
}

type passwordCredential struct {
	salt []byte
	hash [sha512.Size]byte
}

type basicAuthenticator struct {
	users map[string]passwordCredential
}

func loadBasicAuthenticator(path string) (*basicAuthenticator, error) {
	var file passwordFile
	if err := readJSONFile(path, &file); err != nil {
		return nil, fmt.Errorf("read basic auth file: %w", err)
	}

	users := make(map[string]passwordCredential, len(file.Users))
	changed := false
	for i := range file.Users {
		entry := &file.Users[i]
		if entry.Username == "" {
			return nil, fmt.Errorf("basic auth file: user %d has an empty username", i)
		}
		if _, exists := users[entry.Username]; exists {
			return nil, fmt.Errorf("basic auth file: duplicate username %q", entry.Username)
		}

		if entry.Password != "" {
			salt := make([]byte, passwordSaltSize)
			if _, err := rand.Read(salt); err != nil {
				return nil, fmt.Errorf("generate password salt for %q: %w", entry.Username, err)
			}
			hash := saltedPasswordHash(salt, entry.Password)
			entry.Salt = hex.EncodeToString(salt)
			entry.PasswordSHA512 = hex.EncodeToString(hash[:])
			entry.Password = ""
			changed = true
		}

		credential, err := decodePasswordCredential(*entry)
		if err != nil {
			return nil, fmt.Errorf("basic auth file: user %q: %w", entry.Username, err)
		}
		users[entry.Username] = credential
	}
	if len(users) == 0 {
		return nil, errors.New("basic auth file contains no users")
	}

	if changed {
		if err := writeJSONFileAtomic(path, file); err != nil {
			return nil, fmt.Errorf("replace plaintext passwords in basic auth file: %w", err)
		}
	}
	return &basicAuthenticator{users: users}, nil
}

func decodePasswordCredential(entry passwordFileUser) (passwordCredential, error) {
	if entry.PasswordSHA512 == "" || entry.Salt == "" {
		return passwordCredential{}, errors.New("password or password_sha512 and salt are required")
	}
	salt, err := hex.DecodeString(entry.Salt)
	if err != nil || len(salt) == 0 {
		return passwordCredential{}, errors.New("salt must be non-empty hexadecimal")
	}
	hash, err := hex.DecodeString(entry.PasswordSHA512)
	if err != nil || len(hash) != sha512.Size {
		return passwordCredential{}, fmt.Errorf("password_sha512 must be %d hexadecimal bytes", sha512.Size)
	}
	var fixedHash [sha512.Size]byte
	copy(fixedHash[:], hash)
	return passwordCredential{salt: salt, hash: fixedHash}, nil
}

func saltedPasswordHash(salt []byte, password string) [sha512.Size]byte {
	h := sha512.New()
	_, _ = h.Write(salt)
	_, _ = io.WriteString(h, password)
	var result [sha512.Size]byte
	copy(result[:], h.Sum(nil))
	return result
}

func (a *basicAuthenticator) authenticate(r *http.Request) authResult {
	username, password, supplied := r.BasicAuth()
	credential, found := a.users[username]
	if !found {
		credential = passwordCredential{salt: make([]byte, passwordSaltSize)}
	}
	candidate := saltedPasswordHash(credential.salt, password)
	passwordOK := subtle.ConstantTimeCompare(candidate[:], credential.hash[:]) == 1
	return authResult{username: username, ok: supplied && found && passwordOK}
}

type mtlsFile struct {
	Users []mtlsFileUser `json:"users"`
}

type mtlsFileUser struct {
	DN       string `json:"dn"`
	Username string `json:"username"`
}

type mtlsAuthenticator struct {
	users []mtlsFileUser
}

func loadMTLSAuthenticator(path string) (*mtlsAuthenticator, error) {
	var file mtlsFile
	if err := readJSONFile(path, &file); err != nil {
		return nil, fmt.Errorf("read mTLS auth file: %w", err)
	}
	if len(file.Users) == 0 {
		return nil, errors.New("mTLS auth file contains no users")
	}
	for i, entry := range file.Users {
		if entry.DN == "" || entry.Username == "" {
			return nil, fmt.Errorf("mTLS auth file: user %d requires non-empty dn and username", i)
		}
	}
	return &mtlsAuthenticator{users: file.Users}, nil
}

func (a *mtlsAuthenticator) authenticate(r *http.Request) authResult {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return authResult{}
	}
	subject := r.TLS.PeerCertificates[0].Subject.String()
	for _, entry := range a.users {
		if strings.Contains(subject, entry.DN) {
			return authResult{username: entry.Username, ok: true}
		}
	}
	return authResult{}
}

func readJSONFile(path string, target any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeJSONFileAtomic(path string, value any) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".salmon-cannon-auth-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
