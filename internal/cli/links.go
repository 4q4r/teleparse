package cli

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Sentinel errors for message-link parsing.
var (
	// errLinkNeedsMsgID rejects chat-only links: get is message-scoped, whole
	// chats belong to the walk pipeline (dl/scan).
	errLinkNeedsMsgID = errors.New(
		"link carries no message id: get is message-scoped " +
			"(use `teleparse chats list` and `teleparse dl <chat>` for a whole chat)")
	errLinkBadList = errors.New("invalid message id list")
)

// Link grammar constants: the accepted link hosts, the private-channel path
// marker, minimum segment counts, the id-list capacity hint, the
// id-expansion guard and the comment-thread query parameters.
const (
	linkHostAlias     = "telegram.me"
	linkPrivatePrefix = "c"
	linkQueryThread   = "thread"
	linkQueryComment  = "comment"
	// minPrivateSegments counts c/<id>/<msg>; minUserSegments counts
	// <user>/<msg>.
	minPrivateSegments = 3
	minUserSegments    = 2
	idListCapacity     = 4
	// maxRangeSpan bounds one `a-b` id range so a typo cannot expand into
	// millions of fetch ids.
	maxRangeSpan = 10_000
)

// linkTarget is one parsed message-link context: the chat spec (in scope
// resolver grammar), the explicit message ids to fetch and — for forum topic
// or comment-thread links — the getReplies context id (0 = flat chat).
type linkTarget struct {
	PeerSpec string
	MsgIDs   []int
	TopicID  int
}

// parseLinks parses t.me and tg:// message links into per-context fetch
// targets. Links to the same chat merge: ids union, distinct topic contexts
// stay apart. Ids come back deduplicated and ascending.
func parseLinks(links []string) ([]linkTarget, error) {
	merged := map[string]*linkTarget{}

	var order []string

	for _, raw := range links {
		parsed, err := parseLink(raw)
		if err != nil {
			return nil, err
		}

		key := parsed.PeerSpec + "\x00" + strconv.Itoa(parsed.TopicID)

		existing, seen := merged[key]
		if !seen {
			clone := parsed
			merged[key] = &clone

			order = append(order, key)

			continue
		}

		existing.MsgIDs = append(existing.MsgIDs, parsed.MsgIDs...)
	}

	targets := make([]linkTarget, 0, len(order))

	for _, key := range order {
		target := *merged[key]
		target.MsgIDs = normalizeIDs(target.MsgIDs)

		if len(target.MsgIDs) == 0 {
			return nil, fmt.Errorf("%q: %w", target.PeerSpec, errLinkBadList)
		}

		targets = append(targets, target)
	}

	return targets, nil
}

// parseLink parses one link onto its fetch context.
func parseLink(raw string) (linkTarget, error) {
	spec := strings.TrimSpace(raw)

	if strings.HasPrefix(spec, "tg://") {
		return parseTGLink(spec)
	}

	for _, scheme := range [...]string{"https://", "http://"} {
		spec = strings.TrimPrefix(spec, scheme)
	}

	pathPart, queryPart, _ := strings.Cut(spec, "?")

	segments := strings.Split(strings.TrimPrefix(pathPart, "/"), "/")

	host := strings.ToLower(segments[0])
	if host != "t.me" && host != linkHostAlias {
		return linkTarget{}, fmt.Errorf("%q: %w", raw, errLinkBadList)
	}

	rest := segments[1:]

	if len(rest) == 0 {
		return linkTarget{}, fmt.Errorf("%q: %w", raw, errLinkNeedsMsgID)
	}

	var (
		target linkTarget
		idsSeg string
		topic  int
		err    error
	)

	if rest[0] == linkPrivatePrefix {
		if len(rest) < minPrivateSegments {
			return linkTarget{}, fmt.Errorf("%q: %w", raw, errLinkNeedsMsgID)
		}

		if _, parseErr := strconv.ParseInt(rest[1], 10, 64); parseErr != nil {
			return linkTarget{}, fmt.Errorf("%q: %w", raw, errLinkBadList)
		}

		target.PeerSpec = "t.me/" + linkPrivatePrefix + "/" + rest[1]

		idsSeg, topic, err = tailSegments(rest[2:], raw)
	} else {
		if len(rest) < minUserSegments {
			return linkTarget{}, fmt.Errorf("%q: %w", raw, errLinkNeedsMsgID)
		}

		target.PeerSpec = "t.me/" + strings.ToLower(rest[0])

		idsSeg, topic, err = tailSegments(rest[1:], raw)
	}

	if err != nil {
		return linkTarget{}, err
	}

	target.TopicID = topic

	target.MsgIDs, err = parseIDList(idsSeg)
	if err != nil {
		return linkTarget{}, fmt.Errorf("%q: %w", raw, err)
	}

	if err := applyThreadQuery(&target, queryPart, raw); err != nil {
		return linkTarget{}, err
	}

	return target, nil
}

