package filters

// forwardPredicates constrains forward presence, origin and original date.
// Origin-specific filters never match messages without a forward.
func forwardPredicates(opts *Options) ([]NamedPredicate, error) {
	minDate, err := resolveDate("fwd-date-after", opts.FwdDateAfter, "")
	if err != nil {
		return nil, err
	}

	maxDate, err := resolveDate("fwd-date-before", opts.FwdDateBefore, "")
	if err != nil {
		return nil, err
	}

	var preds []NamedPredicate

	if opts.Forwarded.IsSet() {
		preds = append(preds, triPredicate("forwarded", opts.Forwarded.Value(), forwardPresent))
	}

	if len(opts.FwdFrom) > 0 {
		preds = append(preds, NamedPredicate{Name: "fwd_from", Fn: func(ctx *Context) bool {
			return fwdFromMatches(opts.FwdFrom, ctx)
		}})
	}

	if opts.FwdHidden.IsSet() {
		preds = append(preds, triPredicate("fwd_hidden", opts.FwdHidden.Value(), func(ctx *Context) bool {
			return forwardPresent(ctx) && ctx.Message.Forward.Hidden
		}))
	}

	if minDate != 0 {
		preds = append(preds, NamedPredicate{Name: "fwd_date_after", Fn: func(ctx *Context) bool {
			if !forwardPresent(ctx) {
				return false
			}

			return ctx.Message.Forward.Date >= minDate
		}})
	}

	if maxDate != 0 {
		preds = append(preds, NamedPredicate{Name: "fwd_date_before", Fn: func(ctx *Context) bool {
			if !forwardPresent(ctx) {
				return false
			}

			return ctx.Message.Forward.Date <= maxDate
		}})
	}

	return preds, nil
}

func forwardPresent(ctx *Context) bool {
	return ctx.Message.Forward != nil && ctx.Message.Forward.Present
}

func fwdFromMatches(entries []string, ctx *Context) bool {
	if !forwardPresent(ctx) {
		return false
	}

	for _, entry := range entries {
		// Forward headers never expose phone numbers, so origin matching
		// stays id/username; phone entries simply do not resolve here.
		if matchIDHandleOrPhone(entry, ctx.Message.Forward.FromID, ctx.Message.Forward.FromUsername, "") {
			return true
		}
	}

	return false
}
