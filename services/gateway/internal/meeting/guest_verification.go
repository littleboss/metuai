package meeting

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	guestEmailVerificationTTL         = 15 * time.Minute
	guestEmailVerificationMaxAttempts = 5
)

var (
	ErrGuestEmailVerificationUnavailable      = errors.New("guest email verification is unavailable")
	ErrGuestEmailVerificationInvalid          = errors.New("invalid guest email verification code")
	ErrGuestEmailVerificationExpired          = errors.New("guest email verification code expired")
	ErrGuestEmailVerificationAttemptsExceeded = errors.New("guest email verification attempt limit exceeded")
)

type GuestVerificationMail struct {
	To           string
	Code         string
	MagicToken   string
	MagicURL     string
	MeetingID    string
	MeetingTitle string
}

type GuestVerificationSender interface {
	Send(ctx context.Context, message GuestVerificationMail) error
}

// GuestEmailVerifier creates a short-lived, one-time challenge before a guest receives an email-bearing session.
type GuestEmailVerifier struct {
	repo       Repository
	sender     GuestVerificationSender
	ttl        time.Duration
	AppBaseURL string
}

func NewGuestEmailVerifier(repo Repository, sender GuestVerificationSender) *GuestEmailVerifier {
	return &GuestEmailVerifier{repo: repo, sender: sender, ttl: guestEmailVerificationTTL}
}

func (v *GuestEmailVerifier) magicURL(meetingID, token string) string {
	base := strings.TrimRight(strings.TrimSpace(v.AppBaseURL), "/")
	if base == "" {
		base = "http://127.0.0.1:5173"
	}
	return base + "/meeting/" + meetingID + "?verify_token=" + token
}

// IssuedGuestChallenge 是组织者出示的明文验证码/魔法链接。哈希只落库。
type IssuedGuestChallenge struct {
	GuestID    string
	Email      string
	Code       string
	MagicToken string
	MagicURL   string
	ExpiresAt  time.Time
}

func normalizeIssuedGuestID(raw string) string {
	return strings.TrimPrefix(strings.TrimSpace(raw), "guest:")
}

func (v *GuestEmailVerifier) issue(meeting Meeting, guestID, email string) (IssuedGuestChallenge, error) {
	if v == nil || v.repo == nil {
		return IssuedGuestChallenge{}, ErrGuestEmailVerificationUnavailable
	}
	email = normalizeGuestEmail(email)
	guestID = normalizeIssuedGuestID(guestID)
	if email == "" || guestID == "" {
		return IssuedGuestChallenge{}, ErrGuestEmailVerificationInvalid
	}
	code, err := guestEmailVerificationCode()
	if err != nil {
		return IssuedGuestChallenge{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return IssuedGuestChallenge{}, fmt.Errorf("hash guest email verification code: %w", err)
	}
	token, err := randomMagicToken()
	if err != nil {
		return IssuedGuestChallenge{}, err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(v.ttl)
	if err := v.repo.SaveGuestEmailChallenge(GuestEmailChallenge{
		MeetingID: meeting.ID,
		GuestID:   guestID,
		Email:     email,
		CodeHash:  string(hash),
		TokenHash: hashMagicToken(token),
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}); err != nil {
		return IssuedGuestChallenge{}, err
	}
	return IssuedGuestChallenge{
		GuestID:    guestID,
		Email:      email,
		Code:       code,
		MagicToken: token,
		MagicURL:   v.magicURL(meeting.ID, token),
		ExpiresAt:  expiresAt,
	}, nil
}

func (v *GuestEmailVerifier) Request(ctx context.Context, meeting Meeting, guestID, email string) (time.Time, error) {
	if v == nil || v.sender == nil {
		return time.Time{}, ErrGuestEmailVerificationUnavailable
	}
	issued, err := v.issue(meeting, guestID, email)
	if err != nil {
		return time.Time{}, err
	}
	if err := v.sender.Send(ctx, GuestVerificationMail{
		To:           issued.Email,
		Code:         issued.Code,
		MagicToken:   issued.MagicToken,
		MagicURL:     issued.MagicURL,
		MeetingID:    meeting.ID,
		MeetingTitle: meeting.Title,
	}); err != nil {
		return time.Time{}, fmt.Errorf("send guest email verification: %w", err)
	}
	return issued.ExpiresAt, nil
}

// IssueInRoom 不依赖 SMTP：组织者当场出示验证码，或把魔法链接复制给嘉宾。
func (v *GuestEmailVerifier) IssueInRoom(meeting Meeting, guestID, email string) (IssuedGuestChallenge, error) {
	return v.issue(meeting, guestID, email)
}

func (v *GuestEmailVerifier) Confirm(meetingID, guestID, email, code string) (GuestEmailChallenge, error) {
	if v == nil || v.repo == nil {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationUnavailable
	}
	return v.repo.VerifyGuestEmailChallenge(meetingID, guestID, email, code)
}

func (v *GuestEmailVerifier) ConfirmMagic(meetingID, token string) (GuestEmailChallenge, error) {
	if v == nil || v.repo == nil {
		return GuestEmailChallenge{}, ErrGuestEmailVerificationUnavailable
	}
	return v.repo.VerifyGuestMagicToken(meetingID, token)
}

func randomMagicToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate guest magic token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func hashMagicToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func guestEmailVerificationCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("generate guest email verification code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func normalizeGuestEmail(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	parsed, err := mail.ParseAddress(raw)
	if err != nil || parsed.Address != raw {
		return ""
	}
	return raw
}

type SMTPConfig struct {
	Host       string
	Port       string
	Username   string
	Password   string
	From       string
	RequireTLS bool
}

type SMTPVerificationSender struct {
	config SMTPConfig
}

func NewSMTPVerificationSender(config SMTPConfig) (*SMTPVerificationSender, error) {
	config.Host = strings.TrimSpace(config.Host)
	config.Port = strings.TrimSpace(config.Port)
	config.Username = strings.TrimSpace(config.Username)
	config.From = strings.TrimSpace(config.From)
	if config.Host == "" && config.From == "" {
		return nil, nil
	}
	if config.Host == "" || config.Port == "" || config.From == "" {
		return nil, fmt.Errorf("SMTP_HOST, SMTP_PORT, and SMTP_FROM must be configured together")
	}
	if _, err := mail.ParseAddress(config.From); err != nil {
		return nil, fmt.Errorf("invalid SMTP_FROM: %w", err)
	}
	return &SMTPVerificationSender{config: config}, nil
}

func (s *SMTPVerificationSender) Send(ctx context.Context, message GuestVerificationMail) error {
	if s == nil {
		return ErrGuestEmailVerificationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := smtp.Dial(net.JoinHostPort(s.config.Host, s.config.Port))
	if err != nil {
		return err
	}
	defer client.Quit()

	if supportsTLS, _ := client.Extension("STARTTLS"); supportsTLS {
		if err := client.StartTLS(&tls.Config{ServerName: s.config.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else if s.config.RequireTLS {
		return fmt.Errorf("SMTP server does not support STARTTLS")
	}
	if s.config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(s.config.From); err != nil {
		return err
	}
	if err := client.Rcpt(message.To); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	body := fmt.Sprintf("Your METUAI verification code for meeting %s is: %s\r\n\r\nOr open this link (expires in 15 minutes):\r\n%s\r\n", message.MeetingID, message.Code, message.MagicURL)
	if _, err := io.WriteString(writer, "From: "+s.config.From+"\r\nTo: "+message.To+"\r\nSubject: METUAI guest email verification\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n"+body); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}
