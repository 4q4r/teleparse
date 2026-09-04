package tg

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"teleparse/internal/config"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	gotdtg "github.com/gotd/td/tg"
)

// e164Pattern matches +E.164 phone numbers (max 15 digits).
var e164Pattern = regexp.MustCompile(`^\+[1-9]\d{7,14}$`)

// Prompter collects interactive credentials without binding login to a terminal.
type Prompter interface {
	// Line asks for a visible one-line answer.
	Line(prompt string) (string, error)
	// Hidden asks for a secret one-line answer (echo disabled when possible).
	Hidden(prompt string) (string, error)
}

// ValidatePhone requires the +E.164 format Telegram expects.
func ValidatePhone(phone string) error {
	if !e164Pattern.MatchString(strings.TrimSpace(phone)) {
		return fmt.Errorf("%q: %w", phone, ErrBadPhone)
	}

	return nil
}

// promptAuth implements auth.UserAuthenticator on top of a Prompter: phone
// first (or the pre-supplied one), code next, 2FA password only if requested.
type promptAuth struct {
	phone string
	ask   Prompter
}

func (a promptAuth) Phone(ctx context.Context) (string, error) {
	if a.phone != "" {
		if err := ValidatePhone(a.phone); err != nil {
			return "", err
		}

		return a.phone, nil
	}

	for {
		answer, err := a.ask.Line("Phone (+E.164, e.g. +15551234567)")
		if err != nil {
			return "", fmt.Errorf("read phone: %w", err)
		}

		answer = strings.TrimSpace(answer)

		if err := ValidatePhone(answer); err == nil {
			return answer, nil
		}

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("phone entry: %w", ctx.Err())
		default:
		}
	}
}

func (a promptAuth) Code(_ context.Context, _ *gotdtg.AuthSentCode) (string, error) {
	answer, err := a.ask.Line("Login code")
	if err != nil {
		return "", fmt.Errorf("read code: %w", err)
	}

	return strings.TrimSpace(answer), nil
}

func (a promptAuth) Password(_ context.Context) (string, error) {
	answer, err := a.ask.Hidden("2FA password (empty if none)")
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}

	return answer, nil
}

func (promptAuth) AcceptTermsOfService(_ context.Context, _ gotdtg.HelpTermsOfService) error {
	return nil
}

func (promptAuth) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, ErrSignUpUnsupported
}

// Login runs the interactive phone/code/password flow for account, reusing an
// existing session when one is already authorized.
func Login(
	ctx context.Context,
	account string,
	phone string,
	ask Prompter,
	cfg *config.Config,
	paths *config.Paths,
) error {
	return Run(ctx, account, cfg, paths, func(ctx context.Context, client *telegram.Client) error {
		flow := auth.NewFlow(promptAuth{phone: phone, ask: ask}, auth.SendCodeOptions{})

		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("login %q: %w", account, err)
		}

		return nil
	})
}

// Logout terminates the server-side session and deletes the local account data.
func Logout(ctx context.Context, account string, cfg *config.Config, paths *config.Paths) error {
	if err := Run(ctx, account, cfg, paths, func(ctx context.Context, client *telegram.Client) error {
		if _, err := client.API().AuthLogOut(ctx); err != nil {
			return fmt.Errorf("auth log out: %w", err)
		}

		return nil
	}); err != nil {
		return err
	}

	manager := NewAccountManager(paths.AccountsDir)

	if err := manager.Delete(account); err != nil {
		return fmt.Errorf("delete account %q: %w", account, err)
	}

	return nil
}

// SelfInfo is the WhoAmI answer: identity plus home DC when known.
type SelfInfo struct {
	Authorized bool
	ID         int64
	FirstName  string
	LastName   string
	Username   string
	Phone      string
	DC         int
}

// WhoAmI reports the current user and session DC straight from storage.
func WhoAmI(ctx context.Context, client *telegram.Client, storage session.Storage) (*SelfInfo, error) {
	status, err := client.Auth().Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth status: %w", err)
	}

	info := &SelfInfo{Authorized: status.Authorized}

	if status.Authorized && status.User != nil {
		info.ID = status.User.ID
		info.FirstName = status.User.FirstName
		info.LastName = status.User.LastName
		info.Username = status.User.Username
		info.Phone = status.User.Phone
	}

	data, err := (&session.Loader{Storage: storage}).Load(ctx)
	if err == nil {
		info.DC = data.DC
	}

	return info, nil
}
