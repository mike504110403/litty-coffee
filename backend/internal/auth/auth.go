package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const CookieName = "litty_session"

var ErrBadCredentials = errors.New("bad credentials")

type Service struct {
	DB     *pgxpool.Pool
	Secret []byte
	Secure bool
}

type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

// EnsureAdmin creates the initial admin when the table is empty.
func (s *Service) EnsureAdmin(ctx context.Context, username, password string) error {
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM admin_users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if len(password) < 8 {
		return errors.New("ADMIN_PASSWORD (>=8 chars) required to create the first admin")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx,
		`INSERT INTO admin_users (username, password_hash, display_name) VALUES ($1, $2, $1)`,
		username, string(hash))
	return err
}

func (s *Service) Login(ctx context.Context, username, password string) (User, string, error) {
	var u User
	var hash string
	err := s.DB.QueryRow(ctx,
		`SELECT id, username, display_name, password_hash FROM admin_users WHERE username = $1`,
		username).Scan(&u.ID, &u.Username, &u.DisplayName, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, "", ErrBadCredentials
	}
	if err != nil {
		return u, "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return u, "", ErrBadCredentials
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(u.ID, 10),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
	}).SignedString(s.Secret)
	return u, tok, err
}

func (s *Service) SetCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode,
	})
}

type ctxKey struct{}

func UserID(ctx context.Context) int64 { v, _ := ctx.Value(ctxKey{}).(int64); return v }

func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		claims := &jwt.RegisteredClaims{}
		_, err = jwt.ParseWithClaims(c.Value, claims, func(*jwt.Token) (any, error) { return s.Secret, nil },
			jwt.WithValidMethods([]string{"HS256"}))
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		id, _ := strconv.ParseInt(claims.Subject, 10, 64)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

func (s *Service) Me(ctx context.Context, id int64) (User, error) {
	var u User
	err := s.DB.QueryRow(ctx, `SELECT id, username, display_name FROM admin_users WHERE id = $1`, id).
		Scan(&u.ID, &u.Username, &u.DisplayName)
	return u, err
}
