package filters

// datePredicates applies inclusive message-date bounds and the edit flag.
// The bounds re-check client-side what the server pushdown already narrows,
// guarding against best-effort server filtering.
func datePredicates(opts *Options) ([]NamedPredicate, error) {
	minDate, err := resolveDate("after/last", opts.After, opts.Last)
	if err != nil {
		return nil, err
	}

	maxDate, err := resolveDate("before/older-than", opts.Before, opts.OlderThan)
	if err != nil {
		return nil, err
	}

	var preds []NamedPredicate

	if minDate != 0 {
		preds = append(preds, NamedPredicate{Name: "after", Fn: func(ctx *Context) bool {
			return ctx.Message.Date >= minDate
		}})
	}

	if maxDate != 0 {
		preds = append(preds, NamedPredicate{Name: "before", Fn: func(ctx *Context) bool {
			return ctx.Message.Date <= maxDate
		}})
	}

	if opts.Edited.IsSet() {
		preds = append(preds, triPredicate("edited", opts.Edited.Value(), func(ctx *Context) bool {
			return ctx.Message.HasEdit
		}))
	}

	return preds, nil
}
