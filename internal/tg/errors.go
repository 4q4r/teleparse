// Package tg is the gateway over gotd/td: multi-account session management,
// interactive login, proxy dialers, dialogs and contacts helpers. It isolates
// every gotd import behind one internal boundary.
package tg

import "errors"

// Sentinel errors wrapped by dynamic context messages.
var (
	ErrAccountName       = errors.New("account name must be 1-64 chars of [a-z0-9_-]")
	ErrAccountInUse      = errors.New("account in use")
	ErrAPICredsMissing   = errors.New("TELEPARSE_API_ID and TELEPARSE_API_HASH must be set")
	ErrBadProxyURL       = errors.New("cannot parse proxy URL")
	ErrBadProxyScheme    = errors.New("unsupported proxy scheme (want socks5:// socks4:// http:// mtproto:// webproxy://)")
	ErrBadProxySecret    = errors.New("invalid mtproto secret")
	ErrWebProxyNotWired  = errors.New("webproxy:// resolver is not wired")
	ErrProxyRefused      = errors.New("proxy refused connection")
	ErrBadPhone          = errors.New("phone must be in +E.164 format, e.g. +15551234567")
	ErrSignUpUnsupported = errors.New("sign up is not supported; log in with an existing account")
	ErrChatNotFound      = errors.New("chat not found")
	ErrBadArchivedFilter = errors.New("archived must be one of only|exclude|any")
	ErrTelethonMissing   = errors.New("telethon session file not found")
	ErrTelethonSchema    = errors.New("not a telethon session database")
	ErrDeviceCorrupt     = errors.New("stored device profile is corrupt")
)

// Chat type vocabulary used across dialogs, filters and output.
const (
	ChatTypePrivate    = "private"
	ChatTypeBot        = "bot"
	ChatTypeGroup      = "group"
	ChatTypeSupergroup = "supergroup"
	ChatTypeForum      = "forum"
	ChatTypeChannel    = "channel"
)
