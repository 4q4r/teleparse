package filters

// engagementPredicates covers replies, view/forward/reaction counters,
// specific reactions and pinned state.
func engagementPredicates(opts *Options) []NamedPredicate {
	var preds []NamedPredicate

	if opts.IsReply.IsSet() {
		preds = append(preds, triPredicate("is_reply", opts.IsReply.Value(), func(ctx *Context) bool {
			return ctx.Message.Reply != nil && ctx.Message.Reply.Present
		}))
	}

	if opts.MinViews > 0 {
		preds = append(preds, NamedPredicate{Name: "min_views", Fn: func(ctx *Context) bool {
			return ctx.Message.Views >= opts.MinViews
		}})
	}

	if opts.MinForwards > 0 {
		preds = append(preds, NamedPredicate{Name: "min_forwards", Fn: func(ctx *Context) bool {
			return ctx.Message.Forwards >= opts.MinForwards
		}})
	}

	if opts.MinReactions > 0 {
		preds = append(preds, NamedPredicate{Name: "min_reactions", Fn: func(ctx *Context) bool {
			return ctx.Message.TotalReactions >= opts.MinReactions
		}})
	}

	if len(opts.Reaction) > 0 {
		preds = append(preds, NamedPredicate{Name: "reaction", Fn: func(ctx *Context) bool {
			return reactionsMatch(opts.Reaction, ctx.Message.Reactions)
		}})
	}

	if opts.Pinned.IsSet() {
		preds = append(preds, triPredicate("pinned", opts.Pinned.Value(), func(ctx *Context) bool {
			return ctx.Message.Pinned
		}))
	}

	return preds
}

// miscPredicates covers inclusive id ranges, service classification and
// message flags.
func miscPredicates(opts *Options) []NamedPredicate {
	var preds []NamedPredicate

	if opts.MinID > 0 {
		preds = append(preds, NamedPredicate{Name: "min_id", Fn: func(ctx *Context) bool {
			return ctx.Message.ID >= opts.MinID
		}})
	}

	if opts.MaxID > 0 {
		preds = append(preds, NamedPredicate{Name: "max_id", Fn: func(ctx *Context) bool {
			return ctx.Message.ID <= opts.MaxID
		}})
	}

	if opts.Service == "only" || opts.Service == "exclude" {
		want := opts.Service == "only"

		preds = append(preds, NamedPredicate{Name: "service", Fn: func(ctx *Context) bool {
			return ctx.Message.Service == want
		}})
	}

	if opts.Silent.IsSet() {
		preds = append(preds, triPredicate("silent", opts.Silent.Value(), func(ctx *Context) bool {
			return ctx.Message.Silent
		}))
	}

	if opts.HasSpoiler.IsSet() {
		preds = append(preds, triPredicate("has_spoiler", opts.HasSpoiler.Value(), func(ctx *Context) bool {
			return ctx.Message.Spoiler
		}))
	}

	return preds
}

func reactionsMatch(wanted []string, reactions []Reaction) bool {
	for _, reaction := range reactions {
		if contains(wanted, reaction.Emoji) {
			return true
		}
	}

	return false
}
