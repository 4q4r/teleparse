package filters

// Pushdown is the set of constraints delegated to Telegram's server-side
// message search. Zero values mean "not constrained server-side"; anything
// not expressible server-side is evaluated by client-side predicates instead.
// MessagesFilter is one of photo, video, photo_video, document, url, gif,
// voice, music, round_video, round_voice, geo, contact, pinned, chat_photos,
// phone_calls, my_mentions or empty. MinDate and MaxDate are unix seconds,
// 0 meaning unset. Notes documents server-behavior caveats for --explain.
type Pushdown struct {
	MessagesFilter string
	SearchQuery    string
	MinDate        int64
	MaxDate        int64
	FromUsers      []string
	Notes          []string
}

// NamedPredicate is a named client-side matcher. Predicates are small and
// independent: Compile appends one per active option and the caller ANDs
// them, so new families slot in by appending to the compile chain.
type NamedPredicate struct {
	Name string
	Fn   func(*Context) bool
}

// Plan is the compiled form of Options: server pushdown plus ordered
// client-side predicates and human-readable explain lines.
type Plan struct {
	Pushdown     Pushdown
	Predicates   []NamedPredicate
	ExplainLines []string
}