// tailSegments consumes a link's tail after the chat identity and returns
// the id-bearing segment plus the optional forum topic id (0 when absent):
// one numeric segment addresses a flat message, two address a topic message,
// more are malformed.
func tailSegments(rest []string, raw string) (string, int, error) {
	switch len(rest) {
	case 1:
		return rest[0], 0, nil
	case 2:
		topic, err := positiveInt(rest[0])
		if err != nil {
			return "", 0, fmt.Errorf("%q: %w", raw, errLinkBadList)
		}

		return rest[1], topic, nil
	default:
		return "", 0, fmt.Errorf("%q: %w", raw, errLinkBadList)
	}
}

// applyThreadQuery folds ?thread=/ ?comment= onto the target: the comment
// ids become the fetch ids and the replies context id becomes the path topic
// when present, else the path post id.
func applyThreadQuery(target *linkTarget, queryPart, raw string) error {
	if queryPart == "" {
		return nil
	}

	values, err := url.ParseQuery(queryPart)
	if err != nil {
		return fmt.Errorf("%q: %w: %w", raw, errLinkBadList, err)
	}

	thread, hasThread := values[linkQueryThread]
	comment, hasComment := values[linkQueryComment]

	if !hasThread && !hasComment {
		return nil
	}

	idTexts := make([]string, 0, len(thread)+len(comment))
	idTexts = append(idTexts, thread...)
	idTexts = append(idTexts, comment...)

	ids := make([]int, 0, len(idTexts))

	for _, idText := range idTexts {
		threadID, err := positiveInt(idText)
		if err != nil {
			return fmt.Errorf("%q: %w", raw, errLinkBadList)
		}

		ids = append(ids, threadID)
	}

	if target.TopicID == 0 && len(target.MsgIDs) > 0 {
		target.TopicID = target.MsgIDs[0]
	}

	target.MsgIDs = ids

	return nil
}

// parseTGLink parses tg://resolve?domain=...&post=N[&thread=T] links.
func parseTGLink(raw string) (linkTarget, error) {
	values, err := url.ParseQuery(strings.TrimPrefix(raw, "tg://resolve?"))
	if err != nil {
		return linkTarget{}, fmt.Errorf("%q: %w: %w", raw, errLinkBadList, err)
	}

	domain := strings.ToLower(values.Get("domain"))
	if domain == "" {
		return linkTarget{}, fmt.Errorf("%q: %w", raw, errLinkBadList)
	}

	post, err := positiveInt(values.Get("post"))
	if err != nil {
		return linkTarget{}, fmt.Errorf("%q: %w", raw, errLinkNeedsMsgID)
	}

	target := linkTarget{PeerSpec: "t.me/" + domain, MsgIDs: []int{post}}

	if err := applyThreadQuery(&target, values.Encode(), raw); err != nil {
		return linkTarget{}, err
	}

	return target, nil
}

// parseIDList expands "100", "100-200" and "100,102,105" grammar into the
// literal id set.
func parseIDList(text string) ([]int, error) {
	ids := make([]int, 0, idListCapacity)

	for _, part := range strings.Split(text, ",") {
		start, end, ranged := strings.Cut(part, "-")

		first, err := positiveInt(start)
		if err != nil {
			return nil, errLinkBadList
		}

		if !ranged {
			ids = append(ids, first)

			continue
		}

		last, err := positiveInt(end)
		if err != nil || last < first || last-first+1 > maxRangeSpan {
			return nil, fmt.Errorf("%q: %w", part, errLinkBadRange)
		}

		for id := first; id <= last; id++ {
			ids = append(ids, id)
		}
	}

	if len(ids) == 0 {
		return nil, errLinkBadList
	}

	return ids, nil
}

// errLinkBadRange marks over-wide or reversed id ranges.
var errLinkBadRange = errors.New("invalid message id range (max span 10000)")

// positiveInt parses a strictly positive decimal int.
func positiveInt(text string) (int, error) {
	value, err := strconv.Atoi(text)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%q: %w", text, errLinkBadList)
	}

	return value, nil
}

// normalizeIDs deduplicates and sorts id sets ascending.
func normalizeIDs(ids []int) []int {
	slices.Sort(ids)

	return slices.Compact(ids)
}
