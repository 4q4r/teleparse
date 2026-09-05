package tg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/4q4r/teleparse/internal/config"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth/qrlogin"
	gotdtg "github.com/gotd/td/tg"
	"rsc.io/qr"
)

// qrQuietZone is the whitespace margin, in QR modules, drawn around the
// matrix so phone cameras can lock onto the code.
const qrQuietZone = 2

// qrInstructionHow is the approval hint printed with every QR render.
const qrInstructionHow = "Approve the login on another device: Settings -> Devices -> Link Desktop Device"

// qrInstructionScan points at the matrix in full (terminal) rendering.
const qrInstructionScan = "Scan the QR code below with Telegram on that device"

// qrInstructionLink carries the fallback URL line prefix.
const qrInstructionLink = "Or open this link on a device with Telegram installed:"

// QROptions configures a QR login: deadline plus where and how the code is
// rendered.
type QROptions struct {
	// Timeout bounds the whole wait for device approval.
	Timeout time.Duration
	// ASCII drops the half-block matrix and prints the tg:// URL only
	// (--no-ascii or a non-terminal stderr).
	ASCII bool
	// TTY reports whether the output sink is an interactive terminal; the
	// matrix is drawn only when TTY is true and ASCII is false.
	TTY bool
	// Out receives the instructions, the matrix and the URL (stderr in
	// production; QR noise never goes to the machine-readable stdout).
	Out io.Writer
}

// qrLoginer is the narrow seam over gotd's QR loop so the token-rotation and
// timeout state machine can run against fakes. It is satisfied verbatim by
// qrlogin.QR (returned by telegram.Client.QR).
type qrLoginer interface {
	// Auth exports a token, calls show for every (re)render and blocks until
	// the token is accepted, the deadline hits or the flow fails.
	Auth(
		ctx context.Context,
		loggedIn qrlogin.LoggedIn,
		show func(ctx context.Context, token qrlogin.Token) error,
		exceptIDs ...int64,
	) (*gotdtg.AuthAuthorization, error)
}

// QRLogin logs the account in by QR code: it renders the tg://login token
// (half-block matrix on a terminal, bare URL under --no-ascii or when stderr
// is not a TTY), refreshes it on rotation and waits for approval on another
// device until opts.Timeout elapses. The authorized session is persisted in
// the account storage exactly like phone login.
func QRLogin(
	ctx context.Context,
	account string,
	creds Creds,
	opts QROptions,
	cfg *config.Config,
	paths *config.Paths,
) (*SelfInfo, error) {
	manager := NewAccountManager(paths.AccountsDir)

	storage, err := manager.Storage(account)
	if err != nil {
		return nil, err
	}

	dispatcher := gotdtg.NewUpdateDispatcher()

	loggedIn := qrlogin.OnLoginToken(dispatcher)

	info := &SelfInfo{}

	runErr := RunWithUpdates(ctx, account, creds, cfg, paths, dispatcher, func(
		ctx context.Context,
		client *telegram.Client,
	) error {
		ctx, cancel := context.WithTimeout(ctx, opts.Timeout)

		defer cancel()

		if _, err := runQRLogin(ctx, client.QR(), loggedIn, opts.Out, opts.TTY && !opts.ASCII); err != nil {
			return err
		}

		who, err := WhoAmI(ctx, client, storage)
		if err != nil {
			return err
		}

		info = who

		return nil
	})
	if runErr != nil {
		return nil, runErr
	}

	return info, nil
}

// runQRLogin drives the QR flow through the seam, rendering every token (the
// whole block is redrawn on rotation) and mapping the deadline miss to
// ErrQRTimeout.
func runQRLogin(
	ctx context.Context,
	login qrLoginer,
	loggedIn qrlogin.LoggedIn,
	out io.Writer,
	full bool,
) (*gotdtg.AuthAuthorization, error) {
	rotated := false

	authz, err := login.Auth(ctx, loggedIn, func(_ context.Context, token qrlogin.Token) error {
		if rotated {
			const refreshNote = "QR token refreshed; scan the new code:"

			if _, err := fmt.Fprintln(out, "\n"+refreshNote); err != nil {
				return fmt.Errorf("write refresh note: %w", err)
			}
		}

		rotated = true

		return renderQR(out, token, full)
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w", ErrQRTimeout)
		}

		return nil, fmt.Errorf("run qr login: %w", err)
	}

	return authz, nil
}

// renderQR writes the approval instructions and the tg://login URL; in full
// mode it also draws the token matrix with half-block runes (the one
// deliberate non-ASCII exception, gated behind the TTY && !--no-ascii check).
func renderQR(w io.Writer, token qrlogin.Token, full bool) error {
	var builder strings.Builder

	builder.WriteString(qrInstructionHow + "\n")

	if full {
		builder.WriteString(qrInstructionScan + "\n")

		if err := renderQRMatrix(&builder, token); err != nil {
			return err
		}
	}

	builder.WriteString(qrInstructionLink + "\n")
	builder.WriteString(token.URL() + "\n")

	if _, err := fmt.Fprint(w, builder.String()); err != nil {
		return fmt.Errorf("write QR: %w", err)
	}

	return nil
}

// renderQRMatrix appends the token's QR matrix using half-block runes: two
// matrix rows per text line, with a quiet-zone margin around the code.
func renderQRMatrix(builder *strings.Builder, token qrlogin.Token) error {
	code, err := qr.Encode(token.URL(), qr.L)
	if err != nil {
		return fmt.Errorf("encode QR matrix: %w", err)
	}

	total := code.Size + qrQuietZone*2

	// blackAt reports whether the module at (col, row) is dark, treating the
	// quiet-zone margin as blank.
	blackAt := func(col, row int) bool {
		inCol := col-qrQuietZone >= 0 && col-qrQuietZone < code.Size
		inRow := row-qrQuietZone >= 0 && row-qrQuietZone < code.Size

		return inCol && inRow && code.Black(col-qrQuietZone, row-qrQuietZone)
	}

	for row := 0; row < total; row += 2 {
		for col := range total {
			top, bottom := blackAt(col, row), blackAt(col, row+1)

			switch {
			case top && bottom:
				builder.WriteString("\u2588") // full block
			case top:
				builder.WriteString("\u2580") // upper half
			case bottom:
				builder.WriteString("\u2584") // lower half
			default:
				builder.WriteString(" ")
			}
		}

		builder.WriteString("\n")
	}

	return nil
}
